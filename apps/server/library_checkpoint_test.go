package main

import (
	"fmt"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
)

// Seed an archived fixed node for metadata and complete backup fixtures.
func seedLibraryNode(t *testing.T, a *authority, project string, source contracts.SessionReadReference, nodeState contracts.WorkNodeState) string {
	t.Helper()
	id := fmt.Sprintf("work_%s_%d", source.ServerSessionID, source.Version)
	if err := a.store.update(func(state *databaseState) error {
		now := a.timestamp()
		state.WorkNodes.Set(id, workNodeRecord{WorkNodeID: id, ProjectID: project, ServerSessionID: source.ServerSessionID, SourceVersion: source.Version, State: nodeState, Objective: "Fixture work", WorkStartedAt: now, WorkUpdatedAt: now, CreatedAt: now, UpdatedAt: now, Parents: []string{}})
		if nodeState == contracts.WorkNodeCurrent {
			state.currentNodes().Set(source.ServerSessionID, id)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

// Ordinary no-change outcomes do not exempt explicitly collected Versions.

// Live V3 may consume the fixed V2 checkpoint, while Session items continue
// through the ordinary extraction path.

// Graph input changes after inference starts; a retry must reload graph only.
