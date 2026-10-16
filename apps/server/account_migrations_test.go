package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
)

func TestRetiredTrialLoginCannotBeEnabledAndRevokesOldCredentials(t *testing.T) {
	t.Setenv("COCONET_DEBUG_LOGIN", "true")
	h := newTestHandler(t).(*serverHandler)
	a, err := h.authority.externalAccount("coconet:debug", "D01", "Trial A", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := h.authority.externalAccount("coconet:debug", "D02", "Trial B", "")
	if err != nil {
		t.Fatal(err)
	}
	_, tokenA, err := h.authority.createDashboardSessionForAccount(a, "")
	if err != nil {
		t.Fatal(err)
	}
	_, tokenB, err := h.authority.createDashboardSessionForAccount(b, "")
	if err != nil {
		t.Fatal(err)
	}
	project, err := h.authority.createProject(a, contracts.CreateProjectRequest{SchemaVersion: 1, RequestID: "debug-project", Name: "Team trial"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.authority.joinProject(b, project.Project.ProjectID, "debug-join"); err != nil {
		t.Fatal(err)
	}
	if projects, err := h.authority.dashboardProjects(b); err != nil || len(projects.Projects) != 1 {
		t.Fatal("debug account did not join normal project")
	}
	real, err := h.authority.externalAccount("https://github.com", "D01", "Real user", "")
	if err != nil || real.AccountID == a.AccountID {
		t.Fatal("debug identity collided with provider")
	}
	_, realToken, err := h.authority.createDashboardSessionForAccount(real, "")
	if err != nil {
		t.Fatal(err)
	}
	device := authorizeTestDevice(t, h.authority, a, "Debug device")
	verifier := strings.Repeat("v", 43)
	pending, err := h.authority.startDeviceAuthorization(contracts.DeviceAuthorizationRequest{DeviceName: "Unredeemed device", CodeChallenge: pkceChallenge(verifier)}, "https://fixture.test")
	if err != nil {
		t.Fatal(err)
	}
	approved := true
	if _, err := h.authority.decideDeviceAuthorization(a, contracts.DeviceAuthorizationDecision{AuthorizationID: pending.AuthorizationID, Approve: &approved}); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := h.authority.revokeLegacyTestAccess(); err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(http.MethodPost, "https://fixture.test/v1/auth/debug", strings.NewReader(`{"schema_version":1,"id":"D01"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://fixture.test")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatalf("retired login returned %d", w.Code)
	}
	for _, token := range []string{tokenA, tokenB} {
		if _, err = h.authority.authenticateDashboard(token); err == nil {
			t.Fatal("disabled test account retained login")
		}
	}
	if _, err := h.authority.pollDeviceAuthorization(contracts.DeviceAuthorizationPoll{DeviceCode: pending.DeviceCode, CodeVerifier: verifier}); err == nil {
		t.Fatal("disabled debug approval minted a new device token")
	}
	if _, err = h.authority.authenticate(device.AccessToken); err == nil {
		t.Fatal("disabled test account retained device authorization")
	}
	if _, err = h.authority.authenticateDashboard(realToken); err != nil {
		t.Fatal("normal account session changed")
	}
}
