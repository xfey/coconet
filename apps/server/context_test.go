package main

import (
	"strings"
	"testing"
	"time"

	"github.com/xfey/coconet/adapters"
	"github.com/xfey/coconet/packages/contracts"
)

type contextSessionFixture struct {
	ProjectID       string
	ServerSessionID string
	Author          contracts.Account
	AgentType       contracts.AgentType
	Versions        int
	Activity        contracts.ActivityState
	RootSessionID   string
	ParentSessionID string
	ParentVersion   int
}

func createContextProject(t *testing.T, a *authority, owner contracts.Account, name, requestID string) contracts.Project {
	t.Helper()
	access, err := a.createProject(owner, contracts.CreateProjectRequest{SchemaVersion: 1, RequestID: requestID, Name: name})
	if err != nil || access.Project == nil {
		t.Fatalf("create context Project: %#v %v", access, err)
	}
	return *access.Project
}

func addContextMember(t *testing.T, a *authority, projectID string, account contracts.Account) {
	t.Helper()
	if err := a.store.update(func(state *databaseState) error {
		now := a.timestamp()
		state.Memberships.Set(membershipKey(projectID, account.AccountID), contracts.Membership{
			SchemaVersion: 1, ProjectID: projectID, Account: account, Role: contracts.RoleMember,
			Status: contracts.MembershipActive, CreatedAt: now, UpdatedAt: now,
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func seedContextSession(t *testing.T, a *authority, fixture contextSessionFixture) {
	t.Helper()
	if fixture.Activity == "" {
		fixture.Activity = contracts.ActivityUnknown
	}
	if fixture.RootSessionID == "" {
		fixture.RootSessionID = fixture.ServerSessionID
	}
	now := a.timestamp()
	if err := a.store.update(func(state *databaseState) error {
		state.Sessions.Set(fixture.ServerSessionID, contracts.SessionRecord{
			SchemaVersion: 1, ServerSessionID: fixture.ServerSessionID, ProjectID: fixture.ProjectID,
			AuthorAccountID: fixture.Author.AccountID, AgentType: fixture.AgentType, NativeSessionID: "native-" + fixture.ServerSessionID,
			ActivityState: fixture.Activity, LatestCommittedVersion: fixture.Versions, LatestSyncedAt: now,
			RootServerSessionID: fixture.RootSessionID, ParentServerSessionID: fixture.ParentSessionID,
			ParentSessionVersion: fixture.ParentVersion, CreatedAt: now, UpdatedAt: now,
		})
		for version := 1; version <= fixture.Versions; version++ {
			state.Snapshots.Set(versionKey(fixture.ServerSessionID, version), contracts.Snapshot{
				SchemaVersion: 1, ServerSessionID: fixture.ServerSessionID, Version: version,
				SourceObservedAt: now, CommittedAt: now,
			})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func contextTurn(id string, ordinal int, role, kind, text string) contracts.NormalizedTurn {
	return contracts.NormalizedTurn{
		TurnID: id, Ordinal: ordinal, Role: role, Kind: kind, Text: text,
		OccurredAt:  time.Date(2026, 8, 24, 10, ordinal, 0, 0, time.UTC).Format(time.RFC3339),
		ContentHash: adapters.Digest(strings.Join([]string{role, kind, text}, "\x00")),
	}
}

func contextTurnWithPaths(id string, ordinal int, role, kind, text, relativePath string) contracts.NormalizedTurn {
	turn := contextTurn(id, ordinal, role, kind, text)
	turn.RelativePaths = []string{relativePath}
	return turn
}

func contextIndexRequest(projectID, sessionID string, version int, requestID string, normalized contracts.NormalizedObservation) contracts.IndexSessionRequest {
	return contracts.IndexSessionRequest{
		SchemaVersion: 1, RequestID: requestID, ProjectID: projectID,
		ServerSessionID: sessionID, Version: version, Normalized: normalized,
	}
}

func indexContextTurns(t *testing.T, a *authority, actor contracts.Account, projectID, sessionID string, version int, requestID string, turns []contracts.NormalizedTurn, checkpoint adapters.ObservationCheckpoint) (contracts.IndexSessionResponse, adapters.ObservationCheckpoint) {
	t.Helper()
	observation, next := adapters.ApplyCheckpoint(turns, checkpoint)
	summary := contracts.SessionIndexRecord{SchemaVersion: 1, ServerSessionID: sessionID, Version: version, SourceEpoch: 1, ToCursor: observation.ToCursor, TurnCount: len(turns), IndexedAt: a.timestamp()}
	if err := a.store.update(func(state *databaseState) error {
		state.snapshotContents().Set(versionKey(sessionID, version), normalizedVersionRecord{Summary: summary, Reset: true, Turns: turns, SourceTurns: turns})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return contracts.IndexSessionResponse{SchemaVersion: 1, Status: contracts.StatusOK, Index: summary}, next
}
