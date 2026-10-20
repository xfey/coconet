package workgraph

// LibraryNode resolves a collection's fixed display and source. Legacy
// collections already sealed their author node, which remains its snapshot.
func LibraryNode(state State, id string) (Node, bool) {
	if _, ok := state.Library[id]; !ok {
		return Node{}, false
	}
	if node, ok := state.LibrarySources[id]; ok {
		return node, true
	}
	node, ok := state.Nodes[id]
	return node, ok
}

// SelectNode verifies a fixed, readable selection without requiring DAG
// processing to have finished. A supplied node ID must match either its current
// source or the explicitly collected version; it never silently advances.
func SelectNode(state State, id string, expected *Source) (Node, error) {
	if id == "" {
		if expected == nil || !ReadableSources(state)[expected.Key()] {
			return Node{}, ErrSource
		}
		return Node{Source: *expected}, nil
	}
	node, ok := state.Nodes[id]
	if ok && (expected == nil || node.Source == *expected) {
		return node, nil
	}
	pinned, exists := LibraryNode(state, id)
	if exists && (expected == nil || pinned.Source == *expected) {
		return pinned, nil
	}
	return Node{}, ErrSource
}

func applyPinnedLibrary(state *State, commit Commit) error {
	d := commit.Library
	if d.Source == nil || !validID(d.NodeID) {
		return ErrSource
	}
	if d.Remove {
		if node, exists := LibraryNode(*state, d.NodeID); exists && node.Source != *d.Source {
			return ErrSource
		}
		if _, exists := state.Library[d.NodeID]; !exists {
			if _, err := SelectNode(*state, d.NodeID, d.Source); err != nil {
				return err
			}
		}
		delete(state.Library, d.NodeID)
		delete(state.LibrarySources, d.NodeID)
		if state.Nodes[d.NodeID].State == "withdrawn" {
			delete(state.Nodes, d.NodeID)
		}
		return nil
	}
	node, err := SelectNode(*state, d.NodeID, d.Source)
	if err != nil {
		return err
	}
	// Keep only this source's display, not a frozen copy of the graph, parent
	// dependencies or session item checkpoints.
	node.Parents, node.Checkpoint = []Edge{}, nil
	node.State, node.Relations, node.RelationThrough = "sealed", "ready", node.EndTurn
	if state.LibrarySources == nil {
		state.LibrarySources = map[string]Node{}
	}
	state.LibrarySources[d.NodeID] = node
	state.Library[d.NodeID] = d.Note
	return nil
}
