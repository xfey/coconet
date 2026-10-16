package contracts

// ProjectActivity freezes the presentation and source at the time of recording.
// Node parents describe that snapshot, not the graph's currently known edges.
type ProjectActivity struct {
	ID             string               `json:"id"`
	ProjectID      string               `json:"project_id"`
	Kind           string               `json:"kind"`
	RecordedAt     string               `json:"recorded_at"`
	Historical     bool                 `json:"historical"`
	Actor          SessionAuthor        `json:"actor"`
	Source         SessionReadReference `json:"source"`
	Node           *ProjectWorkNode     `json:"node,omitempty"`
	LibraryEntry   *LibraryEntry        `json:"library_entry,omitempty"`
	ChildSessionID string               `json:"child_session_id,omitempty"`
}

type ListProjectActivityRequest struct {
	Limit   int    `json:"limit,omitempty"`
	Cursor  string `json:"cursor,omitempty"`
	EventID string `json:"event_id,omitempty"`
}

type ListProjectActivityResponse struct {
	SchemaVersion int               `json:"schema_version"`
	Status        Status            `json:"status"`
	ProjectID     string            `json:"project_id"`
	Events        []ProjectActivity `json:"events"`
	NextCursor    string            `json:"next_cursor,omitempty"`
}

type DashboardLoginRequest struct {
	SchemaVersion int    `json:"schema_version"`
	AccountKey    string `json:"account_key"`
}

type DashboardSessionResponse struct {
	SchemaVersion int     `json:"schema_version"`
	Status        Status  `json:"status"`
	Account       Account `json:"account"`
	ExpiresAt     string  `json:"expires_at"`
}

type DashboardProjectsResponse struct {
	SchemaVersion int                `json:"schema_version"`
	Status        Status             `json:"status"`
	Projects      []ProjectReference `json:"projects"`
}

// The selected source fences a current node that advanced while the panel was open.
// EntryID fences removal against a later collection of the same source.
type DashboardLibraryRequest struct {
	SchemaVersion int                  `json:"schema_version"`
	RequestID     string               `json:"request_id"`
	Source        SessionReadReference `json:"source"`
	Collected     *bool                `json:"collected"`
	EntryID       string               `json:"entry_id,omitempty"`
}

type DashboardLibraryResponse struct {
	SchemaVersion int           `json:"schema_version"`
	Status        Status        `json:"status"`
	Collected     bool          `json:"collected"`
	Entry         *LibraryEntry `json:"entry,omitempty"`
}

type DashboardDialogueRequest struct {
	SchemaVersion   int    `json:"schema_version"`
	RequestID       string `json:"request_id"`
	ProjectID       string `json:"project_id"`
	ServerSessionID string `json:"server_session_id"`
	Version         int    `json:"version"`
	Limit           int    `json:"limit,omitempty"`
	Cursor          string `json:"cursor,omitempty"`
}

type DashboardMessage struct {
	TurnID       string `json:"turn_id"`
	Role         string `json:"role"`
	Text         string `json:"text"`
	TextOffset   int    `json:"text_offset"`
	TextComplete bool   `json:"text_complete"`
}

type DashboardDialogueResponse struct {
	SchemaVersion   int                `json:"schema_version"`
	Status          Status             `json:"status"`
	ServerSessionID string             `json:"server_session_id"`
	Version         int                `json:"version"`
	AgentType       AgentType          `json:"agent_type"`
	Author          SessionAuthor      `json:"author"`
	Messages        []DashboardMessage `json:"messages"`
	NextCursor      string             `json:"next_cursor,omitempty"`
}

// The Runtime persists its randomly generated replacement before submitting it.
// Retrying with that replacement recovers a lost response without storing keys on the Server.
type ResetAccountKeyRequest struct {
	SchemaVersion int    `json:"schema_version"`
	NewKey        string `json:"new_key"`
}

type ResetAccountKeyResponse struct {
	SchemaVersion int    `json:"schema_version"`
	Status        Status `json:"status"`
	AccountID     string `json:"account_id"`
}
