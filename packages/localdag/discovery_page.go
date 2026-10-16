package localdag

import (
	"encoding/base64"
	"encoding/json"
	"errors"

	"github.com/xfey/coconet/packages/workgraph"
)

const maxDiscoveryBytes = 2 << 20

type GraphQuery struct {
	Limit  int    `json:"limit,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}
type discoveryCursor struct {
	Revision int64  `json:"revision"`
	Scope    string `json:"scope"`
	Nodes    int    `json:"nodes"`
	Sessions int    `json:"sessions"`
}

func readDiscoveryCursor(raw string, revision int64, scope string) (discoveryCursor, error) {
	c := discoveryCursor{Revision: revision, Scope: scope}
	if raw == "" {
		return c, nil
	}
	if len(raw) > 1024 {
		return c, errors.New("cursor exceeds 1024 characters; use next_cursor from the previous response or restart without a cursor")
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || json.Unmarshal(data, &c) != nil || c.Nodes < 0 || c.Sessions < 0 || c.Scope != scope {
		return c, errors.New("cursor is invalid or belongs to another selection; restart without a cursor and use next_cursor with the same tool and query")
	}
	if c.Revision != revision {
		return c, errors.New("project changed between pages; restart this selection without a cursor")
	}
	return c, nil
}
func nextDiscoveryCursor(c discoveryCursor) string {
	data, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(data)
}

// GraphPage pages nodes and Session items independently at one accepted revision.
// Returned edge IDs may lie on other pages; they are not removed or reinterpreted.
func (e *Engine) GraphPage(project string, query GraphQuery, libraryOnly bool) (GraphView, error) {
	if query.Limit < 0 || query.Limit > 100 {
		return GraphView{}, errors.New("graph limit must be 1–100")
	}
	if query.Limit == 0 {
		query.Limit = 50
	}
	state, err := e.Store.State(project)
	if err != nil {
		return GraphView{}, err
	}
	graph := projectGraphWithHistory(state, libraryOnly)
	if libraryOnly {
		selected := []WorkNode{}
		for _, node := range graph.Nodes {
			if node.Library != nil {
				selected = append(selected, node)
			}
		}
		graph.Nodes = selected
		graph.SessionWork = []SessionWork{}
		graph.Coverage.Scope = "collected_nodes"
	}
	scope := workgraph.Digest([]any{project, libraryOnly})
	cursor, err := readDiscoveryCursor(query.Cursor, graph.Revision, scope)
	if err != nil {
		return GraphView{}, err
	}
	if cursor.Nodes > len(graph.Nodes) || cursor.Sessions > len(graph.SessionWork) {
		return GraphView{}, errors.New("cursor offset is unavailable; restart this listing without a cursor")
	}
	totalNodes, totalSessions := len(graph.Nodes), len(graph.SessionWork)
	graph.Nodes = graph.Nodes[cursor.Nodes:min(totalNodes, cursor.Nodes+query.Limit)]
	graph.SessionWork = graph.SessionWork[cursor.Sessions:min(totalSessions, cursor.Sessions+query.Limit)]
	for {
		more := cursor.Nodes+len(graph.Nodes) < totalNodes || cursor.Sessions+len(graph.SessionWork) < totalSessions
		graph.Coverage.Truncated = more || cursor.Nodes > 0 || cursor.Sessions > 0
		if more {
			graph.NextCursor = nextDiscoveryCursor(discoveryCursor{graph.Revision, scope, cursor.Nodes + len(graph.Nodes), cursor.Sessions + len(graph.SessionWork)})
		} else {
			graph.NextCursor = ""
		}
		data, err := json.Marshal(graph)
		if err != nil {
			return GraphView{}, err
		}
		if len(data) <= maxDiscoveryBytes {
			return graph, nil
		}
		if len(graph.SessionWork) > 0 {
			graph.SessionWork = graph.SessionWork[:len(graph.SessionWork)-1]
		} else if len(graph.Nodes) > 1 {
			graph.Nodes = graph.Nodes[:len(graph.Nodes)-1]
		} else {
			return GraphView{}, errors.New("one discovery record exceeds response capacity")
		}
		if len(graph.Nodes)+len(graph.SessionWork) == 0 {
			return GraphView{}, errors.New("one discovery record exceeds response capacity")
		}
	}
}
