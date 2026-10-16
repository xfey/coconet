package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
)

func TestWorkspaceSnapshotRegistrationRetryAndIsolation(t *testing.T) {
	t.Setenv("COCONET_HOME", t.TempDir())
	paths, err := resolveRuntimePaths()
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var reports []contracts.WorkspaceReport
	fail := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method != "PUT" || r.URL.Path != "/v1/workspaces" {
			t.Error("unexpected route", r.Method, r.URL.Path)
			w.WriteHeader(404)
			return
		}
		var input contracts.WorkspaceReport
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		reports = append(reports, input)
		if fail {
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"status":"unavailable","code":"test_offline"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(contracts.WorkspaceReportResponse{SchemaVersion: 1, Status: contracts.StatusOK, Revision: input.Revision})
	}))
	defer server.Close()
	target := fixtureDeploymentTarget(server.URL)
	account := fixtureAccount("acct_workspace1", "Developer")
	if err := saveCredential(paths, target, account, "fixture-workspace-credential-token"); err != nil {
		t.Fatal(err)
	}
	client, err := newAPIClient(server.URL, "fixture-workspace-credential-token")
	if err != nil {
		t.Fatal(err)
	}
	client.capabilities = []string{contracts.DeploymentCapabilityWorkspaceRegistry}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	binding := fixtureBinding(root, "coco_workspace1", account.AccountID, target)
	if err := saveBinding(paths, binding); err != nil {
		t.Fatal(err)
	}
	other := fixtureBinding(t.TempDir(), "coco_workspace2", "acct_otheruser", target)
	if err := saveBinding(paths, other); err != nil {
		t.Fatal(err)
	}
	report := func(force bool) error {
		return reportWorkspaceSnapshot(context.Background(), paths, target, force, client)
	}
	get := func() []contracts.WorkspaceReport {
		mu.Lock()
		defer mu.Unlock()
		return append([]contracts.WorkspaceReport{}, reports...)
	}
	if err := report(true); err != nil {
		t.Fatal(err)
	}
	first := get()[0]
	if len(first.Folders) != 1 || first.Folders[0].Path != root || first.Revision != 1 {
		t.Fatal(first)
	}
	if err := report(false); err != nil {
		t.Fatal(err)
	}
	if len(get()) != 1 {
		t.Fatal("unchanged background snapshot not throttled")
	}
	// Concurrent explicit status calls serialize; their identity and revision remain stable.
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := report(true); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	for _, r := range get() {
		if r.InstallationID != first.InstallationID || r.Revision != 1 {
			t.Fatal("duplicate installation", r)
		}
	}
	if _, err := removeBindingAtRoot(paths, root); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	fail = true
	mu.Unlock()
	if err := report(true); err == nil {
		t.Fatal("offline failure missing")
	}
	pending := get()
	last := pending[len(pending)-1]
	if len(last.Folders) != 0 || last.Revision != 2 {
		t.Fatal("disconnect did not create empty snapshot", last)
	}
	if err := report(false); err != nil {
		t.Fatal(err)
	}
	if len(get()) != len(pending) {
		t.Fatal("failed background snapshot ignored backoff")
	}
	mu.Lock()
	fail = false
	mu.Unlock()
	if err := report(true); err != nil {
		t.Fatal(err)
	}
	all := get()
	last = all[len(all)-1]
	if last.Revision != 2 || last.InstallationID != first.InstallationID {
		t.Fatal("retry changed revision/identity")
	}
	if err := saveBinding(paths, binding); err != nil {
		t.Fatal(err)
	}
	if err := saveCredential(paths, target, account, "fixture-workspace-new-credential"); err != nil {
		t.Fatal(err)
	}
	if err := report(true); err != nil {
		t.Fatal(err)
	}
	all = get()
	last = all[len(all)-1]
	if last.Revision != 3 || last.InstallationID != first.InstallationID {
		t.Fatal("login rotation duplicated installation")
	}
	files, err := filepath.Glob(filepath.Join(paths.root, "deployments", target.DeploymentID, "workspaces-*.json"))
	if err != nil || len(files) != 1 {
		t.Fatal(files, err)
	}
	info, err := os.Stat(files[0])
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("private state permissions", err)
	}
	count := len(all)
	client.capabilities = nil
	if err := report(true); err != nil {
		t.Fatal(err)
	}
	if len(get()) != count {
		t.Fatal("old server was sent unsupported registration")
	}
	// A distinct installation root has a new ID even on the same hostname/account.
	t.Setenv("COCONET_HOME", t.TempDir())
	second, err := resolveRuntimePaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := saveCredential(second, target, account, "fixture-workspace-new-credential"); err != nil {
		t.Fatal(err)
	}
	if err := saveBinding(second, binding); err != nil {
		t.Fatal(err)
	}
	client.capabilities = []string{contracts.DeploymentCapabilityWorkspaceRegistry}
	if err := reportWorkspaceSnapshot(context.Background(), second, target, true, client); err != nil {
		t.Fatal(err)
	}
	all = get()
	if all[len(all)-1].InstallationID == first.InstallationID {
		t.Fatal("two installations merged")
	}
}
