package contracts

const DeploymentCapabilityWorkspaceRegistry = "workspace_registry_v1"

// A report replaces one installation's directory snapshot after local binding
// changes. Revision orders retries; it is independent of device credentials.
type WorkspaceFolder struct {
	ProjectID   string `json:"project_id"`
	Path        string `json:"path"`
	ConnectedAt string `json:"connected_at"`
}

type WorkspaceReport struct {
	SchemaVersion  int               `json:"schema_version"`
	InstallationID string            `json:"installation_id"`
	DeviceName     string            `json:"device_name"`
	Revision       int64             `json:"revision"`
	Folders        []WorkspaceFolder `json:"folders"`
}

type WorkspaceReportResponse struct {
	SchemaVersion int    `json:"schema_version"`
	Status        Status `json:"status"`
	Revision      int64  `json:"revision"`
}

type ProjectFolder struct {
	ID          string `json:"id"`
	AccountID   string `json:"account_id"`
	DisplayName string `json:"display_name"`
	DeviceName  string `json:"device_name"`
	Path        string `json:"path"`
	ConnectedAt string `json:"connected_at"`
	ReportedAt  string `json:"reported_at"`
}

type ProjectFoldersResponse struct {
	SchemaVersion int             `json:"schema_version"`
	Status        Status          `json:"status"`
	Folders       []ProjectFolder `json:"folders"`
	NextCursor    string          `json:"next_cursor,omitempty"`
}
