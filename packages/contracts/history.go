package contracts

const DeploymentCapabilityHistoryBatches = "history_batches_v1"

const DeploymentCapabilityNativeLineage = "native_lineage_hints_v1"

// HistoryManifestMember identifies a snapshot captured before the batch is opened.
// A failed capture has no artifact; later captures belong to another batch.
type HistoryManifestMember struct {
	MemberID        string    `json:"member_id"`
	AgentType       AgentType `json:"agent_type"`
	NativeSessionID string    `json:"native_session_id"`
	ArtifactSHA256  string    `json:"artifact_sha256,omitempty"`
	FailureCode     string    `json:"failure_code,omitempty"`
}

type HistoryDiscoveryIssue struct {
	AgentType AgentType `json:"agent_type"`
	Code      string    `json:"code"`
}

type BeginHistoryBatchRequest struct {
	RetryOfBatchID  string                  `json:"retry_of_batch_id,omitempty"`
	SchemaVersion   int                     `json:"schema_version"`
	RequestID       string                  `json:"request_id"`
	ProjectID       string                  `json:"project_id"`
	BatchID         string                  `json:"batch_id"`
	Members         []HistoryManifestMember `json:"members"`
	DiscoveryIssues []HistoryDiscoveryIssue `json:"discovery_issues"`
}

type ResolveHistoryMemberRequest struct {
	SchemaVersion int                   `json:"schema_version"`
	RequestID     string                `json:"request_id"`
	ProjectID     string                `json:"project_id"`
	BatchID       string                `json:"batch_id"`
	MemberID      string                `json:"member_id"`
	Source        *SessionReadReference `json:"source,omitempty"`
	FailureCode   string                `json:"failure_code,omitempty"`
}

type CompleteHistoryBatchRequest struct {
	SchemaVersion int    `json:"schema_version"`
	RequestID     string `json:"request_id"`
	ProjectID     string `json:"project_id"`
	BatchID       string `json:"batch_id"`
}

type HistoryMember struct {
	HistoryManifestMember
	Extraction  *HistoryExtractionResult `json:"extraction,omitempty"`
	Source      *SessionReadReference    `json:"source,omitempty"`
	Indexed     bool                     `json:"indexed"`
	ResultError string                   `json:"result_error,omitempty"`
}

// Each batch retains its first completed attempt, including failures.
type HistoryExtractionResult struct {
	Status      string `json:"status"`
	ErrorCode   string `json:"error_code,omitempty"`
	CompletedAt string `json:"completed_at"`
}

// HistoryRecoveryReference attributes a new processing round to a frozen failure.
type HistoryRecoveryReference struct {
	BatchID  string `json:"batch_id"`
	MemberID string `json:"member_id"`
}

// SourcesReady only closes the upload/index boundary. It does not publish a DAG.
type HistoryBatch struct {
	RetryOfBatchID        string                    `json:"retry_of_batch_id,omitempty"`
	RecoveryOf            *HistoryRecoveryReference `json:"recovery_of,omitempty"`
	GraphBuild            *HistoryGraphBuild        `json:"graph_build,omitempty"`
	ExtractionCompletedAt string                    `json:"extraction_completed_at,omitempty"`
	BatchID               string                    `json:"batch_id"`
	ProjectID             string                    `json:"project_id"`
	AccountID             string                    `json:"account_id"`
	Kind                  string                    `json:"kind"`
	CutoffAt              string                    `json:"cutoff_at"`
	CompletedAt           string                    `json:"completed_at,omitempty"`
	State                 string                    `json:"state"`
	Incomplete            bool                      `json:"incomplete"`
	Members               []HistoryMember           `json:"members"`
	DiscoveryIssues       []HistoryDiscoveryIssue   `json:"discovery_issues"`
}

// Candidate persistence and publication are separate durable transitions.
type HistoryGraphBuild struct {
	State         string `json:"state"`
	Attempts      int    `json:"attempts"`
	LastError     string `json:"last_error,omitempty"`
	NextAttemptAt string `json:"next_attempt_at,omitempty"`
	PublishedAt   string `json:"published_at,omitempty"`
}

// Legacy Projects retain incremental behavior and have no reconstructed T0.
type ProjectProjectionState struct {
	ProjectID      string `json:"project_id"`
	Mode           string `json:"mode"`
	State          string `json:"state"`
	T0             string `json:"t0,omitempty"`
	InitialBatchID string `json:"initial_batch_id,omitempty"`
}

type HistoryBatchResponse struct {
	SchemaVersion int                    `json:"schema_version"`
	Status        Status                 `json:"status"`
	Batch         HistoryBatch           `json:"batch"`
	Projection    ProjectProjectionState `json:"projection"`
}

type ProjectProjectionResponse struct {
	Issues         []ProjectionIssue        `json:"issues"`
	RecentFailures []FailureHealthAggregate `json:"recent_failures,omitempty"`
	SchemaVersion  int                      `json:"schema_version"`
	Status         Status                   `json:"status"`
	Projection     ProjectProjectionState   `json:"projection"`
	Batches        []HistoryBatch           `json:"batches"`
	Suspended      []SuspendedProjection    `json:"suspended"`
}

// Issues describe current actionable state; recent_failures remains an audit history.
type ProjectionIssue struct {
	Stage         string                `json:"stage"`
	State         string                `json:"state"`
	ErrorCode     string                `json:"error_code"`
	RetryLocation string                `json:"retry_location"`
	BatchID       string                `json:"batch_id,omitempty"`
	AgentType     AgentType             `json:"agent_type,omitempty"`
	Source        *SessionReadReference `json:"source,omitempty"`
	Attempts      int                   `json:"attempts"`
	NextAttemptAt string                `json:"next_attempt_at,omitempty"`
	Explanation   FailureExplanation    `json:"explanation"`
}

// Suspended work retains its fixed source and requires an explicit retry after repair.
type SuspendedProjection struct {
	Explanation *FailureExplanation   `json:"explanation,omitempty"`
	Stage       string                `json:"stage"`
	Source      *SessionReadReference `json:"source,omitempty"`
	BatchID     string                `json:"batch_id,omitempty"`
	ErrorCode   string                `json:"error_code"`
	Attempts    int                   `json:"attempts"`
}

type RetryProjectProjectionRequest struct {
	SchemaVersion int    `json:"schema_version"`
	RequestID     string `json:"request_id"`
	ProjectID     string `json:"project_id"`
}

type RetryProjectProjectionResponse struct {
	SchemaVersion int    `json:"schema_version"`
	Status        Status `json:"status"`
	RequestID     string `json:"request_id"`
	ProjectID     string `json:"project_id"`
}
