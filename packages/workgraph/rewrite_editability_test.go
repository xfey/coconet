package workgraph

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func checkpointStage(s *scenario, source Source, id string) {
	s.t.Helper()
	s.commit(s.state.Sessions[source.SessionID].Owner, func(c *Commit) {
		c.Stage = &StageDecision{Checkpoint: true, Source: source, SessionRevision: s.state.Sessions[source.SessionID].Revision, Action: "start", NewNodeID: id, Text: &Text{Objective: id}, Reason: "new work"}
	})
}

func rewindTo(s *scenario, sessionID string, turns int, restore, ended bool) Source {
	s.t.Helper()
	session := s.state.Sessions[sessionID]
	source := Source{sessionID, session.Latest + 1}
	s.commit(session.Owner, func(c *Commit) {
		c.Snapshot = &Snapshot{Source: source, Owner: session.Owner, Agent: session.Agent, NativeID: session.NativeID, Turns: turns, Ended: ended, Digest: Digest(source), Conversation: Digest(source), ObservedAt: testTime,
			Rewrite: &ConversationRewrite{PreviousVersion: session.Latest, CommonTurns: turns, RestoreEditableTails: restore}}
	})
	return source
}

func checkRewindReplay(s *scenario) {
	s.t.Helper()
	if err := ValidateState(s.state); err != nil {
		s.t.Fatal(err)
	}
	state := New("project")
	for _, c := range s.commits {
		data, _ := json.Marshal(c)
		var restored Commit
		if err := json.Unmarshal(data, &restored); err != nil {
			s.t.Fatal(err)
		}
		var err error
		state, _, err = Apply(state, restored)
		if err != nil {
			s.t.Fatal(err)
		}
	}
	if Digest(state) != Digest(s.state) {
		s.t.Fatal("rewind replay diverged")
	}
}

func TestRewindRestoresOnlyUnpinnedLiveTail(t *testing.T) {
	for _, mode := range []string{"editable", "referenced", "collected", "ended", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			s := newScenario(t)
			for i, id := range []string{"a", "b", "c"} {
				checkpointStage(s, s.capture("own", "alice", i+1), id)
			}
			if mode == "referenced" {
				checkpointStage(s, s.capture("peer", "bob", 1), "peer")
				s.relations("peer", "b")
			}
			if mode == "collected" {
				s.commit("alice", func(c *Commit) { c.Library = &LibraryChange{NodeID: "b"} })
			}
			before := s.state.Nodes["b"]
			revision := s.state.Sessions["own"].Revision
			rewindTo(s, "own", 2, mode != "legacy", mode == "ended")
			after := s.state.Nodes["b"]
			want := "sealed"
			if mode == "editable" {
				want = "current"
				if after.Revision != before.Revision+1 || s.state.Sessions["own"].Revision <= revision {
					t.Fatal("restoration did not fence stale work")
				}
			} else if after.Revision != before.Revision {
				t.Fatal("protected or legacy tail changed revision")
			}
			if after.State != want || s.state.Nodes["a"].State != "sealed" || s.state.Sessions["own"].Tail != "b" {
				t.Fatal("wrong tail mutability", s.state.Nodes)
			}
			if _, exists := s.state.Nodes["c"]; exists {
				t.Fatal("withdrawn work remains")
			}
			after.State, after.Revision, after.UpdatedAt = before.State, before.Revision, before.UpdatedAt
			if !reflect.DeepEqual(after, before) {
				t.Fatal("restoration changed fixed content")
			}
			if mode == "legacy" {
				data, _ := json.Marshal(s.commits[len(s.commits)-1])
				if strings.Contains(string(data), "restore_editable_tails") {
					t.Fatal("legacy serialization changed")
				}
			}
			checkRewindReplay(s)
		})
	}
}

func TestRewindReleasesForeignTailOnlyAfterLastReference(t *testing.T) {
	for _, mode := range []string{"editable", "collected", "ended", "non_tail", "withdrawn_child", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			s := newScenario(t)
			checkpointStage(s, s.capture("supplier", "bob", 1), "supplier")
			if mode == "ended" {
				v := Source{"supplier", 2}
				s.commit("bob", func(c *Commit) {
					c.Snapshot = &Snapshot{Source: v, Owner: "bob", Agent: "codex", NativeID: "supplier", Turns: 2, Ended: true, Digest: Digest(v), Conversation: Digest(v), ObservedAt: testTime}
				})
				s.stage(v, "none", "", "")
			}
			if mode == "collected" {
				s.commit("bob", func(c *Commit) { c.Library = &LibraryChange{NodeID: "supplier"} })
			}
			if mode == "non_tail" {
				checkpointStage(s, s.capture("supplier", "bob", 2), "new_tail")
			}
			for _, id := range []string{"first", "second"} {
				checkpointStage(s, s.capture(id, "alice", 1), id)
				s.relations(id, "supplier")
			}
			if mode == "withdrawn_child" {
				s.commit("alice", func(c *Commit) { c.Library = &LibraryChange{NodeID: "second"} })
			}
			before := s.state.Nodes["supplier"]
			beforeSession := s.state.Sessions["supplier"]
			rewindTo(s, "first", 0, mode != "legacy", false)
			if s.state.Nodes["supplier"].State != "sealed" {
				t.Fatal("remaining reference lost its protection")
			}
			rewindTo(s, "second", 0, mode != "legacy", false)
			after := s.state.Nodes["supplier"]
			want := "sealed"
			if mode == "editable" || mode == "withdrawn_child" {
				want = "current"
				if after.Revision != before.Revision+1 || s.state.Sessions["supplier"].Revision != beforeSession.Revision+1 {
					t.Fatal("foreign baseline not advanced")
				}
			}
			if after.State != want {
				t.Fatal("wrong foreign tail state", after)
			}
			after.State, after.Revision, after.UpdatedAt = before.State, before.Revision, before.UpdatedAt
			if !reflect.DeepEqual(after, before) {
				t.Fatal("foreign content changed")
			}
			if mode == "withdrawn_child" && (s.state.Nodes["second"].State != "withdrawn" || len(s.state.Nodes["second"].Parents) != 0) {
				t.Fatal("collected withdrawn child kept a pin")
			}
			checkRewindReplay(s)
		})
	}
}

func TestRewindRestorationFencesForeignStageAndRelations(t *testing.T) {
	s := newScenario(t)
	checkpointStage(s, s.capture("supplier", "bob", 1), "supplier")
	checkpointStage(s, s.capture("consumer", "alice", 1), "consumer")
	s.relations("consumer", "supplier")
	checkpointStage(s, s.capture("unrelated", "bob", 1), "unrelated")
	s.commit("bob", func(c *Commit) { c.Library = &LibraryChange{NodeID: "unrelated"} })
	s.commit("bob", func(c *Commit) { c.Library = &LibraryChange{NodeID: "unrelated", Remove: true} })
	untouched := s.state.Nodes["unrelated"]
	v := s.capture("supplier", "bob", 2)
	oldSession, oldNode := s.state.Sessions["supplier"], s.state.Nodes["supplier"]
	rewindTo(s, "consumer", 0, true, false)
	if !reflect.DeepEqual(s.state.Nodes["unrelated"], untouched) {
		t.Fatal("unrelated tail reopened")
	}
	stale := []Commit{
		{Stage: &StageDecision{Source: v, SessionRevision: oldSession.Revision, Action: "start", NewNodeID: "split", Text: &Text{Objective: "stale split"}, Reason: "old input"}},
		{Relations: &RelationDecision{NodeID: "supplier", NodeRevision: oldNode.Revision, Through: oldNode.EndTurn, Complete: true, Reason: "old input"}},
		{Relations: &RelationDecision{NodeID: "unrelated", NodeRevision: untouched.Revision, Through: untouched.EndTurn, Complete: true, Reason: "old parent", Parents: []Parent{{NodeID: "supplier", Revision: oldNode.Revision, Source: oldNode.Source, Reason: "uses old state", Evidence: []string{"e1"}}}}},
	}
	for _, c := range stale {
		c.Schema, c.ID, c.ProjectID, c.Actor, c.Base, c.RunID, c.At = 1, "stale", "project", "bob", s.state.Revision, "stale", testTime
		if _, _, err := Apply(s.state, c); !errors.Is(err, ErrConflict) {
			t.Fatal("stale work accepted", err)
		}
	}
	s.stage(v, "continue", "", "continued supplier")
	if s.state.Sessions["supplier"].Tail != "supplier" {
		t.Fatal("unnecessary new node")
	}
	checkRewindReplay(s)
}
