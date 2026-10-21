package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/xfey/coconet/packages/contracts"
)

type projectNoteReceipt struct {
	Digest string `json:"digest"`
}

func notesAt(state *databaseState, project string) contracts.ProjectNotes {
	notes, ok := state.ProjectNotes.Lookup(project)
	if !ok {
		return contracts.ProjectNotes{SchemaVersion: 1, ProjectID: project, Notes: []contracts.ProjectNote{}}
	}
	return notes
}

// One ordered text snapshot for the existing read-only Runtime transport.
// User content remains plain text; author identity and publication time are
// supplied by the server, never accepted from a browser mutation.
func notesDocument(notes contracts.ProjectNotes) contracts.ProjectDocument {
	var text strings.Builder
	for i, note := range notes.Notes {
		if i > 0 {
			text.WriteString("\n\n---\n\n")
		}
		name := strings.Join(strings.Fields(note.Author.DisplayName), " ")
		if name == "" {
			name = note.Author.AccountID
		}
		fmt.Fprintf(&text, "[%d] %s · %s\n%s", i+1, name, note.CreatedAt, note.Text)
	}
	return contracts.ProjectDocument{SchemaVersion: 1, ProjectID: notes.ProjectID, Version: notes.Version, Markdown: text.String(), SavedAt: notes.SavedAt}
}

func (a *authority) projectNotes(actor contracts.Account, project string) (contracts.ProjectNotes, error) {
	var result contracts.ProjectNotes
	err := a.store.view(func(state databaseState) error {
		if _, _, err := requireReadableMember(&state, actor.AccountID, project); err != nil {
			return err
		}
		result = notesAt(&state, project)
		return nil
	})
	return result, err
}

func (a *authority) saveProjectNote(actor contracts.Account, project, id string, input contracts.SaveProjectNoteRequest, remove bool) (contracts.ProjectNotes, error) {
	var result contracts.ProjectNotes
	if input.SchemaVersion != 1 || input.BaseRevision < 0 {
		return result, invalid("invalid_note_revision", "schema_version must be 1; base_revision is 0 for a new note, or the revision read before editing or deleting it.")
	}
	if err := validateRequestID(id); err != nil {
		return result, invalid("invalid_note_id", "Use a stable note ID when creating a note and keep it for retries.")
	}
	if err := validateRequestID(input.RequestID); err != nil {
		return result, err
	}
	if !utf8.ValidString(input.Text) || len(input.Text) > contracts.MaxProjectDocumentBytes || !remove && strings.TrimSpace(input.Text) == "" {
		return result, invalid("invalid_note_content", "text must be nonempty UTF-8, no larger than 256 KiB. Delete the note to remove its content.")
	}
	payload, _ := json.Marshal([]any{id, input.BaseRevision, input.Text, remove})
	digest := fmt.Sprintf("%x", sha256.Sum256(payload))
	key := operationKey(actor.AccountID, "notes:"+project, input.RequestID)
	err := a.store.update(func(state *databaseState) error {
		if _, _, err := requireActiveMember(state, actor.AccountID, project); err != nil {
			return err
		}
		result = notesAt(state, project)
		if receipt, ok := state.ProjectNoteReceipts.Lookup(key); ok {
			if receipt.Digest != digest {
				return conflict("note_request_reused", "This request was used for a different change. Use a new request ID.")
			}
			// Return the current view, not an old snapshot that could resurrect
			// a note deleted after a successful but unacknowledged save.
			return nil
		}
		index := -1
		for i, note := range result.Notes {
			if note.ID == id {
				index = i
				break
			}
		}
		if index >= 0 {
			note := result.Notes[index]
			if note.Author.AccountID != actor.AccountID {
				return forbidden("note_not_owned", "Only the person who published this note can edit or delete it.")
			}
			if note.Revision != input.BaseRevision {
				return conflict("note_conflict", "This note changed on another device. Keep your draft and compare it with the latest note before retrying.")
			}
			if !remove && note.Text == input.Text {
				state.ProjectNoteReceipts.Set(key, projectNoteReceipt{Digest: digest})
				return nil
			}
		} else if remove || input.BaseRevision != 0 {
			return notFound("note_missing", "This note was deleted. Keep your draft and publish it as a new note if needed.")
		}
		// Copy before changing the slice: a rejected oversized mutation must
		// not affect the stored view, including an in-memory transaction.
		result.Notes = append([]contracts.ProjectNote{}, result.Notes...)
		now := a.timestamp()
		switch {
		case remove:
			result.Notes = append(result.Notes[:index], result.Notes[index+1:]...)
		case index >= 0:
			result.Notes[index].Text = input.Text
			result.Notes[index].Revision++
		case index < 0:
			result.Notes = append(result.Notes, contracts.ProjectNote{ID: id, Revision: 1, Text: input.Text, CreatedAt: now, Author: contracts.SessionAuthor{AccountID: actor.AccountID, DisplayName: actor.DisplayName}})
		}
		if len(notesDocument(result).Markdown) > contracts.MaxProjectDocumentBytes {
			return invalid("notes_too_large", "The combined project notes exceed 256 KiB. Shorten or delete some of your notes before saving.")
		}
		result.Version++
		result.SavedAt = now
		state.ProjectNotes.Set(project, result)
		state.ProjectNoteReceipts.Set(key, projectNoteReceipt{Digest: digest})
		markDashboardChanged(state, project)
		return nil
	})
	return result, err
}

func (h *serverHandler) handleProjectDocument(w http.ResponseWriter, r *http.Request, requestID string, actor contracts.Account, project string, parts []string, browser bool) {
	w.Header().Set("Cache-Control", "no-store")
	if len(parts) == 0 && r.Method == http.MethodGet {
		result, err := h.authority.projectNotes(actor, project)
		if browser {
			h.writeResult(w, requestID, 200, result, err)
		} else {
			h.writeResult(w, requestID, 200, notesDocument(result), err)
		}
		return
	}
	if browser && len(parts) == 2 && parts[0] == "notes" && (r.Method == http.MethodPut || r.Method == http.MethodDelete) {
		var input contracts.SaveProjectNoteRequest
		if err := decodeJSONLimit(r, &input, 6*contracts.MaxProjectDocumentBytes+4096); err != nil {
			h.writeError(w, requestID, invalid("invalid_note_json", "Send schema_version, request_id, base_revision and text for this note; do not send author information."))
			return
		}
		result, err := h.authority.saveProjectNote(actor, project, parts[1], input, r.Method == http.MethodDelete)
		h.writeResult(w, requestID, 200, result, err)
		return
	}
	h.methodNotAllowed(w, requestID, http.MethodGet)
}
