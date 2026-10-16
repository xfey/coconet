package workgraph

import "errors"

// rewriteConversation is an owner-authorized, deterministic withdrawal. It
// never rewrites another Session's text and never invents replacement edges.
func rewriteConversation(state *State, snapshot Snapshot, at string) error {
	r := snapshot.Rewrite
	s := state.Sessions[snapshot.SessionID]
	prior, ok := state.Snapshots[(Source{s.ID, s.Latest}).Key()]
	if !ok || r.PreviousVersion != s.Latest || r.CommonTurns < 0 || r.CommonTurns >= prior.Turns || r.CommonTurns > snapshot.Turns || snapshot.Inherited {
		return errors.New("invalid native branch replacement boundary")
	}
	cutoff := r.CommonTurns
	// Old retained nodes may predate semantic checkpoints. In that case the
	// only trustworthy recovery input is this Session's effective transcript.
	for _, node := range state.Nodes {
		if node.Source.SessionID == s.ID && node.State != "withdrawn" && node.EndTurn <= cutoff && node.Checkpoint == nil {
			cutoff = -1
			break
		}
	}
	removed := map[string]bool{}
	released := map[string]bool{}
	var tail Node
	for id, node := range state.Nodes {
		if node.Source.SessionID != s.ID || node.State == "withdrawn" {
			continue
		}
		if node.EndTurn > cutoff {
			removed[id] = true
			for _, edge := range node.Parents {
				released[edge.Parent] = true
			}
			if _, collected := state.Library[id]; collected {
				node.State, node.Parents, node.Relations = "withdrawn", []Edge{}, "ready"
				node.RelationThrough = node.EndTurn
				node.Revision++
				node.UpdatedAt = at
				state.Nodes[id] = node
			} else {
				delete(state.Nodes, id)
			}
			continue
		}
		if tail.ID == "" || node.EndTurn > tail.EndTurn || node.EndTurn == tail.EndTurn && node.Source.Version > tail.Source.Version {
			tail = node
		}
	}
	for id, node := range state.Nodes {
		parents := make([]Edge, 0, len(node.Parents))
		for _, edge := range node.Parents {
			if !removed[edge.Parent] {
				parents = append(parents, edge)
			}
		}
		if len(parents) == len(node.Parents) {
			continue
		}
		node.Parents, node.UpdatedAt = parents, at
		node.Revision++
		state.Nodes[id] = node
		other := state.Sessions[node.Source.SessionID]
		other.Revision++ // fences an in-flight update of that mutable tail
		state.Sessions[other.ID] = other
	}
	for key, previous := range state.Snapshots {
		if previous.SessionID == s.ID && previous.Turns > r.CommonTurns {
			previous.Superseded = true
			state.Snapshots[key] = previous
		}
	}
	s = state.Sessions[s.ID]
	s.Tail, s.Items = tail.ID, []Item{}
	s.ConsumedTurns = min(snapshot.InheritedTurns, r.CommonTurns)
	if tail.ID != "" {
		s.ConsumedTurns = tail.EndTurn
		s.Items = append([]Item{}, tail.Checkpoint.Items...)
	}
	// Skip obsolete pending versions while the new immutable version remains
	// the next processing job. ConsumedTurns is restored independently.
	s.ProcessedThrough = s.Latest
	s.Revision++
	state.Sessions[s.ID] = s
	if r.RestoreEditableTails {
		released[s.Tail] = true
		restoreEditableTails(state, released, snapshot, at)
	}
	return nil
}

// Only tails affected by this withdrawal can reopen. Remaining dependencies,
// explicit collections and ended Sessions still pin their exact evidence.
// This changes mutability, never another Session's text or source.
func restoreEditableTails(state *State, candidates map[string]bool, incoming Snapshot, at string) {
	pinned := map[string]bool{}
	for _, node := range state.Nodes {
		for _, edge := range node.Parents {
			pinned[edge.Parent] = true
		}
	}
	for id := range candidates {
		node, exists := state.Nodes[id]
		if !exists || node.State != "sealed" || pinned[id] {
			continue
		}
		if _, collected := state.Library[id]; collected {
			continue
		}
		session := state.Sessions[node.Source.SessionID]
		latest, exists := state.Snapshots[(Source{session.ID, session.Latest}).Key()]
		if session.ID == incoming.SessionID {
			latest, exists = incoming, true
		}
		if session.Tail != id || !exists || latest.Ended {
			continue
		}
		node.State, node.UpdatedAt = "current", at
		node.Revision++
		state.Nodes[id] = node
		session.Revision++ // invalidate in-flight stage decisions on foreign tails too
		state.Sessions[session.ID] = session
	}
}

// ActiveSources excludes withdrawn versions immediately, even while local
// task leases or an explicit Library bookmark still protect their bytes.
func ActiveSources(state State) map[string]bool {
	active := RetainedSources(state)
	for key := range active {
		if state.Snapshots[key].Superseded {
			delete(active, key)
		}
	}
	return active
}

// ReadableSources includes explicit fixed Library evidence. Default content
// search deliberately uses ActiveSources instead.
func ReadableSources(state State) map[string]bool {
	readable := ActiveSources(state)
	for id := range state.Library {
		if node, ok := state.Nodes[id]; ok {
			readable[node.Source.Key()] = true
		}
	}
	return readable
}
