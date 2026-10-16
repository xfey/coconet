package contracts

const DeploymentCapabilityVisibleText = "session_visible_text_v1"

// VisibleTextProjection describes a complete native source view, independently
// of the normalized delta. Omitted turns have no user-facing text. Byte ranges
// refer to the unchanged normalized Text bound by TurnID and ContentHash.
type VisibleTextProjection struct {
	Version      int               `json:"version"`
	SourceCursor string            `json:"source_cursor"`
	Turns        []VisibleTextTurn `json:"turns"`
}

type VisibleTextTurn struct {
	TurnID      string      `json:"turn_id"`
	ContentHash string      `json:"content_hash"`
	Ranges      []TextRange `json:"ranges"`
}

// TextRange uses UTF-8 byte offsets, with an exclusive End.
type TextRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}
