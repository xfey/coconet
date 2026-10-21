package contracts

// ProjectDocument is a user-edited project note, independent of the work graph.
// Version zero denotes a document that has never been saved.
const MaxProjectDocumentBytes = 256 << 10

type ProjectDocument struct {
	SchemaVersion int           `json:"schema_version"`
	ProjectID     string        `json:"project_id"`
	Version       int64         `json:"version"`
	Markdown      string        `json:"markdown"`
	SavedAt       string        `json:"saved_at,omitempty"`
	Author        SessionAuthor `json:"author"`
}

type SaveProjectDocumentRequest struct {
	SchemaVersion int    `json:"schema_version"`
	RequestID     string `json:"request_id"`
	BaseVersion   int64  `json:"base_version"`
	Markdown      string `json:"markdown"`
}

type ProjectDocumentVersion struct {
	Version int64         `json:"version"`
	SavedAt string        `json:"saved_at"`
	Author  SessionAuthor `json:"author"`
}

type ProjectDocumentHistory struct {
	SchemaVersion int                      `json:"schema_version"`
	ProjectID     string                   `json:"project_id"`
	Versions      []ProjectDocumentVersion `json:"versions"`
	NextBefore    int64                    `json:"next_before,omitempty"`
}
