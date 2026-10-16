package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/xfey/coconet/packages/contracts"
)

const keychainService = "dev.coconet.runtime"

// Stored bindings are opaque references: keep them readable for disconnect
// after a project namespace is retired. New connections use ProjectUID's
// canonical coco_ validation; reading a binding never creates membership.
var projectIDPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{11,127}$`)

type runtimePaths struct {
	root            string
	credentialPath  string
	bindingPath     string
	operationPath   string
	integrationPath string
	explicitHome    bool
}

type storedCredential struct {
	SchemaVersion         int               `json:"schema_version"`
	DeploymentID          string            `json:"deployment_id"`
	ServerURL             string            `json:"server_url"`
	ServerPublicKeySHA256 string            `json:"server_public_key_sha256"`
	Account               contracts.Account `json:"account"`
	AccessToken           string            `json:"access_token,omitempty"`
	TokenStorage          string            `json:"token_storage"`
}

type credentialState struct {
	SchemaVersion int                `json:"schema_version"`
	Credentials   []storedCredential `json:"credentials"`
}

type pendingInitOperation struct {
	CollaborationRoot string `json:"collaboration_root"`
	DeploymentID      string `json:"deployment_id"`
	AccountID         string `json:"account_id"`
	RequestID         string `json:"request_id"`
}

type pendingOperationState struct {
	SchemaVersion int                    `json:"schema_version"`
	Initializes   []pendingInitOperation `json:"initializes"`
}

func resolveRuntimePaths() (runtimePaths, error) {
	root := os.Getenv("COCONET_HOME")
	explicit := root != ""
	if root == "" {
		configRoot, err := os.UserConfigDir()
		if err != nil {
			return runtimePaths{}, fmt.Errorf("resolve user config directory: %w", err)
		}
		root = filepath.Join(configRoot, "coconet")
	}
	canonical, err := filepath.Abs(root)
	if err != nil {
		return runtimePaths{}, err
	}
	return runtimePaths{
		root:            filepath.Clean(canonical),
		credentialPath:  filepath.Join(canonical, "credentials.json"),
		bindingPath:     filepath.Join(canonical, "bindings.json"),
		operationPath:   filepath.Join(canonical, "operations.json"),
		integrationPath: filepath.Join(canonical, "agent-integrations.json"),
		explicitHome:    explicit,
	}, nil
}

func loadCredential(paths runtimePaths, target deploymentTarget) (storedCredential, bool, error) {
	if err := validateDeploymentTarget(target); err != nil {
		return storedCredential{}, false, err
	}
	content, err := os.ReadFile(paths.credentialPath)
	if errors.Is(err, os.ErrNotExist) {
		return storedCredential{}, false, nil
	}
	if err != nil {
		return storedCredential{}, false, fmt.Errorf("read local credential: %w", err)
	}
	if err := os.Chmod(paths.credentialPath, 0o600); err != nil {
		return storedCredential{}, false, fmt.Errorf("secure local credential: %w", err)
	}
	state, err := decodeCredentialState(content)
	if err != nil {
		return storedCredential{}, false, err
	}
	for _, credential := range state.Credentials {
		if credential.DeploymentID != target.DeploymentID {
			continue
		}
		if credential.ServerPublicKeySHA256 != target.ServerPublicKeySHA256 {
			return storedCredential{}, false, errors.New("stored Deployment public key differs from the connected target")
		}
		return hydrateCredential(credential)
	}
	return storedCredential{}, false, nil
}

func saveCredential(paths runtimePaths, target deploymentTarget, account contracts.Account, accessToken string) error {
	if accessToken == "" {
		return errors.New("access token is empty")
	}
	if err := validateDeploymentTarget(target); err != nil {
		return err
	}
	credential := storedCredential{
		SchemaVersion:         contracts.LocalCredentialSchemaVersion,
		DeploymentID:          target.DeploymentID,
		ServerURL:             target.ServerURL,
		ServerPublicKeySHA256: target.ServerPublicKeySHA256,
		Account:               account,
		AccessToken:           accessToken,
		TokenStorage:          "file",
	}
	state, err := loadCredentialStateForWrite(paths)
	if err != nil {
		return err
	}
	if runtime.GOOS == "darwin" && !paths.explicitHome {
		credential.TokenStorage = "keychain"
		credential.AccessToken = ""
		if err := writeKeychainToken(keychainAccount(credential), accessToken); err != nil {
			return fmt.Errorf("write macOS Keychain credential: %w", err)
		}
	}
	updated := make([]storedCredential, 0, len(state.Credentials)+1)
	for _, existing := range state.Credentials {
		if existing.DeploymentID == target.DeploymentID {
			continue
		}
		updated = append(updated, existing)
	}
	state.Credentials = append(updated, credential)
	return atomicWriteJSON(paths.credentialPath, state, 0o600)
}

func keychainAccount(credential storedCredential) string {
	return credential.Account.AccountID + "@" + credential.DeploymentID
}

func hydrateCredential(credential storedCredential) (storedCredential, bool, error) {
	if credential.SchemaVersion != contracts.LocalCredentialSchemaVersion || credential.Account.AccountID == "" {
		return storedCredential{}, false, errors.New("stored Deployment credential is invalid")
	}
	if credential.TokenStorage == "keychain" {
		token, err := readKeychainToken(keychainAccount(credential))
		if err != nil {
			return storedCredential{}, false, fmt.Errorf("read macOS Keychain credential: %w", err)
		}
		credential.AccessToken = token
	}
	if credential.AccessToken == "" {
		return storedCredential{}, false, nil
	}
	return credential, true, nil
}

func decodeCredentialState(content []byte) (credentialState, error) {
	var state credentialState
	if err := decodeStrict(content, &state); err != nil {
		return credentialState{}, fmt.Errorf("decode local credentials: %w", err)
	}
	if state.SchemaVersion != contracts.LocalCredentialSchemaVersion {
		return credentialState{}, errors.New("unsupported local credential schema version")
	}
	if state.Credentials == nil {
		state.Credentials = []storedCredential{}
	}
	seen := map[string]bool{}
	for _, credential := range state.Credentials {
		target := deploymentTarget{DeploymentID: credential.DeploymentID, ServerURL: credential.ServerURL, ServerPublicKeySHA256: credential.ServerPublicKeySHA256}
		if credential.SchemaVersion != contracts.LocalCredentialSchemaVersion || credential.Account.AccountID == "" || seen[credential.DeploymentID] || validateDeploymentTarget(target) != nil {
			return credentialState{}, errors.New("local credential state contains an invalid or duplicate Deployment")
		}
		seen[credential.DeploymentID] = true
	}
	return state, nil
}

func loadCredentialStateForWrite(paths runtimePaths) (credentialState, error) {
	content, err := os.ReadFile(paths.credentialPath)
	if errors.Is(err, os.ErrNotExist) {
		return credentialState{SchemaVersion: contracts.LocalCredentialSchemaVersion, Credentials: []storedCredential{}}, nil
	}
	if err != nil {
		return credentialState{}, err
	}
	return decodeCredentialState(content)
}

func loadCredentialTargets(paths runtimePaths) ([]deploymentTarget, error) {
	content, err := os.ReadFile(paths.credentialPath)
	if errors.Is(err, os.ErrNotExist) {
		return []deploymentTarget{}, nil
	}
	if err != nil {
		return nil, err
	}
	state, err := decodeCredentialState(content)
	if err != nil {
		return nil, err
	}
	targets := make([]deploymentTarget, 0, len(state.Credentials))
	for _, credential := range state.Credentials {
		targets = append(targets, deploymentTarget{DeploymentID: credential.DeploymentID, ServerURL: credential.ServerURL, ServerPublicKeySHA256: credential.ServerPublicKeySHA256})
	}
	return targets, nil
}

func readKeychainToken(account string) (string, error) {
	output, err := exec.Command("security", "find-generic-password", "-s", keychainService, "-a", account, "-w").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func writeKeychainToken(account, token string) error {
	command := exec.Command("security", "add-generic-password", "-U", "-s", keychainService, "-a", account, "-w", token)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func loadBindingState(paths runtimePaths) (contracts.LocalBindingState, error) {
	content, err := os.ReadFile(paths.bindingPath)
	if errors.Is(err, os.ErrNotExist) {
		return contracts.LocalBindingState{SchemaVersion: contracts.LocalBindingSchemaVersion, Bindings: []contracts.LocalBinding{}}, nil
	}
	if err != nil {
		return contracts.LocalBindingState{}, fmt.Errorf("read local bindings: %w", err)
	}
	var state contracts.LocalBindingState
	if err := decodeStrict(content, &state); err != nil {
		return contracts.LocalBindingState{}, fmt.Errorf("decode local bindings: %w", err)
	}
	if state.SchemaVersion != contracts.LocalBindingSchemaVersion {
		return contracts.LocalBindingState{}, errors.New("unsupported local binding schema version")
	}
	if state.Bindings == nil {
		state.Bindings = []contracts.LocalBinding{}
	}
	for _, binding := range state.Bindings {
		if err := validateLocalBinding(binding); err != nil {
			return contracts.LocalBindingState{}, err
		}
	}
	return state, nil
}

func loadBindingStateForWrite(paths runtimePaths) (contracts.LocalBindingState, error) {
	return loadBindingState(paths)
}

func saveBinding(paths runtimePaths, binding contracts.LocalBinding) error {
	unlock, err := lockLocalBindings(paths)
	if err != nil {
		return err
	}
	defer unlock()
	state, err := loadBindingStateForWrite(paths)
	if err != nil {
		return err
	}
	state, err = mergeLocalBinding(state, binding)
	if err != nil {
		return err
	}
	return atomicWriteJSON(paths.bindingPath, state, 0o600)
}

func mergeLocalBinding(state contracts.LocalBindingState, binding contracts.LocalBinding) (contracts.LocalBindingState, error) {
	if err := validateLocalBinding(binding); err != nil {
		return contracts.LocalBindingState{}, errors.New("local binding is incomplete")
	}
	canonicalRoot, err := canonicalPath(binding.CollaborationRoot)
	if err != nil {
		return contracts.LocalBindingState{}, fmt.Errorf("canonicalize collaboration root: %w", err)
	}
	binding.CollaborationRoot = canonicalRoot
	updated := make([]contracts.LocalBinding, 0, len(state.Bindings)+1)
	for _, existing := range state.Bindings {
		if samePath(existing.CollaborationRoot, binding.CollaborationRoot) {
			if existing.DeploymentID != binding.DeploymentID || existing.ProjectID != binding.ProjectID || existing.AccountID != binding.AccountID {
				return contracts.LocalBindingState{}, errors.New("collaboration root is already bound to another Coconet Project or Account")
			}
			continue
		}
		if existing.DeploymentID == binding.DeploymentID && existing.ProjectID == binding.ProjectID && existing.AccountID == binding.AccountID {
			if _, statErr := os.Stat(existing.CollaborationRoot); errors.Is(statErr, os.ErrNotExist) {
				continue
			}
		}
		updated = append(updated, existing)
	}
	state.Bindings = append(updated, binding)
	return state, nil
}

func validateLocalBinding(binding contracts.LocalBinding) error {
	if binding.Workflow != "" && binding.Workflow != "local-dag-v1" {
		return errors.New("unsupported local workspace workflow; upgrade Coconet")
	}
	if binding.SchemaVersion != contracts.LocalBindingSchemaVersion || binding.AccountID == "" || !projectIDPattern.MatchString(binding.ProjectID) {
		return errors.New("local binding is incomplete")
	}
	if _, err := time.Parse(time.RFC3339Nano, binding.ConnectedAt); err != nil {
		return errors.New("local binding connected_at is invalid")
	}
	target := deploymentTarget{
		DeploymentID: binding.DeploymentID, ServerURL: binding.LocalServerURL,
		ServerPublicKeySHA256: binding.ServerPublicKeySHA256,
	}
	if validateDeploymentTarget(target) != nil {
		return errors.New("local binding Deployment target is invalid")
	}
	if !filepath.IsAbs(binding.CollaborationRoot) || filepath.Clean(binding.CollaborationRoot) != binding.CollaborationRoot {
		return errors.New("local binding collaboration root is not canonical")
	}
	return nil
}

func bindingAtRoot(paths runtimePaths, root string) (*contracts.LocalBinding, error) {
	canonical, err := canonicalPath(root)
	if err != nil {
		return nil, err
	}
	state, err := loadBindingState(paths)
	if err != nil {
		return nil, err
	}
	for index := range state.Bindings {
		if samePath(state.Bindings[index].CollaborationRoot, canonical) {
			binding := state.Bindings[index]
			return &binding, nil
		}
	}
	return nil, nil
}

func removeBindingAtRoot(paths runtimePaths, root string) (*contracts.LocalBinding, error) {
	unlock, err := lockLocalBindings(paths)
	if err != nil {
		return nil, err
	}
	defer unlock()
	canonical, err := canonicalPath(root)
	if err != nil {
		return nil, err
	}
	state, err := loadBindingState(paths)
	if err != nil {
		return nil, err
	}
	updated := make([]contracts.LocalBinding, 0, len(state.Bindings))
	var removed *contracts.LocalBinding
	for _, existing := range state.Bindings {
		if samePath(existing.CollaborationRoot, canonical) {
			copy := existing
			removed = &copy
			continue
		}
		updated = append(updated, existing)
	}
	if removed == nil {
		return nil, nil
	}
	state.Bindings = updated
	if err := atomicWriteJSON(paths.bindingPath, state, 0o600); err != nil {
		return nil, err
	}
	return removed, nil
}

func bindingForAnyCWD(paths runtimePaths, cwd string) (*contracts.LocalBinding, error) {
	canonical, err := canonicalPath(cwd)
	if err != nil {
		return nil, err
	}
	state, err := loadBindingState(paths)
	if err != nil {
		return nil, err
	}
	var selected *contracts.LocalBinding
	for index := range state.Bindings {
		binding := &state.Bindings[index]
		if !pathContains(binding.CollaborationRoot, canonical) {
			continue
		}
		if selected == nil || len(binding.CollaborationRoot) > len(selected.CollaborationRoot) {
			copy := *binding
			selected = &copy
		}
	}
	return selected, nil
}

func pendingInitRequestID(paths runtimePaths, root, deploymentID, accountID string) (string, error) {
	canonical, err := canonicalPath(root)
	if err != nil {
		return "", err
	}
	state, err := loadPendingOperations(paths)
	if err != nil {
		return "", err
	}
	for _, operation := range state.Initializes {
		if samePath(operation.CollaborationRoot, canonical) && operation.DeploymentID == deploymentID && operation.AccountID == accountID {
			return operation.RequestID, nil
		}
	}
	requestID := newRequestID()
	state.Initializes = append(state.Initializes, pendingInitOperation{
		CollaborationRoot: canonical, DeploymentID: deploymentID, AccountID: accountID, RequestID: requestID,
	})
	if err := atomicWriteJSON(paths.operationPath, state, 0o600); err != nil {
		return "", err
	}
	return requestID, nil
}

func clearPendingInit(paths runtimePaths, root, deploymentID, accountID string) error {
	canonical, err := canonicalPath(root)
	if err != nil {
		return err
	}
	state, err := loadPendingOperations(paths)
	if err != nil {
		return err
	}
	updated := make([]pendingInitOperation, 0, len(state.Initializes))
	changed := false
	for _, operation := range state.Initializes {
		if samePath(operation.CollaborationRoot, canonical) && operation.DeploymentID == deploymentID && operation.AccountID == accountID {
			changed = true
			continue
		}
		updated = append(updated, operation)
	}
	if !changed {
		return nil
	}
	state.Initializes = updated
	return atomicWriteJSON(paths.operationPath, state, 0o600)
}

func loadPendingOperations(paths runtimePaths) (pendingOperationState, error) {
	content, err := os.ReadFile(paths.operationPath)
	if errors.Is(err, os.ErrNotExist) {
		return pendingOperationState{SchemaVersion: 1, Initializes: []pendingInitOperation{}}, nil
	}
	if err != nil {
		return pendingOperationState{}, err
	}
	var state pendingOperationState
	if err := decodeStrict(content, &state); err != nil || state.SchemaVersion != 1 {
		if err == nil {
			err = errors.New("unsupported pending operation schema version")
		}
		return pendingOperationState{}, err
	}
	if state.Initializes == nil {
		state.Initializes = []pendingInitOperation{}
	}
	for _, operation := range state.Initializes {
		if !filepath.IsAbs(operation.CollaborationRoot) || operation.DeploymentID == "" || operation.AccountID == "" || len(operation.RequestID) < 8 {
			return pendingOperationState{}, errors.New("pending init operation is incomplete")
		}
	}
	return state, nil
}

func deploymentSyncPath(paths runtimePaths, deploymentID string) string {
	return filepath.Join(paths.root, "deployments", deploymentID, "runtime.db")
}

func atomicWriteJSON(path string, value any, mode os.FileMode) error {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".coconet-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
	return syncLocalDirectory(filepath.Dir(path))
}

func decodeStrict(content []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("expected one JSON value")
	}
	return nil
}

func samePath(left, right string) bool {
	if resolvedLeft, leftErr := canonicalPath(left); leftErr == nil {
		if resolvedRight, rightErr := canonicalPath(right); rightErr == nil {
			left = resolvedLeft
			right = resolvedRight
		}
	}
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func pathContains(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func lockLocalBindings(paths runtimePaths) (func() error, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return acquireLocalFileLock(ctx, paths.bindingPath+".lock")
}
