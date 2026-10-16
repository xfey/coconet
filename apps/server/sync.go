package main

import (
	"fmt"
	"net/http"

	"github.com/xfey/coconet/packages/contracts"
)

const maxSessionsPerProject = 100000

func unavailable(code, message string) error {
	return &authorityError{HTTPStatus: http.StatusServiceUnavailable, Status: contracts.StatusUnavailable, Code: code, Message: message, Retryable: true}
}

func conflict(code, message string) error {
	return &authorityError{HTTPStatus: http.StatusConflict, Status: contracts.StatusInvalidRequest, Code: code, Message: message}
}

func integrity(code, message string) error {
	return &authorityError{HTTPStatus: http.StatusUnprocessableEntity, Status: contracts.StatusIntegrityError, Code: code, Message: message}
}

// Only the latest verification per Session is retained. Hash states are created
// from verified original bytes, never supplied by the Client. Object keys and
// both content hashes fence reuse; Pull independently verifies stored bytes.
type artifactVerification struct {
	Keys   []string `json:"keys"`
	States [][]byte `json:"states"`
}

func requireActiveMember(state *databaseState, actorID, projectID string) (contracts.Project, contracts.Membership, error) {
	project, ok := state.Projects.Lookup(projectID)
	if !ok {
		return contracts.Project{}, contracts.Membership{}, notFound("project_not_found", "project was not found")
	}
	if project.Status != contracts.ProjectActive {
		return contracts.Project{}, contracts.Membership{}, &authorityError{HTTPStatus: http.StatusConflict, Status: contracts.StatusProjectArchived, Code: "project_archived", Message: "archived projects cannot accept Session updates"}
	}
	membership, ok := state.Memberships.Lookup(membershipKey(projectID, actorID))
	if !ok || membership.Status != contracts.MembershipActive {
		return contracts.Project{}, contracts.Membership{}, forbidden("membership_required", "active project membership is required")
	}
	return project, membership, nil
}

func artifactChunkKey(projectID, hash string) string { return projectID + ":" + hash }

func versionKey(serverSessionID string, version int) string {
	return fmt.Sprintf("%s:%d", serverSessionID, version)
}

func artifactStoredChunkKey(project string, chunk contracts.ArtifactChunk) string {
	key := artifactChunkKey(project, chunk.SHA256)
	if chunk.Encoding != "" {
		key += ":" + chunk.Encoding + ":" + chunk.StoredSHA256
	}
	return key
}
