package workgraph

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

const testTime = "2026-10-09T00:00:00Z"

type scenario struct {
	state   State
	commits []Commit
	t       *testing.T
}

func newScenario(t *testing.T) *scenario { return &scenario{state: New("project"), t: t} }
func (s *scenario) commit(actor string, configure func(*Commit)) Change {
	s.t.Helper()
	c := Commit{Schema: 1, ID: fmt.Sprintf("commit_%d", len(s.commits)), ProjectID: "project", Actor: actor, Base: s.state.Revision, RunID: fmt.Sprintf("run_%d", len(s.commits)), At: testTime}
	configure(&c)
	next, change, err := Apply(s.state, c)
	if err != nil {
		s.t.Fatal(err)
	}
	s.state = next
	s.commits = append(s.commits, c)
	return change
}
func (s *scenario) capture(id, actor string, turns int) Source {
	source := Source{SessionID: id, Version: s.state.Sessions[id].Latest + 1}
	s.commit(actor, func(c *Commit) {
		c.Snapshot = &Snapshot{Source: source, Owner: actor, Agent: "codex", NativeID: id, Turns: turns, Digest: Digest(source), Conversation: Digest("conversation"), ObservedAt: testTime}
	})
	return source
}
func (s *scenario) stage(source Source, action, node, objective string) {
	session := s.state.Sessions[source.SessionID]
	s.commit(session.Owner, func(c *Commit) {
		c.Stage = &StageDecision{Source: source, SessionRevision: session.Revision, Action: action, NewNodeID: node, Reason: "accepted outcome"}
		if objective != "" {
			c.Stage.Text = &Text{Objective: objective, Summary: "work in progress"}
		}
	})
}
func (s *scenario) relations(id string, parents ...string) Change {
	node := s.state.Nodes[id]
	return s.commit(s.state.Sessions[node.Source.SessionID].Owner, func(c *Commit) {
		c.Relations = &RelationDecision{NodeID: id, NodeRevision: node.Revision, Through: node.EndTurn, Complete: true, Reason: "checked actual result use"}
		for _, id := range parents {
			p := s.state.Nodes[id]
			c.Relations.Parents = append(c.Relations.Parents, Parent{NodeID: id, Revision: p.Revision, Source: p.Source, Reason: "adopts this result", Evidence: []string{"turn_2"}})
		}
	})
}

func TestSameOutcomeAdvancesSourceWithoutRewritingText(t *testing.T) {
	s := newScenario(t)
	v1 := s.capture("session", "alice", 2)
	s.stage(v1, "start", "retry", "实现重试机制")
	s.relations("retry")
	before := s.state.Nodes["retry"]
	v2 := s.capture("session", "alice", 4)
	s.stage(v2, "continue", "", "")
	node := s.state.Nodes["retry"]
	if node.Source != v2 || node.Text != before.Text || len(s.state.Nodes) != 1 {
		t.Fatal("same-stage confirmation must advance source without replacing text")
	}
	if node.RelationThrough != 2 || node.Relations != "pending" || s.state.Sessions["session"].ConsumedTurns != 4 {
		t.Fatal("relation progress must not silently follow stage progress")
	}
	roots := RetainedSources(s.state)
	if roots[v1.Key()] || !roots[v2.Key()] {
		t.Fatal("superseded source should not remain a graph retention root")
	}
}

func TestLaterResultUseAddsEdgeToExistingNodeAndSealsSupplier(t *testing.T) {
	s := newScenario(t)
	a := s.capture("auth_session", "alice", 2)
	s.stage(a, "start", "auth", "完成鉴权方案")
	b := s.capture("retry_session", "bob", 2)
	s.stage(b, "start", "retry", "实现重试机制")
	s.relations("retry")
	b = s.capture("retry_session", "bob", 4)
	s.stage(b, "continue", "", "")
	base := s.state.Sessions["auth_session"].Revision
	change := s.relations("retry", "auth")
	if len(s.state.Nodes) != 2 || len(s.state.Nodes["retry"].Parents) != 1 || s.state.Nodes["retry"].Parents[0].Source != a {
		t.Fatal("late dependency must not create another stage or move its supplier")
	}
	if s.state.Nodes["auth"].State != "sealed" || s.state.Sessions["auth_session"].Revision <= base {
		t.Fatal("supplier owner must observe a new baseline after sealing")
	}
	if !reflect.DeepEqual(change.Nodes, []string{"auth", "retry"}) {
		t.Fatal("audit must include both the edge and foreign sealing")
	}
	if s.state.Nodes["retry"].RelationThrough != 4 {
		t.Fatal("successful relation processing did not consume its complete window")
	}
}

func TestNoContributionDoesNotPretendToConfirmContinuity(t *testing.T) {
	s := newScenario(t)
	v1 := s.capture("session", "alice", 2)
	s.stage(v1, "start", "node", "改进重试")
	v2 := s.capture("session", "alice", 4)
	s.stage(v2, "none", "", "")
	if s.state.Nodes["node"].Source != v1 || s.state.Sessions["session"].ProcessedThrough != 2 {
		t.Fatal("no contribution must consume input without rewriting the node source")
	}
	if len(RetainedSources(s.state)) != 2 {
		t.Fatal("both the latest snapshot and the fixed node remain roots")
	}
}

func TestIndependentOutcomeDoesNotAcquireAutomaticSequentialEdge(t *testing.T) {
	s := newScenario(t)
	a := s.capture("session", "alice", 2)
	s.stage(a, "start", "retry", "改进重试")
	b := s.capture("session", "alice", 4)
	s.stage(b, "start", "import", "评估批量导入")
	if s.state.Nodes["retry"].State != "sealed" || s.state.Nodes["retry"].Source != a || len(s.state.Nodes["import"].Parents) != 0 {
		t.Fatal("stage transition must seal the previous source without inventing dependency")
	}
}

func TestInvalidRelationsAreAtomicAndDoNotSealSources(t *testing.T) {
	s := newScenario(t)
	a := s.capture("sa", "alice", 2)
	s.stage(a, "start", "a", "第一项")
	b := s.capture("sb", "bob", 2)
	s.stage(b, "start", "b", "第二项")
	s.relations("b", "a")
	before := Digest(s.state)
	target := s.state.Nodes["a"]
	parent := s.state.Nodes["b"]
	c := Commit{Schema: 1, ID: "cycle", ProjectID: "project", Actor: "alice", Base: s.state.Revision, RunID: "attempt", At: testTime, Relations: &RelationDecision{NodeID: "a", NodeRevision: target.Revision, Through: target.EndTurn, Complete: true, Reason: "proposal", Parents: []Parent{{NodeID: "b", Revision: parent.Revision, Source: parent.Source, Reason: "uses", Evidence: []string{"turn_1"}}}}}
	if _, _, err := Apply(s.state, c); err == nil {
		t.Fatal("cycle accepted")
	}
	if Digest(s.state) != before || s.state.Nodes["b"].State != "current" {
		t.Fatal("failed commit mutated its input")
	}
	c.Base--
	if _, _, err := Apply(s.state, c); !errors.Is(err, ErrConflict) {
		t.Fatalf("wanted conflict, got %v", err)
	}
}

func TestOldRelationCandidateCannotApplyToAdvancedNode(t *testing.T) {
	s := newScenario(t)
	a := s.capture("s", "alice", 2)
	s.stage(a, "start", "node", "实现功能")
	node := s.state.Nodes["node"]
	b := s.capture("s", "alice", 4)
	s.stage(b, "continue", "", "")
	c := Commit{Schema: 1, ID: "old", ProjectID: "project", Actor: "alice", Base: s.state.Revision, RunID: "attempt", At: testTime, Relations: &RelationDecision{NodeID: node.ID, NodeRevision: node.Revision, Through: node.EndTurn, Complete: true, Reason: "checked"}}
	if _, _, err := Apply(s.state, c); !errors.Is(err, ErrConflict) {
		t.Fatal("stale relationship candidate applied")
	}
	if s.state.Nodes["node"].RelationThrough != 0 {
		t.Fatal("stale attempt consumed evidence")
	}
}

func TestSavedCommitsReplayExactly(t *testing.T) {
	s := newScenario(t)
	a := s.capture("sa", "alice", 2)
	s.stage(a, "start", "auth", "鉴权方案")
	b := s.capture("sb", "bob", 2)
	s.stage(b, "start", "retry", "重试机制")
	s.relations("retry", "auth")
	replayed := New("project")
	for _, c := range s.commits {
		next, _, err := Apply(replayed, c)
		if err != nil {
			t.Fatal(err)
		}
		replayed = next
	}
	if !reflect.DeepEqual(replayed, s.state) {
		t.Fatal("same saved commits did not reproduce the same graph")
	}
}

func TestItemsAndNodeChangesCommitTogether(t *testing.T) {
	s := newScenario(t)
	source := s.capture("session", "alice", 2)
	before := Digest(s.state)
	c := Commit{Schema: 1, ID: "bad_items", ProjectID: "project", Actor: "alice", Base: s.state.Revision, RunID: "attempt", At: testTime, Stage: &StageDecision{Source: source, Action: "start", Text: &Text{Objective: "修复"}, NewNodeID: "node", Reason: "new accepted goal", Items: ItemChanges{Complete: []string{"missing"}}}}
	if _, _, err := Apply(s.state, c); err == nil || Digest(s.state) != before {
		t.Fatal("invalid item change partially published a node")
	}
	c.Stage.Items = ItemChanges{Add: []Item{{ID: "item", Text: "完成修复与测试"}}}
	next, _, err := Apply(s.state, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Nodes) != 1 || len(next.Sessions["session"].Items) != 1 {
		t.Fatal("node and item did not publish together")
	}
}

func TestMissingSourceAndForeignWriterAreRejected(t *testing.T) {
	s := newScenario(t)
	source := s.capture("session", "alice", 2)
	c := Commit{Schema: 1, ID: "bad", ProjectID: "project", Actor: "bob", Base: s.state.Revision, RunID: "attempt", At: testTime, Stage: &StageDecision{Source: source, Action: "none", Reason: "checked"}}
	if _, _, err := Apply(s.state, c); !errors.Is(err, ErrPermission) {
		t.Fatalf("foreign writer accepted: %v", err)
	}
	c.Actor = "alice"
	c.Stage.Source.Version = 9
	if _, _, err := Apply(s.state, c); !errors.Is(err, ErrSource) {
		t.Fatalf("missing source accepted: %v", err)
	}
}
