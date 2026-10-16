package integration

import (
	"bufio"
	"bytes"
	"encoding/json"
	"github.com/xfey/coconet/packages/contracts"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestPhase2TwoAccountProjectLifecycle(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("Coconet MVP supports macOS and Linux")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required")
	}
	repositoryRoot := projectRoot(t)
	temporaryRoot := t.TempDir()
	runtimeBinary := filepath.Join(temporaryRoot, "coconet")
	serverBinary := filepath.Join(temporaryRoot, "coconet-server")
	buildBinary(t, repositoryRoot, runtimeBinary, "./apps/runtime")
	buildBinary(t, repositoryRoot, serverBinary, "./apps/server")

	serverDataDir := filepath.Join(temporaryRoot, "server")
	databasePath := filepath.Join(serverDataDir, "server.db")
	serverURL, serverConfig, registrationToken, stopServer := startServer(t, serverBinary, serverDataDir)
	defer stopServer()

	ownerRepository := filepath.Join(temporaryRoot, "owner repository")
	git(t, repositoryRoot, "init", "-q", ownerRepository)
	git(t, ownerRepository, "config", "user.name", "Fixture Owner")
	git(t, ownerRepository, "config", "user.email", "owner@coconet.test")
	ownerHome := filepath.Join(temporaryRoot, "owner-home")
	runCoconet(t, runtimeBinary, ownerRepository, ownerHome, serverURL, registrationToken, "y\n", "init", "--new")
	ownerBinding := readOnlyBinding(t, filepath.Join(ownerHome, "bindings.json"))
	if ownerBinding.SchemaVersion != 3 || !strings.HasPrefix(ownerBinding.DeploymentID, "dpl_") || ownerBinding.LocalServerURL != serverURL || !strings.HasPrefix(ownerBinding.ProjectID, "coco_") {
		t.Fatalf("invalid owner binding %#v", ownerBinding)
	}
	if _, err := os.Stat(filepath.Join(ownerRepository, ".coconet")); !os.IsNotExist(err) {
		t.Fatalf("init wrote repository-local Coconet state: %v", err)
	}
	projectID := ownerBinding.ProjectID

	memberRepository := filepath.Join(temporaryRoot, "member clone")
	git(t, repositoryRoot, "clone", "-q", ownerRepository, memberRepository)
	git(t, memberRepository, "config", "user.name", "Fixture Member")
	git(t, memberRepository, "config", "user.email", "member@coconet.test")
	memberHome := filepath.Join(temporaryRoot, "member-home")
	runCoconet(t, runtimeBinary, memberRepository, memberHome, serverURL, registrationToken, "y\n", "connect", projectID, "--server", serverURL)
	memberBinding := readOnlyBinding(t, filepath.Join(memberHome, "bindings.json"))
	if memberBinding.ProjectID != ownerBinding.ProjectID || memberBinding.DeploymentID != ownerBinding.DeploymentID {
		t.Fatalf("member did not join the same Project: owner=%#v member=%#v", ownerBinding, memberBinding)
	}

	stopServer()
	serverURL, stopServer = startServerAtURL(t, serverBinary, serverConfig, serverURL)
	runCoconet(t, runtimeBinary, memberRepository, memberHome, serverURL, registrationToken, "", "status")
	stopServer()

	// Directory paths belong only to the explicit registry, never Project identity.
	database, err := bolt.Open(databasePath, 0600, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.View(func(tx *bolt.Tx) error {
		root := tx.Bucket([]byte("records"))
		if root == nil {
			t.Fatal("record storage missing")
		}
		var registry, content []byte
		if err := root.ForEach(func(name, _ []byte) error {
			return root.Bucket(name).ForEach(func(_, value []byte) error {
				if string(name) == "workspaces" {
					registry = append(registry, value...)
				} else {
					content = append(content, value...)
				}
				return nil
			})
		}); err != nil {
			return err
		}
		if !bytes.Contains(registry, []byte(ownerRepository)) || !bytes.Contains(registry, []byte(memberRepository)) {
			t.Fatalf("confirmed directories missing from registry: %s; want %s and %s", registry, ownerRepository, memberRepository)
		}
		if bytes.Contains(content, []byte(ownerRepository)) || bytes.Contains(content, []byte(memberRepository)) {
			t.Fatal("directory path leaked outside project registry")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

}

type localBindingFixture struct {
	Workflow              string `json:"workflow,omitempty"`
	SchemaVersion         int    `json:"schema_version"`
	DeploymentID          string `json:"deployment_id"`
	ServerPublicKeySHA256 string `json:"server_public_key_sha256"`
	ProjectID             string `json:"project_id"`
	AccountID             string `json:"account_id"`
	CollaborationRoot     string `json:"collaboration_root"`
	LocalServerURL        string `json:"local_server_url"`
	ConnectedAt           string `json:"connected_at"`
}

type localBindingStateFixture struct {
	SchemaVersion int                   `json:"schema_version"`
	Bindings      []localBindingFixture `json:"bindings"`
}

func readOnlyBinding(t *testing.T, path string) localBindingFixture {
	t.Helper()
	var state localBindingStateFixture
	decodeFile(t, path, &state)
	if state.SchemaVersion != 3 || len(state.Bindings) != 1 {
		t.Fatalf("unexpected local binding state %#v", state)
	}
	return state.Bindings[0]
}

func projectRoot(t *testing.T) string {
	t.Helper()
	command := exec.Command("git", "rev-parse", "--show-toplevel")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("find project root: %v: %s", err, output)
	}
	return strings.TrimSpace(string(output))
}

func buildBinary(t *testing.T, root, output, packagePath string) {
	t.Helper()
	command := exec.Command("go", "build", "-o", output, packagePath)
	command.Dir = root
	if result, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v: %s", packagePath, err, result)
	}
}

func startServer(t *testing.T, binary, dataDir string) (string, string, string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	serverURL := "http://" + address
	command := exec.Command(binary, "init", "--data-dir", dataDir, "--listen", address, "--advertised-endpoint", serverURL)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("initialize Server: %v: %s", err, output)
	}
	seedFixtureAccounts(t, binary, filepath.Join(dataDir, "server.json"))
	serverURL, stop := startServerAtURL(t, binary, filepath.Join(dataDir, "server.json"), serverURL)
	return serverURL, filepath.Join(dataDir, "server.json"), "", stop
}

func startServerAtURL(t *testing.T, binary, configPath, serverURL string) (string, func()) {
	t.Helper()
	command := exec.Command(binary, "serve", "--config", configPath)
	command.Env = append(os.Environ(), "COCONET_TRUSTED_PROXY_CIDRS=127.0.0.1/32")
	var serverOutput bytes.Buffer
	command.Stdout = &serverOutput
	command.Stderr = &serverOutput
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		_ = command.Process.Signal(os.Interrupt)
		done := make(chan error, 1)
		go func() { done <- command.Wait() }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = command.Process.Kill()
			<-done
		}
	}
	t.Cleanup(stop)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(serverURL + "/healthz")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return serverURL, stop
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	stop()
	t.Fatalf("server did not become ready: %s", serverOutput.String())
	return "", func() {}
}

func runCoconet(t *testing.T, binary, cwd, home, serverURL, registrationToken, input string, args ...string) string {
	t.Helper()
	for index, arg := range args {
		if arg == "--server" && index+1 < len(args) {
			seedFixtureLogin(t, home, args[index+1])
		}
	}
	command := exec.Command(binary, args...)
	command.Dir = cwd
	command.Stdin = strings.NewReader(input)
	command.Env = runtimeEnvironment(t, cwd, home, serverURL, registrationToken)
	var output bytes.Buffer
	reader, writer := io.Pipe()
	command.Stdout, command.Stderr = writer, writer
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
		}
	})
	done := make(chan error, 1)
	go func() { err := command.Wait(); _ = writer.Close(); done <- err }()
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := scanner.Text()
		output.WriteString(line + "\n")
		if strings.Contains(line, "/#authorize=") {
			approveFixtureConnection(t, home, strings.TrimSpace(line), args)
		}
	}
	if err := <-done; err != nil {
		t.Fatalf("coconet %v: %v\n%s", args, err, output.String())
	}
	return output.String()
}

func git(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
}

func decodeFile(t *testing.T, path string, output any) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(content, output); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

// Real CLI + production HTTP authorization, with isolated operator accounts.
func approveFixtureConnection(t *testing.T, home, link string, args []string) {
	t.Helper()
	target, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	origin := target.Scheme + "://" + target.Host
	params, _ := url.ParseQuery(target.Fragment)
	id := params.Get("authorize")
	var state struct {
		Credentials []struct {
			DeploymentID string `json:"deployment_id"`
			AccessToken  string `json:"access_token"`
		}
	}
	decodeFile(t, filepath.Join(home, "credentials.json"), &state)
	response, err := http.Get(origin + "/v1/deployment?challenge=fixture_account_setup_1234")
	if err != nil {
		t.Fatal(err)
	}
	var deployment contracts.DeploymentInfo
	_ = json.NewDecoder(response.Body).Decode(&deployment)
	response.Body.Close()
	key := ""
	for _, credential := range state.Credentials {
		if credential.DeploymentID == deployment.DeploymentID {
			key = credential.AccessToken
		}
	}
	if key == "" {
		t.Fatal("fixture credential missing")
	}
	var cookie *http.Cookie
	request := func(path string, input, output any) {
		data, _ := json.Marshal(input)
		req, _ := http.NewRequest("POST", origin+path, bytes.NewReader(data))
		req.Header.Set("Origin", "https://"+target.Host)
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("fixture approval %s: %d", path, resp.StatusCode)
		}
		if cookie == nil {
			for _, value := range resp.Cookies() {
				if strings.Contains(value.Name, "dashboard") {
					cookie = value
				}
			}
		}
		if output != nil {
			if err := json.NewDecoder(resp.Body).Decode(output); err != nil {
				t.Fatal(err)
			}
		}
	}
	request("/v1/dashboard/session", map[string]any{"schema_version": 1, "account_key": key}, nil)
	var preview contracts.DeviceAuthorizationPreview
	request("/v1/dashboard/authorize", contracts.DeviceAuthorizationDecision{AuthorizationID: id}, &preview)
	project := ""
	for i, value := range args {
		if value == "connect" && i+1 < len(args) && strings.HasPrefix(args[i+1], "coco_") {
			project = args[i+1]
		}
	}
	yes := true
	decision := contracts.DeviceAuthorizationDecision{AuthorizationID: id, Approve: &yes, ProjectID: project}
	if preview.Directory != nil {
		decision.DirectoryRevision = preview.Directory.Revision
	}
	request("/v1/dashboard/authorize", decision, nil)
}
