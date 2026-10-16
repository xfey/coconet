package workgraph

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"testing"
)

// Exercise legal histories, including snapshots ahead of processing, none,
// mutable-source advances, ended sessions, late relations, and Library seals.
// Restoring every sampled boundary must preserve the exact subsequent result.
func TestCheckpointRoundTripsGeneratedHistories(t *testing.T) {
	for seed := int64(0); seed < 8; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			s := newScenario(t)
			baselines := map[int]State{}
			nodeIDs := []string{}
			check := func() {
				t.Helper()
				if err := ValidateState(s.state); err != nil {
					t.Fatalf("revision %d: %v", s.state.Revision, err)
				}
				if len(s.commits)%13 == 0 {
					data, err := json.Marshal(s.state)
					if err != nil {
						t.Fatal(err)
					}
					var restored State
					if err := json.Unmarshal(data, &restored); err != nil {
						t.Fatal(err)
					}
					baselines[len(s.commits)] = restored
				}
			}
			for step := 0; step < 60; step++ {
				id := fmt.Sprintf("session_%d", rng.Intn(5))
				session := s.state.Sessions[id]
				turns := s.state.Snapshots[(Source{id, session.Latest}).Key()].Turns + rng.Intn(4)
				source := Source{id, session.Latest + 1}
				s.commit("owner_"+id, func(c *Commit) {
					c.Snapshot = &Snapshot{Source: source, Owner: c.Actor, Agent: "fixture", NativeID: id, Turns: turns, Digest: Digest(source), Conversation: Digest("conversation"), Ended: rng.Intn(10) == 0, ObservedAt: testTime}
				})
				check()
				// Sometimes leave captures queued. Later processing must not skip them.
				if rng.Intn(4) == 0 {
					continue
				}
				for version := session.ProcessedThrough + 1; version <= source.Version; version++ {
					current := s.state.Nodes[s.state.Sessions[id].Tail]
					action := "none"
					if rng.Intn(4) != 0 {
						action = "start"
						if current.State == "current" && rng.Intn(3) != 0 {
							action = "continue"
						}
					}
					node, objective := "", ""
					if action == "start" {
						node = fmt.Sprintf("node_%d", len(nodeIDs))
						objective = "Complete a distinct outcome"
						nodeIDs = append(nodeIDs, node)
					}
					s.stage(Source{id, version}, action, node, objective)
					check()
					if action == "none" {
						continue
					}
					if node == "" {
						node = current.ID
					}
					if rng.Intn(3) == 0 {
						n := s.state.Nodes[node]
						s.commit("owner_"+id, func(c *Commit) {
							c.Relations = &RelationDecision{NodeID: node, NodeRevision: n.Revision, Through: n.EndTurn, Complete: false, Reason: "Need more context"}
						})
					} else {
						parents := []string{}
						// Only earlier-created nodes: no accidental cycle in a valid trace.
						for _, candidate := range nodeIDs {
							if candidate == node {
								break
							}
							if rng.Intn(20) == 0 {
								parents = append(parents, candidate)
							}
						}
						s.relations(node, parents...)
					}
					check()
					if rng.Intn(8) == 0 {
						s.commit("owner_"+id, func(c *Commit) { c.Library = &LibraryChange{NodeID: node, Note: "Keep this decision"} })
						check()
					}
				}
			}
			for boundary, state := range baselines {
				for _, commit := range s.commits[boundary:] {
					var err error
					state, _, err = Apply(state, commit)
					if err != nil {
						t.Fatalf("boundary %d: %v", boundary, err)
					}
				}
				if Digest(state) != Digest(s.state) {
					t.Fatalf("boundary %d changed replay", boundary)
				}
			}
			t.Logf("commits=%d checkpoint_boundaries=%d nodes=%d", len(s.commits), len(baselines), len(s.state.Nodes))
		})
	}
}

func TestCheckpointRoundTripsRepeatedRewinds(t *testing.T) {
	for seed := int64(0); seed < 8; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			s := newScenario(t)
			for step := 0; step < 60; step++ {
				session := s.state.Sessions["session"]
				prior := s.state.Snapshots[(Source{"session", session.Latest}).Key()]
				turns := prior.Turns + 1
				var rewrite *ConversationRewrite
				if prior.Turns > 0 && rng.Intn(3) == 0 {
					common := rng.Intn(prior.Turns)
					turns = common + rng.Intn(3)
					rewrite = &ConversationRewrite{PreviousVersion: session.Latest, CommonTurns: common, RestoreEditableTails: true}
				}
				source := Source{"session", session.Latest + 1}
				s.commit("alice", func(c *Commit) {
					c.Snapshot = &Snapshot{Source: source, Owner: c.Actor, Agent: "codex", NativeID: "native", Turns: turns, Conversation: Digest(source), Digest: Digest(source), Rewrite: rewrite, ObservedAt: testTime}
				})
				if err := ValidateState(s.state); err != nil {
					t.Fatalf("capture %d: %v", step, err)
				}
				session = s.state.Sessions["session"]
				action, node := "none", ""
				if turns > session.ConsumedTurns {
					action, node = "start", fmt.Sprintf("node_%d", step)
					if s.state.Nodes[session.Tail].State == "current" && rng.Intn(2) == 0 {
						action, node = "continue", ""
					}
				}
				s.commit("alice", func(c *Commit) {
					c.Stage = &StageDecision{Checkpoint: true, Source: source, SessionRevision: session.Revision, Action: action, NewNodeID: node, Reason: "valid new evidence"}
					if action != "none" {
						c.Stage.Text = &Text{Objective: "work"}
					}
				})
				if err := ValidateState(s.state); err != nil {
					t.Fatalf("stage %d: %v", step, err)
				}
				encoded, _ := json.Marshal(s.state)
				var restored State
				if err := json.Unmarshal(encoded, &restored); err != nil {
					t.Fatal(err)
				}
				if err := ValidateState(restored); err != nil {
					t.Fatal(err)
				}
				replay := New("project")
				for _, commit := range s.commits {
					var err error
					replay, _, err = Apply(replay, commit)
					if err != nil {
						t.Fatal(err)
					}
				}
				if Digest(replay) != Digest(restored) {
					t.Fatal("replay diverged after rewind")
				}
			}
		})
	}
}
