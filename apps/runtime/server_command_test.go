package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
)

func TestServerPreferencesAreOfflineAndDoNotChangeExistingState(t *testing.T) {
	root := filepath.Join(t.TempDir(), "uncreated")
	setRuntimeTestHome(t, root)
	paths, _ := resolveRuntimePaths()
	var out, stderr bytes.Buffer
	if code := run(context.Background(), []string{"server", "list", "--json"}, nil, &out, &stderr); code != 0 {
		t.Fatal(code, stderr.String())
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("listing created local state", err)
	}
	first := fixtureDeploymentTarget("http://one.example.test:7788")
	first.Capabilities = []string{contracts.DeploymentCapabilityLocalDAG}
	second := first
	second.DeploymentID, second.ServerURL, second.ServerPublicKeySHA256 = "dpl_second0001", "https://two.example.test", strings.Repeat("2", 64)
	account := fixtureAccount("acct_preferences01", "Fixture")
	for _, target := range []deploymentTarget{first, second} {
		if err := saveCredential(paths, target, account, "private-token-not-for-listing"); err != nil {
			t.Fatal(err)
		}
	}
	folder := t.TempDir()
	if err := persistBinding(paths, folder, first, account.AccountID, "coco_preferences001", ""); err != nil {
		t.Fatal(err)
	}
	beforeBinding, _ := os.ReadFile(paths.bindingPath)
	beforeCredentials, _ := os.ReadFile(paths.credentialPath)
	pending, err := pendingInitRequestID(paths, folder, first.DeploymentID, account.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{second.ServerURL, "http://THREE.example.test:80/", "official"} {
		out.Reset()
		stderr.Reset()
		if code := runServerCommand([]string{"use", input}, &out, &stderr); code != 0 {
			t.Fatal(code, stderr.String())
		}
	}
	if got, err := defaultServer(paths); err != nil || got != defaultServerURL {
		t.Fatal(got, err)
	}
	if info, err := os.Stat(clientConfigPath(paths)); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
	configData, _ := os.ReadFile(clientConfigPath(paths))
	var fields map[string]any
	if err := json.Unmarshal(configData, &fields); err != nil || len(fields) != 1 {
		t.Fatal(string(configData), err)
	}
	for file, want := range map[string][]byte{paths.bindingPath: beforeBinding, paths.credentialPath: beforeCredentials} {
		got, _ := os.ReadFile(file)
		if !bytes.Equal(got, want) {
			t.Fatal("switch altered state", file)
		}
	}
	if got, err := pendingInitRequestID(paths, folder, first.DeploymentID, account.AccountID); err != nil || got != pending {
		t.Fatal("switch replaced pending work", err)
	}
	out.Reset()
	stderr.Reset()
	if code := runServerCommand([]string{"list", "--json"}, &out, &stderr); code != 0 {
		t.Fatal(code, stderr.String())
	}
	if strings.Contains(out.String(), "private-token") || !strings.Contains(out.String(), first.ServerURL) || !strings.Contains(out.String(), second.ServerURL) {
		t.Fatal("incorrect listing")
	}
	var listed struct {
		Servers []serverListItem `json:"servers"`
	}
	if err := json.Unmarshal(out.Bytes(), &listed); err != nil || len(listed.Servers) != 3 {
		t.Fatal(out.String(), err)
	}
	for _, item := range listed.Servers {
		if item.URL == first.ServerURL && item.ConnectedFolders != 1 {
			t.Fatal(item)
		}
	}
}

func TestServerPreferenceValidationAndRecovery(t *testing.T) {
	setRuntimeTestHome(t, t.TempDir())
	paths, _ := resolveRuntimePaths()
	if _, err := saveDefaultServer(paths, "http://Dev.Example.test:80/"); err != nil {
		t.Fatal(err)
	}
	if got, err := defaultServer(paths); err != nil || got != "http://dev.example.test" {
		t.Fatal(got, err)
	}
	before, _ := os.ReadFile(clientConfigPath(paths))
	for _, bad := range []string{"", "ftp://example.com", "https://user:secret@example.com", "https://example.com/project//other", "https://example.com/?token=private", "https://example.com/#fragment"} {
		if _, err := saveDefaultServer(paths, bad); err == nil {
			t.Fatal("accepted invalid endpoint", bad)
		}
		got, _ := os.ReadFile(clientConfigPath(paths))
		if !bytes.Equal(got, before) {
			t.Fatal("failed switch changed config")
		}
	}
	for _, data := range []string{`{`, `{}`, `{"default_server":"http://ok.test","token":"private"}`, `{"default_server":"https://ok.test"} {}`} {
		if err := os.WriteFile(clientConfigPath(paths), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := defaultServer(paths); err == nil || !strings.Contains(err.Error(), "coconet server use") || strings.Contains(err.Error(), "private") {
			t.Fatal("missing actionable config error", err)
		}
	}
	if _, err := saveDefaultServer(paths, "official"); err != nil {
		t.Fatal("could not repair config", err)
	}
	if got, err := defaultServer(paths); err != nil || got != defaultServerURL {
		t.Fatal(got, err)
	}
}

func TestRuntimeUsesStandardConfigDirectory(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	root, explicit, err := defaultRuntimeConfigDirectory()
	if err != nil || root != filepath.Join(configRoot, "coconet") || !explicit {
		t.Fatal(root, explicit, err)
	}
	t.Setenv("XDG_CONFIG_HOME", "relative")
	if _, _, err := defaultRuntimeConfigDirectory(); err == nil {
		t.Fatal("accepted relative config directory")
	}
}

func TestServerSelectionDoesNotRedirectOnBrokenLocalState(t *testing.T) {
	setRuntimeTestHome(t, t.TempDir())
	paths, _ := resolveRuntimePaths()
	folder := t.TempDir()
	t.Setenv("COCONET_CWD", folder)
	binding := fixtureBinding(folder, "coco_preferences001", "acct_preferences01")
	if err := saveBinding(paths, binding); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(clientConfigPath(paths), []byte(`{`), 0600); err != nil {
		t.Fatal(err)
	}
	target, err := selectedDeployment(context.Background(), paths, "")
	if err != nil || target.DeploymentID != binding.DeploymentID || target.ServerURL != binding.LocalServerURL {
		t.Fatal("broken preference affected the existing folder", target, err)
	}
	var out, stderr bytes.Buffer
	if code := runLoginCommand(context.Background(), "whoami", nil, &out, &stderr); code != 1 || !strings.Contains(stderr.String(), "not signed in") {
		t.Fatal("bound whoami used the broken default", code, stderr.String())
	}
	if _, err := saveDefaultServer(paths, "http://unused.example.test:7788"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.bindingPath, []byte(`{`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"login", "logout", "whoami"} {
		out.Reset()
		stderr.Reset()
		if code := runLoginCommand(context.Background(), command, nil, &out, &stderr); code != 1 || !strings.Contains(stderr.String(), "decode local bindings") {
			t.Fatal("unreadable binding fell back to another server", command, code, stderr.String())
		}
	}
}
