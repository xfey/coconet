package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	bolt "go.etcd.io/bbolt"
	"golang.org/x/oauth2"
)

func TestUnifiedAccountMigrationAndRestart(t *testing.T) {
	testUnifiedAccountMigrationAndRestart(t, false)
}

func TestPostgresUnifiedAccountMigrationAndRestart(t *testing.T) {
	testUnifiedAccountMigrationAndRestart(t, true)
}

func testUnifiedAccountMigrationAndRestart(t *testing.T, postgres bool) {
	var reopen func() *store
	if postgres {
		address := isolatedPostgresURL(t)
		reopen = func() *store { return postgresTestStore(t, address) }
	} else {
		path := filepath.Join(t.TempDir(), "account.db")
		reopen = func() *store {
			storage, err := openStore(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = storage.close() })
			return storage
		}
	}
	storage := reopen()
	a := newAuthority(storage)
	legacy, key := issueTestIdentity(t, a, "Developer", "")
	project, err := a.createProject(legacy, contracts.CreateProjectRequest{SchemaVersion: 1, RequestID: "request-before-account-upgrade", Name: "Existing work"})
	if err != nil {
		t.Fatal(err)
	}
	seedContextSession(t, a, contextSessionFixture{ProjectID: project.Project.ProjectID, Author: legacy, AgentType: contracts.AgentCodex, ServerSessionID: "sess_account_upgrade", Versions: 1})
	seedLibraryNode(t, a, project.Project.ProjectID, contracts.SessionReadReference{ServerSessionID: "sess_account_upgrade", Version: 1}, contracts.WorkNodeCurrent)
	_, browser, err := a.createDashboardSession(key, "")
	if err != nil {
		t.Fatal(err)
	}
	var before databaseState
	if err := storage.view(func(state databaseState) error {
		var err error
		before, err = materializeRecordState(state)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	delete(document, "external_identities")
	delete(document, "authorizations")
	for _, value := range document["dashboard_sessions"].(map[string]any) {
		value.(map[string]any)["key_hash"] = hashToken(key)
	}
	encoded, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if postgres {
		_, err = storage.postgres.Exec(`UPDATE coconet_state SET schema_version=19, document=$1::jsonb WHERE singleton`, encoded)
	} else {
		err = storage.db.Update(func(tx *bolt.Tx) error {
			if err := setFixtureSchema(t, tx, []byte{19}); err != nil {
				return err
			}
			return tx.Bucket(stateBucket).Put(stateKey, encoded)
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	restart := func() {
		t.Helper()
		if err := storage.close(); err != nil {
			t.Fatal(err)
		}
		storage = reopen()
		a.store = storage
	}
	restart()
	if err := storage.view(func(after databaseState) error {
		var err error
		after, err = materializeRecordState(after)
		if err != nil {
			return err
		}
		before.storageTime, after.storageTime = time.Time{}, time.Time{}
		if !reflect.DeepEqual(before, after) {
			t.Fatal("account upgrade changed existing business data")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.authenticate(key); err != nil {
		t.Fatal("upgrade invalidated existing credential", err)
	}
	if _, err := a.authenticateDashboard(browser); err != nil {
		t.Fatal("upgrade invalidated existing browser", err)
	}
	actor, err := a.externalAccount("https://github.com", "42", legacy.DisplayName, legacy.Email)
	if err != nil || actor.AccountID == legacy.AccountID {
		t.Fatal("login claimed the old identity by profile", err)
	}
	if access, err := a.projectAccess(actor, project.Project.ProjectID); err != nil || access.Status != contracts.StatusForbidden {
		t.Fatal("new identity automatically inherited an old project")
	}
	setupProject, err := a.createProject(actor, contracts.CreateProjectRequest{SchemaVersion: 1, RequestID: "request-account-setup", Name: "New work"})
	if err != nil {
		t.Fatal(err)
	}
	device := authorizeTestDevice(t, a, actor, "Existing device")
	_, newBrowser, err := a.createDashboardSessionForAccount(actor, "")
	if err != nil {
		t.Fatal(err)
	}
	verifier := oauth2.GenerateVerifier()
	started, err := a.startDeviceAuthorization(contracts.DeviceAuthorizationRequest{DeviceName: "Pending device", CodeChallenge: pkceChallenge(verifier), ConnectProject: true, ProjectName: "Pending work", ExpectedAccountID: actor.AccountID}, "https://fixture.test")
	if err != nil {
		t.Fatal(err)
	}
	restart()
	again, err := a.externalAccount("https://github.com", "42", "Renamed at provider", "")
	if err != nil || again != actor {
		t.Fatal("restart lost stable identity or changed local profile", err)
	}
	if _, err := a.authenticate(device.AccessToken); err != nil {
		t.Fatal("restart invalidated device", err)
	}
	if _, err := a.authenticateDashboard(newBrowser); err != nil {
		t.Fatal("restart invalidated independent browser", err)
	}
	poll := contracts.DeviceAuthorizationPoll{DeviceCode: started.DeviceCode, CodeVerifier: verifier}
	if result, err := a.pollDeviceAuthorization(poll); err != nil || result.Status != contracts.StatusPending {
		t.Fatal("restart lost pending authorization", err)
	}
	yes := true
	if _, err := a.decideDeviceAuthorization(actor, contracts.DeviceAuthorizationDecision{AuthorizationID: started.AuthorizationID, Approve: &yes, ProjectID: setupProject.Project.ProjectID}); err != nil {
		t.Fatal(err)
	}
	restart()
	granted, err := a.pollDeviceAuthorization(poll)
	if err != nil || granted.Account == nil || granted.Account.AccountID != actor.AccountID || granted.AccessToken == "" || granted.ProjectID != setupProject.Project.ProjectID {
		t.Fatal("restart lost approved authorization", err)
	}
	restart()
	if _, err := a.pollDeviceAuthorization(poll); err == nil {
		t.Fatal("consumed authorization was replayed after restart")
	}
}

func TestDeviceAuthorizationDeclineAndCredentialExpiry(t *testing.T) {
	a := newTestHandler(t).(*serverHandler).authority
	actor, _ := issueTestIdentity(t, a, "Fixture", "")
	device := authorizeTestDevice(t, a, actor, "Mac")
	verifier := oauth2.GenerateVerifier()
	started, err := a.startDeviceAuthorization(contracts.DeviceAuthorizationRequest{DeviceName: "Linux", CodeChallenge: pkceChallenge(verifier)}, "https://fixture.test")
	if err != nil {
		t.Fatal(err)
	}
	no := false
	if _, err := a.decideDeviceAuthorization(actor, contracts.DeviceAuthorizationDecision{AuthorizationID: started.AuthorizationID, Approve: &no}); err != nil {
		t.Fatal(err)
	}
	_, err = a.pollDeviceAuthorization(contracts.DeviceAuthorizationPoll{DeviceCode: started.DeviceCode, CodeVerifier: verifier})
	assertAuthorityCode(t, err, "authorization_denied")
	yes := true
	_, err = a.decideDeviceAuthorization(actor, contracts.DeviceAuthorizationDecision{AuthorizationID: started.AuthorizationID, Approve: &yes})
	assertAuthorityCode(t, err, "authorization_completed")
	now := a.now()
	a.now = func() time.Time { return now.Add(deviceCredentialTTL + time.Second) }
	if _, err := a.authenticate(device.AccessToken); err == nil {
		t.Fatal("expired device credential remained valid")
	}
	devices, err := a.accountDevices(actor)
	if err != nil || len(devices.Devices) != 0 {
		t.Fatal("expired device still listed", err)
	}
}

func TestUnifiedAccountDevicesAndBrowserAreIndependent(t *testing.T) {
	a := newTestHandler(t).(*serverHandler).authority
	actor, err := a.externalAccount("https://github.com", "42", "Developer", "")
	if err != nil {
		t.Fatal(err)
	}
	again, err := a.externalAccount("https://github.com", "42", "Changed provider name", "")
	if err != nil || again.AccountID != actor.AccountID {
		t.Fatal("repeated login created another account", err)
	}
	other, _ := a.externalAccount("https://private.example/oidc", "42", "Developer", "")
	if other.AccountID == actor.AccountID {
		t.Fatal("provider identities were merged")
	}
	_, browser, err := a.createDashboardSessionForAccount(actor, "")
	if err != nil {
		t.Fatal(err)
	}
	first := authorizeTestDevice(t, a, actor, "Mac")
	second := authorizeTestDevice(t, a, actor, "Linux")
	if first.AccessToken == second.AccessToken || first.Account.AccountID != second.Account.AccountID {
		t.Fatal("device credential isolation failed")
	}
	devices, err := a.accountDevices(actor)
	if err != nil || len(devices.Devices) != 2 {
		t.Fatal("device listing failed", err)
	}
	if err := a.revokeDevice(other, devices.Devices[0].ID); err == nil {
		t.Fatal("another account revoked a device")
	}
	if err := a.revokeDevice(actor, devices.Devices[0].ID); err != nil {
		t.Fatal(err)
	}
	valid := 0
	for _, token := range []string{first.AccessToken, second.AccessToken} {
		if _, err := a.authenticate(token); err == nil {
			valid++
		}
	}
	if valid != 1 {
		t.Fatal("revocation did not isolate the device")
	}
	if _, err := a.authenticateDashboard(browser); err != nil {
		t.Fatal("device revocation signed out browser", err)
	}
	updated, err := a.updateAccountProfile(actor, contracts.AccountProfileUpdate{DisplayName: "New name"})
	if err != nil || updated.DisplayName != "New name" {
		t.Fatal(err)
	}
	current, err := a.authenticateDashboard(browser)
	if err != nil || current.Account.DisplayName != "New name" {
		t.Fatal("browser profile became stale", err)
	}
}

func authorizeTestDevice(t *testing.T, a *authority, actor contracts.Account, name string) contracts.DeviceAuthorization {
	t.Helper()
	verifier := oauth2.GenerateVerifier()
	started, err := a.startDeviceAuthorization(contracts.DeviceAuthorizationRequest{DeviceName: name, CodeChallenge: pkceChallenge(verifier)}, "https://fixture.test")
	if err != nil {
		t.Fatal(err)
	}
	poll := contracts.DeviceAuthorizationPoll{DeviceCode: started.DeviceCode, CodeVerifier: verifier}
	pending, err := a.pollDeviceAuthorization(poll)
	if err != nil || pending.Status != contracts.StatusPending || pending.AccessToken != "" {
		t.Fatal("unapproved device received credentials", err)
	}
	yes := true
	if _, err := a.decideDeviceAuthorization(actor, contracts.DeviceAuthorizationDecision{AuthorizationID: started.AuthorizationID, Approve: &yes}); err != nil {
		t.Fatal(err)
	}
	result, err := a.pollDeviceAuthorization(poll)
	if err != nil || result.Status != contracts.StatusOK || result.AccessToken == "" {
		t.Fatal("authorized device did not receive credentials", err)
	}
	if _, err := a.pollDeviceAuthorization(poll); err == nil {
		t.Fatal("one-time grant replay succeeded")
	}
	return result
}

func TestDeviceAuthorizationRejectsSpoofingExpiryAndConcurrentReplay(t *testing.T) {
	a := newTestHandler(t).(*serverHandler).authority
	actor, _ := issueTestIdentity(t, a, "Fixture", "")
	verifier := oauth2.GenerateVerifier()
	for _, callback := range []string{"http://localhost:80/callback", "http://127.0.0.1.evil:80/callback", "https://evil.test/callback", "http://127.0.0.1:80/callback?token=x"} {
		if _, err := a.startDeviceAuthorization(contracts.DeviceAuthorizationRequest{DeviceName: "Mac", CodeChallenge: pkceChallenge(verifier), CallbackURL: callback}, "https://fixture.test"); err == nil {
			t.Fatal("unsafe callback accepted", callback)
		}
	}
	started, err := a.startDeviceAuthorization(contracts.DeviceAuthorizationRequest{DeviceName: "Mac", CodeChallenge: pkceChallenge(verifier)}, "https://fixture.test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.pollDeviceAuthorization(contracts.DeviceAuthorizationPoll{DeviceCode: started.DeviceCode, CodeVerifier: oauth2.GenerateVerifier()}); err == nil {
		t.Fatal("wrong PKCE accepted")
	}
	yes := true
	if _, err := a.decideDeviceAuthorization(actor, contracts.DeviceAuthorizationDecision{AuthorizationID: started.AuthorizationID, Approve: &yes}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := a.pollDeviceAuthorization(contracts.DeviceAuthorizationPoll{DeviceCode: started.DeviceCode, CodeVerifier: verifier})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatal("grant was not consumed atomically")
	}
	started, _ = a.startDeviceAuthorization(contracts.DeviceAuthorizationRequest{DeviceName: "Mac", CodeChallenge: pkceChallenge(verifier)}, "https://fixture.test")
	now := a.now()
	a.now = func() time.Time { return now.Add(authorizationTTL + time.Second) }
	if _, err := a.decideDeviceAuthorization(actor, contracts.DeviceAuthorizationDecision{AuthorizationID: started.AuthorizationID, Approve: &yes}); err == nil {
		t.Fatal("expired grant accepted")
	}
}

func TestGitHubBrowserLoginUsesStableIdentityAndOneTimeState(t *testing.T) {
	h := newTestHandler(t).(*serverHandler)
	var challenge string
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			_ = r.ParseForm()
			if r.Form.Get("code") != "good" || pkceChallenge(r.Form.Get("code_verifier")) != challenge {
				http.Error(w, "invalid grant", 400)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "provider-secret", "token_type": "bearer"})
		case "/user":
			if r.Header.Get("Authorization") != "Bearer provider-secret" {
				http.Error(w, "unauthorized", 401)
				return
			}
			_, _ = w.Write([]byte(`{"id":42,"login":"fixture","name":"Fixture user"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer idp.Close()
	server := httptest.NewTLSServer(h)
	defer server.Close()
	h.auth = &loginProvider{Kind: "github", Label: "GitHub", Issuer: "https://github.com", UserURL: idp.URL + "/user", HTTP: idp.Client(), OAuth: oauth2.Config{ClientID: "fixture", ClientSecret: "test-secret", RedirectURL: server.URL + "/v1/auth/callback", Endpoint: oauth2.Endpoint{AuthURL: idp.URL + "/authorize", TokenURL: idp.URL + "/token", AuthStyle: oauth2.AuthStyleInParams}}}
	const returnProject = "coco_1111111111111111"
	for _, project := range []string{"//elsewhere.test", "prj_1111111111111111", "coco_invalid"} {
		response, _ := dashboardRequest(t, server, "POST", "/v1/auth/start", map[string]string{"project_id": project}, nil, server.URL)
		if response.StatusCode != http.StatusBadRequest {
			t.Fatal("invalid project return target accepted")
		}
	}
	response, body := dashboardRequest(t, server, "POST", "/v1/auth/start", map[string]string{"project_id": returnProject}, nil, server.URL)
	if response.StatusCode != 200 {
		t.Fatal("login start failed", string(body))
	}
	var start struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(body, &start) != nil {
		t.Fatal("invalid start response")
	}
	authorize, _ := url.Parse(start.URL)
	challenge = authorize.Query().Get("code_challenge")
	state := authorize.Query().Get("state")
	if challenge == "" || authorize.Query().Get("code_challenge_method") != "S256" || state == "" {
		t.Fatal("missing state or PKCE")
	}
	client := server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	callback := server.URL + "/v1/auth/callback?state=" + url.QueryEscape(state) + "&code=good"
	for _, cookie := range []*http.Cookie{nil, {Name: loginCookieName, Value: "another-browser"}} {
		request, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, callback, nil)
		if cookie != nil {
			request.AddCookie(cookie)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if !strings.Contains(response.Header.Get("Location"), "auth_error") {
			t.Fatal("callback accepted a missing or incorrect browser cookie")
		}
	}
	request, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, callback, nil)
	request.AddCookie(response.Cookies()[0])
	logged, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	logged.Body.Close()
	if logged.StatusCode != 303 || logged.Header.Get("Location") != "/"+returnProject {
		t.Fatal("callback failed", logged.Header.Get("Location"))
	}
	var cookie *http.Cookie
	for _, c := range logged.Cookies() {
		if c.Name == dashboardCookieName {
			cookie = c
		}
	}
	if cookie == nil || !cookie.HttpOnly || !cookie.Secure {
		t.Fatal("missing secure browser session")
	}
	me, err := h.authority.authenticateDashboard(cookie.Value)
	if err != nil || me.Account.DisplayName != "Fixture user" {
		t.Fatal("login did not create account", err)
	}
	replayed, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	replayed.Body.Close()
	if !strings.Contains(replayed.Header.Get("Location"), "auth_error") {
		t.Fatal("callback state replay succeeded")
	}
	response, _ = dashboardRequest(t, server, "POST", "/v1/auth/start", map[string]string{}, nil, "https://evil.test")
	if response.StatusCode != 403 {
		t.Fatal("cross-origin login accepted")
	}
}

func TestProjectSetupAuthorizationRequiresSelectedActiveMembership(t *testing.T) {
	a := newTestAuthority(t)
	actor, _ := issueTestIdentity(t, a, "Owner", "")
	other, _ := issueTestIdentity(t, a, "Other", "")
	project, err := a.createProject(actor, contracts.CreateProjectRequest{SchemaVersion: 1, RequestID: "setup-project-request", Name: "Shared work"})
	if err != nil {
		t.Fatal(err)
	}
	projectID := project.Project.ProjectID
	yes, no := true, false
	for _, scenario := range []string{"selected", "missing", "unrelated", "wrong-account", "declined", "login-only", "membership-revoked"} {
		t.Run(scenario, func(t *testing.T) {
			verifier := oauth2.GenerateVerifier()
			input := contracts.DeviceAuthorizationRequest{DeviceName: "Setup device", CodeChallenge: pkceChallenge(verifier), ConnectProject: true, ProjectName: "local-folder", ExpectedAccountID: actor.AccountID}
			if scenario == "login-only" {
				input.ConnectProject, input.ProjectName = false, ""
			}
			start, err := a.startDeviceAuthorization(input, "https://fixture.test")
			if err != nil {
				t.Fatal(err)
			}
			poll := contracts.DeviceAuthorizationPoll{DeviceCode: start.DeviceCode, CodeVerifier: verifier}
			preview, err := a.decideDeviceAuthorization(actor, contracts.DeviceAuthorizationDecision{AuthorizationID: start.AuthorizationID})
			if err != nil || preview.ConnectProject != input.ConnectProject || preview.ProjectName != input.ProjectName {
				t.Fatalf("setup preview: %#v %v", preview, err)
			}
			decision := contracts.DeviceAuthorizationDecision{AuthorizationID: start.AuthorizationID, Approve: &yes, ProjectID: projectID}
			approver := actor
			switch scenario {
			case "missing":
				decision.ProjectID = ""
			case "unrelated":
				decision.ProjectID = "coco_unknown"
			case "wrong-account":
				approver = other
			case "declined":
				decision.Approve, decision.ProjectID = &no, ""
			}
			_, err = a.decideDeviceAuthorization(approver, decision)
			switch scenario {
			case "missing", "unrelated", "wrong-account", "login-only":
				if err == nil {
					t.Fatal("invalid project approval succeeded")
				}
				result, err := a.pollDeviceAuthorization(poll)
				if err != nil || result.Status != contracts.StatusPending || result.AccessToken != "" {
					t.Fatal("rejected approval issued credential", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "membership-revoked" {
				_, err = a.leaveProject(actor, projectID, contracts.ProjectMutationRequest{SchemaVersion: 1, RequestID: "setup-revoke-membership"})
				if err != nil {
					t.Fatal(err)
				}
			}
			result, err := a.pollDeviceAuthorization(poll)
			if scenario == "declined" || scenario == "membership-revoked" {
				if err == nil || result.AccessToken != "" {
					t.Fatal("revoked/declined setup issued credential")
				}
				return
			}
			if err != nil || result.ProjectID != projectID || result.Account == nil || result.Account.AccountID != actor.AccountID {
				t.Fatalf("wrong setup grant: %#v %v", result, err)
			}
			if _, err := a.pollDeviceAuthorization(poll); err == nil {
				t.Fatal("setup grant replay succeeded")
			}
		})
	}
}
