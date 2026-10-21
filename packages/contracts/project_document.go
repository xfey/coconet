package contracts

// ProjectDocument is the single text snapshot read by Agents. Markdown is kept
// as the wire field so existing Runtime installations can read plain notes too.
const MaxProjectDocumentBytes = 256 << 10

type ProjectDocument struct {
	SchemaVersion int           `json:"schema_version"`
	ProjectID     string        `json:"project_id"`
	Version       int64         `json:"version"`
	Markdown      string        `json:"markdown"`
	SavedAt       string        `json:"saved_at,omitempty"`
	Author        SessionAuthor `json:"author"`
}

type ProjectNote struct {
	ID        string        `json:"id"`
	Revision  int64         `json:"revision"`
	Text      string        `json:"text"`
	CreatedAt string        `json:"created_at"`
	Author    SessionAuthor `json:"author"`
}

type ProjectNotes struct {
	SchemaVersion int           `json:"schema_version"`
	ProjectID     string        `json:"project_id"`
	Version       int64         `json:"version"`
	SavedAt       string        `json:"saved_at,omitempty"`
	Notes         []ProjectNote `json:"notes"`
}

type SaveProjectNoteRequest struct {
	SchemaVersion int    `json:"schema_version"`
	RequestID     string `json:"request_id"`
	BaseRevision  int64  `json:"base_revision"`
	Text          string `json:"text"`
}
