package workgraph

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func TestRetirementKeepsBoundariesReferencesAndPendingWork(t *testing.T) {
	s := newScenario(t)
	first := s.capture("one", "alice", 2)
	s.stage(first, "start", "kept", "First outcome")
	s.commit("alice", func(c *Commit) { c.Library = &LibraryChange{NodeID: "kept", Note: "Keep original decision"} })
	second := s.capture("one", "alice", 4)
	s.stage(second, "start", "current", "Second outcome")
	third := s.capture("one", "alice", 6)
	s.stage(third, "continue", "", "")
	fourth := s.capture("one", "alice", 8)
	s.stage(fourth, "continue", "", "")
	pending := s.capture("one", "alice", 10)
	latest := s.capture("one", "alice", 12)
	other := s.capture("two", "bob", 2)
	s.stage(other, "start", "consumer", "Uses earlier work")
	s.relations("consumer", "kept")
	candidates := RetirementCandidates(s.state, "alice", map[string]bool{third.Key(): true})
	if len(candidates) != 1 || candidates[0] != second {
		t.Fatal(candidates)
	}
	before := Digest(s.state)
	for _, source := range []Source{first, fourth, pending, latest, other} {
		commit := Commit{Schema: 1, ID: "reject", ProjectID: "project", Actor: "alice", Base: s.state.Revision, RunID: "r", At: testTime, Retire: &Retirement{Sources: []Source{second, source}}}
		if _, _, err := Apply(s.state, commit); err == nil {
			t.Fatal("protected source retired", source)
		}
		if Digest(s.state) != before {
			t.Fatal("rejected collection mutated source state")
		}
	}
	s.commit("alice", func(c *Commit) { c.Retire = &Retirement{Sources: candidates} })
	if _, ok := s.state.Snapshots[second.Key()]; ok {
		t.Fatal("retired descriptor remains")
	}
	if s.state.Sessions["one"].Latest != 6 || s.state.Sessions["one"].ProcessedThrough != 4 {
		t.Fatal("version counters changed")
	}
	if err := ValidateState(s.state); err != nil {
		t.Fatal(err)
	}
	// The missing version never becomes writable/readable again.
	retired := Snapshot{Source: second, Owner: "alice", Agent: "codex", NativeID: "one", Turns: 4, Digest: Digest(second), Conversation: Digest("conversation")}
	commit := Commit{Schema: 1, ID: "resurrection", ProjectID: "project", Actor: "alice", Base: s.state.Revision, RunID: "r", At: testTime, Snapshot: &retired}
	if _, _, err := Apply(s.state, commit); err == nil {
		t.Fatal("retired version was resurrected")
	}
	commit.Snapshot = nil
	commit.Stage = &StageDecision{Source: second, SessionRevision: s.state.Sessions["one"].Revision, Action: "continue", Reason: "backtrack"}
	if _, _, err := Apply(s.state, commit); !errors.Is(err, ErrSource) {
		t.Fatal(err)
	}
	// Pending versions still process sequentially after checkpoint recovery.
	data, _ := json.Marshal(s.state)
	var restored State
	json.Unmarshal(data, &restored)
	s.stage(pending, "continue", "", "")
	s.stage(latest, "continue", "", "")
	for _, c := range s.commits[len(s.commits)-2:] {
		var err error
		restored, _, err = Apply(restored, c)
		if err != nil {
			t.Fatal(err)
		}
	}
	if Digest(restored) != Digest(s.state) {
		t.Fatal("retirement checkpoint replay diverged")
	}
	replay := New("project")
	for _, c := range s.commits {
		var err error
		replay, _, err = Apply(replay, c)
		if err != nil {
			t.Fatal(err)
		}
	}
	if Digest(replay) != Digest(s.state) {
		t.Fatal("full replay diverged")
	}
}

func TestSparseCheckpointRejectsUnprocessedGapsAndRegressions(t *testing.T) {
	s := newScenario(t)
	a := s.capture("s", "alice", 2)
	s.stage(a, "none", "", "")
	b := s.capture("s", "alice", 4)
	s.stage(b, "none", "", "")
	pending := s.capture("s", "alice", 6)
	latest := s.capture("s", "alice", 8)
	s.commit("alice", func(c *Commit) { c.Retire = &Retirement{Sources: []Source{a}} })
	if err := ValidateState(s.state); err != nil {
		t.Fatal(err)
	}
	for _, source := range []Source{b, pending, latest} {
		broken := cloneState(s.state)
		delete(broken.Snapshots, source.Key())
		if ValidateState(broken) == nil {
			t.Fatal("accepted missing protected boundary", source)
		}
	}
	broken := cloneState(s.state)
	snapshot := broken.Snapshots[latest.Key()]
	snapshot.Turns = 1
	broken.Snapshots[latest.Key()] = snapshot
	if ValidateState(broken) == nil {
		t.Fatal("accepted backwards conversation")
	}
}

func TestRetirementShrinksCheckpointIndependentlyOfHistoricVersions(t *testing.T) {
	state := scaleState(1000, 50)
	before, _ := json.Marshal(state)
	rounds := 0
	for {
		sources := RetirementCandidates(state, "account.device", nil)
		if len(sources) == 0 {
			break
		}
		id := fmt.Sprintf("retire_%d", rounds)
		commit := Commit{Schema: 1, ID: id, ProjectID: state.ProjectID, Actor: "account.device", Base: state.Revision, RunID: id, At: testTime, Retire: &Retirement{Sources: sources}}
		var err error
		state, _, err = Apply(state, commit)
		if err != nil {
			t.Fatal(err)
		}
		rounds++
	}
	if err := ValidateState(state); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(state)
	if len(state.Snapshots) != 1000 || len(after) > 2<<20 || len(before) <= 16<<20 {
		t.Fatal(len(state.Snapshots), len(before), len(after))
	}
	t.Logf("nodes=1000 historic_versions=50000 live_sources=%d checkpoint_before=%d after=%d retire_commits=%d", len(state.Snapshots), len(before), len(after), rounds)
}

func BenchmarkRetiredCheckpoint(b *testing.B) {
	state := scaleState(1000, 50)
	// Build the same measured structure as the retirement test, outside timing.
	roots := RetainedSources(state)
	for key := range state.Snapshots {
		if !roots[key] {
			delete(state.Snapshots, key)
		}
	}
	if err := ValidateState(state); err != nil {
		b.Fatal(err)
	}
	data, _ := json.Marshal(state)
	b.ReportMetric(float64(len(data)), "checkpoint-bytes")
	last := state.Snapshots[(Source{"session_000999", 50}).Key()]
	last.Version++
	last.Turns += 2
	last.Digest = Digest(last.Source)
	commit := Commit{Schema: 1, ID: "next", ProjectID: state.ProjectID, Actor: last.Owner, Base: state.Revision, RunID: "next", At: testTime, Snapshot: &last}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, _, err := Apply(state, commit); err != nil {
			b.Fatal(err)
		}
	}
}
