package localdag

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/workgraph"
)

// Discovery projections deliberately exclude native identity, content object
// addresses, processing prompts and transcript text. Reading a graph never
// downloads any Session content or invokes a model.
type WorkNode struct {
	ID string `json:"id"`
	workgraph.Text
	Source    workgraph.Source `json:"source"`
	Owner     string           `json:"owner"`
	Agent     string           `json:"agent"`
	State     string           `json:"state"`
	Relations string           `json:"relations"`
	Parents   []string         `json:"parents"`
	Children  []string         `json:"children"`
	UpdatedAt string           `json:"updated_at"`
	Library   *LibraryNote     `json:"library,omitempty"`
}

type LibraryNote struct {
	Note string `json:"note"`
}

type SessionWork struct {
	SessionID        string           `json:"session_id"`
	Owner            string           `json:"owner"`
	Latest           int              `json:"latest_version"`
	ProcessedThrough int              `json:"processed_through"`
	Items            []workgraph.Item `json:"open_items"`
}

type Coverage struct {
	Sessions             int    `json:"sessions"`
	StagePendingSessions int    `json:"stage_pending_sessions"`
	Nodes                int    `json:"nodes"`
	RelationsPending     int    `json:"relations_pending"`
	Scope                string `json:"scope"`
	Truncated            bool   `json:"truncated"`
}

type GraphView struct {
	ProjectID   string        `json:"project_id"`
	Revision    int64         `json:"revision"`
	Nodes       []WorkNode    `json:"nodes"`
	SessionWork []SessionWork `json:"session_work"`
	Coverage    Coverage      `json:"coverage"`
	NextCursor  string        `json:"next_cursor,omitempty"`
}

func (e *Engine) Graph(project string) (GraphView, error) {
	state, err := e.Store.State(project)
	if err != nil {
		return GraphView{}, err
	}
	return projectGraph(state), nil
}

func projectGraph(state workgraph.State) GraphView { return projectGraphWithHistory(state, false) }

func projectGraphWithHistory(state workgraph.State, includeWithdrawn bool) GraphView {
	view := GraphView{ProjectID: state.ProjectID, Revision: state.Revision, Nodes: []WorkNode{}, SessionWork: []SessionWork{},
		Coverage: Coverage{Sessions: len(state.Sessions), Nodes: len(state.Nodes), Scope: "structured_local_replica"}}
	children := map[string][]string{}
	for _, node := range state.Nodes {
		for _, edge := range node.Parents {
			children[edge.Parent] = append(children[edge.Parent], node.ID)
		}
	}
	for _, node := range state.Nodes {
		if node.State == "withdrawn" && !includeWithdrawn {
			continue
		}
		session := state.Sessions[node.Source.SessionID]
		value := WorkNode{ID: node.ID, Text: node.Text, Source: node.Source, Owner: session.Owner, Agent: session.Agent,
			State: node.State, Relations: node.Relations, Parents: []string{}, Children: append([]string{}, children[node.ID]...), UpdatedAt: node.UpdatedAt}
		for _, edge := range node.Parents {
			value.Parents = append(value.Parents, edge.Parent)
		}
		sort.Strings(value.Parents)
		sort.Strings(value.Children)
		if note, ok := state.Library[node.ID]; ok {
			value.Library = &LibraryNote{Note: note}
		}
		if node.Relations != "ready" {
			view.Coverage.RelationsPending++
		}
		view.Nodes = append(view.Nodes, value)
	}
	view.Coverage.Nodes = len(view.Nodes)
	sort.Slice(view.Nodes, func(i, j int) bool { return view.Nodes[i].ID < view.Nodes[j].ID })
	for _, session := range state.Sessions {
		view.SessionWork = append(view.SessionWork, SessionWork{SessionID: session.ID, Owner: session.Owner, Latest: session.Latest,
			ProcessedThrough: session.ProcessedThrough, Items: append([]workgraph.Item{}, session.Items...)})
		if session.ProcessedThrough < session.Latest {
			view.Coverage.StagePendingSessions++
		}
	}
	sort.Slice(view.SessionWork, func(i, j int) bool { return view.SessionWork[i].SessionID < view.SessionWork[j].SessionID })
	return view
}

type WorkSearch struct {
	Query  string `json:"query"`
	Limit  int    `json:"limit,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

type WorkMatch struct {
	Node    WorkNode `json:"node"`
	Reasons []string `json:"reasons"`
}

type SearchView struct {
	ProjectID      string                    `json:"project_id"`
	ContentMatches []graphstore.ContentMatch `json:"content_matches"`
	Index          graphstore.IndexCoverage  `json:"index"`
	Revision       int64                     `json:"revision"`
	Nodes          []WorkMatch               `json:"nodes"`
	Coverage       Coverage                  `json:"coverage"`
	NextCursor     string                    `json:"next_cursor,omitempty"`
}

// Search combines accepted node text with locally indexed compact conversations.
// It performs no download or inference and reports exact index coverage.
func (e *Engine) Search(project string, query WorkSearch) (SearchView, error) {
	if len(strings.Fields(query.Query)) > 64 || strings.TrimSpace(query.Query) == "" || utf8.RuneCountInString(query.Query) > 4000 || query.Limit < 0 || query.Limit > 20 {
		return SearchView{}, errors.New("search requires 1–4000 characters, at most 64 terms and a limit of 1–20")
	}
	if query.Limit == 0 {
		query.Limit = 10
	}
	state, err := e.Store.State(project)
	if err != nil {
		return SearchView{}, err
	}
	graph := projectGraph(state)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	index, err := e.Store.IndexCoverage(ctx, state)
	if err != nil {
		return SearchView{}, err
	}
	result := SearchView{ProjectID: graph.ProjectID, Revision: graph.Revision, Nodes: []WorkMatch{}, Coverage: graph.Coverage, Index: index, ContentMatches: []graphstore.ContentMatch{}}
	result.Coverage.Scope = "node_text_library_notes_and_indexed_conversations"
	terms := strings.Fields(strings.ToLower(query.Query))
	scores := map[string]int{}
	for _, node := range graph.Nodes {
		fields := []struct{ name, text string }{{"objective", node.Objective}, {"summary", node.Summary}, {"approach", node.Approach}}
		if node.Library != nil {
			fields = append(fields, struct{ name, text string }{"library_note", node.Library.Note})
		}
		reasons := []string{}
		for _, field := range fields {
			for _, term := range terms {
				if strings.Contains(strings.ToLower(field.text), term) {
					reasons = append(reasons, field.name)
					break
				}
			}
		}
		if len(reasons) > 0 {
			scores[node.ID] = len(reasons)
			result.Nodes = append(result.Nodes, WorkMatch{Node: node, Reasons: reasons})
		}
	}
	sort.SliceStable(result.Nodes, func(i, j int) bool {
		a, b := result.Nodes[i].Node, result.Nodes[j].Node
		if scores[a.ID] != scores[b.ID] {
			return scores[a.ID] > scores[b.ID]
		}
		return a.ID < b.ID
	})
	scope := workgraph.Digest([]any{project, query.Query, index.Instance, index.Generation})
	cursor, err := readDiscoveryCursor(query.Cursor, graph.Revision, scope)
	if err != nil {
		return SearchView{}, err
	}
	if cursor.Nodes > len(result.Nodes) {
		return SearchView{}, errors.New("cursor offset is unavailable; restart this search without a cursor")
	}
	total := len(result.Nodes)
	result.Nodes = result.Nodes[cursor.Nodes:min(total, cursor.Nodes+query.Limit)]
	body, err := e.Store.SearchContent(ctx, state, query.Query, cursor.Sessions, query.Limit-len(result.Nodes), index)
	if err != nil {
		return SearchView{}, err
	}
	result.ContentMatches = body.Matches
	for {
		result.Coverage.Truncated = cursor.Nodes > 0 || cursor.Sessions > 0 || cursor.Nodes+len(result.Nodes) < total || body.More
		if cursor.Nodes+len(result.Nodes) < total || body.More {
			result.NextCursor = nextDiscoveryCursor(discoveryCursor{graph.Revision, scope, cursor.Nodes + len(result.Nodes), cursor.Sessions + len(result.ContentMatches)})
		} else {
			result.NextCursor = ""
		}
		data, err := json.Marshal(result)
		if err != nil {
			return SearchView{}, err
		}
		if len(data) <= maxDiscoveryBytes {
			return result, nil
		}
		if len(result.ContentMatches) > 0 {
			result.ContentMatches = result.ContentMatches[:len(result.ContentMatches)-1]
			body.More = true
			continue
		}
		if len(result.Nodes) <= 1 {
			return SearchView{}, errors.New("one search record exceeds response capacity")
		}
		result.Nodes = result.Nodes[:len(result.Nodes)-1]
	}
}
