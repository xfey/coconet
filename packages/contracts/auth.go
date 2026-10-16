package contracts

const DeploymentCapabilityAccountLogin = "account_login_v2"
const DeploymentCapabilityProjectSetup = "project_setup_v2"
const DeploymentCapabilityDirectorySetup = "directory_setup_v2"

// ConnectionDirectory is a CLI-verified folder on the device requesting access.
// CWD is the chosen folder; Path is the effective collaboration root.
type ConnectionDirectory struct {
	CWD       string `json:"cwd"`
	Path      string `json:"path"`
	Remote    bool   `json:"remote"`
	CanBrowse bool   `json:"can_browse"`
	Revision  int    `json:"revision"`
}

type AuthConfiguration struct {
	SchemaVersion int    `json:"schema_version"`
	Provider      string `json:"provider"`
	Label         string `json:"label"`
}

type DeviceAuthorizationRequest struct {
	DeviceName         string               `json:"device_name"`
	CodeChallenge      string               `json:"code_challenge"`
	CallbackURL        string               `json:"callback_url,omitempty"`
	ConnectProject     bool                 `json:"connect_project,omitempty"`
	ProjectName        string               `json:"project_name,omitempty"`
	ExpectedAccountID  string               `json:"expected_account_id,omitempty"`
	SuggestedProjectID string               `json:"suggested_project_id,omitempty"`
	Directory          *ConnectionDirectory `json:"directory,omitempty"`
}

type DeviceAuthorization struct {
	Status             Status               `json:"status"`
	DeviceCode         string               `json:"device_code,omitempty"`
	AuthorizationID    string               `json:"authorization_id,omitempty"`
	VerificationURI    string               `json:"verification_uri,omitempty"`
	ExpiresIn          int                  `json:"expires_in,omitempty"`
	Interval           int                  `json:"interval,omitempty"`
	AccessToken        string               `json:"access_token,omitempty"`
	Account            *Account             `json:"account,omitempty"`
	ProjectID          string               `json:"project_id,omitempty"`
	Directory          *ConnectionDirectory `json:"directory,omitempty"`
	DirectoryRequestID string               `json:"directory_request_id,omitempty"`
}

type DeviceAuthorizationPoll struct {
	DeviceCode   string `json:"device_code"`
	CodeVerifier string `json:"code_verifier"`
}

type DeviceAuthorizationDecision struct {
	AuthorizationID   string `json:"authorization_id"`
	Approve           *bool  `json:"approve,omitempty"`
	ProjectID         string `json:"project_id,omitempty"`
	ChooseDirectory   bool   `json:"choose_directory,omitempty"`
	DirectoryRevision int    `json:"directory_revision,omitempty"`
}

type DeviceAuthorizationPreview struct {
	DeviceName         string               `json:"device_name"`
	CallbackURL        string               `json:"callback_url,omitempty"`
	ConnectProject     bool                 `json:"connect_project,omitempty"`
	ProjectName        string               `json:"project_name,omitempty"`
	SuggestedProjectID string               `json:"suggested_project_id,omitempty"`
	Directory          *ConnectionDirectory `json:"directory,omitempty"`
	DirectoryPending   bool                 `json:"directory_pending,omitempty"`
	DirectoryError     string               `json:"directory_error,omitempty"`
}

// Only the CLI holding the PKCE verifier can complete a folder chooser request.
type DeviceDirectoryUpdate struct {
	DeviceAuthorizationPoll
	RequestID string `json:"request_id"`
	CWD       string `json:"cwd,omitempty"`
	Path      string `json:"path,omitempty"`
	Error     string `json:"error,omitempty"`
}

type AccountDevice struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
	ExpiresAt string `json:"expires_at"`
}

type AccountDevices struct {
	Devices []AccountDevice `json:"devices"`
}

type AccountProfileUpdate struct {
	DisplayName string `json:"display_name"`
}
