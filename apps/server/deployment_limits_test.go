package main

import (
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contracts"
)

func TestDeploymentQuotasConfigureAndRetainIdempotentAccess(t *testing.T) {
	t.Setenv("COCONET_MAX_PROJECTS_PER_ACCOUNT", "1")
	t.Setenv("COCONET_MAX_MEMBERS_PER_PROJECT", "1")
	a := newTestAuthority(t)
	var err error
	a.limits, err = deploymentLimitsFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	owner, _ := issueTestIdentity(t, a, "Owner", "")
	peer, _ := issueTestIdentity(t, a, "Peer", "")
	request := contracts.CreateProjectRequest{SchemaVersion: 1, RequestID: "create-first", Name: "first"}
	first, err := a.createProject(owner, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.createProject(owner, request); err != nil {
		t.Fatal("quota blocked completed retry", err)
	}
	request.RequestID = "create-second"
	if _, err := a.createProject(owner, request); err == nil || !strings.Contains(err.Error(), "1 active") {
		t.Fatal("configured project quota missing", err)
	}
	if _, err := a.joinProject(peer, first.Project.ProjectID, "join-peer"); err == nil || !strings.Contains(err.Error(), "1 active") {
		t.Fatal("configured member quota missing", err)
	}
	if _, err := a.joinProject(owner, first.Project.ProjectID, "join-owner"); err != nil {
		t.Fatal("quota blocked current member", err)
	}
	t.Setenv("COCONET_MAX_PROJECTS_PER_ACCOUNT", "2")
	t.Setenv("COCONET_MAX_MEMBERS_PER_PROJECT", "2")
	a.limits, err = deploymentLimitsFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.createProject(owner, request); err != nil {
		t.Fatal("increased quota not respected", err)
	}
	if _, err := a.joinProject(peer, first.Project.ProjectID, "join-peer"); err != nil {
		t.Fatal("increased member quota not respected", err)
	}
	for _, value := range []string{"0", "-1", "1.5", "unlimited", "9999999999999999999999"} {
		t.Setenv("COCONET_MAX_PROJECTS_PER_ACCOUNT", value)
		if _, err := deploymentLimitsFromEnvironment(); err == nil {
			t.Fatal("invalid quota accepted", value)
		}
	}
}

func TestRatePoliciesExposeActualRetryWindow(t *testing.T) {
	limiter := newRequestLimiter()
	now := time.Now()
	limiter.now = func() time.Time { return now }
	for i := 0; i < 10; i++ {
		if err := limiter.check("project.create.account", "one"); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(1250 * time.Millisecond)
	err := limiter.check("project.create.account", "one")
	if err == nil {
		t.Fatal("quota not enforced")
	}
	h := &serverHandler{}
	response := httptest.NewRecorder()
	h.writeError(response, "rate-test", err)
	if response.Code != 429 || response.Header().Get("Retry-After") != strconv.Itoa(3599) || !strings.Contains(response.Body.String(), "retry after 3599 seconds") {
		t.Fatal(response.Code, response.Header(), response.Body.String())
	}
	if err := limiter.check("project.create.account", "two"); err != nil {
		t.Fatal("unrelated member blocked", err)
	}
	now = now.Add(time.Hour)
	if err := limiter.check("project.create.account", "one"); err != nil {
		t.Fatal("expired window did not recover", err)
	}
}
