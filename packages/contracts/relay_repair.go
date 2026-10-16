package contracts

// Repair metadata never contains Session text or transfer capabilities.
type RelayRepair struct {
	SHA256       string `json:"sha256"`
	ExpiresAt    string `json:"expires_at"`
	ClaimedUntil string `json:"claimed_until,omitempty"`
	Claimant     string `json:"claimant,omitempty"`
}
type RelayRepairPage struct {
	SchemaVersion int           `json:"schema_version"`
	Requests      []RelayRepair `json:"requests"`
	Next          string        `json:"next,omitempty"`
}
type RelayRepairRequest struct {
	SchemaVersion int    `json:"schema_version"`
	SHA256        string `json:"sha256"`
	DeviceID      string `json:"device_id,omitempty"`
}
