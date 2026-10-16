package contracts

// WorkEvidence is a navigation window within the node's fixed source Version.
// It identifies observed material, not a claim of sentence-level attribution.
type WorkEvidence struct {
	StartTurnID string `json:"start_turn_id"`
	EndTurnID   string `json:"end_turn_id"`
	TurnCount   int    `json:"turn_count"`
	Scope       string `json:"scope"`
}

type SearchWorkRequest struct {
	IncludeLibraryEntries bool     `json:"include_library_entries,omitempty"`
	SchemaVersion         int      `json:"schema_version"`
	RequestID             string   `json:"request_id"`
	ProjectID             string   `json:"project_id"`
	Query                 string   `json:"query,omitempty"`
	Paths                 []string `json:"paths,omitempty"`
	Limit                 int      `json:"limit,omitempty"`
}

type WorkSearchMatch struct {
	LibraryEntry *LibraryEntry   `json:"library_entry,omitempty"`
	Node         ProjectWorkNode `json:"node"`
	Reasons      []string        `json:"reasons"`
	Children     []string        `json:"children"`
}

type WorkSearchCoverage struct {
	ProjectedNodes int    `json:"projected_nodes"`
	MatchingNodes  int    `json:"matching_nodes"`
	Projection     string `json:"projection"`
	Truncated      bool   `json:"truncated"`
}

type SearchWorkResponse struct {
	SchemaVersion int                `json:"schema_version"`
	Status        Status             `json:"status"`
	RequestID     string             `json:"request_id"`
	Retryable     bool               `json:"retryable"`
	Message       string             `json:"message,omitempty"`
	Nodes         []WorkSearchMatch  `json:"nodes"`
	Coverage      WorkSearchCoverage `json:"coverage"`
}
