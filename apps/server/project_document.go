package main

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/xfey/coconet/packages/contracts"
)

type projectDocumentReceipt struct {
	ProjectID string `json:"project_id"`
	Digest    string `json:"digest"`
	Version   int64  `json:"version"`
}

func documentVersionKey(project string, version int64) string {
	return project + ":" + strconv.FormatInt(version, 10)
}

func documentAt(state *databaseState, project string, version int64) contracts.ProjectDocument {
	if version == 0 {
		return contracts.ProjectDocument{SchemaVersion: 1, ProjectID: project}
	}
	return state.ProjectDocuments.Get(documentVersionKey(project, version))
}

func (a *authority) projectDocument(actor contracts.Account, project string, version int64) (contracts.ProjectDocument, error) {
	var result contracts.ProjectDocument
	err := a.store.view(func(state databaseState) error {
		if _, _, err := requireReadableMember(&state, actor.AccountID, project); err != nil {
			return err
		}
		head := state.ProjectDocumentHeads.Get(project)
		if version < 0 {
			version = head
		}
		if version > head {
			return notFound("document_version_missing", "This document version does not exist; refresh the version history.")
		}
		result = documentAt(&state, project, version)
		return nil
	})
	return result, err
}

func (a *authority) saveProjectDocument(actor contracts.Account, project string, input contracts.SaveProjectDocumentRequest) (contracts.ProjectDocument, error) {
	var result contracts.ProjectDocument
	if input.SchemaVersion != 1 || input.BaseVersion < 0 {
		return result, invalid("invalid_document_version", "schema_version must be 1 and base_version must be the nonnegative version read before editing.")
	}
	if err := validateRequestID(input.RequestID); err != nil {
		return result, err
	}
	if len(input.Markdown) > contracts.MaxProjectDocumentBytes || !utf8.ValidString(input.Markdown) {
		return result, invalid("invalid_document_content", "markdown must be valid UTF-8 and no larger than 256 KiB.")
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%d:", input.BaseVersion)+input.Markdown)))
	key := operationKey(actor.AccountID, "document:"+project, input.RequestID)
	err := a.store.update(func(state *databaseState) error {
		if _, _, err := requireActiveMember(state, actor.AccountID, project); err != nil {
			return err
		}
		if receipt, ok := state.ProjectDocumentReceipts.Lookup(key); ok {
			if receipt.Digest != digest {
				return conflict("document_request_reused", "This save request was already used for different content; start a new save request.")
			}
			result = documentAt(state, project, receipt.Version)
			return nil
		}
		head := state.ProjectDocumentHeads.Get(project)
		if head != input.BaseVersion {
			return conflict("document_conflict", "Someone saved a newer version. Keep your draft, read the latest document, then merge and save again.")
		}
		result = documentAt(state, project, head)
		if result.Markdown != input.Markdown {
			result = contracts.ProjectDocument{SchemaVersion: 1, ProjectID: project, Version: head + 1, Markdown: input.Markdown, SavedAt: a.timestamp(), Author: contracts.SessionAuthor{AccountID: actor.AccountID, DisplayName: actor.DisplayName}}
			state.ProjectDocuments.Set(documentVersionKey(project, result.Version), result)
			state.ProjectDocumentHeads.Set(project, result.Version)
			markDashboardChanged(state, project)
		}
		state.ProjectDocumentReceipts.Set(key, projectDocumentReceipt{ProjectID: project, Digest: digest, Version: result.Version})
		return nil
	})
	return result, err
}

func (a *authority) projectDocumentHistory(actor contracts.Account, project string, before int64) (contracts.ProjectDocumentHistory, error) {
	result := contracts.ProjectDocumentHistory{SchemaVersion: 1, ProjectID: project, Versions: []contracts.ProjectDocumentVersion{}}
	err := a.store.view(func(state databaseState) error {
		if _, _, err := requireReadableMember(&state, actor.AccountID, project); err != nil {
			return err
		}
		head := state.ProjectDocumentHeads.Get(project)
		start := head
		if before > 0 {
			start = min(start, before-1)
		}
		for v := start; v > 0 && len(result.Versions) < 50; v-- {
			doc := documentAt(&state, project, v)
			result.Versions = append(result.Versions, contracts.ProjectDocumentVersion{Version: v, SavedAt: doc.SavedAt, Author: doc.Author})
		}
		if len(result.Versions) > 0 && result.Versions[len(result.Versions)-1].Version > 1 {
			result.NextBefore = result.Versions[len(result.Versions)-1].Version
		}
		return nil
	})
	return result, err
}

func (h *serverHandler) handleProjectDocument(w http.ResponseWriter, r *http.Request, requestID string, actor contracts.Account, project string, parts []string, browser bool) {
	w.Header().Set("Cache-Control", "no-store")
	if len(parts) == 0 && r.Method == http.MethodGet {
		result, err := h.authority.projectDocument(actor, project, -1)
		h.writeResult(w, requestID, 200, result, err)
		return
	}
	if browser && len(parts) == 0 && r.Method == http.MethodPut {
		var input contracts.SaveProjectDocumentRequest
		if err := decodeJSONLimit(r, &input, 6*contracts.MaxProjectDocumentBytes+4096); err != nil {
			h.writeError(w, requestID, invalid("invalid_document_json", "Send a JSON save request with schema_version, request_id, base_version and markdown (up to 256 KiB)."))
			return
		}
		result, err := h.authority.saveProjectDocument(actor, project, input)
		h.writeResult(w, requestID, 200, result, err)
		return
	}
	if browser && r.Method == http.MethodGet && len(parts) == 1 && parts[0] == "history" {
		before := int64(0)
		var err error
		if value := r.URL.Query().Get("before"); value != "" {
			before, err = strconv.ParseInt(value, 10, 64)
		}
		if err != nil || before < 0 {
			h.writeError(w, requestID, invalid("invalid_document_cursor", "before must be a nonnegative version number from the previous history page."))
			return
		}
		result, err := h.authority.projectDocumentHistory(actor, project, before)
		h.writeResult(w, requestID, 200, result, err)
		return
	}
	if browser && r.Method == http.MethodGet && len(parts) == 2 && parts[0] == "versions" {
		version, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || version < 1 {
			h.writeError(w, requestID, invalid("invalid_document_version", "The requested document version must be a positive integer."))
			return
		}
		result, err := h.authority.projectDocument(actor, project, version)
		h.writeResult(w, requestID, 200, result, err)
		return
	}
	h.methodNotAllowed(w, requestID, http.MethodGet)
}
