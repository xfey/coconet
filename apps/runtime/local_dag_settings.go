package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/localdag"
	"github.com/xfey/coconet/packages/replication"
	"github.com/xfey/coconet/packages/workgraph"
)

const localDAGWorkflow = "local-dag-v1"

// Bindings are the single authoritative workspace map. A successful atomic
// binding write activates the managed workflow; no copied runtime/relay/token
// files can diverge from it. Stores are isolated by deployment and account.
func localDAGBindingRoot(paths runtimePaths, binding contracts.LocalBinding) string {
	key := workgraph.Digest([]string{binding.DeploymentID, binding.ServerPublicKeySHA256, binding.AccountID})
	return filepath.Join(paths.root, "local-dag", "scopes", key)
}

func localDAGRootAt(cwd string) (string, error) {
	if os.Getenv("COCONET_LOCAL_DAG_HOME") != "" {
		return explicitLocalDAGRoot()
	}
	paths, err := resolveRuntimePaths()
	if err != nil {
		return "", err
	}
	binding, err := bindingForAnyCWD(paths, cwd)
	if err != nil {
		return "", err
	}
	if binding != nil && binding.Workflow == localDAGWorkflow {
		return localDAGBindingRoot(paths, *binding), nil
	}
	if binding != nil {
		return "", errors.New("this folder uses the retired server workflow; run `coconet connect` here to upgrade its existing association; subsequent conversation updates are processed with their source Agent")
	}
	return filepath.Join(paths.root, "local-dag"), nil
}

func localDAGScope(paths runtimePaths, root string) ([]contracts.LocalBinding, bool, error) {
	parent := filepath.Join(paths.root, "local-dag", "scopes")
	if filepath.Dir(root) != parent {
		return nil, false, nil
	}
	state, err := loadBindingState(paths)
	if err != nil {
		return nil, true, err
	}
	var bindings []contracts.LocalBinding
	for _, binding := range state.Bindings {
		if binding.Workflow == localDAGWorkflow && localDAGBindingRoot(paths, binding) == root {
			bindings = append(bindings, binding)
		}
	}
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].CollaborationRoot < bindings[j].CollaborationRoot })
	if len(bindings) == 0 {
		return nil, true, errors.New("this Runtime has no connected workspace; reconnect the folder")
	}
	// A scope is one pinned authority. Different URL aliases must be configured
	// consistently, rather than letting iteration order choose a destination.
	for _, binding := range bindings[1:] {
		if binding.LocalServerURL != bindings[0].LocalServerURL {
			return nil, true, errors.New("connected folders on this deployment use different Relay endpoints; reconnect with one endpoint")
		}
	}
	return bindings, true, nil
}

type localDAGIdentity struct {
	Schema int    `json:"schema"`
	Device string `json:"device"`
}

func loadLocalDAGIdentity(paths runtimePaths, create bool) (string, error) {
	path := filepath.Join(paths.root, "device-identity.json")
	if create {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		unlock, err := acquireLocalFileLock(ctx, path+".lock")
		if err != nil {
			return "", err
		}
		defer unlock()
	}
	data, err := readLocalPrivateFile(path, 4096, true)
	var identity localDAGIdentity
	if errors.Is(err, os.ErrNotExist) && create {
		entries, readErr := os.ReadDir(filepath.Join(paths.root, "local-dag", "scopes"))
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return "", readErr
		}
		if len(entries) > 0 {
			return "", errors.New("device identity is missing while local replicas exist; restore device-identity.json from backup")
		}
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return "", err
		}
		identity = localDAGIdentity{Schema: 1, Device: "device_" + hex.EncodeToString(id[:])}
		err = atomicWriteJSON(path, identity, 0600)
	} else if err == nil {
		err = decodeStrict(data, &identity)
	}
	if err != nil {
		return "", err
	}
	if identity.Schema != 1 || len(identity.Device) != len("device_")+32 || identity.Device[:7] != "device_" {
		return "", errors.New("invalid persistent device identity; do not reset it while pending work exists")
	}
	if _, err := hex.DecodeString(identity.Device[7:]); err != nil {
		return "", errors.New("invalid device identity")
	}
	return identity.Device, nil
}

// Only an optional processing pause is stored. Provider, authentication and
// model are inherited from each captured conversation's Agent installation.
type localDAGInferenceControl struct {
	Schema int  `json:"schema"`
	Paused bool `json:"paused"`
}

func loadInferenceControl(root string) (localDAGInferenceControl, error) {
	control := localDAGInferenceControl{Schema: 1}
	data, err := readLocalPrivateFile(filepath.Join(root, "inference-control.json"), 4096, true)
	if errors.Is(err, os.ErrNotExist) {
		return control, nil
	}
	if err != nil {
		return control, err
	}
	if err = decodeStrict(data, &control); err != nil {
		return control, err
	}
	if control.Schema != 1 {
		return control, errors.New("invalid local processing control")
	}
	return control, nil
}

func loadConnectedLocalDAG(root string) (localDAGServiceConfig, bool, error) {
	var config localDAGServiceConfig
	paths, err := resolveRuntimePaths()
	if err != nil {
		return config, true, err
	}
	bindings, managed, err := localDAGScope(paths, root)
	if err != nil || !managed {
		return config, managed, err
	}
	device, err := loadLocalDAGIdentity(paths, false)
	if err != nil {
		return config, true, err
	}
	config = localDAGServiceConfig{Schema: 1, Actor: contracts.RelayWriter(bindings[0].AccountID, device), Managed: true, Provider: "automatic", TimeoutSeconds: 120}
	for _, binding := range bindings {
		config.Workspaces = append(config.Workspaces, localDAGWorkspace{Path: binding.CollaborationRoot, ProjectID: binding.ProjectID})
	}
	control, err := loadInferenceControl(root)
	if err != nil {
		return config, true, err
	}
	if control.Paused {
		config.PauseReason = "Local processing is paused. Run coconet inference resume."
	}

	target, _ := deploymentTargetFromBinding(bindings[0])
	credentials, err := loadCredentialStateForWrite(paths)
	if err != nil {
		config.PauseReason = "Local credentials are unavailable. Repair sign-in with coconet login; captured sessions are retained."
		return config, true, nil
	}
	found := slices.ContainsFunc(credentials.Credentials, func(c storedCredential) bool {
		return c.DeploymentID == target.DeploymentID && c.ServerPublicKeySHA256 == target.ServerPublicKeySHA256 && c.Account.AccountID == bindings[0].AccountID && (c.TokenStorage == "keychain" || c.AccessToken != "")
	})
	if !found {
		config.PauseReason = "Sign in with coconet login to resume synchronization and processing."
	}
	return config, true, nil
}

func configureConnectedLocalDAG(root string, engine *localdag.Engine) (bool, error) {
	paths, err := resolveRuntimePaths()
	if err != nil {
		return true, err
	}
	bindings, managed, err := localDAGScope(paths, root)
	if err != nil || !managed {
		return managed, err
	}
	device, err := loadLocalDAGIdentity(paths, false)
	if err != nil {
		return true, err
	}
	binding := bindings[0]
	target, err := deploymentTargetFromBinding(binding)
	if err != nil {
		return true, err
	}
	var mu sync.Mutex
	var checkedToken string
	var checkedAt time.Time
	credential := func(ctx context.Context, project string) (string, error) {
		currentBindings, _, err := localDAGScope(paths, root)
		if err != nil {
			return "", err
		}
		if !slices.ContainsFunc(currentBindings, func(b contracts.LocalBinding) bool { return b.ProjectID == project }) {
			return "", errors.New("project is no longer connected on this device")
		}
		current, found, err := loadCredential(paths, target)
		if err != nil {
			return "", err
		}
		if !found || current.Account.AccountID != binding.AccountID {
			return "", errors.New("Relay sign-in required; run coconet login")
		}
		mu.Lock()
		defer mu.Unlock()
		if checkedToken != current.AccessToken || time.Since(checkedAt) > time.Minute {
			client, err := authenticatedClientForDeployment(ctx, target, current.AccessToken)
			if err != nil {
				return "", err
			}
			if !slices.Contains(client.capabilities, contracts.DeploymentCapabilityRelay) {
				return "", errors.New("server does not support Relay; upgrade the server")
			}
			checkedToken, checkedAt = current.AccessToken, time.Now()
		}
		return current.AccessToken, nil
	}
	httpClient, err := coconetHTTPClient(60 * time.Second)
	if err != nil {
		return true, err
	}
	remote := &replication.Client{Store: engine.Store, Remote: replication.HTTP{BaseURL: target.ServerURL, Credential: credential, Client: httpClient}, Account: binding.AccountID, Device: device}
	if engine.Actor != "" && engine.Actor != remote.Writer() {
		return true, errors.New("actor differs from persistent connected device identity")
	}
	identity := workgraph.Digest([]string{target.ServerURL, binding.AccountID, device})
	if err := engine.Store.BindMode("relay/" + identity); err != nil {
		return true, err
	}
	for _, binding := range bindings {
		if err := engine.Store.WatchProject(binding.ProjectID); err != nil {
			return true, err
		}
	}
	engine.Actor, engine.Publisher = remote.Writer(), remote
	return true, nil
}

func finishLocalDAGConnection(root string, stdout interface{ Write([]byte) (int, error) }) error {
	if os.Getenv("COCONET_DISABLE_BACKGROUND_SYNC") != "1" {
		if err := startManagedLocalDAG(root); err != nil {
			return err
		}
	}
	config, err := loadLocalDAGService(root)
	if err != nil {
		return err
	}
	if config.PauseReason != "" {
		fmt.Fprintln(stdout, config.PauseReason)
	}
	return nil
}

func localDAGIsConnectedRoot(root string) bool {
	paths, err := resolveRuntimePaths()
	return err == nil && filepath.Dir(root) == filepath.Join(paths.root, "local-dag", "scopes")
}
func localDAGActiveProjects(root string, store *graphstore.Store) ([]string, error) {
	paths, err := resolveRuntimePaths()
	if err != nil {
		return nil, err
	}
	bindings, managed, err := localDAGScope(paths, root)
	if err != nil {
		return nil, err
	}
	if !managed {
		return store.Projects()
	}
	projects := []string{}
	for _, binding := range bindings {
		projects = append(projects, binding.ProjectID)
	}
	sort.Strings(projects)
	return slices.Compact(projects), nil
}
func localDAGObservationAllowed(root string, request localDAGObservationRequest) (bool, error) {
	paths, err := resolveRuntimePaths()
	if err != nil {
		return false, err
	}
	bindings, _, err := localDAGScope(paths, root)
	if err != nil {
		return false, err
	}
	for _, binding := range bindings {
		if binding.ProjectID == request.ProjectID && samePath(binding.CollaborationRoot, request.Root) && pathContains(binding.CollaborationRoot, request.CWD) {
			return true, nil
		}
	}
	return false, nil
}
