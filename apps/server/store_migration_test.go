package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
	bolt "go.etcd.io/bbolt"
)

func TestStoreMigratesSchemaOnePreservingAccounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema-one.db")
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	legacy := newDatabaseState()
	owner := contracts.Account{SchemaVersion: 1, AccountID: "acct_legacyowner", DisplayName: "Legacy Owner", Email: "owner@legacy.test", CreatedAt: "2026-08-24T09:00:00Z"}
	pending := contracts.Account{SchemaVersion: 1, AccountID: "acct_legacypending", DisplayName: "Legacy Pending", Email: "pending@legacy.test", CreatedAt: "2026-08-24T09:00:00Z"}
	legacy.Accounts.Set(owner.AccountID, owner)
	legacy.Accounts.Set(pending.AccountID, pending)
	legacy.Projects.Set("prj_legacyfixture", contracts.Project{SchemaVersion: 1, ProjectID: "prj_legacyfixture", Name: "legacy", OwnerID: owner.AccountID, Status: contracts.ProjectActive, CreatedAt: owner.CreatedAt})
	legacy.Memberships.Set(membershipKey("prj_legacyfixture", owner.AccountID), contracts.Membership{SchemaVersion: 1, ProjectID: "prj_legacyfixture", Account: owner, Role: contracts.RoleOwner, Status: contracts.MembershipActive, CreatedAt: owner.CreatedAt, UpdatedAt: owner.CreatedAt})
	legacy.Memberships.Set(membershipKey("prj_legacyfixture", pending.AccountID), contracts.Membership{SchemaVersion: 1, ProjectID: "prj_legacyfixture", Account: pending, Role: contracts.RoleMember, Status: contracts.MembershipPending, CreatedAt: pending.CreatedAt, UpdatedAt: pending.CreatedAt})
	legacy.JoinRequests.Set("join_legacyfixture", contracts.JoinRequest{SchemaVersion: 1, JoinRequestID: "join_legacyfixture", ProjectID: "prj_legacyfixture", Account: pending, Status: contracts.JoinRequestPending, CreatedAt: pending.CreatedAt})
	manifest := contracts.ArtifactManifest{
		SchemaVersion: 1, ArtifactSHA256: strings.Repeat("a", 64), ArtifactSize: 17,
		ArtifactMediaType: "application/x-coddis-codex-jsonl", ManifestSHA256: strings.Repeat("b", 64),
		ChunkingSchemaVersion: 1, Chunks: []contracts.ArtifactChunk{{SHA256: strings.Repeat("c", 64), RawSize: 17}},
	}
	sessionID := "sess_legacycontent01"
	legacy.Sessions.Set(sessionID, contracts.SessionRecord{
		SchemaVersion: 1, ServerSessionID: sessionID, ProjectID: "prj_legacyfixture", AuthorAccountID: owner.AccountID,
		AgentType: contracts.AgentCodex, NativeSessionID: "native-legacy-content", ActivityState: contracts.ActivityEnded,
		LatestCommittedVersion: 1, LatestIndexedVersion: 1, LatestSyncedAt: owner.CreatedAt,
		RootServerSessionID: sessionID, CreatedAt: owner.CreatedAt, UpdatedAt: owner.CreatedAt,
	})
	legacy.SessionKeys.Set(uniqueSessionKey("prj_legacyfixture", contracts.AgentCodex, owner.AccountID, "native-legacy-content"), sessionID)
	legacy.Snapshots.Set(versionKey(sessionID, 1), contracts.Snapshot{
		SchemaVersion: 1, ServerSessionID: sessionID, Version: 1, Manifest: manifest,
		ObservedActivity: contracts.ActivityEnded, SourceObservedAt: owner.CreatedAt,
		AdapterName: "codex-jsonl", AdapterVersion: "1", CommittedAt: owner.CreatedAt,
	})
	legacy.Chunks.Set(manifest.Chunks[0].SHA256, chunkRecord{
		ProjectID: "prj_legacyfixture", SHA256: manifest.Chunks[0].SHA256, RawSize: 17,
		ObjectKey: "objects/legacy-content", VerifiedAt: owner.CreatedAt,
	})
	legacy.snapshotContents().Set(versionKey(sessionID, 1), normalizedVersionRecord{
		Summary: contracts.SessionIndexRecord{
			SchemaVersion: 1, ServerSessionID: sessionID, Version: 1, ToCursor: "cursor-legacy",
			TurnCount: 1, IndexSHA256: strings.Repeat("d", 64), IndexedAt: owner.CreatedAt,
		},
		Turns: []contracts.NormalizedTurn{{
			TurnID: "turn-legacy", Ordinal: 1, Role: "user", Kind: "message", Text: "preserved content",
			ContentHash: strings.Repeat("e", 64), OccurredAt: owner.CreatedAt,
		}},
	})
	legacy.AuditEvents.Set("audit_legacycontent", contracts.AuditEvent{
		SchemaVersion: 1, AuditEventID: "audit_legacycontent", RequestID: "req_legacycontent",
		ProjectID: "prj_legacyfixture", ActorID: owner.AccountID, Action: "session.commit",
		TargetID: sessionID, Outcome: "ok", OccurredAt: owner.CreatedAt,
	})
	preservedBefore := preservedMigrationState(t, legacy)
	oldState := map[string]any{}
	content, err := json.Marshal(legacy)
	if err == nil {
		err = json.Unmarshal(content, &oldState)
	}
	if err != nil {
		t.Fatal(err)
	}
	content, err = json.Marshal(oldState)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(metaBucket)
		if err != nil {
			return err
		}
		state, err := tx.CreateBucketIfNotExists(stateBucket)
		if err != nil {
			return err
		}
		if err := setFixtureSchema(t, tx, []byte{1}); err != nil {
			return err
		}
		return state.Put(stateKey, content)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	storage, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = storage.close() }()
	if err := storage.view(func(state databaseState) error {
		project := state.Projects.Get("prj_legacyfixture")
		ownerMembership := state.Memberships.Get(membershipKey(project.ProjectID, owner.AccountID))
		if project.CreatedBy != owner.AccountID || ownerMembership.Role != contracts.RoleMember || ownerMembership.Status != contracts.MembershipActive {
			t.Fatalf("legacy owner did not become an equal active member: %#v %#v", project, ownerMembership)
		}
		if _, exists := state.Memberships.Lookup(membershipKey(project.ProjectID, pending.AccountID)); exists || state.JoinRequests.Get("join_legacyfixture").Status != contracts.JoinRequestRejected {
			t.Fatalf("legacy pending request was not rejected: %#v %#v", state.Memberships.All(), state.JoinRequests.All())
		}
		index := state.snapshotContents().Get(versionKey(sessionID, 1))
		history, historyErr := reconstructNormalizedTurns(state, sessionID, 1)
		source, sourceErr := reconstructSourceTurns(state, sessionID, 1)
		if index.Summary.SourceEpoch != 1 || sourceErr != nil || historyErr != nil || len(source) != 1 || len(history) != 1 || history[0].Ordinal != 0 {
			t.Fatalf("legacy normalized index was not migrated to cumulative history: %#v", index)
		}
		if preservedAfter := preservedMigrationState(t, state); !bytes.Equal(preservedBefore, preservedAfter) {
			t.Fatal("schema migration changed Session, artifact, index, lineage, author, or audit metadata")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := storage.db.View(func(tx *bolt.Tx) error {
		if value := tx.Bucket(metaBucket).Get(schemaKey); len(value) != 1 || value[0] != databaseSchemaVersion {
			t.Fatalf("schema marker = %v, want %d", value, databaseSchemaVersion)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := storage.close(); err != nil {
		t.Fatal(err)
	}
	storage, err = openStore(path)
	if err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	if err := storage.view(func(state databaseState) error {
		project := state.Projects.Get("prj_legacyfixture")
		ownerMembership := state.Memberships.Get(membershipKey(project.ProjectID, owner.AccountID))
		if project.CreatedBy != owner.AccountID || ownerMembership.Role != contracts.RoleMember {
			t.Fatalf("repeat migration changed equal membership state: %#v %#v", project, ownerMembership)
		}
		if _, exists := state.Memberships.Lookup(membershipKey(project.ProjectID, pending.AccountID)); exists || state.JoinRequests.Get("join_legacyfixture").Status != contracts.JoinRequestRejected {
			t.Fatal("repeat migration restored legacy pending authority")
		}
		if preservedAfter := preservedMigrationState(t, state); !bytes.Equal(preservedBefore, preservedAfter) {
			t.Fatal("repeat migration changed preserved content metadata")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestStoreMigratesSchemaThreePreservingIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema-three.db")
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	legacy := newDatabaseState()
	legacy.Projects.Set("prj_preserved", contracts.Project{
		SchemaVersion: contracts.SchemaVersion,
		ProjectID:     "prj_preserved",
		Name:          "preserved",
		Status:        contracts.ProjectActive,
	})
	document, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(document, &raw); err != nil {
		t.Fatal(err)
	}
	document, err = json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(metaBucket)
		if err != nil {
			return err
		}
		state, err := tx.CreateBucketIfNotExists(stateBucket)
		if err != nil {
			return err
		}
		if err := setFixtureSchema(t, tx, []byte{3}); err != nil {
			return err
		}
		return state.Put(stateKey, document)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	storage, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.close()
	if err := storage.view(func(state databaseState) error {
		if _, ok := state.Projects.Lookup("prj_preserved"); !ok {
			t.Fatal("schema migration removed persistent Project data")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestStoreMigratesSchemaFourWithEmptySemanticProjectionState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema-four.db")
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	legacy := newDatabaseState()
	legacy.Projects.Set("prj_schemafour01", contracts.Project{
		SchemaVersion: 1, ProjectID: "prj_schemafour01", Name: "schema four", Status: contracts.ProjectActive,
	})
	document, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(document, &raw); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"work_extractions", "work_nodes", "current_work_nodes", "semantic_projection_jobs", "projected_session_versions"} {
		delete(raw, field)
	}
	document, err = json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(metaBucket)
		if err != nil {
			return err
		}
		state, err := tx.CreateBucketIfNotExists(stateBucket)
		if err != nil {
			return err
		}
		if err := setFixtureSchema(t, tx, []byte{4}); err != nil {
			return err
		}
		return state.Put(stateKey, document)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	storage, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.close()
	if err := storage.view(func(state databaseState) error {
		if _, ok := state.Projects.Lookup("prj_schemafour01"); !ok {
			t.Fatal("schema 4 migration removed the existing Project")
		}
		if state.WorkStates == nil || state.WorkNodes == nil || state.ProjectionJobs == nil {
			t.Fatalf("schema 4 migration left semantic maps nil: %#v", state)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func preservedMigrationState(t *testing.T, state databaseState) []byte {
	t.Helper()
	versions := map[string]contracts.Snapshot{}
	for key, v := range state.Snapshots.All() {
		v.Conversation = nil
		versions[key] = v
	}
	sessions := map[string]contracts.SessionRecord{}
	for key, s := range state.Sessions.All() {
		s.SyncCheckpoint = nil
		sessions[key] = s
	}
	content, err := json.Marshal(struct {
		Sessions    map[string]contracts.SessionRecord    `json:"sessions"`
		SessionKeys map[string]string                     `json:"session_keys"`
		Versions    map[string]contracts.Snapshot         `json:"versions"`
		Manifests   map[string]contracts.ArtifactManifest `json:"manifests"`
		Chunks      map[string]chunkRecord                `json:"chunks"`
		AuditEvents map[string]contracts.AuditEvent       `json:"audit_events"`
	}{
		Sessions: sessions, SessionKeys: state.SessionKeys.All(), Versions: versions,
		Chunks: state.Chunks.All(), AuditEvents: state.AuditEvents.All(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return content
}
