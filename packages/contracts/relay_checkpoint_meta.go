package contracts

// The JSON envelope base64-encodes the 16 MiB state; proxies must allow 24 MiB.
const MaxRelayCheckpointEnvelopeBytes = 24 << 20

type RelayCheckpointAck struct {
	SchemaVersion int    `json:"schema_version"`
	ProjectID     string `json:"project_id"`
	Revision      int64  `json:"revision"`
	CommitID      string `json:"commit_id"`
	SHA256        string `json:"sha256"`
}

func (r RelayCheckpointReceipt) Acknowledgement() RelayCheckpointAck {
	return RelayCheckpointAck{SchemaVersion: 1, ProjectID: r.ProjectID, Revision: r.Checkpoint.Revision, CommitID: r.Checkpoint.CommitID, SHA256: r.Checkpoint.SHA256}
}

type RelayCheckpointHead struct {
	SchemaVersion int                 `json:"schema_version"`
	ProjectID     string              `json:"project_id"`
	Generation    int64               `json:"generation"`
	Head          int64               `json:"head"`
	Checkpoint    *RelayCheckpointAck `json:"checkpoint"`
}
