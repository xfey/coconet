package main

type snapshotLease struct {
	ActorID         string `json:"actor_id"`
	ProjectID       string `json:"project_id"`
	ServerSessionID string `json:"server_session_id"`
	Version         int    `json:"version"`
	Purpose         string `json:"purpose"`
	ExpiresAt       string `json:"expires_at"`
}
