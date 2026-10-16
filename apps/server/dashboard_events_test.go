package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestDashboardEventsCommitRollbackAndReplay(t *testing.T) {
	a, _, _, project, _ := dashboardFixture(t)
	updates, stop, _ := a.store.events.subscribe(project.ProjectID)
	defer stop()
	var before int64
	a.store.view(func(state databaseState) error { before = dashboardRevision(state, project.ProjectID); return nil })
	mutate := func(state *databaseState) error { markDashboardChanged(state, project.ProjectID); return nil }

	if err := a.store.update(func(state *databaseState) error { mutate(state); return errors.New("rollback") }); err == nil {
		t.Fatal("expected rollback")
	}
	select {
	case <-updates:
		t.Fatal("rollback emitted an event")
	default:
	}
	if err := a.store.update(mutate); err != nil {
		t.Fatal(err)
	}
	select {
	case <-updates:
	default:
		t.Fatal("commit did not emit")
	}
	a.store.view(func(state databaseState) error {
		if dashboardRevision(state, project.ProjectID) != before+1 {
			t.Fatal("transaction did not batch changes")
		}
		return nil
	})
	if err := a.store.update(func(state *databaseState) error {
		for _, node := range state.WorkNodes.All() {
			setWorkNode(state, node)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-updates:
		t.Fatal("unchanged graph emitted an event")
	default:
	}
}

func TestPostgresDashboardEventsAcrossStoresAndListenerReconnect(t *testing.T) {
	address := isolatedPostgresURL(t)
	left := postgresTestStore(t, address)
	parsed, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	name, err := randomID("dashboard_listener_test")
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("application_name", name)
	parsed.RawQuery = query.Encode()
	right := postgresTestStore(t, parsed.String())
	updates, release, _ := right.events.subscribe("project")
	defer release()
	// Wait for this test's dedicated connection to finish LISTEN, then publish
	// through the other store, whose in-process hub cannot wake this reader.
	deadline := time.Now().Add(5 * time.Second)
	for {
		var ready bool
		if err := left.postgres.QueryRow("SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE application_name = $1 AND query = $2)", name, "LISTEN "+dashboardNotifyChannel).Scan(&ready); err != nil {
			t.Fatal(err)
		}
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("listener did not connect")
		}
		time.Sleep(10 * time.Millisecond)
	}
	commit := func() {
		t.Helper()
		if err := left.update(func(state *databaseState) error { markDashboardChanged(state, "project"); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	wait := func(revision int64) {
		t.Helper()
		for {
			select {
			case <-updates:
				var current int64
				if err := right.view(func(state databaseState) error { current = dashboardRevision(state, "project"); return nil }); err != nil {
					t.Fatal(err)
				}
				if current == revision {
					return
				}
			case <-time.After(5 * time.Second):
				t.Fatal("notification gap was not repaired")
			}
		}
	}
	commit()
	wait(2)
	if _, err := left.postgres.Exec("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name = $1 AND query = $2", name, "LISTEN "+dashboardNotifyChannel); err != nil {
		t.Fatal(err)
	}
	commit()
	wait(3)
}

func TestDashboardEventStreamInitialUpdateAndRevocation(t *testing.T) {
	a, handler, actor, project, key := dashboardFixture(t)
	_, token, err := a.createDashboardSession(key, "")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/v1/dashboard/projects/"+project.ProjectID+"/events", nil)
	request.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: token})
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || response.Header.Get("Content-Type") != "text/event-stream" || response.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("unexpected SSE headers: %v", response)
	}
	reader := bufio.NewReader(response.Body)
	read := func() (string, int64) {
		t.Helper()
		event, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		data, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if _, err := reader.ReadString('\n'); err != nil {
			t.Fatal(err)
		}
		var body struct {
			Revision int64 `json:"revision"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(data, "data: ")), &body); err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(strings.TrimPrefix(event, "event: ")), body.Revision
	}
	event, before := read()
	if event != "graph" || before < 1 {
		t.Fatal("missing initial revision")
	}
	if err := a.store.update(func(state *databaseState) error { markDashboardChanged(state, project.ProjectID); return nil }); err != nil {
		t.Fatal(err)
	}
	event, after := read()
	if event != "graph" || after != before+1 {
		t.Fatal("missing committed revision")
	}
	if err := a.store.update(func(state *databaseState) error {
		state.Memberships.Delete(membershipKey(project.ProjectID, actor.AccountID))
		state.dashboardAccessChanged = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	event, _ = read()
	if event != "access_revoked" {
		t.Fatal("revoked membership kept stream alive")
	}
}
