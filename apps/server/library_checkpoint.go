package main

import (
	"errors"

	"github.com/xfey/coconet/packages/contracts"
)

// Pending collections retain the original selection and metadata, but are not
// Library entries until the fixed graph association commits atomically.
type libraryCheckpointRecord struct {
	RetryCount    int                    `json:"retry_count,omitempty"`
	Entry         contracts.LibraryEntry `json:"entry"`
	Stage         *semanticStage         `json:"stage,omitempty"`
	Attempts      int                    `json:"attempts"`
	InvalidCount  int                    `json:"invalid_count,omitempty"`
	Suspended     bool                   `json:"suspended,omitempty"`
	NextAttemptAt string                 `json:"next_attempt_at,omitempty"`
	LastError     string                 `json:"last_error,omitempty"`
}

func validLibraryNode(state databaseState, entry contracts.LibraryEntry) bool {
	node, exists := state.WorkNodes.Lookup(entry.WorkNodeID)
	return exists && node.WorkNodeID == entry.WorkNodeID && entry.WorkNodeID != "" && node.ProjectID == entry.ProjectID && node.ServerSessionID == entry.Source.ServerSessionID && node.SourceVersion == entry.Source.Version && node.State == contracts.WorkNodeSealed
}

func exactLibraryNode(state databaseState, entry contracts.LibraryEntry) string {
	selected := ""
	for id, node := range state.WorkNodes.By("server_session_id", entry.Source.ServerSessionID) {
		if node.WorkNodeID == id && node.ProjectID == entry.ProjectID && node.ServerSessionID == entry.Source.ServerSessionID && node.SourceVersion == entry.Source.Version && (selected == "" || id < selected) {
			selected = id
		}
	}
	return selected
}

func publishLibraryCheckpoint(state *databaseState, key, nodeID, now string) error {
	record, exists := state.LibraryCheckpoints.Lookup(key)
	if !exists {
		return nil
	}
	node, exists := state.WorkNodes.Lookup(nodeID)
	if !exists {
		return errors.New("Library checkpoint node is missing")
	}
	if node.State == contracts.WorkNodeCurrent {
		if state.currentNodes().Get(node.ServerSessionID) != nodeID {
			return errors.New("Library checkpoint current node index is inconsistent")
		}
		sealCurrentWorkNode(state, node.ServerSessionID, laterTimestamp(node.UpdatedAt, now))
	}
	entry := record.Entry
	entry.WorkNodeID = nodeID
	if key != versionKey(entry.Source.ServerSessionID, entry.Source.Version) || !validLibrarySource(*state, entry) || !validLibraryNode(*state, entry) {
		return errors.New("Library checkpoint source is inconsistent")
	}
	recordCheckpointActivity(state, state.WorkNodes.Get(nodeID), now, false)
	state.LibraryEntries.Set(key, entry)
	markDashboardChanged(state, entry.ProjectID)
	recordLibraryActivity(state, entry, now, false)
	state.LibraryCheckpoints.Delete(key)
	ensureLibraryTagging(state, key, entry, now)
	return nil
}

func migrateLibraryCheckpoints(state *databaseState) error {
	normalizeDatabaseState(state)
	for key, entry := range state.LibraryEntries.All() {
		if validLibraryNode(*state, entry) {
			continue
		}
		entry.WorkNodeID = ""
		state.LibraryCheckpoints.Set(key, libraryCheckpointRecord{Entry: entry, NextAttemptAt: entry.CollectedAt})
		state.LibraryEntries.Delete(key)
		// Migration needs no model when an exact, valid source node already exists.
		if nodeID := exactLibraryNode(*state, entry); nodeID != "" && validLibrarySource(*state, entry) {
			if err := publishLibraryCheckpoint(state, key, nodeID, entry.CollectedAt); err != nil {
				return err
			}
		}
	}
	return nil
}
