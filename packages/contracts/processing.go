package contracts

import "errors"

const DeploymentCapabilityProcessingTasks = "shared_processing_tasks_v1"
const MaxProcessingInputBytes = 1 << 20

var ErrProcessingBusy = errors.New("processing task is held by another attempt")
var ErrProcessingStale = errors.New("processing task or claim is no longer current")
var ErrProcessingYielded = errors.New("this device yielded after repeated failures; another device may continue, or retry explicitly")

var ErrProcessingPaused = errors.New("processing task exhausted its shared attempts; explicit retry required")

// ProcessingSpec identifies one immutable inference request. Payload is an
// opaque, bounded capsule, never an executable or local provider configuration.
type ProcessingSpec struct {
	Key          string `json:"key"`
	Kind         string `json:"kind"`
	Owner        string `json:"owner"`
	SessionID    string `json:"session_id"`
	Version      int    `json:"version"`
	NodeID       string `json:"node_id,omitempty"`
	NodeRevision int64  `json:"node_revision,omitempty"`
	SHA256       string `json:"sha256"`
	Payload      []byte `json:"payload,omitempty"`
}

type ProcessingTask struct {
	SchemaVersion int            `json:"schema_version"`
	ID            string         `json:"id"`
	ProjectID     string         `json:"project_id"`
	Spec          ProcessingSpec `json:"spec"`
	Sequence      int64          `json:"sequence"`
	Creator       string         `json:"creator"`
	CreatedAt     string         `json:"created_at"`
	ExpiresAt     string         `json:"expires_at"`
	Status        string         `json:"status"`
	Attempts      int            `json:"attempts"`
	AttemptedBy   []string       `json:"attempted_by,omitempty"`
	Claimant      string         `json:"claimant,omitempty"`
	Lease         string         `json:"lease,omitempty"`
	ClaimedUntil  string         `json:"claimed_until,omitempty"`
	RetryAfter    string         `json:"retry_after,omitempty"`
	Error         string         `json:"error,omitempty"`
	CommitID      string         `json:"commit_id,omitempty"`
}
type ProcessingRegister struct {
	SchemaVersion int            `json:"schema_version"`
	DeviceID      string         `json:"device_id"`
	Spec          ProcessingSpec `json:"spec"`
}
type ProcessingAction struct {
	SchemaVersion int    `json:"schema_version"`
	DeviceID      string `json:"device_id"`
	Operation     string `json:"operation"`
	Lease         string `json:"lease,omitempty"`
	Reason        string `json:"reason,omitempty"`
}
type ProcessingPage struct {
	SchemaVersion int              `json:"schema_version"`
	Tasks         []ProcessingTask `json:"tasks"`
	Next          int64            `json:"next,omitempty"`
}

// ProcessingAuthorization is copied into the opaque commit and its transport
// header. Relay validates it against the live task in the append transaction;
// replicas require the two copies to match before applying delegated semantics.
type ProcessingAuthorization struct {
	TaskID       string `json:"task_id"`
	Lease        string `json:"lease"`
	Owner        string `json:"owner"`
	SessionID    string `json:"session_id"`
	Version      int    `json:"version"`
	NodeID       string `json:"node_id,omitempty"`
	NodeRevision int64  `json:"node_revision,omitempty"`
	SHA256       string `json:"sha256"`
}

func (t ProcessingTask) Authorization() ProcessingAuthorization {
	return ProcessingAuthorization{TaskID: t.ID, Lease: t.Lease, Owner: t.Spec.Owner, SessionID: t.Spec.SessionID, Version: t.Spec.Version, NodeID: t.Spec.NodeID, NodeRevision: t.Spec.NodeRevision, SHA256: t.Spec.SHA256}
}
