package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
)

func TestDashboardInvitationPreviewDoesNotJoinOrExposeProjectData(t *testing.T) {
	a, h, _, project, _ := dashboardFixture(t)
	guest, _ := issueTestIdentity(t, a, "Guest", "guest@coconet.test")
	_, token, err := a.createDashboardSessionForAccount(guest, "")
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: dashboardCookieName, Value: token}
	server := httptest.NewTLSServer(h)
	defer server.Close()
	path := "/v1/dashboard/projects/" + project.ProjectID + "/preview"
	if response, _ := dashboardRequest(t, server, "GET", path, nil, nil, ""); response.StatusCode != 401 {
		t.Fatal("unauthenticated preview accepted")
	}
	response, body := dashboardRequest(t, server, "GET", path, nil, cookie, "")
	if response.StatusCode != 200 || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("preview failed or was cacheable: %d", response.StatusCode)
	}
	var fields map[string]string
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fields, map[string]string{"project_id": project.ProjectID, "name": project.Name, "status": "active"}) {
		t.Fatalf("preview should expose only the project label: %v", fields)
	}
	access, err := a.projectAccess(guest, project.ProjectID)
	if err != nil || access.Status != contracts.StatusForbidden || access.Project != nil {
		t.Fatal("preview granted project access", err)
	}
	for _, suffix := range []string{"workspaces", "published-graph"} {
		response, _ := dashboardRequest(t, server, "GET", "/v1/dashboard/projects/"+project.ProjectID+"/"+suffix, nil, cookie, "")
		if response.StatusCode != 403 {
			t.Fatalf("non-member read %s: %d", suffix, response.StatusCode)
		}
	}
	list, err := a.dashboardProjects(guest)
	if err != nil || len(list.Projects) != 0 {
		t.Fatal("preview added project to guest list", err)
	}
	if err := a.store.view(func(state databaseState) error {
		if state.Memberships.Has(membershipKey(project.ProjectID, guest.AccountID)) {
			t.Fatal("preview created a membership")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		id, code string
		status   int
	}{
		{"coco_invalid", "invalid_project_id", 400},
		{"coco_1111111111111111", "project_not_found", 404},
	} {
		response, body := dashboardRequest(t, server, "GET", "/v1/dashboard/projects/"+scenario.id+"/preview", nil, cookie, "")
		var result contracts.ErrorEnvelope
		if err := json.Unmarshal(body, &result); err != nil || response.StatusCode != scenario.status || result.Code != scenario.code {
			t.Fatalf("unexpected preview error: %d %s", response.StatusCode, body)
		}
	}
	archiveTestProject(t, a, project.ProjectID)
	response, body = dashboardRequest(t, server, "GET", path, nil, cookie, "")
	var result contracts.ErrorEnvelope
	if err := json.Unmarshal(body, &result); err != nil || response.StatusCode != 409 || result.Code != "project_archived" {
		t.Fatalf("archived project invitation accepted: %d %s", response.StatusCode, body)
	}
}
