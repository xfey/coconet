package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/conversation"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/localdag"
	"github.com/xfey/coconet/packages/replication"
	"github.com/xfey/coconet/packages/workgraph"
)

// Synthetic producer exercises the actual client, HTTP Relay and browser
// handler. It has no inference runner and never reads a native Agent history.
func publishedGraphFixture(t *testing.T, a *authority, h *serverHandler, account contracts.Account, project, token string) (*replication.Client, func(), func()) {
	t.Helper()
	server := httptest.NewTLSServer(h)
	t.Cleanup(server.Close)
	s, err := graphstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	c := &replication.Client{Store: s, Remote: replication.HTTP{BaseURL: server.URL, Token: token, Client: server.Client()}, Account: account.AccountID, Device: "fixture_device"}
	e := &localdag.Engine{Store: s, Publisher: c, Actor: c.Writer()}
	ctx := context.Background()
	apply := func(id string, stage *workgraph.StageDecision, relations *workgraph.RelationDecision, library *workgraph.LibraryChange) {
		t.Helper()
		state, _ := s.State(project)
		commit := workgraph.Commit{Schema: 1, ID: id, ProjectID: project, Actor: c.Writer(), Base: state.Revision, At: a.timestamp(), RunID: "run_" + id, Stage: stage, Relations: relations, Library: library}
		if err := c.Publish(ctx, commit); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Apply(commit, false); err != nil {
			t.Fatal(err)
		}
	}
	for i, id := range []string{"api", "docs"} {
		source, err := e.Capture(ctx, localdag.Capture{ProjectID: project, Agent: "claude", NativeID: id, Entries: []conversation.Entry{{Turn: contracts.NormalizedTurn{TurnID: "turn", Ordinal: 0, Role: "user", Kind: "message", Text: "理解，先把重试停止的几种情况讲清楚。"}}}, Artifact: []byte("private synthetic native fixture")})
		if err != nil {
			t.Fatal(err)
		}
		objective := "完善重试接口"
		summary := "完成有界重试，测试通过。"
		if i == 1 {
			objective = "补充停止条件说明"
			summary = "沿用已完成的重试接口说明停止条件。<img src=x onerror=\"window.fixtureXSS=true\">"
		}
		apply("stage_"+id, &workgraph.StageDecision{Source: source, Action: "start", NewNodeID: id, Text: &workgraph.Text{Objective: objective, Summary: summary, Approach: "按已验证的行为说明边界，避免扩大结论。"}, Reason: "明确交付", Items: workgraph.ItemChanges{Add: []workgraph.Item{{ID: "followup", Text: "后续检查大文件场景"}}}}, nil, nil)
	}
	state, _ := s.State(project)
	parent := state.Nodes["api"]
	apply("rel_api", nil, &workgraph.RelationDecision{NodeID: "api", NodeRevision: 1, Through: 1, Complete: true, Reason: "独立工作"}, nil)
	apply("rel_docs", nil, &workgraph.RelationDecision{NodeID: "docs", NodeRevision: 1, Through: 1, Complete: true, Reason: "使用接口结果", Parents: []workgraph.Parent{{NodeID: "api", Revision: parent.Revision, Source: parent.Source, Reason: "沿用重试行为", Evidence: []string{"turn"}}}}, nil)
	apply("library", nil, nil, &workgraph.LibraryChange{NodeID: "api", Note: "固定接口行为"})
	publish := func() {
		t.Helper()
		if err := c.PublishCheckpoint(ctx, project); err != nil {
			t.Fatal(err)
		}
	}
	publish()
	advance := func() {
		t.Helper()
		if err := c.Sync(ctx, project); err != nil {
			t.Fatal(err)
		}
		state, _ := s.State(project)
		node := state.Nodes["docs"]
		session := state.Sessions[node.Source.SessionID]
		input := localdag.Capture{ProjectID: project, Agent: "claude", NativeID: "docs", Entries: []conversation.Entry{{Turn: contracts.NormalizedTurn{TurnID: "turn", Ordinal: 0, Role: "user", Kind: "message", Text: "理解，先把重试停止的几种情况讲清楚。"}}, {Turn: contracts.NormalizedTurn{TurnID: "turn_2", Ordinal: 1, Role: "user", Kind: "message", Text: "这个提示还是有点奇怪，改得直接一些。"}}}}
		source, err := e.Capture(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		apply("stage_updated", &workgraph.StageDecision{Source: source, SessionRevision: session.Revision, Action: "continue", Text: &workgraph.Text{Objective: "简化停止条件说明", Summary: "已调整措辞，关系检查尚未完成。"}, Reason: "同一目标的小返修"}, nil, nil)
	}
	return c, advance, publish
}

func TestPublishedGraphReadAccessAndDurableEventCursor(t *testing.T) {
	a, h, actor, project, token := dashboardFixture(t)
	_, cookie, err := a.createDashboardSession(token, "")
	if err != nil {
		t.Fatal(err)
	}
	_, advance, publish := publishedGraphFixture(t, a, h, actor, project.ProjectID, token)
	server := httptest.NewTLSServer(h)
	defer server.Close()
	etag := ""
	request := func() *http.Response {
		t.Helper()
		r, _ := http.NewRequest("GET", server.URL+"/v1/dashboard/projects/"+project.ProjectID+"/published-graph", nil)
		r.Header.Set("If-None-Match", etag)
		r.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: cookie})
		out, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	response := request()
	var body struct {
		Relay         contracts.RelayCheckpointPage `json:"relay"`
		EventRevision int64                         `json:"event_revision"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 || body.Relay.Checkpoint == nil {
		t.Fatal("missing hosted checkpoint")
	}
	etag = response.Header.Get("ETag")
	if etag == "" {
		t.Fatal("missing conditional validator")
	}
	unchanged := request()
	unchanged.Body.Close()
	if unchanged.StatusCode != 304 {
		t.Fatal("unchanged graph transferred again")
	}
	if err := a.store.update(func(s *databaseState) error {
		account, _ := s.Accounts.Lookup(actor.AccountID)
		account.DisplayName = "renamed"
		s.Accounts.Set(actor.AccountID, account)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	renamed := request()
	renamed.Body.Close()
	if renamed.StatusCode != 200 || renamed.Header.Get("ETag") == etag {
		t.Fatal("author change was hidden by graph cache")
	}
	etag = renamed.Header.Get("ETag")
	event, _, err := a.dashboardEventState(cookie, project.ProjectID)
	if err != nil || event != body.EventRevision {
		t.Fatal("event cursor differs from view")
	}
	updates, stop, _ := a.store.events.subscribe(project.ProjectID)
	defer stop()
	advance()
	select {
	case <-updates:
		t.Fatal("unpublished commit notified hosted page")
	default:
	}
	one, _, _ := a.dashboardEventState(cookie, project.ProjectID)
	if one != event {
		t.Fatal("unpublished commit advanced visible cursor")
	}
	publish()
	select {
	case <-updates:
	default:
		t.Fatal("new checkpoint did not notify hosted page")
	}
	two, _, _ := a.dashboardEventState(cookie, project.ProjectID)
	if two <= one {
		t.Fatal("checkpoint at same head did not notify")
	}
	publish()
	three, _, _ := a.dashboardEventState(cookie, project.ProjectID)
	if three != two {
		t.Fatal("unchanged checkpoint caused refresh")
	}
	if err := a.store.update(func(s *databaseState) error {
		s.Memberships.Delete(membershipKey(project.ProjectID, actor.AccountID))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	response = request()
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("revoked member read checkpoint")
	}
}

func TestPublishedGraphBrowserHarness(t *testing.T) {
	file := os.Getenv("COCONET_PUBLISHED_GRAPH_BROWSER_FIXTURE")
	if file == "" {
		t.Skip("isolated browser fixture")
	}
	a, h, actor, project, key := dashboardFixture(t)
	a.now = time.Now
	empty, err := a.createProject(actor, contracts.CreateProjectRequest{SchemaVersion: 1, RequestID: "published-empty", Name: "Empty"})
	if err != nil {
		t.Fatal(err)
	}
	_, advance, publish := publishedGraphFixture(t, a, h, actor, project.ProjectID, key)
	member, memberKey := issueTestIdentity(t, a, "Colleague", "colleague@coconet.test")
	if _, err := a.joinProject(member, project.ProjectID, "browser-member"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(h)
	defer server.Close()
	prefix := os.Getenv("COCONET_BROWSER_PREFIX")
	h.advertisedEndpoint = server.URL + prefix
	data, _ := json.Marshal(map[string]string{"url": server.URL + prefix, "key": key, "member_key": memberKey, "project_id": project.ProjectID, "empty_project_id": empty.Project.ProjectID})
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(file + ".advance"); err == nil {
			os.Remove(file + ".advance")
			advance()
		}
		if _, err := os.Stat(file + ".publish"); err == nil {
			os.Remove(file + ".publish")
			publish()
		}
		if _, err := os.Stat(file + ".stop"); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("browser harness timed out")
}
