package integration

import (
	"encoding/json"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/xfey/coconet/packages/workgraph"
)

func TestPhase7OneRuntimeKeepsTwoDeploymentsAuthoritativeAndIsolated(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("Coconet supports macOS and Linux")
	}
	repositoryRoot := projectRoot(t)
	temporaryRoot := t.TempDir()
	runtimeBinary := filepath.Join(temporaryRoot, "coconet")
	serverBinary := filepath.Join(temporaryRoot, "coconet-server")
	buildBinary(t, repositoryRoot, runtimeBinary, "./apps/runtime")
	buildBinary(t, repositoryRoot, serverBinary, "./apps/server")

	serverA, _, registrationA, stopA := startServer(t, serverBinary, filepath.Join(temporaryRoot, "server-a"))
	defer stopA()
	serverB, _, registrationB, stopB := startServer(t, serverBinary, filepath.Join(temporaryRoot, "server-b"))
	defer stopB()

	clientHome := filepath.Join(temporaryRoot, "client-home")
	repositoryA := filepath.Join(temporaryRoot, "repository-a")
	repositoryB := filepath.Join(temporaryRoot, "repository-b")
	for _, repository := range []string{repositoryA, repositoryB} {
		git(t, repositoryRoot, "init", "-q", repository)
		git(t, repository, "config", "user.name", "Multi Deployment Fixture")
		git(t, repository, "config", "user.email", "multi@coconet.test")
	}
	runCoconet(t, runtimeBinary, repositoryA, clientHome, serverA, registrationA, "y\n", "init", "--new")
	runCoconet(t, runtimeBinary, repositoryB, clientHome, serverA, registrationB, "y\n", "init", "--new", "--server", serverB)

	bindingPath := filepath.Join(clientHome, "bindings.json")
	bindingA := readBindingForRoot(t, bindingPath, repositoryA)
	bindingB := readBindingForRoot(t, bindingPath, repositoryB)
	if bindingA.DeploymentID == bindingB.DeploymentID || bindingA.LocalServerURL != serverA || bindingB.LocalServerURL != serverB {
		t.Fatalf("repositories did not retain independent authorities: %#v %#v", bindingA, bindingB)
	}
	var credentials struct {
		SchemaVersion int `json:"schema_version"`
		Credentials   []struct {
			DeploymentID string `json:"deployment_id"`
			ServerURL    string `json:"server_url"`
		} `json:"credentials"`
	}
	decodeFile(t, filepath.Join(clientHome, "credentials.json"), &credentials)
	if credentials.SchemaVersion != 2 || len(credentials.Credentials) != 2 {
		t.Fatalf("multi-Deployment credentials were not retained: %#v", credentials)
	}
	// Switching the default persists across fresh CLI processes. Existing folder
	// bindings still select their original endpoint and credential.
	runCoconet(t, runtimeBinary, repositoryA, clientHome, serverA, "", "", "server", "use", serverB)
	if output := runCoconet(t, runtimeBinary, temporaryRoot, clientHome, serverA, "", "", "whoami"); !strings.Contains(output, serverB) {
		t.Fatal("unbound command did not use saved default", output)
	}
	if output := runCoconet(t, runtimeBinary, repositoryA, clientHome, serverA, "", "", "whoami"); !strings.Contains(output, serverA) {
		t.Fatal("default switch changed folder authority", output)
	}
	if output := runCoconet(t, runtimeBinary, temporaryRoot, clientHome, serverA, "", "", "whoami", "--server", serverA); !strings.Contains(output, serverA) {
		t.Fatal("explicit endpoint did not take precedence", output)
	}
	if output := runCoconet(t, runtimeBinary, temporaryRoot, clientHome, serverA, "", "", "whoami"); !strings.Contains(output, serverB) {
		t.Fatal("explicit endpoint changed saved default", output)
	}
	serverAOrigin, err := url.Parse(serverA)
	if err != nil {
		t.Fatal(err)
	}
	migratedEndpoint := httptest.NewServer(httputil.NewSingleHostReverseProxy(serverAOrigin))
	t.Cleanup(migratedEndpoint.Close)
	projectA := bindingA.ProjectID
	runCoconet(t, runtimeBinary, repositoryA, clientHome, serverA, "", "y\n", "disconnect")
	runCoconet(t, runtimeBinary, repositoryA, clientHome, serverA, "", "y\n", "connect", projectA, "--server", migratedEndpoint.URL)
	bindingA = readBindingForRoot(t, bindingPath, repositoryA)
	if bindingA.LocalServerURL != migratedEndpoint.URL {
		t.Fatalf("same-identity local endpoint override was not persisted: %#v", bindingA)
	}

	hookPayload := func(cwd, eventID string) string {
		content, _ := json.Marshal(map[string]string{
			"hook_event_name": "Stop", "session_id": "native-" + eventID, "cwd": cwd,
			"observed_at": "2026-08-25T12:00:00Z", "event_id": eventID,
		})
		return string(content)
	}
	runCoconet(t, runtimeBinary, temporaryRoot, clientHome, serverA, "", "", "server", "use", "official")
	// Even with the default switched to the official service, both hooks write
	// only to their original server/account stores, without contacting the default.
	runCoconet(t, runtimeBinary, repositoryA, clientHome, serverA, "", hookPayload(repositoryA, "event-deployment-a"), "hook", "--agent", "codex")
	runCoconet(t, runtimeBinary, repositoryB, clientHome, serverA, "", hookPayload(repositoryB, "event-deployment-b"), "hook", "--agent", "codex")
	for _, binding := range []localBindingFixture{bindingA, bindingB} {
		key := workgraph.Digest([]string{binding.DeploymentID, binding.ServerPublicKeySHA256, binding.AccountID})
		items, err := filepath.Glob(filepath.Join(clientHome, "local-dag", "scopes", key, "inbox", "*.json"))
		if err != nil || len(items) != 1 || binding.Workflow != "local-dag-v1" {
			t.Fatalf("isolated local capture missing: count=%d workflow=%q error=%v", len(items), binding.Workflow, err)
		}
		var captured struct {
			Request struct {
				ProjectID string `json:"project_id"`
			} `json:"request"`
		}
		decodeFile(t, items[0], &captured)
		if captured.Request.ProjectID != binding.ProjectID {
			t.Fatal("capture crossed deployment boundary")
		}
		if _, err := os.Stat(filepath.Join(clientHome, "deployments", binding.DeploymentID, "runtime.db")); !os.IsNotExist(err) {
			t.Fatal("retired queue created")
		}
	}

	// A local binding cannot combine Deployment B's pinned identity with Server A's
	// endpoint. The Runtime must reject it during the unauthenticated handshake.
	var bindingState localBindingStateFixture
	decodeFile(t, bindingPath, &bindingState)
	for index := range bindingState.Bindings {
		if sameFixturePath(bindingState.Bindings[index].CollaborationRoot, repositoryB) {
			bindingState.Bindings[index].LocalServerURL = serverA
		}
	}
	content, err := json.MarshalIndent(bindingState, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bindingPath, append(content, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(runtimeBinary, "status")
	command.Dir = repositoryB
	command.Env = runtimeEnvironment(t, repositoryB, clientHome, serverA, "")
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "pinned identity") {
		t.Fatalf("mixed Deployment authority was not rejected: err=%v output=%s", err, output)
	}
}

func readBindingForRoot(t *testing.T, path, root string) localBindingFixture {
	t.Helper()
	var state localBindingStateFixture
	decodeFile(t, path, &state)
	for _, binding := range state.Bindings {
		if sameFixturePath(binding.CollaborationRoot, root) {
			return binding
		}
	}
	t.Fatalf("binding state omitted %s: %#v", root, state)
	return localBindingFixture{}
}

func sameFixturePath(left, right string) bool {
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	if leftErr == nil && rightErr == nil {
		return os.SameFile(leftInfo, rightInfo)
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func runtimeEnvironment(t *testing.T, cwd, home, serverURL, registrationToken string) []string {
	seedFixtureLogin(t, home, serverURL)
	configPath := filepath.Join(home, "config.json")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		data, _ := json.Marshal(map[string]string{"default_server": serverURL})
		if err := os.WriteFile(configPath, data, 0600); err != nil {
			t.Fatal(err)
		}
	}

	for _, name := range []string{"codex-fixture-test-only", "tclaude-test-only"} {
		if err := os.MkdirAll(filepath.Join(home, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	return append(os.Environ(),
		"COCONET_CWD="+cwd,
		"XDG_CONFIG_HOME="+runtimeFixtureConfigHome(t, home),
		"COCONET_REGISTRATION_TOKEN="+registrationToken,
		"COCONET_CODEX_COMMAND=coconet-test-no-codex",
		"CODEX_HOME="+filepath.Join(home, "codex-fixture-test-only"),
		"COCONET_CLAUDE_COMMAND=coconet-test-no-claude",
		"COCONET_CLAUDE_NODE=coconet-test-no-node",
		"CLAUDE_CONFIG_DIR="+filepath.Join(home, "tclaude-test-only"),
		"COCONET_DISABLE_BACKGROUND_SYNC=1",
		"SSH_CONNECTION=isolated-test",
	)
}

// Processes use the standard config location without touching the real user's state.
func runtimeFixtureConfigHome(t *testing.T, home string) string {
	t.Helper()
	configRoot := t.TempDir()
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(home, filepath.Join(configRoot, "coconet")); err != nil {
		t.Fatal(err)
	}
	return configRoot
}
