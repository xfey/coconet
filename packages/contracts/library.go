package contracts

const (
	DeploymentCapabilityLibraryBrowse     = "session_library_browse_v1"
	DeploymentCapabilityLibraryCheckpoint = "session_library_checkpoint_v1"
	DeploymentCapabilitySessionLibrary    = "session_library_v1"
	DeploymentCapabilityLibrarySearch     = "session_library_search_v1"
	DeploymentCapabilityLibraryTags       = "session_library_tags_v1"
)

// LibraryWorkContext is a bounded view of a checkpoint's currently known edges.
// Node.Parents and Children may be partial; totals describe the complete sets.
type LibraryWorkContext struct {
	Node          ProjectWorkNode `json:"node"`
	ParentsTotal  int             `json:"parents_total"`
	Children      []string        `json:"children"`
	ChildrenTotal int             `json:"children_total"`
}

type LibraryListEntry struct {
	Entry         LibraryEntry       `json:"entry"`
	AgentType     AgentType          `json:"agent_type"`
	LatestVersion int                `json:"latest_version"`
	Work          LibraryWorkContext `json:"work"`
}

type ListLibraryEntriesRequest struct {
	SchemaVersion int                    `json:"schema_version"`
	RequestID     string                 `json:"request_id"`
	ProjectID     string                 `json:"project_id"`
	SessionRefs   []SessionReadReference `json:"session_refs,omitempty"`
	Limit         int                    `json:"limit,omitempty"`
	Cursor        string                 `json:"cursor,omitempty"`
}

type ListLibraryEntriesResponse struct {
	SchemaVersion int                `json:"schema_version"`
	Status        Status             `json:"status"`
	RequestID     string             `json:"request_id"`
	ProjectID     string             `json:"project_id"`
	Retryable     bool               `json:"retryable"`
	Message       string             `json:"message,omitempty"`
	Entries       []LibraryListEntry `json:"entries"`
	NextCursor    string             `json:"next_cursor,omitempty"`
}

type SessionSearchScope string

const (
	SessionSearchAll     SessionSearchScope = "all"
	SessionSearchLibrary SessionSearchScope = "library"
)

// LibraryNoteMatch describes collection metadata, never a transcript Turn.
type LibraryNoteMatch struct {
	Excerpt string   `json:"excerpt"`
	Reasons []string `json:"reasons"`
}

// LibraryEntry retains one explicitly selected, indexed Session Version.
// Source and collection metadata never follow the Session's latest Version.
type LibraryEntry struct {
	WorkNodeID  string               `json:"work_node_id"`
	Tagging     *LibraryTagging      `json:"tagging,omitempty"`
	EntryID     string               `json:"entry_id"`
	ProjectID   string               `json:"project_id"`
	Source      SessionReadReference `json:"source"`
	SubmittedBy string               `json:"submitted_by"`
	CollectedAt string               `json:"collected_at"`
	Note        string               `json:"note,omitempty"`
}

// LibraryTagging is derived metadata. Ready with no tags is a successful
// abstention; absent tagging means an older response without this capability.
type LibraryTagging struct {
	State       string   `json:"state"`
	Tags        []string `json:"tags"`
	GeneratedAt string   `json:"generated_at,omitempty"`
	ErrorCode   string   `json:"error_code,omitempty"`
}

// LibraryTagMatch identifies generated discovery hints, never transcript text.
type LibraryTagMatch struct {
	Tags []string `json:"tags"`
}

type AddSessionToLibraryRequest struct {
	SchemaVersion int                  `json:"schema_version"`
	RequestID     string               `json:"request_id"`
	ProjectID     string               `json:"project_id"`
	Source        SessionReadReference `json:"source"`
	Note          string               `json:"note,omitempty"`
}

type AddSessionToLibraryResponse struct {
	SchemaVersion int          `json:"schema_version"`
	Status        Status       `json:"status"`
	Entry         LibraryEntry `json:"entry"`
}

// State distinguishes a durable pending submission from completed collection.
// Only collected has an Entry. Retry keeps the same RequestID and selection.
type LibrarySubmissionResult struct {
	SchemaVersion int           `json:"schema_version"`
	Status        Status        `json:"status"`
	RequestID     string        `json:"request_id"`
	State         string        `json:"state"`
	Entry         *LibraryEntry `json:"entry,omitempty"`
	CapturedAt    string        `json:"captured_at,omitempty"`
	Retryable     bool          `json:"retryable"`
	Error         string        `json:"error,omitempty"`
}
