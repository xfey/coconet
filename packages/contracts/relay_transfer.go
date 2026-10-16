package contracts

type RelayContentPolicy struct {
	SchemaVersion int   `json:"schema_version"`
	Direct        bool  `json:"direct"`
	Temporary     bool  `json:"temporary"`
	TTLSeconds    int64 `json:"ttl_seconds"`
}

// Transfer URLs are short-lived capabilities. They never enter graph commits,
// processing archives or durable client logs.
type RelayTransferRequest struct {
	SchemaVersion int    `json:"schema_version"`
	SHA256        string `json:"sha256"`
	Size          int64  `json:"size"`
	UploadID      string `json:"upload_id,omitempty"`
}

type RelayTransferTarget struct {
	SchemaVersion int    `json:"schema_version"`
	SHA256        string `json:"sha256"`
	Size          int64  `json:"size"`
	Proxy         bool   `json:"proxy,omitempty"`
	Ready         bool   `json:"ready,omitempty"`
	UploadID      string `json:"upload_id,omitempty"`
	URL           string `json:"url,omitempty"`
	ExpiresAt     string `json:"expires_at,omitempty"`
}
