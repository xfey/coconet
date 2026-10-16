package main

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/conversation"
)

type preparedSnapshotIndex struct {
	Record          normalizedVersionRecord
	History, Native conversation.Prepared
	BaseVersion     int
}

// Compact history keeps stable anchors and visible dialogue, not tool bodies.
// Original bytes remain exclusively in the native artifact.
func conversationEntries(turns []contracts.NormalizedTurn, visible map[visibleTextIdentity][]contracts.TextRange) []conversation.Entry {
	entries := make([]conversation.Entry, len(turns))
	for i, turn := range turns {
		dialogue, _, _ := visibleTurnDialogue(turn, visible)
		text := dialogue.window(0, dialogue.size())
		turn.Text = text
		turn.Symbols = inferSymbols(text)
		turn.ErrorTokens = turn.ErrorTokens[:min(8, len(turn.ErrorTokens))]
		turn.RelativePaths = turn.RelativePaths[:min(8, len(turn.RelativePaths))]
		entries[i].Turn = turn
		if _, known := visible[visibleTurnKey(turn)]; known {
			entries[i].Visible = &text
			if text != "" {
				entries[i].Ranges = []contracts.TextRange{{Start: 0, End: len(text)}}
			}
		}
	}
	return entries
}
func nativeConversationEntries(turns []contracts.NormalizedTurn) []conversation.Entry {
	entries := make([]conversation.Entry, len(turns))
	for i, turn := range turns {
		// Native checkpoints need identity hashes for append validation, but no tool
		// response bodies. Visible range validation only addresses user/assistant.
		if turn.Role != "user" && turn.Role != "assistant" || turn.Kind == "event" {
			turn.Text = ""
		}
		turn.RelativePaths = nil
		turn.Symbols = nil
		turn.ErrorTokens = nil
		entries[i].Turn = turn
	}
	return entries
}

func conversationTurns(entries []conversation.Entry) []contracts.NormalizedTurn {
	turns := make([]contracts.NormalizedTurn, len(entries))
	for i, entry := range entries {
		turns[i] = entry.Turn
	}
	return turns
}

func recordConversation(state databaseState, record normalizedVersionRecord, source bool) ([]conversation.Entry, error) {
	manifest := record.Content
	if source {
		manifest = record.Source
	}
	if manifest == nil {
		return nil, errors.New("snapshot conversation manifest is missing")
	}
	project := state.Sessions.Get(record.Summary.ServerSessionID).ProjectID
	return readConversation(state, project, *manifest, 0, manifest.Count)
}

// Only migrations write content under a metadata transaction. Ordinary ingest
// prepares and stages objects first, then publishes their references atomically.
func migrateSnapshotContents(state *databaseState) error {
	keys := make([]string, 0, state.snapshotContents().Len())
	for key := range state.snapshotContents().All() {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := state.snapshotContents().Get(keys[i]).Summary, state.snapshotContents().Get(keys[j]).Summary
		if a.ServerSessionID != b.ServerSessionID {
			return a.ServerSessionID < b.ServerSessionID
		}
		return a.Version < b.Version
	})
	for _, key := range keys {
		record := state.snapshotContents().Get(key)
		if record.Content != nil {
			state.snapshotContents().Set(key, record)
			continue
		}
		history, err := reconstructNormalizedTurns(*state, record.Summary.ServerSessionID, record.Summary.Version)
		if err != nil {
			return err
		}
		source, err := reconstructSourceTurns(*state, record.Summary.ServerSessionID, record.Summary.Version)
		if err != nil {
			return err
		}
		visible, err := reconstructVisibleText(*state, record.Summary.ServerSessionID, record.Summary.Version)
		if err != nil {
			return err
		}
		for i := range source {
			source[i].Ordinal = i
		}
		current := map[visibleTextIdentity]contracts.NormalizedTurn{}
		for _, turn := range source {
			current[visibleTurnKey(turn)] = turn
		}
		for i, turn := range history {
			if native, ok := current[visibleTurnKey(turn)]; ok {
				history[i].Text = native.Text
			}
		}
		content, err := conversation.Build(conversationEntries(history, visible))
		if err != nil {
			return err
		}
		native, err := conversation.Build(nativeConversationEntries(source))
		if err != nil {
			return err
		}
		project := state.Sessions.Get(record.Summary.ServerSessionID).ProjectID
		for _, prepared := range []conversation.Prepared{content, native} {
			for hash, data := range prepared.Objects {
				objectKey := contentObjectKey(project, hash)
				if object, ok := state.ContentObjects.Lookup(objectKey); ok && object.Ready {
					continue
				}
				writer, ok := state.contents.(artifactWriter)
				if !ok {
					return errors.New("migration requires a writable content backend")
				}
				id, err := randomID("chunk")
				if err != nil {
					return err
				}
				object := contentObjectRecord{ProjectID: project, SHA256: hash, Size: len(data), ObjectKey: "objects/" + id, Ready: true}
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				err = writer.Write(ctx, artifactObject{Key: object.ObjectKey, SHA256: hash, Size: int64(len(data))}, data)
				cancel()
				if err != nil {
					return err
				}
				state.ContentObjects.Set(objectKey, object)
			}
		}
		record.Summary.IndexSHA256, err = hashNormalizedTurns(conversationTurns(conversationEntries(history, visible)))
		if err != nil {
			return err
		}
		record.Content, record.Source = &content.Manifest, &native.Manifest
		record.Turns, record.SourceTurns, record.VisibleTextDelta = nil, nil, nil
		record.SourceBaseVersion, record.Summary.SourceBaseVersion, record.Summary.BaseIndexVersion = 0, 0, 0
		state.snapshotContents().Set(key, record)
	}
	return nil
}
