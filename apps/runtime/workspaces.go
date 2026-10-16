package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/xfey/coconet/packages/contracts"
)

type workspaceReportState struct {
	InstallationID     string    `json:"installation_id"`
	Revision           int64     `json:"revision"`
	PayloadSHA256      string    `json:"payload_sha256"`
	AcknowledgedSHA256 string    `json:"acknowledged_sha256"`
	AcknowledgedAt     time.Time `json:"acknowledged_at"`
	NextAttemptAt      time.Time `json:"next_attempt_at"`
}

// The local binding file is authoritative. Replacing its snapshot also repairs
// an offline disconnect and backfills older clients' directories after upgrade.
func reportWorkspaceSnapshot(ctx context.Context, paths runtimePaths, target deploymentTarget, force bool, client *apiClient) (err error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	credential, found, err := loadCredential(paths, target)
	if err != nil || !found {
		return err
	}
	accountHash := sha256.Sum256([]byte(credential.Account.AccountID))
	path := filepath.Join(filepath.Dir(deploymentSyncPath(paths, target.DeploymentID)), "workspaces-"+hex.EncodeToString(accountHash[:8])+".json")
	release, err := acquireLocalFileLock(ctx, path+".lock")
	if err != nil {
		return err
	}
	defer release()
	bindings, err := loadBindingState(paths)
	if err != nil {
		return err
	}
	folders := []contracts.WorkspaceFolder{}
	for _, binding := range bindings.Bindings {
		if binding.DeploymentID == target.DeploymentID && binding.AccountID == credential.Account.AccountID {
			folders = append(folders, contracts.WorkspaceFolder{ProjectID: binding.ProjectID, Path: binding.CollaborationRoot, ConnectedAt: binding.ConnectedAt})
		}
	}
	sort.Slice(folders, func(i, j int) bool { return folders[i].Path < folders[j].Path })
	var state workspaceReportState
	content, err := os.ReadFile(path)
	if err == nil {
		err = decodeStrict(content, &state)
	}
	if errors.Is(err, os.ErrNotExist) {
		if len(folders) == 0 {
			return nil
		}
		id := make([]byte, 16)
		if _, err = rand.Read(id); err != nil {
			return err
		}
		state.InstallationID = "workspace_" + hex.EncodeToString(id)
	} else if err != nil {
		return err
	}
	identity, decodeErr := hex.DecodeString(strings.TrimPrefix(state.InstallationID, "workspace_"))
	if !strings.HasPrefix(state.InstallationID, "workspace_") || decodeErr != nil || len(identity) != 16 || state.Revision < 0 || state.Revision >= 9007199254740991 {
		return errors.New("local directory registration state is invalid")
	}
	if !force && time.Now().Before(state.NextAttemptAt) {
		return nil
	}
	defer func() {
		if err != nil {
			state.NextAttemptAt = time.Now().Add(time.Minute)
			_ = atomicWriteJSON(path, state, 0o600)
		}
	}()
	name, _ := os.Hostname()
	if name == "" {
		name = "Computer"
	}
	if len(name) > 120 {
		name = name[:120]
	}
	tokenHash := sha256.Sum256([]byte(credential.AccessToken))
	payload, err := json.Marshal(struct {
		Device     string
		Folders    []contracts.WorkspaceFolder
		Credential [32]byte
	}{name, folders, tokenHash})
	if err != nil {
		return err
	}
	digest := sha256.Sum256(payload)
	fingerprint := hex.EncodeToString(digest[:])
	if !force && state.AcknowledgedSHA256 == fingerprint && time.Since(state.AcknowledgedAt) < 24*time.Hour {
		return nil
	}
	if client == nil {
		client, err = authenticatedClientForDeployment(ctx, target, credential.AccessToken)
		if err != nil {
			return err
		}
	}
	if !slices.Contains(client.capabilities, contracts.DeploymentCapabilityWorkspaceRegistry) {
		return nil
	}
	if state.PayloadSHA256 != fingerprint {
		state.Revision++
		state.PayloadSHA256 = fingerprint
		if err := atomicWriteJSON(path, state, 0o600); err != nil {
			return err
		}
	}
	var response contracts.WorkspaceReportResponse
	err = client.do(ctx, http.MethodPut, "/v1/workspaces", contracts.WorkspaceReport{SchemaVersion: 1, InstallationID: state.InstallationID, DeviceName: name, Revision: state.Revision, Folders: folders}, &response, true)
	if err != nil {
		return err
	}
	if response.Status != contracts.StatusOK || response.SchemaVersion != 1 || response.Revision != state.Revision {
		return errors.New("server returned an invalid directory registration")
	}
	state.AcknowledgedSHA256, state.AcknowledgedAt = fingerprint, time.Now().UTC()
	state.NextAttemptAt = time.Time{}
	return atomicWriteJSON(path, state, 0o600)
}
