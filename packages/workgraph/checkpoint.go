package workgraph

import (
	"errors"
	"sort"
	"time"
)

// ValidateState checks a bootstrap state without invoking a model or pretending
// to replay its history. Authenticity of the anchor belongs to replication.
func ValidateState(s State) error {
	bad := errors.New("invalid checkpoint state")
	if s.Schema != SchemaVersion || !validID(s.ProjectID) || s.Revision < 1 || s.Revision > 9007199254740991 || s.Sessions == nil || s.Snapshots == nil || s.Nodes == nil || s.Library == nil {
		return bad
	}
	pending := map[string]int{}
	type boundary struct {
		version, turns int
		rewrite        bool
		superseded     bool
	}
	snapshots := map[string][]boundary{}
	for key, snapshot := range s.Snapshots {
		if snapshot.InheritedTurns < 0 || snapshot.InheritedTurns > snapshot.Turns {
			return bad
		}
		if r := snapshot.Rewrite; r != nil && (snapshot.Inherited || r.PreviousVersion != snapshot.Version-1 || r.PreviousVersion < 1 || r.CommonTurns < 0 || r.CommonTurns > snapshot.Turns) {
			return bad
		}
		session, ok := s.Sessions[snapshot.SessionID]
		if !ok || snapshot.Key() != key || snapshot.Version < 1 || snapshot.Version > session.Latest || snapshot.Owner != session.Owner || snapshot.Agent != session.Agent || snapshot.NativeID != session.NativeID || snapshot.Turns < 0 || !validDigest(snapshot.Digest) || !validDigest(snapshot.Conversation) || snapshot.Artifact != "" && !validDigest(snapshot.Artifact) {
			return bad
		}
		if snapshot.Inherited && (snapshot.Version != 1 || session.ProcessedThrough < 1) {
			return bad
		}
		snapshots[snapshot.SessionID] = append(snapshots[snapshot.SessionID], boundary{snapshot.Version, snapshot.Turns, snapshot.Rewrite != nil, snapshot.Superseded})
		if snapshot.Version > session.ProcessedThrough {
			pending[snapshot.SessionID]++
		}
	}
	for id, session := range s.Sessions {
		if id != session.ID || !validID(id) || !validID(session.Owner) || session.Agent == "" || session.NativeID == "" || session.Latest < 1 || pending[id] != session.Latest-session.ProcessedThrough || session.ProcessedThrough < 0 || session.ProcessedThrough > session.Latest || session.Revision < 0 || session.ConsumedTurns < 0 || len(session.Items) > 256 {
			return bad
		}
		// A checkpoint may omit retired processed descriptors, but not the latest,
		// consumed boundary or any unprocessed version. Check ordering among live
		// descriptors without iterating over a potentially huge historic counter.
		versions := snapshots[id]
		sort.Slice(versions, func(i, j int) bool { return versions[i].version < versions[j].version })
		if len(versions) == 0 || versions[len(versions)-1].version != session.Latest {
			return bad
		}
		for i := 1; i < len(versions); i++ {
			if versions[i].turns < versions[i-1].turns && !versions[i].rewrite && !versions[i-1].superseded {
				return bad
			}
		}
		if session.ProcessedThrough == 0 {
			if session.ConsumedTurns != 0 || session.Tail != "" {
				return bad
			}
		} else {
			snapshot, ok := s.Snapshots[(Source{id, session.ProcessedThrough}).Key()]
			next, pending := s.Snapshots[(Source{id, session.ProcessedThrough + 1}).Key()]
			rewriting := pending && next.Rewrite != nil
			if !ok || !rewriting && (snapshot.Superseded || session.ConsumedTurns != snapshot.Turns) {
				return bad
			}
			if rewriting {
				boundary := min(next.InheritedTurns, next.Rewrite.CommonTurns)
				if node, exists := s.Nodes[session.Tail]; exists {
					boundary = node.EndTurn
				}
				if session.ConsumedTurns != boundary || boundary > next.Rewrite.CommonTurns {
					return bad
				}
			}
		}
		if session.Tail != "" {
			if node, ok := s.Nodes[session.Tail]; !ok || node.Source.SessionID != id {
				return bad
			}
		}
		seen := map[string]bool{}
		for _, item := range session.Items {
			if !validID(item.ID) || seen[item.ID] || !validItemText(item.Text) {
				return bad
			}
			seen[item.ID] = true
		}
	}
	for id, node := range s.Nodes {
		if node.Checkpoint != nil {
			if len(node.Checkpoint.Items) > 256 {
				return bad
			}
			seen := map[string]bool{}
			for _, item := range node.Checkpoint.Items {
				if !validID(item.ID) || seen[item.ID] || !validItemText(item.Text) {
					return bad
				}
				seen[item.ID] = true
			}
		}
		session, ok := s.Sessions[node.Source.SessionID]
		snapshot, exists := s.Snapshots[node.Source.Key()]
		if !ok || !exists || id != node.ID || !validID(id) || node.Revision < 1 || node.Revision > s.Revision || !validID(node.RunID) || validateText(node.Text) != nil || node.Source.Version > session.ProcessedThrough || node.StartTurn < 0 || node.EndTurn < node.StartTurn || node.EndTurn != snapshot.Turns || node.RelationThrough < node.StartTurn || node.RelationThrough > node.EndTurn {
			return bad
		}
		if node.State != "current" && node.State != "sealed" && node.State != "withdrawn" || node.State == "current" && session.Tail != id {
			return bad
		}
		if node.Relations != "pending" && node.Relations != "ready" && node.Relations != "needs_context" || node.Relations == "ready" && node.RelationThrough != node.EndTurn {
			return bad
		}
		if node.State == "withdrawn" {
			if _, ok := s.Library[id]; !ok || len(node.Parents) != 0 {
				return bad
			}
		} else if snapshot.Superseded {
			return bad
		}
		if _, err := time.Parse(time.RFC3339Nano, node.CreatedAt); err != nil {
			return bad
		}
		if _, err := time.Parse(time.RFC3339Nano, node.UpdatedAt); err != nil {
			return bad
		}
		seen := map[string]bool{}
		for _, edge := range node.Parents {
			parent, ok := s.Nodes[edge.Parent]
			if !ok || seen[edge.Parent] || parent.State != "sealed" || parent.Source != edge.Source || edge.Reason == "" || len(edge.Reason) > 4000 || !validID(edge.RunID) || len(edge.Evidence) == 0 || len(edge.Evidence) > 32 {
				return bad
			}
			for _, ref := range edge.Evidence {
				if !validID(ref) {
					return bad
				}
			}
			seen[edge.Parent] = true
		}
	}
	for id, note := range s.Library {
		if node, ok := LibraryNode(s, id); !ok || (node.State != "sealed" && node.State != "withdrawn") || len(note) > 8000 {
			return bad
		}
	}
	for id, node := range s.LibrarySources {
		_, collected := s.Library[id]
		snapshot, exists := s.Snapshots[node.Source.Key()]
		if !collected || !exists || !validID(id) || node.ID != id || node.Revision < 1 || node.Revision > s.Revision || node.Source.Version > s.Sessions[node.Source.SessionID].ProcessedThrough || validateText(node.Text) != nil || node.State != "sealed" || node.Checkpoint != nil || len(node.Parents) != 0 || node.Relations != "ready" || node.StartTurn < 0 || node.EndTurn < node.StartTurn || node.EndTurn != snapshot.Turns || node.RelationThrough != node.EndTurn || !validID(node.RunID) {
			return bad
		}
		if _, err := time.Parse(time.RFC3339Nano, node.CreatedAt); err != nil {
			return bad
		}
		if _, err := time.Parse(time.RFC3339Nano, node.UpdatedAt); err != nil {
			return bad
		}
	}
	return validateGraph(s)
}
