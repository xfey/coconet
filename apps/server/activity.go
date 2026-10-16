package main

import (
	"sort"
	"time"

	"github.com/xfey/coconet/packages/contracts"
)

const (
	activityCheckpoint = "checkpoint"
	activityLibrary    = "library_collected"
	activityFork       = "fork_registered"
)

// Append inside the fact's transaction. IDs make retries idempotent; the
// per-project append order is independent of device clocks and late imports.
func appendProjectActivity(state *databaseState, event contracts.ProjectActivity, now string) {
	if state.activityMigration {
		return
	}
	if exists := state.ActivityEvents.Has(event.ID); exists {
		return
	}
	if !state.storageTime.IsZero() {
		now = state.storageTime.UTC().Format(time.RFC3339Nano)
	}
	event.RecordedAt = now
	state.ActivityEvents.Set(event.ID, event)
	state.ActivityOrder.Set(event.ProjectID, append(state.ActivityOrder.Get(event.ProjectID), event.ID))
}

func activityAuthor(state databaseState, accountID string) contracts.SessionAuthor {
	return contracts.SessionAuthor{AccountID: accountID, DisplayName: state.Accounts.Get(accountID).DisplayName}
}

func activityNode(state databaseState, node workNodeRecord) contracts.ProjectWorkNode {
	var evidence *contracts.WorkEvidence
	if node.Evidence != nil {
		copy := *node.Evidence
		evidence = &copy
	}
	return contracts.ProjectWorkNode{
		WorkNodeID: node.WorkNodeID, Objective: node.Objective, Summary: node.Summary, Approach: node.Approach, State: node.State,
		Author:   activityAuthor(state, state.Sessions.Get(node.ServerSessionID).AuthorAccountID),
		Source:   contracts.SessionReadReference{ServerSessionID: node.ServerSessionID, Version: node.SourceVersion},
		WorkTime: contracts.WorkTimeRange{Start: node.WorkStartedAt, End: node.WorkUpdatedAt},
		Parents:  append([]string{}, node.Parents...), Evidence: evidence,
	}
}

func recordCheckpointActivity(state *databaseState, node workNodeRecord, now string, historical bool) {
	if state.pendingActivityCheckpoints != nil {
		state.pendingActivityCheckpoints[node.WorkNodeID] = state.pendingActivityCheckpoints[node.WorkNodeID] || historical
		return
	}
	if node.State != contracts.WorkNodeSealed {
		return
	}
	snapshot := activityNode(*state, node)
	appendProjectActivity(state, contracts.ProjectActivity{ID: "activity_checkpoint_" + node.WorkNodeID, ProjectID: node.ProjectID,
		Kind: activityCheckpoint, Historical: historical, Actor: snapshot.Author, Source: snapshot.Source, Node: &snapshot}, now)
}

func recordLibraryActivity(state *databaseState, entry contracts.LibraryEntry, now string, historical bool) {
	node := activityNode(*state, state.WorkNodes.Get(entry.WorkNodeID))
	entry.Tagging = nil // Tags are current discovery metadata, not a collection-time fact.
	appendProjectActivity(state, contracts.ProjectActivity{ID: "activity_library_" + entry.EntryID, ProjectID: entry.ProjectID,
		Kind: activityLibrary, Historical: historical, Actor: activityAuthor(*state, entry.SubmittedBy),
		Source: entry.Source, Node: &node, LibraryEntry: &entry}, now)
}

func recordForkActivity(state *databaseState, child contracts.SessionRecord, now string, historical bool) {
	appendProjectActivity(state, contracts.ProjectActivity{ID: "activity_fork_" + child.ServerSessionID, ProjectID: child.ProjectID,
		Kind: activityFork, Historical: historical, Actor: activityAuthor(*state, child.AuthorAccountID),
		Source:         contracts.SessionReadReference{ServerSessionID: child.ParentServerSessionID, Version: child.ParentSessionVersion},
		ChildSessionID: child.ServerSessionID}, now)
}

// Old state proves fixed checkpoints and lineage, not their complete event
// history. Record an explicitly historical baseline without inventing times.
func migrateProjectActivity(state *databaseState) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	ids := make([]string, 0, state.WorkNodes.Len())
	for id := range state.WorkNodes.All() {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		recordCheckpointActivity(state, state.WorkNodes.Get(id), now, true)
	}
	ids = ids[:0]
	for id := range state.LibraryEntries.All() {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		entry := state.LibraryEntries.Get(id)
		if validLibraryNode(*state, entry) {
			recordLibraryActivity(state, entry, now, true)
		}
	}
	ids = ids[:0]
	for id, session := range state.Sessions.All() {
		if session.ParentServerSessionID != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		recordForkActivity(state, state.Sessions.Get(id), now, true)
	}
}

type activityCursor struct {
	ProjectID string `json:"project_id"`
	Before    int    `json:"before"`
}
