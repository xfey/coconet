package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
	bolt "go.etcd.io/bbolt"
)

func TestWorkspaceRegistrySnapshotsAndAccess(t *testing.T) {
	a := newTestAuthority(t)
	owner, token := issueTestIdentity(t, a, "Owner", "workspace-owner@test.local")
	teammate, otherToken := issueTestIdentity(t, a, "Teammate", "workspace-team@test.local")
	outsider, _ := issueTestIdentity(t, a, "Outsider", "workspace-other@test.local")
	created, err := a.createProject(owner, contracts.CreateProjectRequest{SchemaVersion: 1, RequestID: "workspace-create-1", Name: "shared"})
	if err != nil {
		t.Fatal(err)
	}
	project := created.Project.ProjectID
	if err := a.store.update(func(s *databaseState) error {
		member := s.Memberships.Get(membershipKey(project, owner.AccountID))
		member.Account = teammate
		s.Memberships.Set(membershipKey(project, teammate.AccountID), member)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	report := contracts.WorkspaceReport{SchemaVersion: 1, InstallationID: "workspace_" + strings.Repeat("a", 32), DeviceName: "Same computer name", Revision: 1, Folders: []contracts.WorkspaceFolder{{ProjectID: project, Path: "/work/中文 folder", ConnectedAt: "2026-09-15T01:00:00Z"}, {ProjectID: project, Path: "/work/second", ConnectedAt: "2026-09-15T01:00:00Z"}, {ProjectID: "coco_unrelated", Path: "/private/path", ConnectedAt: "2026-09-15T01:00:00Z"}}}
	submit := func(actor contracts.Account, token string, input contracts.WorkspaceReport) {
		t.Helper()
		if _, err := a.reportWorkspaces(actor, hashToken(token), input); err != nil {
			t.Fatal(err)
		}
	}
	read := func(count int) contracts.ProjectFoldersResponse {
		t.Helper()
		page, err := a.projectFolders(owner, project, "", 0)
		if err != nil || len(page.Folders) != count {
			t.Fatalf("folders=%d want=%d err=%v", len(page.Folders), count, err)
		}
		return page
	}
	submit(owner, token, report)
	submit(owner, token, report)
	read(2)
	// Distinct installations and users must survive identical host names/paths.
	second := report
	second.InstallationID = "workspace_" + strings.Repeat("b", 32)
	submit(owner, token, second)
	submit(teammate, otherToken, report)
	read(6)
	page, err := a.projectFolders(owner, project, "", 2)
	if err != nil || len(page.Folders) != 2 || page.NextCursor == "" {
		t.Fatal(page, err)
	}
	seen := map[string]bool{}
	for {
		for _, folder := range page.Folders {
			if seen[folder.ID] || folder.Path == "/private/path" {
				t.Fatal("duplicate or private path")
			}
			seen[folder.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		page, err = a.projectFolders(owner, project, page.NextCursor, 2)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 6 {
		t.Fatal(seen)
	}
	if _, err := a.projectFolders(outsider, project, "", 0); err == nil {
		t.Fatal("outsider read directory paths")
	}
	for _, cursor := range []string{"/work/private", strings.Repeat("z", 64)} {
		if _, err := a.projectFolders(owner, project, cursor, 1); err == nil {
			t.Fatal("invalid cursor accepted")
		}
	}
	changed := report
	changed.DeviceName = "tampered"
	if _, err := a.reportWorkspaces(owner, hashToken(token), changed); err == nil {
		t.Fatal("same revision overwrote snapshot")
	}
	empty := report
	empty.Revision = 2
	empty.Folders = []contracts.WorkspaceFolder{}
	submit(owner, token, empty)
	read(4)
	if _, err := a.reportWorkspaces(owner, hashToken(token), report); err == nil {
		t.Fatal("delayed report resurrected directory")
	}
	if err := a.store.update(func(s *databaseState) error { s.Tokens.Delete(hashToken(otherToken)); return nil }); err != nil {
		t.Fatal(err)
	}
	read(2)
	if _, err := a.reportWorkspaces(teammate, hashToken(otherToken), report); err == nil {
		t.Fatal("revoked token accepted")
	}
	// Rotating a credential retains installation identity without duplicate rows.
	replacement := "replacement-token-with-enough-entropy"
	if err := a.store.update(func(s *databaseState) error {
		s.Tokens.Set(hashToken(replacement), s.Tokens.All()[hashToken(token)])
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	second.Revision++
	submit(owner, replacement, second)
	read(2)
	path := a.store.db.Path()
	if err := a.store.close(); err != nil {
		t.Fatal(err)
	}
	a.store, err = openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.store.close() })
	read(2)

	if err := a.store.update(func(s *databaseState) error {
		s.Memberships.Delete(membershipKey(project, owner.AccountID))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	page, err = a.projectFolders(teammate, project, "", 0)
	if err != nil || len(page.Folders) != 0 {
		t.Fatal("former member remains visible", err)
	}
}

func TestWorkspaceRegistryValidationAndHTTP(t *testing.T) {
	a := newTestAuthority(t)
	actor, token := issueTestIdentity(t, a, "Owner", "workspace-http@test.local")
	base := contracts.WorkspaceReport{SchemaVersion: 1, InstallationID: "workspace_" + strings.Repeat("c", 32), DeviceName: "Laptop", Revision: 1, Folders: []contracts.WorkspaceFolder{}}
	mutations := []func(*contracts.WorkspaceReport){func(r *contracts.WorkspaceReport) { r.Revision = 0 }, func(r *contracts.WorkspaceReport) { r.Revision = 9007199254740992 }, func(r *contracts.WorkspaceReport) { r.InstallationID = "../invalid" }, func(r *contracts.WorkspaceReport) { r.DeviceName = "bad\nname" }, func(r *contracts.WorkspaceReport) { r.Folders = nil }, func(r *contracts.WorkspaceReport) {
		r.Folders = []contracts.WorkspaceFolder{{ProjectID: "p", Path: "relative", ConnectedAt: "2026-09-15T01:00:00Z"}}
	}, func(r *contracts.WorkspaceReport) {
		f := contracts.WorkspaceFolder{ProjectID: "p", Path: "/same", ConnectedAt: "2026-09-15T01:00:00Z"}
		r.Folders = []contracts.WorkspaceFolder{f, f}
	}}
	for i, mutate := range mutations {
		r := base
		mutate(&r)
		if _, err := a.reportWorkspaces(actor, hashToken(token), r); err == nil {
			t.Fatalf("invalid input %d accepted", i)
		}
	}
	identity, err := generateDeploymentIdentity()
	if err != nil {
		t.Fatal(err)
	}
	h := newHandler("fixture", identity, a)
	body, _ := json.Marshal(base)
	for _, test := range []struct {
		method, token string
		status        int
	}{{"PUT", token, 200}, {"PUT", "", 401}, {"POST", token, 405}} {
		r := httptest.NewRequest(test.method, "/v1/workspaces", bytes.NewReader(body))
		if test.token != "" {
			r.Header.Set("Authorization", "bEaReR "+test.token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != test.status {
			t.Fatalf("%s: %d %s", test.method, w.Code, w.Body.String())
		}
	}
	for i := 0; i < 29; i++ {
		r := base
		r.InstallationID = fmt.Sprintf("workspace_%032x", i)
		if _, err := a.reportWorkspaces(actor, hashToken(token), r); err != nil {
			t.Fatal(err)
		}
	}
	base.InstallationID = "workspace_" + strings.Repeat("d", 32)
	if _, err := a.reportWorkspaces(actor, hashToken(token), base); err == nil {
		t.Fatal("installation limit not enforced")
	}

}

func TestWorkspaceRegistrySchemaTwentyMigration(t *testing.T) {
	for _, postgres := range []bool{false, true} {
		t.Run(fmt.Sprint("postgres=", postgres), func(t *testing.T) {
			var reopen func() *store
			if postgres {
				url := isolatedPostgresURL(t)
				reopen = func() *store { return postgresTestStore(t, url) }
			} else {
				path := filepath.Join(t.TempDir(), "migration.db")
				reopen = func() *store {
					s, err := openStore(path)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = s.close() })
					return s
				}
			}
			s := reopen()
			a := newAuthority(s)
			actor, _ := issueTestIdentity(t, a, "Preserved", "workspace-migration@test.local")
			created, err := a.createProject(actor, contracts.CreateProjectRequest{SchemaVersion: 1, RequestID: "workspace-migration-project", Name: "Preserved"})
			if err != nil {
				t.Fatal(err)
			}
			seedContextSession(t, a, contextSessionFixture{ProjectID: created.Project.ProjectID, Author: actor, AgentType: contracts.AgentCodex, ServerSessionID: "sess_workspace_migration", Versions: 1})
			var before []byte
			if err := s.view(func(state databaseState) error { var err error; before, err = json.Marshal(state); return err }); err != nil {
				t.Fatal(err)
			}
			var legacy map[string]json.RawMessage
			_ = json.Unmarshal(before, &legacy)
			delete(legacy, "workspaces")
			document, _ := json.Marshal(legacy)
			if postgres {
				_, err = s.postgres.Exec(`UPDATE coconet_state SET schema_version=20, document=$1::jsonb WHERE singleton`, document)
			} else {
				err = s.db.Update(func(tx *bolt.Tx) error {
					if err := setFixtureSchema(t, tx, []byte{20}); err != nil {
						return err
					}
					return tx.Bucket(stateBucket).Put(stateKey, document)
				})
			}
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if err := s.close(); err != nil {
					t.Fatal(err)
				}
				s = reopen()
				if err := s.view(func(state databaseState) error {
					after, err := json.Marshal(state)
					if state.Workspaces == nil || !bytes.Equal(before, after) {
						t.Fatal("migration changed existing data")
					}
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
