package workgraph

import "testing"

func TestPinnedLibraryDoesNotSealAuthorAndSurvivesRewind(t *testing.T) {
	s := newScenario(t)
	v1 := s.capture("author-session", "alice", 2)
	checkpointStage(s, v1, "work")
	before := s.state.Nodes["work"]
	s.commit("bob", func(c *Commit) {
		c.Library = &LibraryChange{Pin: true, NodeID: "work", Source: &v1, Note: "reference"}
	})
	if Digest(s.state.Nodes["work"]) != Digest(before) {
		t.Fatal("collection changed author node")
	}
	pinned, ok := LibraryNode(s.state, "work")
	if !ok || pinned.Source != v1 || pinned.Text != before.Text || pinned.Checkpoint != nil || len(pinned.Parents) != 0 {
		t.Fatal("incomplete or oversized collection", pinned)
	}
	v2 := s.capture("author-session", "alice", 4)
	s.stage(v2, "continue", "", "")
	if s.state.Nodes["work"].Source != v2 || len(s.state.Nodes) != 1 || s.state.LibrarySources["work"].Source != v1 {
		t.Fatal("collection blocked author continuation or followed the new source")
	}
	for _, source := range []Source{v1, v2} {
		if selected, err := SelectNode(s.state, "work", &source); err != nil || selected.Source != source || !RetainedSources(s.state)[source.Key()] {
			t.Fatal("fixed version no longer readable or retained", source, err)
		}
	}
	checkRewindReplay(s)
	rewindTo(s, "author-session", 0, true, false)
	if _, exists := s.state.Nodes["work"]; exists {
		t.Fatal("withdrawn author node remained in the DAG")
	}
	if !ReadableSources(s.state)[v1.Key()] || ReadableSources(s.state)[v2.Key()] {
		t.Fatal("rewind did not preserve only the explicitly saved branch")
	}
	if selected, err := SelectNode(s.state, "work", &v1); err != nil || Digest(selected) != Digest(pinned) {
		t.Fatal("rewind changed fixed display or source", err)
	}
	checkRewindReplay(s)
	s.commit("charlie", func(c *Commit) {
		c.Library = &LibraryChange{Pin: true, Remove: true, NodeID: "work", Source: &v1}
	})
	if RetainedSources(s.state)[v1.Key()] || ReadableSources(s.state)[v1.Key()] || len(s.state.LibrarySources) != 0 {
		t.Fatal("removal retained withdrawn source")
	}
	checkRewindReplay(s)
}

func TestPinnedLibraryRejectsStaleRemovalAndMalformedCheckpoint(t *testing.T) {
	s := newScenario(t)
	v1 := s.capture("session", "alice", 1)
	s.stage(v1, "start", "work", "first version")
	s.commit("bob", func(c *Commit) { c.Library = &LibraryChange{Pin: true, NodeID: "work", Source: &v1} })
	v2 := s.capture("session", "alice", 2)
	s.stage(v2, "continue", "", "")
	c := Commit{Schema: 1, ID: "remove", ProjectID: "project", Actor: "bob", Base: s.state.Revision, RunID: "remove", At: testTime, Library: &LibraryChange{Pin: true, Remove: true, NodeID: "work", Source: &v2}}
	if _, _, err := Apply(s.state, c); err == nil {
		t.Fatal("removal silently targeted a different saved version")
	}
	for _, damage := range []func(*State){
		func(s *State) { delete(s.Library, "work") },
		func(s *State) { delete(s.Snapshots, v1.Key()) },
		func(s *State) { n := s.LibrarySources["work"]; n.Source.Version = 99; s.LibrarySources["work"] = n },
		func(s *State) {
			n := s.LibrarySources["work"]
			n.Parents = []Edge{{Parent: "work"}}
			s.LibrarySources["work"] = n
		},
	} {
		broken := cloneState(s.state)
		damage(&broken)
		if ValidateState(broken) == nil {
			t.Fatal("invalid collection checkpoint accepted")
		}
	}
}
