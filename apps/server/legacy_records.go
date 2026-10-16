package main

// Stored legacy records are retained solely for non-destructive metadata access
// and complete backup/restore. No model worker or semantic route consumes them.
import (
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/observability"
)

const (
	maxProjectFailures = 256
	maxServerFailures  = 4096
)

type projectFailureRecord struct {
	ProjectID string              `json:"project_id"`
	Event     observability.Event `json:"event"`
}

type failureRecorder struct {
	store *store
	sink  observability.Recorder
	now   func() time.Time
}

type bootstrapSource struct {
	ID            string                          `json:"id"`
	Source        contracts.SessionReadReference  `json:"source"`
	Extraction    semanticStage                   `json:"extraction"`
	WorkTime      contracts.WorkTimeRange         `json:"work_time"`
	InheritedFrom *contracts.SessionReadReference `json:"inherited_from,omitempty"`
}

type bootstrapGraphInput struct {
	Sources []bootstrapSource `json:"sources"`
}

type bootstrapGraphNode struct {
	SourceID string   `json:"source_id"`
	Parents  []string `json:"parents"`
}

type bootstrapGraphOutput struct {
	Nodes []bootstrapGraphNode `json:"nodes"`
}

type bootstrapBuildRecord struct {
	Input       bootstrapGraphInput   `json:"input"`
	InputSHA256 string                `json:"input_sha256"`
	Candidate   *bootstrapGraphOutput `json:"candidate,omitempty"`
	NodeIDs     map[string]string     `json:"node_ids,omitempty"`
}

type semanticCoverage struct {
	InheritedTurns   int `json:"inherited_turns,omitempty"`
	LegacyTurns      int `json:"legacy_turns,omitempty"`
	UnprojectedTurns int `json:"unprojected_turns,omitempty"`
	AvailableTurns   int `json:"available_turns"`
	OmittedTurns     int `json:"omitted_turns"`
	TruncatedTurns   int `json:"truncated_turns"`
}

type semanticStage struct {
	Objective string `json:"objective"`
	Summary   string `json:"summary,omitempty"`
	Approach  string `json:"approach,omitempty"`
}

type openItemRevision struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type openItemChanges struct {
	Add      []string           `json:"add,omitempty"`
	Revise   []openItemRevision `json:"revise,omitempty"`
	Complete []string           `json:"complete,omitempty"`
	Withdraw []string           `json:"withdraw,omitempty"`
}

type semanticGraphNode struct {
	ID              string                  `json:"id"`
	Objective       string                  `json:"objective"`
	Summary         string                  `json:"summary,omitempty"`
	Approach        string                  `json:"approach,omitempty"`
	State           contracts.WorkNodeState `json:"state"`
	WorkStartedAt   string                  `json:"work_started_at"`
	WorkUpdatedAt   string                  `json:"work_updated_at"`
	Parents         []string                `json:"parents"`
	ServerSessionID string                  `json:"server_session_id"`
	SourceVersion   int                     `json:"source_version"`
}

type projectionLease struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

type historyPatchGraphNode struct {
	semanticGraphNode
	InheritedFrom *contracts.SessionReadReference `json:"inherited_from,omitempty"`
}

type historyPatchInput struct {
	Sources []bootstrapSource       `json:"sources"`
	Graph   []historyPatchGraphNode `json:"project_work_dag"`
}

type historyPatchNode struct {
	SourceID string `json:"source_id"`
}

type historyPatchReference struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type historyPatchEdge struct {
	Parent historyPatchReference `json:"parent"`
	Child  historyPatchReference `json:"child"`
}

type historyPatchOutput struct {
	Nodes []historyPatchNode `json:"nodes"`
	Edges []historyPatchEdge `json:"edges"`
}

type historyPatchRecord struct {
	FrozenSources bootstrapGraphInput `json:"frozen_sources"`
	SourcesSHA256 string              `json:"sources_sha256"`
	Input         historyPatchInput   `json:"input"`
	InputSHA256   string              `json:"input_sha256"`
	GraphSHA256   string              `json:"graph_sha256"`
	Candidate     *historyPatchOutput `json:"candidate,omitempty"`
	NodeIDs       map[string]string   `json:"node_ids,omitempty"`
}
