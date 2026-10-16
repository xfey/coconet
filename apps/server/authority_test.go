package main

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/projectlink"
)

func TestEqualMembershipConnectionLeaveAndLegacyArchivePermissions(t *testing.T) {
	a := newTestAuthority(t)
	creator, _ := issueTestIdentity(t, a, "Creator", "creator@coconet.test")
	member, _ := issueTestIdentity(t, a, "Member", "member@coconet.test")
	outsider, _ := issueTestIdentity(t, a, "Outsider", "outsider@coconet.test")

	created, err := a.createProject(creator, contracts.CreateProjectRequest{SchemaVersion: 1, RequestID: "request-create-equal", Name: "fixture-project"})
	if err != nil {
		t.Fatal(err)
	}
	projectID := created.Project.ProjectID
	if created.Project.CreatedBy != creator.AccountID || created.Membership.Role != contracts.RoleMember || !created.Capabilities["share"] || created.Capabilities["manage"] {
		t.Fatalf("creator received legacy authority: %#v", created)
	}
	replayed, err := a.createProject(creator, contracts.CreateProjectRequest{SchemaVersion: 1, RequestID: "request-create-equal", Name: "fixture-project"})
	if err != nil || replayed.Project.ProjectID != projectID {
		t.Fatalf("project creation was not idempotent: %#v %v", replayed, err)
	}
	if _, ok := projectlink.ProjectUID(projectID); !ok {
		t.Fatalf("new Project does not use a 128-bit UID: %q", projectID)
	}

	access, err := a.projectAccess(member, projectID)
	if err != nil || access.Status != contracts.StatusForbidden || access.Project != nil {
		t.Fatalf("outsider received Project metadata: %#v %v", access, err)
	}
	joined, err := a.joinProject(member, projectID, "request-equal-connect")
	if err != nil || joined.Role != contracts.RoleMember || joined.Status != contracts.MembershipActive {
		t.Fatalf("Project ID did not atomically join: %#v %v", joined, err)
	}
	memberAccess, err := a.projectAccess(member, projectID)
	if err != nil || memberAccess.Capabilities["manage"] || !memberAccess.Capabilities["read"] || !memberAccess.Capabilities["write"] || !memberAccess.Capabilities["resume"] || !memberAccess.Capabilities["share"] {
		t.Fatalf("joined member capabilities differ: %#v %v", memberAccess, err)
	}

	for _, err := range []error{
		func() error {
			_, err := a.requestJoin(outsider, projectID, contracts.ProjectMutationRequest{})
			return err
		}(),
		func() error {
			_, err := a.removeMember(creator, projectID, member.AccountID, contracts.ProjectMutationRequest{})
			return err
		}(),
		func() error {
			_, err := a.dissolveProject(creator, projectID, contracts.ProjectMutationRequest{})
			return err
		}(),
	} {
		assertAuthorityCode(t, err, "deprecated_flow")
	}

	left, err := a.leaveProject(creator, projectID, contracts.ProjectMutationRequest{SchemaVersion: 1, RequestID: "request-creator-leave"})
	if err != nil || left.Status != contracts.MembershipLeft {
		t.Fatalf("creator could not leave: %#v %v", left, err)
	}
	if _, err := a.leaveProject(creator, projectID, contracts.ProjectMutationRequest{SchemaVersion: 1, RequestID: "request-creator-leave-again"}); err != nil {
		t.Fatalf("leave was not idempotent: %v", err)
	}
	if _, err := a.joinProject(outsider, projectID, "request-outsider-connect"); err != nil {
		t.Fatalf("Project did not remain joinable after creator leave: %v", err)
	}

	archiveTestProject(t, a, projectID)
	archived, err := a.projectAccess(member, projectID)
	if err != nil || archived.Status != contracts.StatusProjectArchived || !archived.Capabilities["read"] || archived.Capabilities["write"] || archived.Capabilities["share"] {
		t.Fatalf("legacy archive capabilities are invalid: %#v %v", archived, err)
	}
	_, err = a.joinProject(member, projectID, "request-archive-token")
	assertAuthorityCode(t, err, "project_archived")
}

func TestAccountBootstrapCreatesLongLivedCredentialWithoutPlaintextServerToken(t *testing.T) {
	a := newTestAuthority(t)
	first, firstToken := issueTestIdentity(t, a, "First Name", "first@coconet.test")
	second, secondToken := issueTestIdentity(t, a, "First Name", "first@coconet.test")
	if first.AccountID == second.AccountID || firstToken == secondToken {
		t.Fatalf("unexpected account/token identity: %#v %#v", first, second)
	}
	if err := a.store.view(func(state databaseState) error {
		content, err := json.Marshal(state)
		if err == nil && strings.Contains(string(content), firstToken) {
			t.Fatal("server database retained a bearer token in plaintext")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}

	a.now = func() time.Time { return time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC) }
	if authenticated, err := a.authenticate(firstToken); err != nil || authenticated.AccountID != first.AccountID {
		t.Fatalf("long-lived credential did not remain valid: %#v %v", authenticated, err)
	}
}

func TestAccountBootstrapRejectsInvalidGitProfile(t *testing.T) {
	a := newTestAuthority(t)
	if _, err := a.bootstrapAccount(contracts.AccountBootstrapRequest{SchemaVersion: 1, DisplayName: "", Email: "invalid"}); err == nil {
		t.Fatal("invalid account profile was accepted")
	}
}

func TestProjectAndMemberCapacityLimits(t *testing.T) {
	t.Run("projects per creator", func(t *testing.T) {
		a := newTestAuthority(t)
		creator, _ := issueTestIdentity(t, a, "Creator", "project-capacity@coconet.test")
		var first contracts.ProjectAccess
		for index := 0; index < maxProjectsPerAccount; index++ {
			request := contracts.CreateProjectRequest{
				SchemaVersion: contracts.SchemaVersion,
				RequestID:     "request-project-capacity-" + strconv.Itoa(index),
				Name:          "capacity-" + strconv.Itoa(index),
			}
			created, err := a.createProject(creator, request)
			if err != nil {
				t.Fatalf("create Project %d: %v", index, err)
			}
			if index == 0 {
				first = created
			}
		}
		_, err := a.createProject(creator, contracts.CreateProjectRequest{
			SchemaVersion: contracts.SchemaVersion,
			RequestID:     "request-project-capacity-overflow",
			Name:          "capacity-overflow",
		})
		assertAuthorityCode(t, err, "project_limit_reached")

		replayed, err := a.createProject(creator, contracts.CreateProjectRequest{
			SchemaVersion: contracts.SchemaVersion,
			RequestID:     "request-project-capacity-0",
			Name:          "capacity-0",
		})
		if err != nil || replayed.Project.ProjectID != first.Project.ProjectID {
			t.Fatalf("capacity check broke idempotent replay: %#v %v", replayed, err)
		}
	})

	t.Run("active members per Project", func(t *testing.T) {
		a := newTestAuthority(t)
		creator, _ := issueTestIdentity(t, a, "Creator", "member-capacity-creator@coconet.test")
		candidate, _ := issueTestIdentity(t, a, "Candidate", "member-capacity-candidate@coconet.test")
		created, err := a.createProject(creator, contracts.CreateProjectRequest{
			SchemaVersion: contracts.SchemaVersion,
			RequestID:     "request-member-capacity-project",
			Name:          "member-capacity",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := a.store.update(func(state *databaseState) error {
			for index := 0; index < maxActiveMembersPerProject-1; index++ {
				account := contracts.Account{SchemaVersion: contracts.SchemaVersion, AccountID: "acct_capacity_" + strconv.Itoa(index)}
				state.Memberships.Set(membershipKey(created.Project.ProjectID, account.AccountID), contracts.Membership{
					SchemaVersion: contracts.SchemaVersion,
					ProjectID:     created.Project.ProjectID,
					Account:       account,
					Role:          contracts.RoleMember,
					Status:        contracts.MembershipActive,
					CreatedAt:     a.timestamp(),
					UpdatedAt:     a.timestamp(),
				})
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		_, err = a.joinProject(candidate, created.Project.ProjectID, "request-member-capacity-connect")
		assertAuthorityCode(t, err, "project_member_limit_reached")

		if err := a.store.update(func(state *databaseState) error {
			key := membershipKey(created.Project.ProjectID, "acct_capacity_0")
			membership := state.Memberships.Get(key)
			membership.Status = contracts.MembershipLeft
			state.Memberships.Set(key, membership)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		joined, err := a.joinProject(candidate, created.Project.ProjectID, "request-member-capacity-retry")
		if err != nil || joined.Status != contracts.MembershipActive {
			t.Fatalf("freed member capacity was not reusable: %#v %v", joined, err)
		}
	})
}

func newTestAuthority(t *testing.T) *authority {
	t.Helper()
	storage, err := openStore(filepath.Join(t.TempDir(), "authority.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.close() })
	a := newAuthority(storage)
	a.now = func() time.Time { return time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC) }
	return a
}

func issueTestIdentity(t *testing.T, a *authority, name, email string) (contracts.Account, string) {
	t.Helper()
	token, err := a.bootstrapAccount(contracts.AccountBootstrapRequest{SchemaVersion: contracts.SchemaVersion, DisplayName: name, Email: email})
	if err != nil || token.Status != contracts.StatusOK {
		t.Fatalf("issue token: %#v %v", token, err)
	}
	return *token.Account, token.AccessToken
}

func assertAuthorityCode(t *testing.T, err error, code string) {
	t.Helper()
	domain, ok := err.(*authorityError)
	if !ok || domain.Code != code {
		t.Fatalf("expected authority error %q, got %#v", code, err)
	}
}

func archiveTestProject(t *testing.T, a *authority, projectID string) {
	t.Helper()
	if err := a.store.update(func(state *databaseState) error {
		project := state.Projects.Get(projectID)
		project.Status = contracts.ProjectArchived
		project.ArchivedAt = a.timestamp()
		state.Projects.Set(projectID, project)
		for key, membership := range state.Memberships.All() {
			if membership.ProjectID == projectID && membership.Status == contracts.MembershipActive {
				membership.Status = contracts.MembershipArchived
				membership.UpdatedAt = a.timestamp()
				state.Memberships.Set(key, membership)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
