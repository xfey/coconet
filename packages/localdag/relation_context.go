package localdag

import "github.com/xfey/coconet/packages/workgraph"

// Only changes to possible semantic evidence wake a context-paused relation.
// Routine captures, timestamps, publication receipts and another node merely
// finishing its analysis cannot cause unbounded background model retries.
func relationContextBasis(state workgraph.State, target string) string {
	type candidate struct {
		Text    workgraph.Text
		Source  workgraph.Source
		State   string
		Parents []string
	}
	candidates := map[string]candidate{}
	for id, node := range state.Nodes {
		if id == target || node.State == "withdrawn" {
			continue
		}
		parents := []string{}
		for _, edge := range node.Parents {
			parents = append(parents, edge.Parent)
		}
		candidates[id] = candidate{Text: node.Text, Source: node.Source, State: node.State, Parents: parents}
	}
	return workgraph.Digest(candidates)
}
