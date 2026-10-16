package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xfey/coconet/adapters"
	"github.com/xfey/coconet/packages/contracts"
	bolt "go.etcd.io/bbolt"
)

func recordSnapshot(t *testing.T, s *store) []byte {
	t.Helper()
	var result []byte
	if err := s.db.View(func(tx *bolt.Tx) error { result = snapshotStateBytes(t, tx); return nil }); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestRecordTransactionsIndexesAndRollback(t *testing.T) {
	a := newTestAuthority(t)
	s := a.store
	var stats recordAccessStats
	s.observeRecords = func(value recordAccessStats) { stats = value }
	seed := func(state *databaseState) error {
		state.Sessions.Set("a", contracts.SessionRecord{ServerSessionID: "a", ProjectID: "p"})
		state.Sessions.Set("b", contracts.SessionRecord{ServerSessionID: "b", ProjectID: "other"})
		state.WorkNodes.Set("node", workNodeRecord{ProjectID: "p", Parents: []string{"initial"}})
		state.Memberships.Set("member", contracts.Membership{ProjectID: "p", Account: contracts.Account{AccountID: "owner"}})
		return nil
	}
	if err := s.update(seed); err != nil {
		t.Fatal(err)
	}
	before := recordSnapshot(t, s)
	rejected := errors.New("reject transaction")
	if err := s.update(func(state *databaseState) error {
		row := state.Sessions.Get("a")
		row.ProjectID = "other"
		state.Sessions.Set("a", row)
		state.Sessions.Delete("b")
		state.Sessions.Set("c", contracts.SessionRecord{ServerSessionID: "c", ProjectID: "p"})
		if got := state.Sessions.By("project_id", "other"); len(got) != 1 || got["a"].ProjectID != "other" {
			t.Fatal("index overlay", got)
		}
		if len(state.Sessions.By("project_id", "p")) != 1 || state.Sessions.Len() != 2 {
			t.Fatal("index/count overlay inconsistent")
		}
		state.WorkNodes.Get("node").Parents[0] = "rejected"
		markDashboardChanged(state, "p")
		return rejected
	}); !errors.Is(err, rejected) {
		t.Fatal(err)
	}
	if !bytes.Equal(before, recordSnapshot(t, s)) {
		t.Fatal("rollback changed business state or revision")
	}
	if err := s.update(func(state *databaseState) error {
		_ = state.Sessions.By("project_id", "p")
		if len(state.Memberships.By("account_id", "owner")) != 1 {
			t.Fatal("nested account index missing")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if stats.WrittenRecords != 0 {
		t.Fatalf("no-op rewrote records: %+v", stats)
	}
	if err := s.update(func(state *databaseState) error {
		state.WorkNodes.Get("node").Parents[0] = "committed"
		row := state.Sessions.Get("a")
		row.ProjectID = "other"
		state.Sessions.Set("a", row)
		state.Sessions.Delete("b")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	path := s.db.Path()
	if err := s.close(); err != nil {
		t.Fatal(err)
	}
	s, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	if err := s.view(func(state databaseState) error {
		if len(state.Sessions.By("project_id", "p")) != 0 || len(state.Sessions.By("project_id", "other")) != 1 || state.Sessions.Len() != 1 || state.Sessions.Has("b") {
			t.Fatal("durable index/count inconsistent")
		}
		if state.WorkNodes.Get("node").Parents[0] != "committed" {
			t.Fatal("nested mutation lost")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Replacing a collection must remove old durable rows and indexes as well.
	if err := s.update(func(state *databaseState) error {
		state.Sessions = newRecordCollectionWith(map[string]contracts.SessionRecord{"c": {ServerSessionID: "c", ProjectID: "third"}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.View(validateRecordSnapshot); err != nil {
		t.Fatal(err)
	}
	if err := s.view(func(state databaseState) error {
		if state.Sessions.Has("a") || state.Sessions.Len() != 1 || len(state.Sessions.By("project_id", "third")) != 1 {
			t.Fatal("collection replacement lost consistency")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRecordMigrationPreservesFixedHistoryAndBackup(t *testing.T) {
	a := newTestAuthority(t)
	actor, _ := issueTestIdentity(t, a, "Record author", "records@coconet.test")
	project := createContextProject(t, a, actor, "records", "request-record-project")
	id := "srv_records"
	seedContextSession(t, a, contextSessionFixture{ProjectID: project.ProjectID, ServerSessionID: id, Author: actor, AgentType: contracts.AgentClaude, Versions: 2})
	turns := []contracts.NormalizedTurn{contextTurn("u", 0, "user", "message", "visibleobjective"), contextTurn("tool", 1, "tool", "tool_result", strings.Repeat("opaque-tool-output ", 1000))}
	_, checkpoint := indexContextTurns(t, a, actor, project.ProjectID, id, 1, "request-record-v1", turns, adapters.ObservationCheckpoint{})
	turns = append(turns, contextTurn("a", 2, "assistant", "message", "finishedobjective"))
	indexContextTurns(t, a, actor, project.ProjectID, id, 2, "request-record-v2", turns, checkpoint)
	s := a.store
	if err := s.db.Update(func(tx *bolt.Tx) error {
		if err := tx.Bucket(metaBucket).Put(deploymentKey, []byte("test-deployment")); err != nil {
			return err
		}
		if err := tx.Bucket(metaBucket).Put(artifactBackendKey, []byte("test-artifacts")); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	expected := recordSnapshot(t, s)
	var old []byte
	if err := s.db.Update(func(tx *bolt.Tx) error {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(expected, &raw); err != nil {
			return err
		}
		delete(raw, "search_documents")
		var err error
		old, err = json.Marshal(raw)
		if err != nil {
			return err
		}
		if err := tx.Bucket(stateBucket).Put(stateKey, old); err != nil {
			return err
		}
		for _, name := range [][]byte{recordsBucket, recordIndexesBucket, recordCountsBucket} {
			if err := tx.DeleteBucket(name); err != nil {
				return err
			}
		}
		return tx.Bucket(metaBucket).Put(schemaKey, []byte{22})
	}); err != nil {
		t.Fatal(err)
	}
	path := s.db.Path()
	if err := s.close(); err != nil {
		t.Fatal(err)
	}
	// Read-only restore validation neither upgrades nor changes the source.
	if err := validateBboltSnapshotReadOnly(path, "test-deployment"); err != nil {
		t.Fatal(err)
	}
	if err := inspectBboltMetadata(path, "test-deployment", "test-artifacts"); err != nil {
		t.Fatal(err)
	}
	legacyBackup := filepath.Join(t.TempDir(), "before-upgrade.db")
	if err := backupBbolt(path, legacyBackup); err != nil {
		t.Fatal(err)
	}
	backupDB, err := bolt.Open(legacyBackup, 0400, &bolt.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := backupDB.View(func(tx *bolt.Tx) error {
		if tx.Bucket(metaBucket).Get(schemaKey)[0] != 22 || !bytes.Equal(old, tx.Bucket(stateBucket).Get(stateKey)) {
			t.Fatal("backup migrated legacy snapshot")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	backupDB.Close()
	raw, err := bolt.Open(path, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.View(func(tx *bolt.Tx) error {
		if tx.Bucket(metaBucket).Get(schemaKey)[0] != 22 || !bytes.Equal(tx.Bucket(stateBucket).Get(stateKey), old) {
			t.Fatal("read-only validation mutated legacy data")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	for i := 0; i < 2; i++ {
		s, err = openStore(path)
		if err != nil {
			t.Fatal(err)
		}
		a.store = s
		if !bytes.Equal(expected, recordSnapshot(t, s)) {
			t.Fatal("migration changed logical records, fixed versions or derived search")
		}
		if err := s.db.View(func(tx *bolt.Tx) error {
			if tx.Bucket(stateBucket).Get(stateKey) != nil || tx.Bucket(metaBucket).Get(schemaKey)[0] != databaseSchemaVersion || string(tx.Bucket(metaBucket).Get(artifactBackendKey)) != "test-artifacts" {
				t.Fatal("migration layout/identity")
			}
			return validateRecordSnapshot(tx)
		}); err != nil {
			t.Fatal(err)
		}
		for version := 1; version <= 2; version++ {
			if err := s.view(func(state databaseState) error {
				turns, err := reconstructNormalizedTurns(state, id, version)
				if err != nil {
					return err
				}
				if len(turns) != version+1 {
					t.Fatal("migration changed fixed compact history")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}

		if err := s.close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := inspectBboltMetadata(path, "test-deployment", "test-artifacts"); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "records-backup.db")
	if err := backupBbolt(path, backup); err != nil {
		t.Fatal(err)
	}
	if err := validateBboltSnapshotReadOnly(backup, "test-deployment"); err != nil {
		t.Fatal(err)
	}
	restored, err := openStore(backup)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.close()
	if !bytes.Equal(expected, recordSnapshot(t, restored)) {
		t.Fatal("backup lost logical state")
	}
}

func TestRecordMigrationFailureLeavesLegacyUntouched(t *testing.T) {
	for _, mode := range []string{"decode", "write"} {
		t.Run(mode, func(t *testing.T) {
			a := newTestAuthority(t)
			s := a.store
			old := []byte(`{"accounts":`)
			if mode == "write" {
				old = []byte(`{"accounts":{"":{"account_id":"invalid-empty-key"}}}`)
			}
			if err := s.db.Update(func(tx *bolt.Tx) error {
				if err := tx.Bucket(stateBucket).Put(stateKey, old); err != nil {
					return err
				}
				for _, name := range [][]byte{recordsBucket, recordIndexesBucket, recordCountsBucket} {
					if err := tx.DeleteBucket(name); err != nil {
						return err
					}
				}
				return tx.Bucket(metaBucket).Put(schemaKey, []byte{22})
			}); err != nil {
				t.Fatal(err)
			}
			path := s.db.Path()
			s.close()
			if opened, err := openStore(path); err == nil {
				opened.close()
				t.Fatal("invalid migration succeeded")
			}
			raw, err := bolt.Open(path, 0600, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			if err := raw.View(func(tx *bolt.Tx) error {
				if tx.Bucket(recordsBucket) != nil || tx.Bucket(recordIndexesBucket) != nil || tx.Bucket(recordCountsBucket) != nil || tx.Bucket(metaBucket).Get(schemaKey)[0] != 22 || !bytes.Equal(old, tx.Bucket(stateBucket).Get(stateKey)) {
					t.Fatal("failed migration changed source")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRecordSnapshotRejectsCorruption(t *testing.T) {
	for _, mode := range []string{"record", "missing-index", "orphan-index", "count", "failure-count", "failure-json"} {
		t.Run(mode, func(t *testing.T) {
			a := newTestAuthority(t)
			s := a.store
			if err := s.update(func(state *databaseState) error {
				state.Sessions.Set("s", contracts.SessionRecord{ServerSessionID: "s", ProjectID: "p"})
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := s.db.Update(func(tx *bolt.Tx) error {
				index := tx.Bucket(recordIndexesBucket).Bucket([]byte("sessions")).Bucket([]byte("project_id"))
				switch mode {
				case "record":
					return tx.Bucket(recordsBucket).Bucket([]byte("sessions")).Put([]byte("s"), []byte(`{`))
				case "missing-index":
					return index.Delete(append(recordIndexPrefix("p"), "s"...))
				case "orphan-index":
					return index.Put(append(recordIndexPrefix("p"), "orphan"...), []byte{})
				case "count":
					return tx.Bucket(recordCountsBucket).Put([]byte("sessions"), make([]byte, 8))
				case "failure-count":
					return tx.Bucket(recordCountsBucket).Put([]byte("recent_failures"), make([]byte, 8))
				case "failure-json":
					return tx.Bucket(recordsBucket).Bucket([]byte("recent_failures")).Put([]byte("all"), []byte(`{`))
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := s.db.View(validateRecordSnapshot); err == nil {
				t.Fatal("corrupt snapshot accepted")
			}
			if mode == "record" {
				if err := s.view(func(state databaseState) error { _ = state.Sessions.Get("s"); return nil }); err == nil {
					t.Fatal("decode error hidden")
				}
				if err := s.update(func(state *databaseState) error { state.Sessions.Delete("s"); return nil }); err == nil {
					t.Fatal("corrupt record silently overwritten")
				}
			}
		})
	}
}
