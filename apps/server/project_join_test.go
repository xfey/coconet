package main

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"golang.org/x/oauth2"
)

func TestProjectIDJoinIsExplicitConcurrentAndRepeatable(t *testing.T) {
	a := newTestAuthority(t)
	owner, _ := issueTestIdentity(t, a, "Owner", "")
	guest, _ := issueTestIdentity(t, a, "Guest", "")
	project, err := a.createProject(owner, contracts.CreateProjectRequest{RequestID: "create-join-fixture", Name: "Join fixture"})
	if err != nil {
		t.Fatal(err)
	}
	id := project.Project.ProjectID
	access, err := a.projectAccess(guest, id)
	if err != nil || access.Status != contracts.StatusForbidden {
		t.Fatal("read joined project", err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := a.joinProject(guest, id, "same-join-request"); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := a.store.view(func(state databaseState) error {
		if state.Memberships.Len() != 2 {
			t.Fatal("duplicate membership")
		}
		joins := 0
		for _, event := range state.AuditEvents.All() {
			if event.Action == "project.join" {
				joins++
			}
		}
		if joins != 1 {
			t.Fatalf("duplicate joins: %d", joins)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, err = a.leaveProject(guest, id, contracts.ProjectMutationRequest{RequestID: "leave-join-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.joinProject(guest, id, "same-join-request"); err != nil {
		t.Fatal("rejoin", err)
	}
	for _, invalid := range []string{"", "coco_unknown", strings.Repeat("x", 1000)} {
		_, err = a.joinProject(guest, invalid, "invalid-join-request")
		assertAuthorityCode(t, err, "invalid_project_id")
	}
	_, err = a.joinProject(guest, "coco_1111111111111111", "unknown-join-request")
	assertAuthorityCode(t, err, "project_not_found")
}

func TestDeviceAuthorizationJoinsAtomicallyAndKeepsRequestsIsolated(t *testing.T) {
	for _, scenario := range []string{"approve", "preview", "stale-directory", "wrong-account", "expired", "decline", "pending-chooser"} {
		t.Run(scenario, func(t *testing.T) {
			a := newTestAuthority(t)
			owner, _ := issueTestIdentity(t, a, "Owner", "")
			guest, _ := issueTestIdentity(t, a, "Guest", "")
			project, err := a.createProject(owner, contracts.CreateProjectRequest{RequestID: "create-auth-join", Name: "Explicit join"})
			if err != nil {
				t.Fatal(err)
			}
			id := project.Project.ProjectID
			verifier := oauth2.GenerateVerifier()
			input := contracts.DeviceAuthorizationRequest{DeviceName: "Development machine", CodeChallenge: pkceChallenge(verifier), ConnectProject: true, ExpectedAccountID: guest.AccountID, Directory: &contracts.ConnectionDirectory{CWD: "/repo", Path: "/repo", Revision: 1, CanBrowse: true}}
			started, err := a.startDeviceAuthorization(input, "https://fixture.test")
			if err != nil {
				t.Fatal(err)
			}
			another, err := a.startDeviceAuthorization(input, "https://fixture.test")
			if err != nil {
				t.Fatal(err)
			}
			if len(started.AuthorizationID) != 43 || started.AuthorizationID == started.DeviceCode || started.AuthorizationID == another.AuthorizationID || !strings.Contains(started.VerificationURI, "/#authorize="+started.AuthorizationID) {
				t.Fatal("authorization locator not isolated")
			}
			yes, no := true, false
			decision := contracts.DeviceAuthorizationDecision{AuthorizationID: started.AuthorizationID, ProjectID: id, Approve: &yes, DirectoryRevision: 1}
			actor := guest
			switch scenario {
			case "preview":
				decision.Approve = nil
			case "stale-directory":
				decision.DirectoryRevision = 2
			case "wrong-account":
				actor = owner
			case "expired":
				now := a.now()
				a.now = func() time.Time { return now.Add(authorizationTTL) }
			case "decline":
				decision.Approve = &no
			case "pending-chooser":
				_, err = a.decideDeviceAuthorization(guest, contracts.DeviceAuthorizationDecision{AuthorizationID: started.AuthorizationID, ChooseDirectory: true, DirectoryRevision: 1})
				if err != nil {
					t.Fatal(err)
				}
			}
			_, err = a.decideDeviceAuthorization(actor, decision)
			success := scenario == "approve" || scenario == "preview" || scenario == "decline"
			if (err == nil) != success {
				t.Fatalf("unexpected decision: %v", err)
			}
			access, err := a.projectAccess(guest, id)
			if err != nil {
				t.Fatal(err)
			}
			if (access.Status == contracts.StatusOK) != (scenario == "approve") {
				t.Fatal("failed/read decision changed membership", access.Status)
			}
			if scenario == "approve" {
				result, err := a.pollDeviceAuthorization(contracts.DeviceAuthorizationPoll{DeviceCode: started.DeviceCode, CodeVerifier: verifier})
				if err != nil || result.ProjectID != id || result.Account.AccountID != guest.AccountID {
					t.Fatal("approval did not issue joined credential", err)
				}
				pending, err := a.pollDeviceAuthorization(contracts.DeviceAuthorizationPoll{DeviceCode: another.DeviceCode, CodeVerifier: verifier})
				if err != nil || pending.Status != contracts.StatusPending {
					t.Fatal("parallel request auto-approved", err)
				}
			}
		})
	}
}

func TestEmptyAuthorizationLocatorCannotSelectPendingRequest(t *testing.T) {
	a := newTestAuthority(t)
	actor, _ := issueTestIdentity(t, a, "Owner", "")
	if err := a.store.update(func(state *databaseState) error {
		state.Authorizations.Set("fixture-stale", authorizationRecord{Kind: "device", ExpiresAt: a.now().Add(authorizationTTL).Format(time.RFC3339Nano)})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	yes := true
	_, err := a.decideDeviceAuthorization(actor, contracts.DeviceAuthorizationDecision{Approve: &yes})
	assertAuthorityCode(t, err, "invalid_device_authorization")
}
