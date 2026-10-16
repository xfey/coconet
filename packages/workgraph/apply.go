package workgraph

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

func Apply(previous State, commit Commit) (State, Change, error) {
	if previous.Schema != SchemaVersion || commit.Schema != SchemaVersion || !validID(previous.ProjectID) || commit.ProjectID != previous.ProjectID || !validID(commit.ID) || !validID(commit.Actor) || !validID(commit.RunID) {
		return State{}, Change{}, errors.New("invalid commit identity or schema")
	}
	if _, err := time.Parse(time.RFC3339Nano, commit.At); err != nil {
		return State{}, Change{}, errors.New("invalid commit time")
	}
	if previous.Revision != commit.Base {
		return State{}, Change{}, ErrConflict
	}
	payloads := 0
	for _, exists := range []bool{commit.Snapshot != nil, commit.Stage != nil, commit.Relations != nil, commit.Library != nil, commit.Retire != nil} {
		if exists {
			payloads++
		}
	}
	if commit.Processing != nil && commit.Stage == nil && commit.Relations == nil {
		return State{}, Change{}, ErrPermission
	}
	if payloads != 1 {
		return State{}, Change{}, errors.New("commit requires exactly one payload")
	}
	if previous.Sessions == nil || previous.Snapshots == nil || previous.Nodes == nil || previous.Library == nil {
		return State{}, Change{}, errors.New("uninitialized graph collections")
	}
	next := cloneState(previous)
	var err error
	switch {
	case commit.Snapshot != nil:
		err = applySnapshot(&next, commit)
	case commit.Stage != nil:
		err = applyStage(&next, commit)
	case commit.Relations != nil:
		err = applyRelations(&next, commit)
	case commit.Library != nil:
		err = applyLibrary(&next, commit)
	case commit.Retire != nil:
		err = applyRetirement(&next, commit)
	}
	if err != nil {
		return State{}, Change{}, err
	}
	if err := validateGraph(next); err != nil {
		return State{}, Change{}, err
	}
	next.Revision++
	change := Change{CommitID: commit.ID, RunID: commit.RunID, Before: previous.Revision, After: next.Revision, Nodes: []string{}, Sessions: []string{}}
	for id, node := range next.Nodes {
		if !reflect.DeepEqual(node, previous.Nodes[id]) {
			change.Nodes = append(change.Nodes, id)
		}
	}
	for id, session := range next.Sessions {
		if !reflect.DeepEqual(session, previous.Sessions[id]) {
			change.Sessions = append(change.Sessions, id)
		}
	}
	for id := range previous.Nodes {
		if _, exists := next.Nodes[id]; !exists {
			change.Nodes = append(change.Nodes, id)
		}
	}
	sort.Strings(change.Nodes)
	sort.Strings(change.Sessions)
	change.StateDigest = Digest(next)
	return next, change, nil
}

func applySnapshot(state *State, commit Commit) error {
	snapshot := *commit.Snapshot
	if !validID(snapshot.SessionID) || snapshot.Version < 1 || snapshot.Owner != commit.Actor || snapshot.Turns < 0 || snapshot.InheritedTurns < 0 || snapshot.InheritedTurns > snapshot.Turns || snapshot.NativeID == "" || snapshot.Agent == "" || !validDigest(snapshot.Digest) || !validDigest(snapshot.Conversation) || snapshot.Artifact != "" && !validDigest(snapshot.Artifact) {
		return errors.New("invalid immutable snapshot")
	}
	if _, exists := state.Snapshots[snapshot.Key()]; exists {
		return errors.New("snapshot already exists; replay the original commit")
	}
	session, exists := state.Sessions[snapshot.SessionID]
	if snapshot.Inherited && (exists || snapshot.Version != 1) {
		return errors.New("inherited baseline must be the first snapshot of a new Session")
	}
	if !exists {
		session = Session{ID: snapshot.SessionID, Owner: snapshot.Owner, Agent: snapshot.Agent, NativeID: snapshot.NativeID, Items: []Item{}}
	}
	if session.Owner != commit.Actor || session.Agent != snapshot.Agent || session.NativeID != snapshot.NativeID {
		return ErrPermission
	}
	if snapshot.Superseded {
		return errors.New("cannot capture a superseded source")
	}
	if snapshot.Version != session.Latest+1 {
		return errors.New("snapshot versions must advance sequentially")
	}
	if snapshot.Rewrite != nil {
		if err := rewriteConversation(state, snapshot, commit.At); err != nil {
			return err
		}
		session = state.Sessions[snapshot.SessionID]
	} else if session.Latest > 0 {
		prior := state.Snapshots[(Source{SessionID: session.ID, Version: session.Latest}).Key()]
		if snapshot.Turns < prior.Turns {
			return errors.New("cumulative conversation moved backwards")
		}
	}
	session.Latest = snapshot.Version
	if snapshot.Inherited {
		session.ProcessedThrough, session.ConsumedTurns, session.Revision = 1, snapshot.Turns, 1
	}
	state.Sessions[session.ID] = session
	state.Snapshots[snapshot.Key()] = snapshot
	return nil
}

func applyStage(state *State, commit Commit) error {
	d := commit.Stage
	snapshot, exists := state.Snapshots[d.Source.Key()]
	if !exists || snapshot.Superseded {
		return ErrSource
	}
	session := state.Sessions[d.Source.SessionID]
	if !processingAllowed(commit, session.Owner, d.Source, "", 0) {
		return ErrPermission
	}
	if session.Revision != d.SessionRevision {
		return ErrConflict
	}
	if d.Source.Version != session.ProcessedThrough+1 {
		return errors.New("stage processing must not skip a snapshot")
	}
	if strings.TrimSpace(d.Reason) == "" || len(d.Reason) > 4000 {
		return errors.New("stage decision requires a bounded reason")
	}
	current, hasCurrent := state.Nodes[session.Tail]
	hasCurrent = hasCurrent && current.State == "current"
	if d.Text != nil {
		if err := validateText(*d.Text); err != nil {
			return err
		}
	}
	switch d.Action {
	case "continue":
		if !hasCurrent || d.NewNodeID != "" {
			return errors.New("continue requires the mutable tail and no new identity")
		}
		if d.Text != nil {
			current.Text = *d.Text
		}
		current.Source = d.Source
		current.EndTurn = snapshot.Turns
		current.UpdatedAt = commit.At
		current.RunID = commit.RunID
		current.Revision++
		if snapshot.Turns > current.RelationThrough {
			current.Relations = "pending"
		}
		state.Nodes[current.ID] = current
	case "start":
		if d.Text == nil || !validID(d.NewNodeID) {
			return errors.New("start requires stage text and a node identity")
		}
		if _, exists := state.Nodes[d.NewNodeID]; exists {
			return errors.New("duplicate node identity")
		}
		if hasCurrent {
			seal(state, current.ID, commit.At)
		}
		state.Nodes[d.NewNodeID] = Node{ID: d.NewNodeID, Text: *d.Text, Source: d.Source, State: "current", Revision: 1, StartTurn: session.ConsumedTurns, EndTurn: snapshot.Turns, Parents: []Edge{}, Relations: "pending", RelationThrough: session.ConsumedTurns, CreatedAt: commit.At, UpdatedAt: commit.At, RunID: commit.RunID}
		session.Tail = d.NewNodeID
	case "none":
		if d.Text != nil || d.NewNodeID != "" {
			return errors.New("no contribution must not mutate stage text or identity")
		}
	default:
		return errors.New("invalid stage action; unresolved evidence cannot be committed")
	}
	items, err := applyItems(session.Items, d.Items)
	if err != nil {
		return err
	}
	session.Items = items
	if d.Checkpoint && (d.Action == "start" || d.Action == "continue") {
		node := state.Nodes[session.Tail]
		node.Checkpoint = &StageCheckpoint{Items: append([]Item{}, items...)}
		state.Nodes[node.ID] = node
	}
	session.ProcessedThrough = d.Source.Version
	session.ConsumedTurns = snapshot.Turns
	// seal may have advanced the same Session baseline; preserve that invalidation.
	if changed := state.Sessions[session.ID]; changed.Revision > session.Revision {
		session.Revision = changed.Revision
	}
	session.Revision++
	state.Sessions[session.ID] = session
	if snapshot.Ended {
		seal(state, session.Tail, commit.At)
	}
	return nil
}

func applyRelations(state *State, commit Commit) error {
	d := commit.Relations
	node, exists := state.Nodes[d.NodeID]
	if !exists || node.State == "withdrawn" {
		return errors.New("relation target does not exist")
	}
	if !processingAllowed(commit, state.Sessions[node.Source.SessionID].Owner, node.Source, node.ID, node.Revision) {
		return ErrPermission
	}
	if node.Revision != d.NodeRevision {
		return ErrConflict
	}
	if d.Through != node.EndTurn || d.Through < node.RelationThrough {
		return errors.New("relation result must cover the fixed node boundary")
	}
	if strings.TrimSpace(d.Reason) == "" || len(d.Reason) > 4000 {
		return errors.New("relation result requires a bounded reason")
	}
	if !d.Complete {
		if len(d.Parents) != 0 {
			return errors.New("incomplete analysis cannot add edges")
		}
		node.Relations = "needs_context"
	} else {
		seen := map[string]bool{}
		for _, edge := range node.Parents {
			seen[edge.Parent] = true
		}
		proposed := map[string]bool{}
		for _, selected := range d.Parents {
			parent, exists := state.Nodes[selected.NodeID]
			if !exists || parent.State == "withdrawn" || selected.NodeID == node.ID || proposed[selected.NodeID] {
				return errors.New("invalid or duplicate relation parent")
			}
			proposed[selected.NodeID] = true
			if parent.Revision != selected.Revision || parent.Source != selected.Source {
				return ErrConflict
			}
			if _, exists := state.Snapshots[parent.Source.Key()]; !exists {
				return ErrSource
			}
			if strings.TrimSpace(selected.Reason) == "" || len(selected.Reason) > 4000 || len(selected.Evidence) == 0 || len(selected.Evidence) > 32 {
				return errors.New("parent requires bounded use evidence")
			}
			for _, ref := range selected.Evidence {
				if !validID(ref) {
					return errors.New("invalid relation evidence reference")
				}
			}
			if seen[parent.ID] {
				continue
			}
			node.Parents = append(node.Parents, Edge{Parent: parent.ID, Source: parent.Source, Reason: selected.Reason, Evidence: append([]string{}, selected.Evidence...), RunID: commit.RunID})
			seal(state, parent.ID, commit.At)
		}
		sort.Slice(node.Parents, func(i, j int) bool { return node.Parents[i].Parent < node.Parents[j].Parent })
		node.RelationThrough = d.Through
		node.Relations = "ready"
	}
	node.UpdatedAt = commit.At
	state.Nodes[node.ID] = node
	return nil
}

// Library is an explicit account operation, unlike device-owned capture and
// inference. Relay supplies authenticated account.device writer identities.
// Single-part identities remain useful for isolated local fixtures only.
func SameAccountWriter(a, b string) bool {
	if a == b {
		return a != ""
	}
	aa, ad, aok := strings.Cut(a, ".")
	ba, bd, bok := strings.Cut(b, ".")
	return aok && bok && aa != "" && aa == ba && ad != "" && bd != ""
}

func applyLibrary(state *State, commit Commit) error {
	d := commit.Library
	node, exists := state.Nodes[d.NodeID]
	if !exists {
		return ErrSource
	}
	if d.Source != nil && *d.Source != node.Source {
		return ErrSource
	}
	if !SameAccountWriter(state.Sessions[node.Source.SessionID].Owner, commit.Actor) {
		return ErrPermission
	}
	if len(d.Note) > 8000 {
		return errors.New("library note exceeds limit")
	}
	if d.Remove {
		delete(state.Library, node.ID)
		if node.State == "withdrawn" {
			delete(state.Nodes, node.ID)
		}
		return nil
	}
	seal(state, node.ID, commit.At)
	state.Library[node.ID] = d.Note
	return nil
}

func seal(state *State, id, at string) {
	node, exists := state.Nodes[id]
	if !exists || node.State != "current" {
		return
	}
	node.State = "sealed"
	node.Revision++
	node.UpdatedAt = at
	state.Nodes[id] = node
	session := state.Sessions[node.Source.SessionID]
	session.Revision++
	state.Sessions[session.ID] = session
}

func applyItems(previous []Item, changes ItemChanges) ([]Item, error) {
	if len(changes.Add)+len(changes.Revise)+len(changes.Complete)+len(changes.Withdraw) > 256 {
		return nil, errors.New("too many item operations")
	}
	items := append([]Item{}, previous...)
	seen := map[string]bool{}
	locate := func(id string) int {
		for i, item := range items {
			if item.ID == id {
				return i
			}
		}
		return -1
	}
	check := func(id string) error {
		if !validID(id) || seen[id] {
			return errors.New("item identity is invalid or repeated")
		}
		seen[id] = true
		return nil
	}
	for _, item := range changes.Add {
		if err := check(item.ID); err != nil {
			return nil, err
		}
		if locate(item.ID) >= 0 || !validItemText(item.Text) {
			return nil, errors.New("new item is invalid or already exists")
		}
		items = append(items, item)
	}
	for _, item := range changes.Revise {
		if err := check(item.ID); err != nil {
			return nil, err
		}
		i := locate(item.ID)
		if i < 0 || !validItemText(item.Text) {
			return nil, errors.New("revised item is invalid or missing")
		}
		items[i] = item
	}
	for _, id := range append(append([]string{}, changes.Complete...), changes.Withdraw...) {
		if err := check(id); err != nil {
			return nil, err
		}
		i := locate(id)
		if i < 0 {
			return nil, errors.New("completed or withdrawn item is missing")
		}
		items = append(items[:i], items[i+1:]...)
	}
	if len(items) > 256 {
		return nil, errors.New("open item capacity exceeded")
	}
	return items, nil
}

func validateText(text Text) error {
	if strings.TrimSpace(text.Objective) == "" || utf8.RuneCountInString(text.Objective) > 300 || utf8.RuneCountInString(text.Summary)+utf8.RuneCountInString(text.Approach) > 2000 {
		return errors.New("invalid or oversized stage text")
	}
	return nil
}

func validateGraph(state State) error {
	colors := map[string]int{}
	var visit func(string) error
	visit = func(id string) error {
		if colors[id] == 1 {
			return errors.New("relation would create a cycle")
		}
		if colors[id] == 2 {
			return nil
		}
		node, exists := state.Nodes[id]
		if !exists {
			return errors.New("missing graph node")
		}
		if _, ok := state.Snapshots[node.Source.Key()]; !ok {
			return ErrSource
		}
		colors[id] = 1
		for _, parent := range node.Parents {
			if _, ok := state.Snapshots[parent.Source.Key()]; !ok {
				return ErrSource
			}
			if err := visit(parent.Parent); err != nil {
				return err
			}
		}
		colors[id] = 2
		return nil
	}
	for id := range state.Nodes {
		if err := visit(id); err != nil {
			return fmt.Errorf("invalid DAG: %w", err)
		}
	}
	return nil
}

// RetainedSources excludes execution leases; the caller must union its durable
// tasks and active downloads before collecting anything.
func RetainedSources(state State) map[string]bool {
	roots := map[string]bool{}
	for _, session := range state.Sessions {
		if session.Latest > 0 {
			roots[(Source{session.ID, session.Latest}).Key()] = true
		}
		if session.ProcessedThrough > 0 {
			roots[(Source{session.ID, session.ProcessedThrough}).Key()] = true
		}
	}
	for key, snapshot := range state.Snapshots {
		if snapshot.Version > state.Sessions[snapshot.SessionID].ProcessedThrough {
			roots[key] = true
		}
	}
	for _, node := range state.Nodes {
		roots[node.Source.Key()] = true
		for _, edge := range node.Parents {
			roots[edge.Source.Key()] = true
		}
	}
	return roots
}

func validID(value string) bool {
	if value == "" || len(value) > 200 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func validItemText(value string) bool {
	return strings.TrimSpace(value) != "" && !strings.ContainsAny(value, "\r\n") && utf8.RuneCountInString(value) <= 500
}

// Copy only graph data, without serializing and parsing an entire checkpoint.
// Nested slices are independent, including on rejected transactions.
func cloneState(previous State) State {
	next := previous
	next.Sessions = maps.Clone(previous.Sessions)
	next.Snapshots = maps.Clone(previous.Snapshots)
	next.Nodes = maps.Clone(previous.Nodes)
	next.Library = maps.Clone(previous.Library)
	for key, snapshot := range next.Snapshots {
		if snapshot.Rewrite != nil {
			rewrite := *snapshot.Rewrite
			snapshot.Rewrite = &rewrite
			next.Snapshots[key] = snapshot
		}
	}
	for id, session := range next.Sessions {
		session.Items = slices.Clone(session.Items)
		next.Sessions[id] = session
	}
	for id, node := range next.Nodes {
		if node.Checkpoint != nil {
			node.Checkpoint = &StageCheckpoint{Items: slices.Clone(node.Checkpoint.Items)}
		}
		node.Parents = slices.Clone(node.Parents)
		for i := range node.Parents {
			node.Parents[i].Evidence = slices.Clone(node.Parents[i].Evidence)
		}
		next.Nodes[id] = node
	}
	return next
}

// Replication authenticates the task authorization against the Relay receipt.
// This reducer also constrains delegation to exactly one fixed source/target.
func processingAllowed(c Commit, owner string, source Source, node string, revision int64) bool {
	if c.Processing == nil {
		return c.Actor == owner
	}
	a := c.Processing
	return validID(a.TaskID) && validID(a.Lease) && validDigest(a.SHA256) && a.Owner == owner && a.SessionID == source.SessionID && a.Version == source.Version && a.NodeID == node && a.NodeRevision == revision
}
