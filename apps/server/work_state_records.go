package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/xfey/coconet/packages/contracts"
)

// Only committed online state lives here. Stage text is owned by its WorkNode.
// Task candidates cannot change this state until the graph transaction commits.
type sessionWorkRecord struct {
	ProjectID        string                   `json:"project_id"`
	ServerSessionID  string                   `json:"server_session_id"`
	ProcessedThrough int                      `json:"processed_through"`
	ConsumedTurns    int                      `json:"consumed_turns"`
	TailNodeID       string                   `json:"tail_node_id,omitempty"`
	OpenItems        []contracts.WorkOpenItem `json:"open_items"`
	Revision         int64                    `json:"revision"`
	UpdatedAt        string                   `json:"updated_at"`
}

func committedExtraction(state databaseState, id string) *workExtractionRecord {
	w, ok := state.WorkStates.Lookup(id)
	if !ok || w.ProcessedThrough == 0 {
		return nil
	}
	n := state.WorkNodes.Get(w.TailNodeID)
	if n.WorkNodeID == "" && len(w.OpenItems) == 0 {
		return nil
	}
	result := &workExtractionRecord{ProjectID: w.ProjectID, ServerSessionID: id, Version: w.ProcessedThrough, OpenItems: append([]contracts.WorkOpenItem{}, w.OpenItems...), Objective: n.Objective, Summary: n.Summary, Approach: n.Approach, WorkStartedAt: n.WorkStartedAt, WorkUpdatedAt: n.WorkUpdatedAt, GeneratedAt: w.UpdatedAt}
	return result
}

func commitSessionWork(state *databaseState, id string, version int) {
	w := state.WorkStates.Get(id)
	if version <= w.ProcessedThrough {
		return
	}
	key := versionKey(id, version)
	task := state.ProjectionJobs.Get(key)
	if task.Candidate != nil {
		w.OpenItems = append([]contracts.WorkOpenItem{}, task.Candidate.OpenItems...)
	}
	if w.OpenItems == nil {
		w.OpenItems = []contracts.WorkOpenItem{}
	}
	w.ProjectID = state.Sessions.Get(id).ProjectID
	w.ServerSessionID = id
	w.ProcessedThrough = version
	w.ConsumedTurns = state.snapshotContents().Get(key).Summary.TurnCount
	w.Revision++
	w.UpdatedAt = state.Snapshots.Get(key).CommittedAt
	if task.Result != nil {
		w.UpdatedAt = task.Result.CompletedAt
	}
	state.WorkStates.Set(id, w)
	markDashboardChanged(state, w.ProjectID)
}

type workProgressView struct{ state *databaseState }

func (v workProgressView) Get(id string) int    { return v.state.WorkStates.Get(id).ProcessedThrough }
func (v workProgressView) Set(id string, n int) { commitSessionWork(v.state, id, n) }
func (v workProgressView) All() map[string]int {
	m := map[string]int{}
	for id, w := range v.state.WorkStates.All() {
		if w.ProcessedThrough > 0 {
			m[id] = w.ProcessedThrough
		}
	}
	return m
}
func (v workProgressView) Len() int { return len(v.All()) }

type currentNodeView struct{ state *databaseState }

func (s *databaseState) currentNodes() currentNodeView { return currentNodeView{s} }
func (v currentNodeView) Get(id string) string {
	w := v.state.WorkStates.Get(id)
	n := v.state.WorkNodes.Get(w.TailNodeID)
	if n.State == contracts.WorkNodeCurrent {
		return w.TailNodeID
	}
	return ""
}
func (v currentNodeView) Set(id, node string) {
	w := v.state.WorkStates.Get(id)
	w.ServerSessionID = id
	w.ProjectID = v.state.Sessions.Get(id).ProjectID
	w.TailNodeID = node
	v.state.WorkStates.Set(id, w)
}
func (v currentNodeView) Delete(id string) {
	w := v.state.WorkStates.Get(id)
	if !v.state.WorkNodes.Has(w.TailNodeID) {
		w.TailNodeID = ""
		v.state.WorkStates.Set(id, w)
	}
}
func (v currentNodeView) All() map[string]string {
	m := map[string]string{}
	for id := range v.state.WorkStates.All() {
		if n := v.Get(id); n != "" {
			m[id] = n
		}
	}
	return m
}
func (v currentNodeView) Len() int { return len(v.All()) }

type projectionCandidateView struct{ state *databaseState }

func (v projectionCandidateView) Lookup(key string) (workExtractionRecord, bool) {
	t := v.state.ProjectionJobs.Get(key)
	if t.Candidate != nil {
		return *t.Candidate, true
	}
	id, version := parseSnapshotKey(key)
	if w := committedExtraction(*v.state, id); w != nil && w.Version == version {
		return *w, true
	}
	return workExtractionRecord{}, false
}
func (v projectionCandidateView) Get(key string) workExtractionRecord {
	r, _ := v.Lookup(key)
	return r
}
func (v projectionCandidateView) Has(key string) bool { _, ok := v.Lookup(key); return ok }
func (v projectionCandidateView) Set(key string, c workExtractionRecord) {
	t := v.state.ProjectionJobs.Get(key)
	t.ProjectID = c.ProjectID
	t.ServerSessionID = c.ServerSessionID
	t.Version = c.Version
	t.Candidate = &c
	v.state.ProjectionJobs.Set(key, t)
}
func (v projectionCandidateView) Delete(key string) {
	t := v.state.ProjectionJobs.Get(key)
	t.Candidate = nil
	v.state.ProjectionJobs.Set(key, t)
}
func (v projectionCandidateView) All() map[string]workExtractionRecord {
	m := map[string]workExtractionRecord{}
	for key, t := range v.state.ProjectionJobs.All() {
		if t.Candidate != nil {
			m[key] = *t.Candidate
		}
	}
	return m
}
func (v projectionCandidateView) Len() int { return len(v.All()) }

type projectionResultView struct{ state *databaseState }

func (v projectionResultView) Lookup(key string) (extractionResultRecord, bool) {
	t := v.state.ProjectionJobs.Get(key)
	if t.Result != nil {
		return *t.Result, true
	}
	id, version := parseSnapshotKey(key)
	w := v.state.WorkStates.Get(id)
	if version > 0 && w.ProcessedThrough == version {
		return extractionResultRecord{HistoryExtractionResult: contracts.HistoryExtractionResult{Status: "updated", CompletedAt: w.UpdatedAt}}, true
	}
	return extractionResultRecord{}, false
}
func (v projectionResultView) Get(key string) extractionResultRecord { r, _ := v.Lookup(key); return r }
func (v projectionResultView) Set(key string, r extractionResultRecord) {
	t := v.state.ProjectionJobs.Get(key)
	if t.ServerSessionID == "" {
		t.ServerSessionID, t.Version = parseSnapshotKey(key)
		t.ProjectID = v.state.Sessions.Get(t.ServerSessionID).ProjectID
	}
	t.Result = &r
	v.state.ProjectionJobs.Set(key, t)
}
func (v projectionResultView) Delete(key string) {
	t := v.state.ProjectionJobs.Get(key)
	t.Result = nil
	v.state.ProjectionJobs.Set(key, t)
}
func (v projectionResultView) All() map[string]extractionResultRecord {
	m := map[string]extractionResultRecord{}
	for key, t := range v.state.ProjectionJobs.All() {
		if t.Result != nil {
			m[key] = *t.Result
		}
	}
	return m
}
func (v projectionResultView) Len() int { return len(v.All()) }
func parseSnapshotKey(key string) (string, int) {
	i := strings.LastIndex(key, ":")
	if i < 1 {
		return "", 0
	}
	v, _ := strconv.Atoi(key[i+1:])
	return key[:i], v
}

// Schema <=23 had independent extraction and graph progress. Only applied
// graph state becomes committed work; unapplied extraction remains a task.
func migrateWorkRecords(state *databaseState, data []byte) error {
	var old struct {
		Projected map[string]int                    `json:"projected_session_versions"`
		Bootstrap map[string]int                    `json:"bootstrap_session_versions"`
		Current   map[string]string                 `json:"current_work_nodes"`
		Work      map[string]workExtractionRecord   `json:"work_extractions"`
		Results   map[string]extractionResultRecord `json:"extraction_results"`
	}
	if err := json.Unmarshal(data, &old); err != nil {
		return err
	}
	for id, p := range old.Projected {
		if state.WorkStates.Has(id) {
			continue
		}
		w := sessionWorkRecord{ProjectID: state.Sessions.Get(id).ProjectID, ServerSessionID: id, ProcessedThrough: p, TailNodeID: old.Current[id], OpenItems: []contracts.WorkOpenItem{}, Revision: 1}
		last := 0
		for _, c := range old.Work {
			if c.ServerSessionID == id && c.Version <= p && c.Version > last {
				w.OpenItems = append([]contracts.WorkOpenItem{}, c.OpenItems...)
				w.UpdatedAt = c.GeneratedAt
				last = c.Version
			}
		}
		if w.TailNodeID == "" {
			for _, n := range state.WorkNodes.All() {
				if n.ServerSessionID == id && n.SourceVersion <= p {
					prior := state.WorkNodes.Get(w.TailNodeID)
					if prior.SourceVersion < n.SourceVersion {
						w.TailNodeID = n.WorkNodeID
					}
				}
			}
		}
		w.ConsumedTurns = state.snapshotContents().Get(versionKey(id, p)).Summary.TurnCount
		state.WorkStates.Set(id, w)
	}
	for id, n := range old.Current {
		if !state.WorkStates.Has(id) {
			state.currentNodes().Set(id, n)
		}
	}
	for key, t := range state.ProjectionJobs.All() {
		if c, ok := old.Work[key]; ok {
			copy := c
			copy.StageProposed = c.Objective != ""
			t.Candidate = &copy
		}
		if r, ok := old.Results[key]; ok {
			copy := r
			t.Result = &copy
		}
		t.BaseRevision = state.WorkStates.Get(t.ServerSessionID).Revision
		state.ProjectionJobs.Set(key, t)
	}
	for id, w := range state.WorkStates.All() {
		if w.ProcessedThrough < 0 || w.ConsumedTurns < 0 {
			return fmt.Errorf("invalid migrated work state for %s", id)
		}
	}
	return nil
}
