package contracts

// Leases protect a complete multi-object download at an accepted graph revision.
// Project membership, device scope and short expiry are transport concerns.
type RelayReadLeaseRequest struct {
	SchemaVersion int    `json:"schema_version"`
	DeviceID      string `json:"device_id"`
	Revision      int64  `json:"revision"`
	LeaseID       string `json:"lease_id,omitempty"`
}
type RelayReadLease struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id"`
	Revision      int64  `json:"revision"`
	ExpiresAt     string `json:"expires_at"`
}
type RelayCollectionPlan struct {
	SchemaVersion    int      `json:"schema_version"`
	ID               string   `json:"id"`
	DeviceID         string   `json:"device_id"`
	Revision         int64    `json:"revision"`
	CheckpointSHA256 string   `json:"checkpoint_sha256"`
	Objects          []string `json:"objects"`
}
type RelayCollection struct {
	SchemaVersion int                 `json:"schema_version"`
	Plan          RelayCollectionPlan `json:"plan"`
	Writer        string              `json:"writer"`
	Epoch         int64               `json:"epoch"`
	NotBefore     string              `json:"not_before"`
	Status        string              `json:"status"`
	Deleted       int                 `json:"deleted"`
	UpdatedAt     string              `json:"updated_at"`
}

// Catalog contains content metadata only; bodies remain explicitly fetched.
type RelayObjectPage struct {
	SchemaVersion int           `json:"schema_version"`
	Head          int64         `json:"head"`
	Objects       []RelayObject `json:"objects"`
	Next          string        `json:"next,omitempty"`
}
