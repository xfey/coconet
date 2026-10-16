package contracts

const (
	SchemaVersion                   = 1
	DeploymentProtocolVersion       = 1
	ProjectDeclarationSchemaVersion = 2
	LocalCredentialSchemaVersion    = 2
	LocalBindingSchemaVersion       = 3
	DeploymentCapabilityConnections = "project_connections_v3"
)

type Status string

const (
	StatusOK              Status = "ok"
	StatusPending         Status = "pending"
	StatusDegraded        Status = "degraded"
	StatusUnavailable     Status = "unavailable"
	StatusNotConnected    Status = "not_connected"
	StatusForbidden       Status = "forbidden"
	StatusProjectArchived Status = "project_archived"
	StatusNotFound        Status = "not_found"
	StatusUnsupported     Status = "unsupported"
	StatusInvalidRequest  Status = "invalid_request"
	StatusIntegrityError  Status = "integrity_error"
	StatusInternalError   Status = "internal_error"
)

type FailureCategory string

const (
	FailureNetwork            FailureCategory = "network"
	FailureTimeout            FailureCategory = "timeout"
	FailureDeploymentIdentity FailureCategory = "deployment_identity"
	FailureAuthentication     FailureCategory = "authentication"
	FailureAuthorization      FailureCategory = "authorization"
	FailureAdapter            FailureCategory = "adapter"
	FailureLocalState         FailureCategory = "local_state"
	FailureArtifactIntegrity  FailureCategory = "artifact_integrity"
	FailureObjectStore        FailureCategory = "object_store"
	FailureServerInternal     FailureCategory = "server_internal"
)

type ProjectStatus string

const (
	ProjectActive   ProjectStatus = "active"
	ProjectArchived ProjectStatus = "archived"
)

type ProjectRole string

const (
	RoleOwner  ProjectRole = "owner"
	RoleMember ProjectRole = "member"
)

type MembershipStatus string

const (
	MembershipPending  MembershipStatus = "pending"
	MembershipActive   MembershipStatus = "active"
	MembershipLeft     MembershipStatus = "left"
	MembershipRemoved  MembershipStatus = "removed"
	MembershipArchived MembershipStatus = "archived"
)

type JoinRequestStatus string

const (
	JoinRequestPending  JoinRequestStatus = "pending"
	JoinRequestApproved JoinRequestStatus = "approved"
	JoinRequestRejected JoinRequestStatus = "rejected"
)

type AgentType string

const (
	AgentCodex  AgentType = "codex"
	AgentClaude AgentType = "claude"
	AgentCursor AgentType = "cursor"
)

func ValidAgent(agent AgentType) bool {
	return agent == AgentCodex || agent == AgentClaude || agent == AgentCursor
}

type ActivityState string

const (
	ActivityActive  ActivityState = "active"
	ActivityEnded   ActivityState = "ended"
	ActivityUnknown ActivityState = "unknown"
)

type ComponentHealth struct {
	Name            string          `json:"name"`
	Status          Status          `json:"status"`
	Version         string          `json:"version,omitempty"`
	Message         string          `json:"message,omitempty"`
	FailureCategory FailureCategory `json:"failure_category,omitempty"`
}

type SyncHealthSummary struct {
	Pending              int    `json:"pending"`
	Terminal             int    `json:"terminal"`
	OldestPendingAt      string `json:"oldest_pending_at,omitempty"`
	LastSuccessfulSyncAt string `json:"last_successful_sync_at,omitempty"`
}

type RecallHealthAggregate struct {
	Kind        string `json:"kind"`
	Status      string `json:"status"`
	EventCount  int    `json:"event_count"`
	ResultCount int    `json:"result_count"`
}

// FailureExplanation contains curated display text, never the original error.
type FailureExplanation struct {
	Reason string `json:"reason"`
	Action string `json:"action"`
}

type FailureHealthAggregate struct {
	Explanation     *FailureExplanation `json:"explanation,omitempty"`
	Operation       string              `json:"operation"`
	Status          Status              `json:"status"`
	FailureCategory FailureCategory     `json:"failure_category"`
	ErrorCode       string              `json:"error_code"`
	Retryable       bool                `json:"retryable"`
	EventCount      int                 `json:"event_count"`
	LastObservedAt  string              `json:"last_observed_at"`
	CorrelationID   string              `json:"correlation_id,omitempty"`
}

type RuntimeHealthState struct {
	DeploymentCount     int                      `json:"deployment_count"`
	BindingCount        int                      `json:"binding_count"`
	InvalidBindingCount int                      `json:"invalid_binding_count"`
	Sync                SyncHealthSummary        `json:"sync"`
	Recall              []RecallHealthAggregate  `json:"recall,omitempty"`
	Failures            []FailureHealthAggregate `json:"failures,omitempty"`
}

type HealthReport struct {
	SchemaVersion int                 `json:"schema_version"`
	Status        Status              `json:"status"`
	Component     string              `json:"component"`
	Version       string              `json:"version"`
	Components    []ComponentHealth   `json:"components,omitempty"`
	RuntimeState  *RuntimeHealthState `json:"runtime_state,omitempty"`
}

type DeploymentInfo struct {
	SchemaVersion         int      `json:"schema_version"`
	ProtocolVersion       int      `json:"protocol_version"`
	DeploymentID          string   `json:"deployment_id"`
	ServerVersion         string   `json:"server_version"`
	PublicKey             string   `json:"public_key"`
	PublicKeySHA256       string   `json:"public_key_sha256"`
	Challenge             string   `json:"challenge"`
	Signature             string   `json:"signature"`
	Capabilities          []string `json:"capabilities,omitempty"`
	CapabilitiesSignature string   `json:"capabilities_signature,omitempty"`
}

type ErrorEnvelope struct {
	SchemaVersion int    `json:"schema_version"`
	Status        Status `json:"status"`
	RequestID     string `json:"request_id"`
	Code          string `json:"code"`
	Message       string `json:"message"`
	Retryable     bool   `json:"retryable"`
}

type SessionEnvelope struct {
	SchemaVersion   int           `json:"schema_version"`
	AgentType       AgentType     `json:"agent_type"`
	NativeSessionID string        `json:"native_session_id"`
	CWD             string        `json:"cwd"`
	ActivityState   ActivityState `json:"activity_state"`
	SourceObserved  string        `json:"source_observed_at"`
	LastActivityAt  string        `json:"last_activity_at,omitempty"`
	ClientVersion   string        `json:"client_version"`
	AdapterName     string        `json:"adapter_name"`
	AdapterVersion  string        `json:"adapter_version"`
}

type ArtifactObservation struct {
	// Temporary is local ownership metadata, never part of the wire contract.
	// The observer's caller removes this snapshot after copying it to its store.
	Temporary bool   `json:"-"`
	Path      string `json:"path"`
	MediaType string `json:"media_type"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
}

type NormalizedTurn struct {
	TextOmitted   bool     `json:"text_omitted,omitempty"`
	TurnID        string   `json:"turn_id"`
	SourceTurnID  string   `json:"source_turn_id,omitempty"`
	Ordinal       int      `json:"ordinal"`
	Role          string   `json:"role"`
	Kind          string   `json:"kind"`
	Text          string   `json:"text"`
	ToolName      string   `json:"tool_name,omitempty"`
	RelativePaths []string `json:"relative_paths,omitempty"`
	Symbols       []string `json:"symbols,omitempty"`
	ErrorTokens   []string `json:"error_tokens,omitempty"`
	OccurredAt    string   `json:"occurred_at,omitempty"`
	ContentHash   string   `json:"content_hash"`
}

type NormalizedObservation struct {
	FromCursor  string           `json:"from_cursor,omitempty"`
	ToCursor    string           `json:"to_cursor"`
	Reset       bool             `json:"reset"`
	SourceEpoch int              `json:"source_epoch"`
	Turns       []NormalizedTurn `json:"turns"`
}

type NativeLineageHints struct {
	RootID   string `json:"root_id,omitempty"`
	ParentID string `json:"parent_id,omitempty"`
}

type GitObservation struct {
	Head        string `json:"head,omitempty"`
	Branch      string `json:"branch,omitempty"`
	RelativeCWD string `json:"relative_cwd,omitempty"`
}

// NativeActiveHistory lists the authoritative active native message identities,
// including pre-compaction ancestors. It contains no message/tool bodies.
type NativeActiveHistory struct {
	IDs []string `json:"ids"`
}

type SessionObservation struct {
	ActiveHistory *NativeActiveHistory   `json:"active_history,omitempty"`
	SchemaVersion int                    `json:"schema_version"`
	Session       SessionEnvelope        `json:"session"`
	RawArtifact   ArtifactObservation    `json:"raw_artifact"`
	Normalized    NormalizedObservation  `json:"normalized"`
	VisibleText   *VisibleTextProjection `json:"visible_text,omitempty"`
	Git           *GitObservation        `json:"git,omitempty"`
	NativeLineage *NativeLineageHints    `json:"native_lineage,omitempty"`
}

type DiscoveredSession struct {
	SchemaVersion   int           `json:"schema_version"`
	AgentType       AgentType     `json:"agent_type"`
	NativeSessionID string        `json:"native_session_id"`
	CWD             string        `json:"cwd"`
	ActivityState   ActivityState `json:"activity_state"`
	CreatedAt       string        `json:"created_at,omitempty"`
	UpdatedAt       string        `json:"updated_at"`
}

type SessionDiscovery struct {
	SchemaVersion int                 `json:"schema_version"`
	Sessions      []DiscoveredSession `json:"sessions"`
	Truncated     bool                `json:"truncated,omitempty"`
}

type ArtifactChunk struct {
	Encoding     string `json:"encoding,omitempty"`
	StoredSHA256 string `json:"stored_sha256,omitempty"`
	StoredSize   int64  `json:"stored_size,omitempty"`
	Ordinal      int    `json:"ordinal"`
	SHA256       string `json:"sha256"`
	RawSize      int64  `json:"raw_size"`
}

type ArtifactManifest struct {
	SchemaVersion         int             `json:"schema_version"`
	ArtifactSHA256        string          `json:"artifact_sha256"`
	ArtifactSize          int64           `json:"artifact_size"`
	ArtifactMediaType     string          `json:"artifact_media_type"`
	ManifestSHA256        string          `json:"manifest_sha256"`
	ChunkingSchemaVersion int             `json:"chunking_schema_version"`
	Chunks                []ArtifactChunk `json:"chunks"`
}

type SessionRecord struct {
	SyncCheckpoint         *SessionSyncCheckpoint `json:"sync_checkpoint,omitempty"`
	SchemaVersion          int                    `json:"schema_version"`
	ServerSessionID        string                 `json:"server_session_id"`
	ProjectID              string                 `json:"project_id"`
	AuthorAccountID        string                 `json:"author_account_id"`
	AgentType              AgentType              `json:"agent_type"`
	NativeSessionID        string                 `json:"native_session_id"`
	ActivityState          ActivityState          `json:"activity_state"`
	LatestCommittedVersion int                    `json:"latest_committed_version"`
	LatestIndexedVersion   int                    `json:"latest_indexed_version,omitempty"`
	LatestSyncedAt         string                 `json:"latest_synced_at,omitempty"`
	RelativeCWD            string                 `json:"relative_cwd,omitempty"`
	RootServerSessionID    string                 `json:"root_server_session_id,omitempty"`
	ParentServerSessionID  string                 `json:"parent_server_session_id,omitempty"`
	ParentSessionVersion   int                    `json:"parent_session_version,omitempty"`
	SourceArtifactSHA256   string                 `json:"source_artifact_sha256,omitempty"`
	CreatedAt              string                 `json:"created_at"`
	UpdatedAt              string                 `json:"updated_at"`
}

type Snapshot struct {
	Conversation       *SnapshotContent `json:"conversation,omitempty"`
	ConversationSHA256 string           `json:"conversation_sha256,omitempty"`
	VisibleTextVersion int              `json:"visible_text_version,omitempty"`
	SchemaVersion      int              `json:"schema_version"`
	ServerSessionID    string           `json:"server_session_id"`
	Version            int              `json:"version"`
	Manifest           ArtifactManifest `json:"manifest"`
	ObservedActivity   ActivityState    `json:"observed_activity_state"`
	SourceObservedAt   string           `json:"source_observed_at"`
	AdapterName        string           `json:"adapter_name"`
	AdapterVersion     string           `json:"adapter_version"`
	AgentVersion       string           `json:"agent_version,omitempty"`
	Git                *GitObservation  `json:"git,omitempty"`
	CommittedAt        string           `json:"committed_at"`
}

type PrepareSyncRequest struct {
	ConversationSHA256 string           `json:"conversation_sha256,omitempty"`
	VisibleTextVersion int              `json:"visible_text_version,omitempty"`
	SchemaVersion      int              `json:"schema_version"`
	RequestID          string           `json:"request_id"`
	ProjectID          string           `json:"project_id"`
	AgentType          AgentType        `json:"agent_type"`
	NativeSessionID    string           `json:"native_session_id"`
	KnownServerSession string           `json:"known_server_session_id,omitempty"`
	BaseVersion        int              `json:"base_version"`
	RelativeCWD        string           `json:"relative_cwd,omitempty"`
	ActivityState      ActivityState    `json:"activity_state"`
	SourceObservedAt   string           `json:"source_observed_at"`
	AdapterName        string           `json:"adapter_name"`
	AdapterVersion     string           `json:"adapter_version"`
	AgentVersion       string           `json:"agent_version,omitempty"`
	Git                *GitObservation  `json:"git,omitempty"`
	Manifest           ArtifactManifest `json:"manifest"`
}

type ChunkUploadTarget struct {
	SHA256  string            `json:"sha256"`
	RawSize int64             `json:"raw_size"`
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

type PrepareSyncResponse struct {
	SchemaVersion       int                 `json:"schema_version"`
	Status              Status              `json:"status"`
	AttemptToken        string              `json:"attempt_token"`
	ServerSessionID     string              `json:"server_session_id"`
	LatestVersion       int                 `json:"latest_version"`
	AlreadyCommitted    bool                `json:"already_committed"`
	MissingChunkUploads []ChunkUploadTarget `json:"missing_chunk_uploads"`
	CommittedVersion    *Snapshot           `json:"committed_version,omitempty"`
}

type CommitSyncRequest struct {
	Conversation  *SnapshotObservation `json:"conversation,omitempty"`
	SchemaVersion int                  `json:"schema_version"`
	RequestID     string               `json:"request_id"`
	AttemptToken  string               `json:"attempt_token"`
}

type CommitSyncResponse struct {
	SchemaVersion int           `json:"schema_version"`
	Status        Status        `json:"status"`
	Session       SessionRecord `json:"session"`
	Version       Snapshot      `json:"version"`
}

type ChunkDownloadTarget struct {
	Ordinal int               `json:"ordinal"`
	SHA256  string            `json:"sha256"`
	RawSize int64             `json:"raw_size"`
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

type SessionArtifactSource struct {
	WorkNodeID      string                `json:"work_node_id"`
	Conversation    *ConversationManifest `json:"conversation,omitempty"`
	ServerSessionID string                `json:"server_session_id"`
	NativeSessionID string                `json:"native_session_id"`
	Version         int                   `json:"version"`
	ArtifactSHA256  string                `json:"artifact_sha256"`
	AgentType       AgentType             `json:"agent_type"`
	Author          SessionAuthor         `json:"author"`
	Manifest        ArtifactManifest      `json:"manifest"`
	Git             *GitObservation       `json:"git,omitempty"`
	Lineage         SessionLineage        `json:"lineage"`
}

type PullSessionRequest struct {
	WorkNodeID      string `json:"work_node_id"`
	IncludeArtifact bool   `json:"include_artifact,omitempty"`
	ArtifactFormats []int  `json:"artifact_formats,omitempty"`
	SchemaVersion   int    `json:"schema_version"`
	RequestID       string `json:"request_id"`
	ProjectID       string `json:"project_id"`
	ServerSessionID string `json:"server_session_id"`
	Version         int    `json:"version"`
}

type PullSessionResponse struct {
	ContentDownloads []ChunkDownloadTarget `json:"content_downloads"`
	SchemaVersion    int                   `json:"schema_version"`
	Status           Status                `json:"status"`
	RequestID        string                `json:"request_id"`
	Source           SessionArtifactSource `json:"source"`
	Downloads        []ChunkDownloadTarget `json:"downloads"`
}

type PulledSessionSnapshot struct {
	PullID                   string `json:"pull_id"`
	ArtifactPath             string `json:"artifact_path"`
	NormalizedTranscriptPath string `json:"normalized_transcript_path"`
	NormalizedSHA256         string `json:"normalized_sha256"`
	PulledAt                 string `json:"pulled_at"`
}

type PullSessionResult struct {
	SchemaVersion int                    `json:"schema_version"`
	Status        Status                 `json:"status"`
	RequestID     string                 `json:"request_id"`
	Retryable     bool                   `json:"retryable"`
	Message       string                 `json:"message,omitempty"`
	Source        *SessionArtifactSource `json:"source,omitempty"`
	Local         *PulledSessionSnapshot `json:"local,omitempty"`
}

type PrepareForkRequest struct {
	WorkNodeID            string    `json:"work_node_id"`
	SchemaVersion         int       `json:"schema_version"`
	RequestID             string    `json:"request_id"`
	ProjectID             string    `json:"project_id"`
	SourceServerSessionID string    `json:"source_server_session_id"`
	SourceVersion         int       `json:"source_version"`
	SourceArtifactSHA256  string    `json:"source_artifact_sha256"`
	TargetAgentType       AgentType `json:"target_agent_type"`
}

type PrepareForkResponse struct {
	SchemaVersion int                   `json:"schema_version"`
	Status        Status                `json:"status"`
	AttemptToken  string                `json:"attempt_token"`
	Source        SessionArtifactSource `json:"source"`
}

type CompleteForkRequest struct {
	SchemaVersion   int    `json:"schema_version"`
	RequestID       string `json:"request_id"`
	AttemptToken    string `json:"attempt_token"`
	NativeSessionID string `json:"native_session_id"`
	RelativeCWD     string `json:"relative_cwd,omitempty"`
}

type ForkCreatedSession struct {
	ServerSessionID       string    `json:"server_session_id"`
	NativeSessionID       string    `json:"native_session_id"`
	AgentType             AgentType `json:"agent_type"`
	RootServerSessionID   string    `json:"root_server_session_id"`
	ParentServerSessionID string    `json:"parent_server_session_id"`
	ParentSessionVersion  int       `json:"parent_session_version"`
}

type CompleteForkResponse struct {
	SchemaVersion int                   `json:"schema_version"`
	Status        Status                `json:"status"`
	Source        SessionArtifactSource `json:"source"`
	Created       ForkCreatedSession    `json:"created"`
}

type ResumeCommand struct {
	Program string   `json:"program"`
	Args    []string `json:"args"`
	Display string   `json:"display"`
}

type ForkEnvironmentHints struct {
	SourceGitHead      string `json:"source_git_head,omitempty"`
	SourceGitBranch    string `json:"source_git_branch,omitempty"`
	CurrentGitHead     string `json:"current_git_head,omitempty"`
	CurrentGitBranch   string `json:"current_git_branch,omitempty"`
	WorkingTreeDirty   bool   `json:"working_tree_dirty"`
	WorkingTreeChanged bool   `json:"working_tree_changed"`
}

type ForkSessionResult struct {
	SchemaVersion    int                    `json:"schema_version"`
	Status           Status                 `json:"status"`
	RequestID        string                 `json:"request_id"`
	Retryable        bool                   `json:"retryable"`
	Message          string                 `json:"message,omitempty"`
	Source           *SessionArtifactSource `json:"source,omitempty"`
	Created          *ForkCreatedSession    `json:"created,omitempty"`
	Resume           *ResumeCommand         `json:"resume,omitempty"`
	EnvironmentHints *ForkEnvironmentHints  `json:"environment_hints,omitempty"`
}

type IndexSessionRequest struct {
	NativeLineage   *NativeLineageHints    `json:"native_lineage,omitempty"`
	SchemaVersion   int                    `json:"schema_version"`
	RequestID       string                 `json:"request_id"`
	ProjectID       string                 `json:"project_id"`
	ServerSessionID string                 `json:"server_session_id"`
	Version         int                    `json:"version"`
	Normalized      NormalizedObservation  `json:"normalized"`
	VisibleText     *VisibleTextProjection `json:"visible_text,omitempty"`
}

type SessionIndexRecord struct {
	SchemaVersion     int    `json:"schema_version"`
	ServerSessionID   string `json:"server_session_id"`
	Version           int    `json:"version"`
	BaseIndexVersion  int    `json:"base_index_version,omitempty"`
	SourceBaseVersion int    `json:"source_base_version,omitempty"`
	SourceEpoch       int    `json:"source_epoch"`
	ToCursor          string `json:"to_cursor"`
	TurnCount         int    `json:"turn_count"`
	IndexSHA256       string `json:"index_sha256"`
	IndexedAt         string `json:"indexed_at"`
}

type IndexSessionResponse struct {
	SchemaVersion int                `json:"schema_version"`
	Status        Status             `json:"status"`
	Index         SessionIndexRecord `json:"index"`
}

type ProjectReference struct {
	ProjectID string        `json:"project_id"`
	Name      string        `json:"name"`
	Status    ProjectStatus `json:"status"`
}

type SessionAuthor struct {
	AccountID   string `json:"account_id"`
	DisplayName string `json:"display_name"`
}

type SessionLineage struct {
	RootServerSessionID   string `json:"root_server_session_id"`
	ParentServerSessionID string `json:"parent_server_session_id,omitempty"`
	ParentSessionVersion  int    `json:"parent_session_version,omitempty"`
	FamilyResultCount     int    `json:"family_result_count,omitempty"`
}

type SearchSessionsRequest struct {
	IncludeLibraryTags bool                   `json:"include_library_tags,omitempty"`
	Scope              SessionSearchScope     `json:"scope,omitempty"`
	SchemaVersion      int                    `json:"schema_version"`
	RequestID          string                 `json:"request_id"`
	ProjectID          string                 `json:"project_id"`
	Query              string                 `json:"query,omitempty"`
	Paths              []string               `json:"paths,omitempty"`
	SessionRefs        []SessionReadReference `json:"session_refs,omitempty"`
	// Deprecated: include code symbols in Query. Retained for wire compatibility.
	Symbols []string `json:"symbols,omitempty"`
	// Deprecated: include error identifiers in Query. Retained for wire compatibility.
	ErrorTokens []string `json:"error_tokens,omitempty"`
	Limit       int      `json:"limit,omitempty"`
	Cursor      string   `json:"cursor,omitempty"`
}

type SearchQueryInterpretation struct {
	Query       string   `json:"query,omitempty"`
	Paths       []string `json:"paths,omitempty"`
	Symbols     []string `json:"symbols,omitempty"`
	ErrorTokens []string `json:"error_tokens,omitempty"`
}

type SearchMatch struct {
	TurnID        string   `json:"turn_id"`
	Ordinal       int      `json:"ordinal"`
	Excerpt       string   `json:"excerpt"`
	Reasons       []string `json:"reasons"`
	RelativePaths []string `json:"relative_paths,omitempty"`
	Symbols       []string `json:"symbols,omitempty"`
	ErrorTokens   []string `json:"error_tokens,omitempty"`
	Inherited     bool     `json:"inherited"`
}

type SessionReadReference struct {
	ServerSessionID string `json:"server_session_id"`
	Version         int    `json:"version"`
}

type WorkNodeState string

const (
	WorkNodeCurrent WorkNodeState = "current"
	WorkNodeSealed  WorkNodeState = "sealed"
)

type WorkTimeRange struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

type WorkOpenItem struct {
	ID            string `json:"id"`
	Text          string `json:"text"`
	SourceVersion int    `json:"source_version"`
}

type SessionWorkState struct {
	Source    SessionReadReference `json:"source"`
	Author    SessionAuthor        `json:"author"`
	OpenItems []WorkOpenItem       `json:"open_items"`
}

type ProjectWorkNode struct {
	Evidence   *WorkEvidence        `json:"evidence,omitempty"`
	WorkNodeID string               `json:"id"`
	Objective  string               `json:"objective"`
	Summary    string               `json:"summary,omitempty"`
	Approach   string               `json:"approach,omitempty"`
	State      WorkNodeState        `json:"state"`
	Author     SessionAuthor        `json:"author"`
	WorkTime   WorkTimeRange        `json:"work_time"`
	Parents    []string             `json:"parents"`
	Source     SessionReadReference `json:"source"`
}

type ReadProjectWorkGraphRequest struct {
	SchemaVersion int    `json:"schema_version"`
	RequestID     string `json:"request_id"`
	ProjectID     string `json:"project_id"`
}

type ReadProjectWorkGraphResponse struct {
	Revision      int64              `json:"revision,omitempty"`
	SchemaVersion int                `json:"schema_version"`
	Status        Status             `json:"status"`
	RequestID     string             `json:"request_id"`
	Retryable     bool               `json:"retryable"`
	Message       string             `json:"message,omitempty"`
	Warning       string             `json:"warning,omitempty"`
	Nodes         []ProjectWorkNode  `json:"nodes"`
	SessionWork   []SessionWorkState `json:"session_work"`
}

type SearchSessionResult struct {
	LibraryTagMatch  *LibraryTagMatch     `json:"library_tag_match,omitempty"`
	LibraryEntry     *LibraryEntry        `json:"library_entry,omitempty"`
	LibraryNoteMatch *LibraryNoteMatch    `json:"library_note_match,omitempty"`
	ServerSessionID  string               `json:"server_session_id"`
	AgentType        AgentType            `json:"agent_type"`
	Author           SessionAuthor        `json:"author"`
	ActivityState    ActivityState        `json:"activity_state"`
	LatestSyncedAt   string               `json:"latest_synced_at"`
	HitVersion       int                  `json:"hit_version"`
	LatestVersion    int                  `json:"latest_version"`
	Lineage          SessionLineage       `json:"lineage"`
	Matches          []SearchMatch        `json:"matches"`
	ReadRef          SessionReadReference `json:"read_ref"`
}

type SearchSessionsResponse struct {
	Scope               SessionSearchScope        `json:"scope,omitempty"`
	SchemaVersion       int                       `json:"schema_version"`
	Status              Status                    `json:"status"`
	RequestID           string                    `json:"request_id"`
	Retryable           bool                      `json:"retryable"`
	Message             string                    `json:"message,omitempty"`
	Project             *ProjectReference         `json:"project,omitempty"`
	QueryInterpretation SearchQueryInterpretation `json:"query_interpretation"`
	Results             []SearchSessionResult     `json:"results"`
	NextCursor          string                    `json:"next_cursor,omitempty"`
}

type ListRecentSessionsRequest struct {
	SchemaVersion int    `json:"schema_version"`
	RequestID     string `json:"request_id"`
	ProjectID     string `json:"project_id"`
	Limit         int    `json:"limit,omitempty"`
	Cursor        string `json:"cursor,omitempty"`
}

type RecentSessionExcerpt struct {
	TurnID  string `json:"turn_id"`
	Ordinal int    `json:"ordinal"`
	Role    string `json:"role"`
	Kind    string `json:"kind"`
	Text    string `json:"text"`
}

type RecentSessionResult struct {
	ServerSessionID string                `json:"server_session_id"`
	AgentType       AgentType             `json:"agent_type"`
	Author          SessionAuthor         `json:"author"`
	ActivityState   ActivityState         `json:"activity_state"`
	LatestSyncedAt  string                `json:"latest_synced_at"`
	LatestVersion   int                   `json:"latest_version"`
	LatestExcerpt   *RecentSessionExcerpt `json:"latest_excerpt,omitempty"`
	RecentPaths     []string              `json:"recent_paths,omitempty"`
	Lineage         SessionLineage        `json:"lineage"`
	ReadRef         SessionReadReference  `json:"read_ref"`
}

type ListRecentSessionsResponse struct {
	SchemaVersion int                   `json:"schema_version"`
	Status        Status                `json:"status"`
	RequestID     string                `json:"request_id"`
	Retryable     bool                  `json:"retryable"`
	Message       string                `json:"message,omitempty"`
	Project       *ProjectReference     `json:"project,omitempty"`
	Results       []RecentSessionResult `json:"results"`
	NextCursor    string                `json:"next_cursor,omitempty"`
}

type ReadSessionRequest struct {
	SchemaVersion   int    `json:"schema_version"`
	RequestID       string `json:"request_id"`
	ProjectID       string `json:"project_id"`
	ServerSessionID string `json:"server_session_id"`
	Version         int    `json:"version"`
	AroundTurnID    string `json:"around_turn_id,omitempty"`
	ThroughTurnID   string `json:"through_turn_id,omitempty"`
	Before          int    `json:"before,omitempty"`
	After           int    `json:"after,omitempty"`
	Cursor          string `json:"cursor,omitempty"`
	Limit           int    `json:"limit,omitempty"`
}

type ReadSessionTurn struct {
	TurnID        string   `json:"turn_id"`
	Ordinal       int      `json:"ordinal"`
	Role          string   `json:"role"`
	Kind          string   `json:"kind"`
	Text          string   `json:"text"`
	TextOffset    int      `json:"text_offset"`
	TextComplete  bool     `json:"text_complete"`
	ToolName      string   `json:"tool_name,omitempty"`
	RelativePaths []string `json:"relative_paths,omitempty"`
	Symbols       []string `json:"symbols,omitempty"`
	ErrorTokens   []string `json:"error_tokens,omitempty"`
	OccurredAt    string   `json:"occurred_at,omitempty"`
	ContentHash   string   `json:"content_hash"`
	Inherited     bool     `json:"inherited"`
}

type ReadSessionResponse struct {
	SchemaVersion   int               `json:"schema_version"`
	Status          Status            `json:"status"`
	RequestID       string            `json:"request_id"`
	Retryable       bool              `json:"retryable"`
	Message         string            `json:"message,omitempty"`
	ServerSessionID string            `json:"server_session_id,omitempty"`
	AgentType       AgentType         `json:"agent_type,omitempty"`
	ProjectStatus   ProjectStatus     `json:"project_status,omitempty"`
	Version         int               `json:"version,omitempty"`
	LatestVersion   int               `json:"latest_version,omitempty"`
	ActivityState   ActivityState     `json:"activity_state,omitempty"`
	Author          *SessionAuthor    `json:"author,omitempty"`
	Lineage         *SessionLineage   `json:"lineage,omitempty"`
	Turns           []ReadSessionTurn `json:"turns"`
	NextCursor      string            `json:"next_cursor,omitempty"`
}

type MCPHealthResult struct {
	SchemaVersion int          `json:"schema_version"`
	Status        Status       `json:"status"`
	Health        HealthReport `json:"health"`
}

type Account struct {
	SchemaVersion int    `json:"schema_version"`
	AccountID     string `json:"account_id"`
	DisplayName   string `json:"display_name"`
	Email         string `json:"email"`
	CreatedAt     string `json:"created_at"`
}

type AccountBootstrapRequest struct {
	SchemaVersion int    `json:"schema_version"`
	DisplayName   string `json:"display_name"`
	Email         string `json:"email"`
}

type AccessToken struct {
	SchemaVersion int      `json:"schema_version"`
	Status        Status   `json:"status"`
	AccessToken   string   `json:"access_token,omitempty"`
	TokenType     string   `json:"token_type,omitempty"`
	Account       *Account `json:"account,omitempty"`
}

type Project struct {
	SchemaVersion int           `json:"schema_version"`
	ProjectID     string        `json:"project_id"`
	Name          string        `json:"name"`
	CreatedBy     string        `json:"created_by"`
	OwnerID       string        `json:"owner_id,omitempty"`
	Status        ProjectStatus `json:"status"`
	CreatedAt     string        `json:"created_at"`
	ArchivedAt    string        `json:"archived_at,omitempty"`
}

type Membership struct {
	SchemaVersion int              `json:"schema_version"`
	ProjectID     string           `json:"project_id"`
	Account       Account          `json:"account"`
	Role          ProjectRole      `json:"role"`
	Status        MembershipStatus `json:"status"`
	CreatedAt     string           `json:"created_at"`
	UpdatedAt     string           `json:"updated_at"`
}

type JoinRequest struct {
	SchemaVersion int               `json:"schema_version"`
	JoinRequestID string            `json:"join_request_id"`
	ProjectID     string            `json:"project_id"`
	Account       Account           `json:"account"`
	Status        JoinRequestStatus `json:"status"`
	CreatedAt     string            `json:"created_at"`
	DecidedAt     string            `json:"decided_at,omitempty"`
	DecidedBy     string            `json:"decided_by,omitempty"`
}

type AuditEvent struct {
	SchemaVersion int    `json:"schema_version"`
	AuditEventID  string `json:"audit_event_id"`
	RequestID     string `json:"request_id"`
	ProjectID     string `json:"project_id"`
	ActorID       string `json:"actor_id"`
	Action        string `json:"action"`
	TargetID      string `json:"target_id,omitempty"`
	Outcome       string `json:"outcome"`
	OccurredAt    string `json:"occurred_at"`
}

type ProjectAccess struct {
	SchemaVersion int             `json:"schema_version"`
	Status        Status          `json:"status"`
	Project       *Project        `json:"project,omitempty"`
	Membership    *Membership     `json:"membership,omitempty"`
	JoinRequest   *JoinRequest    `json:"join_request,omitempty"`
	Capabilities  map[string]bool `json:"capabilities"`
}

type ProjectManagement struct {
	SchemaVersion int           `json:"schema_version"`
	Project       Project       `json:"project"`
	Current       Membership    `json:"current_membership"`
	Members       []Membership  `json:"members"`
	JoinRequests  []JoinRequest `json:"join_requests"`
}

type CreateProjectRequest struct {
	SchemaVersion int    `json:"schema_version"`
	RequestID     string `json:"request_id"`
	Name          string `json:"name"`
}

type ConnectProjectRequest struct {
	SchemaVersion int    `json:"schema_version"`
	RequestID     string `json:"request_id"`
	ProjectID     string `json:"project_id"`
}

type ProjectMutationRequest struct {
	SchemaVersion int    `json:"schema_version"`
	RequestID     string `json:"request_id"`
}

type JoinDecisionRequest struct {
	SchemaVersion int               `json:"schema_version"`
	RequestID     string            `json:"request_id"`
	Decision      JoinRequestStatus `json:"decision"`
}

type ProjectDeclaration struct {
	SchemaVersion         int    `json:"schema_version"`
	DeploymentID          string `json:"deployment_id"`
	ServerURL             string `json:"server_url"`
	ServerPublicKeySHA256 string `json:"server_public_key_sha256"`
	ProjectID             string `json:"project_id"`
}

type LocalBinding struct {
	Workflow              string `json:"workflow,omitempty"`
	SchemaVersion         int    `json:"schema_version"`
	DeploymentID          string `json:"deployment_id"`
	ServerPublicKeySHA256 string `json:"server_public_key_sha256"`
	ProjectID             string `json:"project_id"`
	AccountID             string `json:"account_id"`
	CollaborationRoot     string `json:"collaboration_root"`
	LocalServerURL        string `json:"local_server_url"`
	ConnectedAt           string `json:"connected_at"`
}

type LocalBindingState struct {
	SchemaVersion int            `json:"schema_version"`
	Bindings      []LocalBinding `json:"bindings"`
}

// SnapshotObservation accompanies the immutable artifact at its single commit boundary.
type SnapshotObservation struct {
	Normalized    NormalizedObservation  `json:"normalized"`
	VisibleText   *VisibleTextProjection `json:"visible_text,omitempty"`
	NativeLineage *NativeLineageHints    `json:"native_lineage,omitempty"`
}

const DeploymentCapabilityAtomicSnapshots = "atomic_snapshots_v1"

// Content manifests are self-contained lists of immutable blocks.
type ConversationBlock struct {
	SHA256 string `json:"sha256"`
	Size   int    `json:"size"`
	First  int    `json:"first"`
	Count  int    `json:"count"`
}
type ConversationManifest struct {
	Format int                 `json:"format"`
	Count  int                 `json:"count"`
	Blocks []ConversationBlock `json:"blocks"`
	SHA256 string              `json:"sha256"`
}
type SnapshotContent struct {
	ObservationSHA256 string                `json:"observation_sha256,omitempty"`
	Content           *ConversationManifest `json:"content"`
	NativeLineage     *NativeLineageHints   `json:"native_lineage,omitempty"`
	VisibleTextSHA256 string                `json:"visible_text_sha256,omitempty"`
	Summary           SessionIndexRecord    `json:"summary"`
}

type SessionSyncCheckpoint struct {
	Version     int                  `json:"version"`
	SourceEpoch int                  `json:"source_epoch"`
	Cursor      string               `json:"cursor"`
	Native      ConversationManifest `json:"native"`
}
