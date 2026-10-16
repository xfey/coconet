package contracts

import "errors"

var ErrRelayHistoryCompacted = errors.New("relay history was compacted; recover from the published checkpoint")
var ErrRelayReceiptMissing = errors.New("relay commit receipt not found")

// Compact acceptance evidence retains idempotency without retaining payloads.
type RelayCommitEvidence struct {
	SchemaVersion   int    `json:"schema_version"`
	ProjectID       string `json:"project_id"`
	CommitID        string `json:"commit_id"`
	Revision        int64  `json:"revision"`
	Writer          string `json:"writer"`
	AcceptedAt      string `json:"accepted_at"`
	PayloadSHA256   string `json:"payload_sha256"`
	StateSHA256     string `json:"state_sha256"`
	CanonicalSHA256 string `json:"canonical_sha256"`
}

const DeploymentCapabilityRelay = "opaque_relay_v2"

// Transport availability alone does not switch the user's workspace workflow.
const DeploymentCapabilityLocalDAG = "managed_local_dag_v1"

// Relay headers are transport metadata. Payload is an opaque versioned codec
// value; the current codec uses JSON.
type RelayCommit struct {
	Processing    *ProcessingAuthorization `json:"processing,omitempty"`
	SchemaVersion int                      `json:"schema_version"`
	ID            string                   `json:"id"`
	DeviceID      string                   `json:"device_id"`
	Base          int64                    `json:"base"`
	Codec         string                   `json:"codec"`
	Payload       []byte                   `json:"payload"`
	SHA256        string                   `json:"sha256"`
	Objects       []string                 `json:"objects"`
	StateSHA256   string                   `json:"state_sha256,omitempty"`
}
type RelayReceipt struct {
	SchemaVersion int         `json:"schema_version"`
	ProjectID     string      `json:"project_id"`
	Revision      int64       `json:"revision"`
	Writer        string      `json:"writer"`
	AcceptedAt    string      `json:"accepted_at"`
	Commit        RelayCommit `json:"commit"`
}
type RelaySyncHints struct {
	Policy         RelayContentPolicy `json:"policy"`
	RepairsPending bool               `json:"repairs_pending"`
}

type RelayPage struct {
	Hints         *RelaySyncHints `json:"hints,omitempty"`
	SchemaVersion int             `json:"schema_version"`
	Head          int64           `json:"head"`
	Entries       []RelayReceipt  `json:"entries"`
}

type RelayObject struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
type RelayObjectCheck struct {
	SchemaVersion int           `json:"schema_version"`
	Objects       []RelayObject `json:"objects"`
}
type RelayMissingObjects struct {
	SchemaVersion int      `json:"schema_version"`
	Missing       []string `json:"missing"`
}

func RelayWriter(account, device string) string { return account + "." + device }

// A checkpoint is a replaceable client-generated cache, anchored to an accepted
// commit. Relay checks bytes and the anchor, never the state inside Payload.
const MaxRelayCheckpointBytes = 16 << 20

type RelayCheckpoint struct {
	SchemaVersion int    `json:"schema_version"`
	DeviceID      string `json:"device_id"`
	Revision      int64  `json:"revision"`
	CommitID      string `json:"commit_id"`
	Codec         string `json:"codec"`
	SHA256        string `json:"sha256"`
	Payload       []byte `json:"payload"`
}

type RelayCheckpointReceipt struct {
	SchemaVersion int             `json:"schema_version"`
	ProjectID     string          `json:"project_id"`
	Writer        string          `json:"writer"`
	AcceptedAt    string          `json:"accepted_at"`
	Checkpoint    RelayCheckpoint `json:"checkpoint"`
	Anchor        RelayReceipt    `json:"anchor"`
}

type RelayCheckpointPage struct {
	Generation    int64                   `json:"generation"`
	SchemaVersion int                     `json:"schema_version"`
	Head          int64                   `json:"head"`
	Checkpoint    *RelayCheckpointReceipt `json:"checkpoint"`
}
