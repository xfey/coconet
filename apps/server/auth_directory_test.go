package main

import (
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"golang.org/x/oauth2"
)

func TestDirectoryAuthorization(t *testing.T)         { testDirectoryAuthorization(t, false) }
func TestPostgresDirectoryAuthorization(t *testing.T) { testDirectoryAuthorization(t, true) }

func testDirectoryAuthorization(t *testing.T, postgres bool) {
	a := newTestAuthority(t)
	if postgres {
		a = newAuthority(postgresTestStore(t, isolatedPostgresURL(t)))
	}
	actor, _ := issueTestIdentity(t, a, "Owner", "")
	other, _ := issueTestIdentity(t, a, "Other", "")
	project, err := a.createProject(actor, contracts.CreateProjectRequest{SchemaVersion: 1, RequestID: "directory-project-request", Name: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	yes, no := true, false
	for _, scenario := range []string{"select", "cancelled", "invalid", "unavailable", "already_connected", "denied", "expired", "remote"} {
		t.Run(scenario, func(t *testing.T) {
			verifier := oauth2.GenerateVerifier()
			directory := contracts.ConnectionDirectory{CWD: "/work/original/src", Path: "/work/original", CanBrowse: true, Revision: 1}
			if scenario == "remote" {
				directory.Remote, directory.CanBrowse = true, false
			}
			start, err := a.startDeviceAuthorization(contracts.DeviceAuthorizationRequest{DeviceName: "Test device", CodeChallenge: pkceChallenge(verifier), ConnectProject: true, Directory: &directory, ExpectedAccountID: actor.AccountID, SuggestedProjectID: project.Project.ProjectID}, "https://fixture.test")
			if err != nil {
				t.Fatal(err)
			}
			poll := contracts.DeviceAuthorizationPoll{DeviceCode: start.DeviceCode, CodeVerifier: verifier}
			decision := contracts.DeviceAuthorizationDecision{AuthorizationID: start.AuthorizationID, Approve: &yes, ProjectID: project.Project.ProjectID}
			if _, err := a.decideDeviceAuthorization(actor, decision); err == nil {
				t.Fatal("approval without the reviewed directory revision succeeded")
			}
			choose := contracts.DeviceAuthorizationDecision{AuthorizationID: start.AuthorizationID, ChooseDirectory: true, DirectoryRevision: 1}
			if _, err := a.decideDeviceAuthorization(other, choose); err == nil {
				t.Fatal("another account opened the chooser")
			}
			preview, err := a.decideDeviceAuthorization(actor, choose)
			if scenario == "remote" {
				if err == nil {
					t.Fatal("remote device enabled a browser-local chooser")
				}
				decision.DirectoryRevision = 1
				if _, err := a.decideDeviceAuthorization(actor, decision); err != nil {
					t.Fatal(err)
				}
				grant, err := a.pollDeviceAuthorization(poll)
				if err != nil || grant.Directory == nil || *grant.Directory != directory {
					t.Fatal("remote directory lost", err)
				}
				return
			}
			if err != nil || !preview.DirectoryPending || preview.Directory == nil || *preview.Directory != directory || preview.SuggestedProjectID != project.Project.ProjectID {
				t.Fatal("invalid chooser preview", err)
			}
			pending, err := a.pollDeviceAuthorization(poll)
			if err != nil || pending.DirectoryRequestID == "" || pending.AccessToken != "" {
				t.Fatal("chooser was not delivered only to the pending CLI", err)
			}
			_, _ = a.decideDeviceAuthorization(actor, choose)
			repeated, _ := a.pollDeviceAuthorization(poll)
			if repeated.DirectoryRequestID != pending.DirectoryRequestID {
				t.Fatal("duplicate click opened a second chooser")
			}
			decision.DirectoryRevision = 1
			if _, err := a.decideDeviceAuthorization(actor, decision); err == nil {
				t.Fatal("approved while a chooser was pending")
			}
			update := contracts.DeviceDirectoryUpdate{DeviceAuthorizationPoll: poll, RequestID: pending.DirectoryRequestID, CWD: "/work/另一个 project/src", Path: "/work/另一个 project"}
			wrong := update
			wrong.CodeVerifier = oauth2.GenerateVerifier()
			if err := a.updateDeviceDirectory(wrong); err == nil {
				t.Fatal("wrong verifier changed folder")
			}
			wrong = update
			wrong.RequestID = "wrong-request"
			if err := a.updateDeviceDirectory(wrong); err == nil {
				t.Fatal("wrong chooser request changed folder")
			}
			wrong = update
			wrong.Path = "/work/unrelated"
			if err := a.updateDeviceDirectory(wrong); err == nil {
				t.Fatal("unrelated collaboration root accepted")
			}
			if scenario == "denied" {
				_, _ = a.decideDeviceAuthorization(actor, contracts.DeviceAuthorizationDecision{AuthorizationID: start.AuthorizationID, Approve: &no})
				if err := a.updateDeviceDirectory(update); err == nil {
					t.Fatal("directory changed after denial")
				}
				return
			}
			if scenario == "expired" {
				now := a.now
				a.now = func() time.Time { return now().Add(authorizationTTL + time.Second) }
				defer func() { a.now = now }()
				if err := a.updateDeviceDirectory(update); err == nil {
					t.Fatal("expired request changed folder")
				}
				return
			}
			if scenario != "select" {
				update.CWD, update.Path, update.Error = "", "", scenario
			} else {
				directory.CWD, directory.Path = update.CWD, update.Path
			}
			if err := a.updateDeviceDirectory(update); err != nil {
				t.Fatal(err)
			}
			if err := a.updateDeviceDirectory(update); err == nil {
				t.Fatal("chooser result replay succeeded")
			}
			if _, err := a.decideDeviceAuthorization(actor, decision); err == nil {
				t.Fatal("stale browser approved the new directory")
			}
			directory.Revision++
			preview, err = a.decideDeviceAuthorization(actor, contracts.DeviceAuthorizationDecision{AuthorizationID: start.AuthorizationID})
			if err != nil || preview.DirectoryPending || *preview.Directory != directory || preview.DirectoryError != update.Error {
				t.Fatal("invalid completed selection preview", err)
			}
			decision.DirectoryRevision = directory.Revision
			if _, err := a.decideDeviceAuthorization(actor, decision); err != nil {
				t.Fatal(err)
			}
			if err := a.updateDeviceDirectory(update); err == nil {
				t.Fatal("approved directory was mutable")
			}
			grant, err := a.pollDeviceAuthorization(poll)
			if err != nil || grant.Directory == nil || *grant.Directory != directory || grant.ProjectID != project.Project.ProjectID {
				t.Fatal("grant lost reviewed directory", err)
			}
			if _, err := a.pollDeviceAuthorization(poll); err == nil {
				t.Fatal("grant replay succeeded")
			}
		})
	}
}

func TestDirectoryStartValidation(t *testing.T) {
	a := newTestAuthority(t)
	for _, value := range []string{"relative/path", "/work/../secret", "/work/\ncommand", "/work/", "/work\x00"} {
		_, err := a.startDeviceAuthorization(contracts.DeviceAuthorizationRequest{DeviceName: "CLI", CodeChallenge: pkceChallenge(oauth2.GenerateVerifier()), ConnectProject: true, Directory: &contracts.ConnectionDirectory{CWD: value, Path: value, Revision: 1}}, "https://fixture.test")
		if err == nil {
			t.Fatalf("invalid device path accepted: %q", value)
		}
	}
}
