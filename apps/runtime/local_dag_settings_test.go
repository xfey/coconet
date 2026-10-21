package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/localdag"
	"github.com/xfey/coconet/packages/replication"
)

func relayConfigurationFixture(t *testing.T) (runtimePaths, deploymentTarget, *fakeCoconetServer) {
	t.Helper()
	remote := newFakeCoconetServer()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/deployment" {
			recorder := httptest.NewRecorder()
			serveFixtureDeployment(recorder, r)
			var info contracts.DeploymentInfo
			json.Unmarshal(recorder.Body.Bytes(), &info)
			slices.Sort(info.Capabilities)
			info.CapabilitiesSignature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(fixtureDeploymentPrivateKey, contracts.DeploymentCapabilitiesSigningPayload(info)))
			json.NewEncoder(w).Encode(info)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/relay/commits") {
			json.NewEncoder(w).Encode(contracts.RelayPage{SchemaVersion: 1, Entries: []contracts.RelayReceipt{}})
			return
		}
		remote.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	remote.advertisedEndpoint = server.URL
	configureRuntimeTest(t, t.TempDir(), t.TempDir(), server.URL)
	t.Setenv("COCONET_LOCAL_DAG_HOME", "")
	if err := os.MkdirAll(os.Getenv("CODEX_HOME"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(os.Getenv("CLAUDE_CONFIG_DIR"), 0700); err != nil {
		t.Fatal(err)
	}
	paths, _ := resolveRuntimePaths()
	target, err := discoverDeployment(context.Background(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return paths, target, remote
}

func TestRelayConnectUsesAtomicBindingWithoutHistoryImport(t *testing.T) {
	paths, target, remote := relayConfigurationFixture(t)
	var output, stderr bytes.Buffer
	if code := runConnect(context.Background(), []string{"--new"}, strings.NewReader(""), &output, &stderr); code != 0 {
		t.Fatalf("connect %d: %s", code, &stderr)
	}
	binding, err := bindingAtRoot(paths, currentDirectory())
	if err != nil || binding == nil || binding.Workflow != localDAGWorkflow {
		t.Fatalf("binding %+v %v", binding, err)
	}
	root, err := managedLocalDAGRoot()
	if err != nil || root != localDAGBindingRoot(paths, *binding) {
		t.Fatal(root, err)
	}
	config, err := loadLocalDAGService(root)
	if err != nil || config.PauseReason != "" || config.Provider != "automatic" || config.MaxCalls != 0 || len(config.Workspaces) != 1 || !config.Managed {
		t.Fatalf("config %+v %v", config, err)
	}
	identity := config.Actor
	for _, name := range []string{"runtime.json", "relay.json", "relay-token", "graph.db"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("unexpected %s: %v", name, err)
		}
	}
	if _, err := os.Stat(deploymentSyncPath(paths, target.DeploymentID)); !os.IsNotExist(err) {
		t.Fatalf("history queue was created: %v", err)
	}
	output.Reset()
	if code := runConnect(context.Background(), nil, strings.NewReader(""), &output, &stderr); code != 0 {
		t.Fatal(code, stderr.String())
	}
	next, err := loadLocalDAGService(root)
	if err != nil || next.Actor != identity || remote.createTransactions != 1 {
		t.Fatal(next, err)
	}
	// A different project in the same workspace after an explicit disconnect
	// keeps the writer identity, but never overwrites the previous project.
	if _, err := removeBindingAtRoot(paths, binding.CollaborationRoot); err != nil {
		t.Fatal(err)
	}
	if err := persistBinding(paths, binding.CollaborationRoot, target, binding.AccountID, "coco_reconnected1", ""); err != nil {
		t.Fatal(err)
	}
	next, err = loadLocalDAGService(root)
	if err != nil || next.Actor != identity || next.Workspaces[0].ProjectID != "coco_reconnected1" {
		t.Fatal(next, err)
	}
}

func TestManagedDeviceIdentityAndConcurrentWorkspaceWrites(t *testing.T) {
	paths, target, remote := relayConfigurationFixture(t)
	var wg sync.WaitGroup
	ids := make(chan string, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := loadLocalDAGIdentity(paths, true)
			if err != nil {
				t.Error(err)
			}
			ids <- id
		}()
	}
	wg.Wait()
	close(ids)
	id := ""
	for next := range ids {
		if id != "" && next != id {
			t.Fatal("writer changed")
		}
		id = next
	}
	for i := 0; i < 12; i++ {
		dir := t.TempDir()
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := persistBinding(paths, dir, target, remote.account.AccountID, remote.project.ProjectID, ""); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	state, err := loadBindingState(paths)
	if err != nil || len(state.Bindings) != 12 {
		t.Fatal(len(state.Bindings), err)
	}
	a := localDAGBindingRoot(paths, state.Bindings[0])
	other := state.Bindings[0]
	other.AccountID = "acct_second01"
	if a == localDAGBindingRoot(paths, other) {
		t.Fatal("accounts share a database")
	}
	other = state.Bindings[0]
	other.DeploymentID = "dpl_second01"
	if a == localDAGBindingRoot(paths, other) {
		t.Fatal("servers share a database")
	}
	if err := os.WriteFile(filepath.Join(paths.root, "device-identity.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadLocalDAGIdentity(paths, true); err == nil {
		t.Fatal("corruption silently replaced writer")
	}
}

func TestManagedRelayRefreshesCredentialsAndRejectsDisconnectedProjects(t *testing.T) {
	paths, target, remote := relayConfigurationFixture(t)
	if err := persistBinding(paths, currentDirectory(), target, remote.account.AccountID, remote.project.ProjectID, ""); err != nil {
		t.Fatal(err)
	}
	binding, _ := bindingAtRoot(paths, currentDirectory())
	root := localDAGBindingRoot(paths, *binding)
	store, err := graphstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	engine := &localdag.Engine{Store: store}
	if managed, err := configureConnectedLocalDAG(root, engine); !managed || err != nil {
		t.Fatal(managed, err)
	}
	transport := engine.Publisher.(*replication.Client).Remote.(replication.HTTP)
	first, err := transport.Credential(context.Background(), binding.ProjectID)
	if err != nil || first == "" {
		t.Fatal(err)
	}
	if err := saveCredential(paths, target, remote.account, strings.Repeat("r", 40)); err != nil {
		t.Fatal(err)
	}
	next, err := transport.Credential(context.Background(), binding.ProjectID)
	if err != nil || next == first {
		t.Fatal("credential not refreshed", err)
	}
	if err := atomicWriteJSON(paths.credentialPath, credentialState{SchemaVersion: contracts.LocalCredentialSchemaVersion, Credentials: []storedCredential{}}, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := transport.Credential(context.Background(), binding.ProjectID); err == nil {
		t.Fatal("logout retained authorization")
	}
	config, err := loadLocalDAGService(root)
	if err != nil || !strings.Contains(config.PauseReason, "Sign in") {
		t.Fatal(config, err)
	}
	if err := saveCredential(paths, target, remote.account, first); err != nil {
		t.Fatal(err)
	}
	if _, err := transport.Credential(context.Background(), binding.ProjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := removeBindingAtRoot(paths, binding.CollaborationRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := transport.Credential(context.Background(), binding.ProjectID); err == nil {
		t.Fatal("disconnect still permits publishing")
	}
}

func TestManagedAutomaticInferenceIgnoresRetiredSettingsAndAcceptsCaptures(t *testing.T) {
	paths, target, remote := relayConfigurationFixture(t)
	if err := persistBinding(paths, currentDirectory(), target, remote.account.AccountID, remote.project.ProjectID, ""); err != nil {
		t.Fatal(err)
	}
	binding, _ := bindingAtRoot(paths, currentDirectory())
	root := localDAGBindingRoot(paths, *binding)
	// Retired provider/model settings cannot disable automatic source routing.
	if err := atomicWriteJSON(filepath.Join(root, "inference.json"), map[string]string{"broken": "settings"}, 0600); err != nil {
		t.Fatal(err)
	}
	config, err := loadLocalDAGService(root)
	if err != nil || config.PauseReason != "" {
		t.Fatal(config, err)
	}
	event := hookEvent{SessionID: "11111111-1111-4111-8111-111111111111", CWD: binding.CollaborationRoot, HookEventName: "Stop"}
	handled, err := enqueueManagedLocalHook(contracts.AgentCodex, event)
	if !handled || err != nil {
		t.Fatal(handled, err)
	}
	events, err := localDAGInbox(root)
	if err != nil || len(events) != 1 {
		t.Fatal(events, err)
	}
	if events[0].Request.ProjectID != binding.ProjectID {
		t.Fatal("wrong project")
	}
	// Provider-less serve keeps query/control available. There is no Runner to
	// call, and the attempt budget must remain untouched.
	store, err := graphstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	engine := &localdag.Engine{Store: store, Actor: "device"}
	offlineRoot := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serveLocalDAG(ctx, offlineRoot, engine, 1, io.Discard) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	var response bytes.Buffer
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response.Reset()
		if handled, code := localDAGClient(ctx, offlineRoot, "status", localDAGClientOptions{}, &response, io.Discard); handled && code == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	var status struct {
		Paused   bool `json:"paused"`
		Attempts int  `json:"attempts"`
	}
	if err := json.Unmarshal(response.Bytes(), &status); err != nil || !status.Paused || status.Attempts != 0 {
		t.Fatal(response.String(), err)
	}
}

func TestManagedMissingIdentityNeverFallsBackToLegacy(t *testing.T) {
	paths, target, remote := relayConfigurationFixture(t)
	if err := persistBinding(paths, currentDirectory(), target, remote.account.AccountID, remote.project.ProjectID, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(paths.root, "device-identity.json")); err != nil {
		t.Fatal(err)
	}
	_, handled, err := resolveLocalMCPProject()
	if !handled || err == nil {
		t.Fatalf("missing identity routed MCP to legacy: handled=%v error=%v", handled, err)
	}
	event := hookEvent{SessionID: "11111111-1111-4111-8111-111111111111", CWD: currentDirectory(), HookEventName: "Stop"}
	handled, err = enqueueManagedLocalHook(contracts.AgentCodex, event)
	if !handled || err == nil {
		t.Fatalf("missing identity routed Hook to legacy: handled=%v error=%v", handled, err)
	}
	if _, err := os.Stat(filepath.Join(paths.root, "device-identity.json")); !os.IsNotExist(err) {
		t.Fatal("missing identity was silently regenerated", err)
	}
}

func TestManagedRelayCredentialsAreCheckedBeforeEveryHTTPRequest(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Write([]byte(`{"schema_version":1,"head":0,"entries":[]}`))
	}))
	defer server.Close()
	allowed := true
	remote := replication.HTTP{BaseURL: server.URL, Credential: func(context.Context, string) (string, error) {
		if allowed {
			return "token", nil
		}
		return "", context.Canceled
	}}
	if _, err := remote.Since(context.Background(), "project", 0); err != nil {
		t.Fatal(err)
	}
	allowed = false
	if _, err := remote.Since(context.Background(), "project", 0); err == nil {
		t.Fatal("revoked credential used")
	}
	if calls.Load() != 1 {
		t.Fatal("sent request after logout")
	}
}

func TestInferenceCLIAutomaticStatusAndOptionalPause(t *testing.T) {
	paths, target, remote := relayConfigurationFixture(t)
	if err := persistBinding(paths, currentDirectory(), target, remote.account.AccountID, remote.project.ProjectID, ""); err != nil {
		t.Fatal(err)
	}
	root, _ := managedLocalDAGRoot()
	var out, diagnostic bytes.Buffer
	if code := runInferenceCommand(context.Background(), []string{"status"}, &out, &diagnostic); code != 0 {
		t.Fatal(code, diagnostic.String())
	}
	var status struct {
		Mode, Model string
		Paused      bool
	}
	if err := json.Unmarshal(out.Bytes(), &status); err != nil || status.Mode != "automatic" || status.Model != "Agent default" || status.Paused {
		t.Fatal(out.String(), err)
	}
	for _, obsolete := range []string{"configure", "login"} {
		if code := runInferenceCommand(context.Background(), []string{obsolete}, &out, &diagnostic); code != 2 {
			t.Fatal("obsolete configuration accepted", code)
		}
	}
	for _, action := range []string{"pause", "resume"} {
		if code := runInferenceCommand(context.Background(), []string{action}, &out, &diagnostic); code != 0 {
			t.Fatal(code, diagnostic.String())
		}
		config, err := loadLocalDAGService(root)
		if err != nil || (config.PauseReason != "") != (action == "pause") {
			t.Fatal(config, err)
		}
		control, err := loadInferenceControl(root)
		if err != nil || control.Paused != (action == "pause") {
			t.Fatal(control, err)
		}
	}
	info, err := os.Stat(filepath.Join(root, "inference-control.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
	if _, err := os.Stat(filepath.Join(root, "inference.json")); !os.IsNotExist(err) {
		t.Fatal("created retired inference settings", err)
	}
}

func waitManagedControl(t *testing.T, root, previous string) localDAGEndpoint {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := readLocalPrivateFile(filepath.Join(root, "control.json"), 4096, true)
		var endpoint localDAGEndpoint
		if err == nil && decodeStrict(data, &endpoint) == nil && endpoint.Token != previous {
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			handled, code := localDAGClient(ctx, root, "status", localDAGClientOptions{}, io.Discard, io.Discard)
			cancel()
			if handled && code == 0 {
				return endpoint
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("managed Runtime did not become available")
	return localDAGEndpoint{}
}

func TestManagedLifecycleReloadsConfigurationAndHandsOffAfterUpgrade(t *testing.T) {
	previousVersion := version
	version = "0.31.0"
	t.Cleanup(func() { version = previousVersion })
	paths, target, remote := relayConfigurationFixture(t)
	if err := persistBinding(paths, currentDirectory(), target, remote.account.AccountID, remote.project.ProjectID, ""); err != nil {
		t.Fatal(err)
	}
	root, _ := managedLocalDAGRoot()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	firstDone := make(chan int, 1)
	go func() { firstDone <- runManagedLocalDAG(ctx, []string{"--root", root}, io.Discard, io.Discard) }()
	first := waitManagedControl(t, root, "")
	// Concurrent launches cannot become additional database owners.
	if code := runManagedLocalDAG(ctx, []string{"--root", root}, io.Discard, io.Discard); code != 0 {
		t.Fatal(code)
	}
	settings := localDAGInferenceControl{Schema: 1, Paused: true}
	if err := atomicWriteJSON(filepath.Join(root, "inference-control.json"), settings, 0600); err != nil {
		t.Fatal(err)
	}
	second := waitManagedControl(t, root, first.Token)
	if second.Version != version {
		t.Fatal(second)
	}
	// Mark the running endpoint as a previous binary; a newly launched binary
	// must request graceful shutdown before it opens the same store.
	second.Version = "0.30.0"
	if err := atomicWriteJSON(filepath.Join(root, "control.json"), second, 0600); err != nil {
		t.Fatal(err)
	}
	upgradedDone := make(chan int, 1)
	go func() { upgradedDone <- runManagedLocalDAG(ctx, []string{"--root", root}, io.Discard, io.Discard) }()
	waitManagedControl(t, root, second.Token)
	select {
	case code := <-firstDone:
		if code != 0 {
			t.Fatal(code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("old process did not exit")
	}
	if err := stopConnectedLocalDAG(ctx, root); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-upgradedDone:
		if code != 0 {
			t.Fatal(code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("new process did not exit")
	}
	store, err := graphstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	budget, err := store.InferenceBudget(time.Now(), 2)
	store.Close()
	if err != nil || budget.Used != 0 {
		t.Fatal("paused runtime invoked inference", budget, err)
	}
}
