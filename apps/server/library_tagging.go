package main

import (
	"github.com/xfey/coconet/packages/contracts"
)

type libraryTaggingRecord struct {
	RetryCount    int              `json:"retry_count,omitempty"`
	EntryID       string           `json:"entry_id"`
	State         string           `json:"state"`
	Tags          []string         `json:"tags,omitempty"`
	GeneratedAt   string           `json:"generated_at,omitempty"`
	Attempts      int              `json:"attempts"`
	InvalidCount  int              `json:"invalid_count,omitempty"`
	NextAttemptAt string           `json:"next_attempt_at,omitempty"`
	LastError     string           `json:"last_error,omitempty"`
	Lease         projectionLease  `json:"lease"`
	InputSHA256   string           `json:"input_sha256,omitempty"`
	PromptSHA256  string           `json:"prompt_sha256,omitempty"`
	InputVersion  string           `json:"input_version,omitempty"`
	Model         string           `json:"model,omitempty"`
	Coverage      semanticCoverage `json:"coverage"`
}

func ensureLibraryTagging(state *databaseState, key string, entry contracts.LibraryEntry, now string) {
	if exists := state.LibraryTaggings.Has(key); !exists {
		state.LibraryTaggings.Set(key, libraryTaggingRecord{EntryID: entry.EntryID, State: "pending", NextAttemptAt: now})
	}
}

func validLibrarySource(state databaseState, entry contracts.LibraryEntry) bool {
	key := versionKey(entry.Source.ServerSessionID, entry.Source.Version)
	session, exists := state.Sessions.Lookup(entry.Source.ServerSessionID)
	committed := state.Snapshots.Has(key)
	indexed := state.snapshotContents().Has(key)
	projectExists := state.Projects.Has(entry.ProjectID)
	return exists && projectExists && session.ProjectID == entry.ProjectID && session.AuthorAccountID == entry.SubmittedBy && entry.EntryID != "" && entry.Source.Version > 0 && committed && indexed
}
