package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contracts"
)

func TestDefaultServerURLUsesCanonicalHostedOrigin(t *testing.T) {
	if defaultServerURL != "https://api.coconet.space" {
		t.Fatalf("unexpected Hosted origin %q", defaultServerURL)
	}
}

func TestInitCreatesOnlyLocalBindingAndRecoversPartialCreateResponse(t *testing.T) {
	remote := newFakeCoconetServer()
	remote.dropFirstCreateResponse = true
	server := httptest.NewServer(remote)
	t.Cleanup(server.Close)
	remote.advertisedEndpoint = server.URL
	repositoryRoot := initTestRepository(t, "new-project")
	configureRuntimeTest(t, t.TempDir(), repositoryRoot, server.URL)

	var output, stderr bytes.Buffer
	if code := runInit(context.Background(), []string{"--new"}, strings.NewReader(""), &output, &stderr); code != 1 {
		t.Fatalf("partial init returned %d, stderr=%q output=%q", code, stderr.String(), output.String())
	}
	if _, err := os.Stat(filepath.Join(repositoryRoot, ".coconet")); !os.IsNotExist(err) {
		t.Fatalf("init wrote a repository-local Coconet directory: %v", err)
	}
	paths, _ := resolveRuntimePaths()
	pending, err := loadPendingOperations(paths)
	if err != nil || len(pending.Initializes) != 1 || pending.Initializes[0].RequestID == "" {
		t.Fatalf("partial init did not preserve its idempotency key: %#v %v", pending, err)
	}

	output.Reset()
	stderr.Reset()
	if code := runInit(context.Background(), []string{"--new"}, strings.NewReader(""), &output, &stderr); code != 0 {
		t.Fatalf("retry init failed code=%d stderr=%q output=%q", code, stderr.String(), output.String())
	}
	binding, err := bindingAtRoot(paths, repositoryRoot)
	if err != nil || binding == nil || binding.SchemaVersion != contracts.LocalBindingSchemaVersion || binding.ProjectID != remote.project.ProjectID || binding.LocalServerURL != server.URL {
		t.Fatalf("unexpected schema 3 binding %#v %v", binding, err)
	}
	content, err := os.ReadFile(paths.bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(content, []byte("declaration_path")) || bytes.Contains(content, []byte("consented_at")) || !bytes.Contains(content, []byte("local_server_url")) {
		t.Fatalf("binding file retained declaration-era fields: %s", content)
	}
	if remote.createTransactions != 1 {
		t.Fatalf("partial response created %d Projects", remote.createTransactions)
	}
	if !strings.HasSuffix(output.String(), "✓ Created and connected to fixture-project\n") || strings.Contains(output.String(), "New conversation updates") {
		t.Fatalf("init should confirm success without issuing an invitation: %q %q", output.String(), stderr.String())
	}
	if !strings.Contains(output.String(), "✓ Created and connected to") || strings.Contains(output.String(), "History") || strings.Contains(output.String(), "UID") || strings.Contains(output.String(), repositoryRoot) {
		t.Fatalf("default init output is not concise: %q", output.String())
	}
	pending, err = loadPendingOperations(paths)
	if err != nil || len(pending.Initializes) != 0 {
		t.Fatalf("completed init retained its recovery marker: %#v %v", pending, err)
	}

	output.Reset()
	stderr.Reset()
	if code := runInit(context.Background(), []string{"--new"}, strings.NewReader(""), &output, &stderr); code != 0 || !strings.Contains(output.String(), "✓ Already connected") {
		t.Fatalf("idempotent init failed code=%d stderr=%q output=%q", code, stderr.String(), output.String())
	}
	if remote.createTransactions != 1 {
		t.Fatalf("idempotent init created another Project: %d", remote.createTransactions)
	}
}

func TestRepeatedInitForArchivedProjectIsReadOnly(t *testing.T) {
	remote := newFakeCoconetServer()
	remote.accessStatus = contracts.StatusProjectArchived
	server := httptest.NewServer(remote)
	t.Cleanup(server.Close)
	configureBoundRepository(t, remote, server.URL)
	var output, stderr bytes.Buffer
	if code := runInit(context.Background(), []string{"--new"}, strings.NewReader(""), &output, &stderr); code != 0 || !strings.Contains(output.String(), "read-only archive") {
		t.Fatalf("archived init tried to share the Project: %d %q %q", code, output.String(), stderr.String())
	}
}

func TestConnectRejectsRootConflictAndMismatchedProjectResponseBeforeLocalMutation(t *testing.T) {
	remote := newFakeCoconetServer()
	server := httptest.NewServer(remote)
	t.Cleanup(server.Close)
	remote.advertisedEndpoint = "https://shared.coconet.test"
	repositoryRoot, paths := configureBoundRepository(t, remote, server.URL)
	conflictingCode := "coco_1111111111111111"

	var output, stderr bytes.Buffer
	if code := runConnect(context.Background(), []string{conflictingCode, "--server", server.URL}, strings.NewReader(""), &output, &stderr); code != 1 || !strings.Contains(stderr.String(), "already connected to a project") {
		t.Fatalf("root conflict was not rejected code=%d stderr=%q", code, stderr.String())
	}
	if remote.connectRequests != 0 {
		t.Fatalf("root conflict started authorization: %d requests", remote.connectRequests)
	}
	binding, err := bindingAtRoot(paths, repositoryRoot)
	if err != nil || binding == nil || binding.ProjectID != remote.project.ProjectID {
		t.Fatalf("root conflict changed the existing binding: %#v %v", binding, err)
	}

	otherRoot := initTestRepository(t, "mismatched-response")
	t.Setenv("COCONET_CWD", otherRoot)
	remote.accessProjectID = "coco_unexpected01"
	validCode := remote.project.ProjectID
	output.Reset()
	stderr.Reset()
	if code := runConnect(context.Background(), []string{validCode, "--server", server.URL}, strings.NewReader(""), &output, &stderr); code != 1 || !strings.Contains(stderr.String(), "active access to this project") {
		t.Fatalf("mismatched Project response was accepted code=%d stderr=%q", code, stderr.String())
	}
	if binding, err := bindingAtRoot(paths, otherRoot); err != nil || binding != nil {
		t.Fatalf("mismatched Project response wrote a binding: %#v %v", binding, err)
	}
}

func TestInvalidProjectIDIsRejectedBeforeRepositoryDiscovery(t *testing.T) {
	t.Setenv("COCONET_CWD", filepath.Join(t.TempDir(), "missing"))
	var output, stderr bytes.Buffer
	if code := runConnect(context.Background(), []string{"not-a-code"}, strings.NewReader(""), &output, &stderr); code != 1 || !strings.Contains(stderr.String(), "Invalid Project ID") || strings.Contains(stderr.String(), "Git repository") {
		t.Fatalf("invalid code boundary failed code=%d stderr=%q", code, stderr.String())
	}
}

func TestStatusUsesStableProjectLinkAndDisconnectIsLocal(t *testing.T) {
	remote := newFakeCoconetServer()
	server := httptest.NewServer(remote)
	t.Cleanup(server.Close)
	remote.advertisedEndpoint = "https://shared.coconet.test"
	_, paths := configureBoundRepository(t, remote, server.URL)
	secondRoot := initTestRepository(t, "second-clone")
	if err := saveBinding(paths, fixtureBinding(secondRoot, remote.project.ProjectID, remote.account.AccountID, fixtureDeploymentTarget(server.URL))); err != nil {
		t.Fatal(err)
	}

	var first, second, stderr bytes.Buffer
	if code := runProjectStatus(context.Background(), nil, &first, &stderr); code != 0 {
		t.Fatalf("status failed code=%d stderr=%q output=%q", code, stderr.String(), first.String())
	}
	stderr.Reset()
	if code := runProjectStatus(context.Background(), nil, &second, &stderr); code != 0 {
		t.Fatalf("second status failed code=%d stderr=%q output=%q", code, stderr.String(), second.String())
	}
	if first.String() != second.String() || !strings.Contains(first.String(), remote.project.ProjectID) || !strings.Contains(first.String(), "/"+remote.project.ProjectID) {
		t.Fatal("status omitted stable Project ID / link", first.String())
	}

	var output bytes.Buffer
	if code := runDisconnect(context.Background(), nil, &output, &stderr); code != 0 {
		t.Fatalf("disconnect failed code=%d stderr=%q output=%q", code, stderr.String(), output.String())
	}
	if remote.left {
		t.Fatal("local disconnect revoked the shared Membership")
	}
	state, err := loadBindingState(paths)
	if err != nil || len(state.Bindings) != 1 || !samePath(state.Bindings[0].CollaborationRoot, secondRoot) {
		t.Fatalf("disconnect changed another local folder: %#v %v", state, err)
	}

	t.Setenv("COCONET_CWD", secondRoot)
	output.Reset()
	if code := runDisconnect(context.Background(), nil, &output, &stderr); code != 0 || !strings.Contains(output.String(), "Disconnected this folder") {
		t.Fatalf("repeat disconnect was not idempotent: code=%d output=%q stderr=%q", code, output.String(), stderr.String())
	}
}

func TestDisconnectRetiredProjectWorksWithoutServer(t *testing.T) {
	remote := newFakeCoconetServer()
	remote.project.ProjectID = "prj_1111111111111111"
	server := httptest.NewServer(remote)
	remote.advertisedEndpoint = server.URL
	repositoryRoot, paths := configureBoundRepository(t, remote, server.URL)
	server.Close()

	var output, stderr bytes.Buffer
	if code := runDisconnect(context.Background(), nil, &output, &stderr); code != 0 {
		t.Fatalf("offline disconnect returned %d, stderr=%q output=%q", code, stderr.String(), output.String())
	}
	binding, err := bindingAtRoot(paths, repositoryRoot)
	if err != nil || binding != nil {
		t.Fatalf("offline disconnect retained the binding: %#v %v", binding, err)
	}
}

func TestDisconnectRetainsBindingWhenLocalCleanupFails(t *testing.T) {
	remote := newFakeCoconetServer()
	server := httptest.NewServer(remote)
	t.Cleanup(server.Close)
	remote.advertisedEndpoint = server.URL
	repositoryRoot, paths := configureBoundRepository(t, remote, server.URL)

	cleanupFailure := errors.New("fixture local cleanup failure")
	var output, stderr bytes.Buffer
	code := runDisconnectWithBindingRemoval(
		context.Background(), nil, &output, &stderr,
		func(runtimePaths, string) error { return cleanupFailure },
	)
	if code != 1 || remote.left || !strings.Contains(stderr.String(), "Could not disconnect this folder") {
		t.Fatalf("partial disconnect boundary failed code=%d left=%t stderr=%q", code, remote.left, stderr.String())
	}
	binding, err := bindingAtRoot(paths, repositoryRoot)
	if err != nil || binding == nil {
		t.Fatalf("failed local cleanup unexpectedly removed binding: %#v %v", binding, err)
	}
}

func TestInitUsesSignedInAccountWithoutGitProfile(t *testing.T) {
	remote := newFakeCoconetServer()
	server := httptest.NewServer(remote)
	t.Cleanup(server.Close)
	repositoryRoot := filepath.Join(t.TempDir(), "missing-profile")
	if output, err := exec.Command("git", "init", "-q", repositoryRoot).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	configureRuntimeTest(t, t.TempDir(), repositoryRoot, server.URL)

	var output, stderr bytes.Buffer
	if code := runInit(context.Background(), []string{"--new"}, strings.NewReader("Manual Account\nmanual@coconet.test\n"), &output, &stderr); code != 0 {
		t.Fatalf("init with manual profile returned %d, stderr=%q output=%q", code, stderr.String(), output.String())
	}
	if remote.bootstrapName != "" || strings.Contains(output.String(), "Name:") || strings.Contains(output.String(), "Email:") {
		t.Fatal("init still tried to bootstrap from Git or manual profile")
	}

}

func TestInitUsesCurrentDirectoryAsNonGitCollaborationRoot(t *testing.T) {
	remote := newFakeCoconetServer()
	server := httptest.NewServer(remote)
	t.Cleanup(server.Close)
	remote.advertisedEndpoint = server.URL
	workspaceRoot := filepath.Join(t.TempDir(), "plain-workspace")
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	configureRuntimeTest(t, t.TempDir(), workspaceRoot, server.URL)

	var output, stderr bytes.Buffer
	input := "Plain Workspace User\nplain@coconet.test\n"
	if code := runInit(context.Background(), []string{"--new", "--verbose"}, strings.NewReader(input), &output, &stderr); code != 0 {
		t.Fatalf("non-Git init failed code=%d stderr=%q output=%q", code, stderr.String(), output.String())
	}
	paths, err := resolveRuntimePaths()
	if err != nil {
		t.Fatal(err)
	}
	binding, err := bindingAtRoot(paths, workspaceRoot)
	if err != nil || binding == nil || !samePath(binding.CollaborationRoot, workspaceRoot) {
		t.Fatalf("non-Git workspace binding is incorrect: %#v %v", binding, err)
	}
	if _, err := os.Stat(filepath.Join(workspaceRoot, ".coconet")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("non-Git init wrote workspace-local state: %v", err)
	}
	if !strings.Contains(output.String(), "Workspace") || !strings.Contains(output.String(), workspaceRoot) {
		t.Fatalf("verbose non-Git scope is missing: %q", output.String())
	}
}

func TestManageIsOnlyACompatibilityHint(t *testing.T) {
	var output, stderr bytes.Buffer
	if code := run(context.Background(), []string{"manage"}, strings.NewReader(""), &output, &stderr); code != 2 || !strings.Contains(stderr.String(), "was removed") {
		t.Fatalf("manage compatibility hint failed code=%d stderr=%q", code, stderr.String())
	}
}

type fakeCoconetServer struct {
	authorization           contracts.DeviceAuthorizationRequest
	mu                      sync.Mutex
	account                 contracts.Account
	project                 contracts.Project
	accessStatus            contracts.Status
	role                    contracts.ProjectRole
	left                    bool
	sessionRequests         int
	bootstrapName           string
	bootstrapEmail          string
	searchStatus            contracts.Status
	searchResults           []contracts.SearchSessionResult
	lastSearch              contracts.SearchSessionsRequest
	deploymentDelay         time.Duration
	searchDelay             time.Duration
	advertisedEndpoint      string
	connectRequests         int
	createTransactions      int
	createRequestIDs        map[string]bool
	dropFirstCreateResponse bool
	droppedCreateResponse   bool
	accessProjectID         string
}

func newFakeCoconetServer() *fakeCoconetServer {
	account := fixtureAccount("acct_fixture01", "Fixture Account")
	return &fakeCoconetServer{
		account: account,
		project: contracts.Project{
			SchemaVersion: 1,
			ProjectID:     "coco_1111111111111112",
			Name:          "fixture-project",
			CreatedBy:     account.AccountID,
			OwnerID:       account.AccountID,
			Status:        contracts.ProjectActive,
			CreatedAt:     "2026-08-24T10:00:00Z",
		},
		accessStatus:     contracts.StatusOK,
		role:             contracts.RoleMember,
		createRequestIDs: make(map[string]bool),
	}
}

func (f *fakeCoconetServer) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if request.URL.Path == "/v1/deployment" && f.deploymentDelay > 0 {
		time.Sleep(f.deploymentDelay)
	}
	if serveFixtureDeployment(response, request) {
		return
	}
	response.Header().Set("Content-Type", "application/json")
	encode := func(status int, value any) {
		response.WriteHeader(status)
		if value != nil {
			_ = json.NewEncoder(response).Encode(value)
		}
	}
	switch {
	case request.URL.Path == "/v1/accounts/bootstrap":
		var input contracts.AccountBootstrapRequest
		_ = json.NewDecoder(request.Body).Decode(&input)
		f.bootstrapName = input.DisplayName
		f.bootstrapEmail = input.Email
		f.account.DisplayName = input.DisplayName
		f.account.Email = input.Email
		encode(http.StatusCreated, contracts.AccessToken{SchemaVersion: 1, Status: contracts.StatusOK, AccessToken: "fixture-token-with-enough-entropy-1234", TokenType: "Bearer", Account: &f.account})
	case request.URL.Path == "/v1/projects" && request.Method == http.MethodGet:
		encode(http.StatusOK, contracts.DashboardProjectsResponse{SchemaVersion: 1, Status: contracts.StatusOK, Projects: []contracts.ProjectReference{}})
	case request.URL.Path == "/v1/account":
		encode(http.StatusOK, f.account)
	case request.URL.Path == "/v1/projects" && request.Method == http.MethodPost:
		var input contracts.CreateProjectRequest
		_ = json.NewDecoder(request.Body).Decode(&input)
		if !f.createRequestIDs[input.RequestID] {
			f.createRequestIDs[input.RequestID] = true
			f.createTransactions++
		}
		if f.dropFirstCreateResponse && !f.droppedCreateResponse {
			f.droppedCreateResponse = true
			return
		}
		membership := f.membership()
		encode(http.StatusCreated, f.access(membership))
	case request.URL.Path == "/v1/auth/device":
		var input contracts.DeviceAuthorizationRequest
		_ = json.NewDecoder(request.Body).Decode(&input)
		f.authorization = input
		f.connectRequests++
		encode(200, contracts.DeviceAuthorization{Status: contracts.StatusPending, DeviceCode: "fixture-device-code", AuthorizationID: strings.Repeat("a", 43), VerificationURI: "http://" + request.Host + "/#authorize=" + strings.Repeat("a", 43), Interval: 1, ExpiresIn: 60})
	case request.URL.Path == "/v1/auth/token":
		f.left = false
		encode(200, contracts.DeviceAuthorization{Status: contracts.StatusOK, AccessToken: "fixture-token-with-enough-entropy-1234", Account: &f.account, ProjectID: f.project.ProjectID, Directory: f.authorization.Directory})
	case strings.HasSuffix(request.URL.Path, "/access"):
		status := f.accessStatus
		if f.left {
			status = contracts.StatusForbidden
		}
		access := contracts.ProjectAccess{SchemaVersion: 1, Status: status, Capabilities: map[string]bool{"read": false, "write": false, "manage": false, "resume": false, "share": false}}
		if status == contracts.StatusOK || status == contracts.StatusProjectArchived {
			member := f.membership()
			access = f.access(member)
			access.Status = status
			if status == contracts.StatusProjectArchived {
				access.Project.Status = contracts.ProjectArchived
				access.Membership.Status = contracts.MembershipArchived
			}
		}
		encode(http.StatusOK, access)
	case request.Method == http.MethodDelete && strings.HasSuffix(request.URL.Path, "/membership"):
		f.left = true
		response.WriteHeader(http.StatusNoContent)
	case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/sessions/search"):
		if f.searchDelay > 0 {
			time.Sleep(f.searchDelay)
		}
		_ = json.NewDecoder(request.Body).Decode(&f.lastSearch)
		if f.searchStatus != "" && f.searchStatus != contracts.StatusOK {
			encode(http.StatusServiceUnavailable, contracts.ErrorEnvelope{SchemaVersion: 1, Status: f.searchStatus, RequestID: f.lastSearch.RequestID, Code: "search_unavailable", Message: "fixture search unavailable", Retryable: true})
			break
		}
		encode(http.StatusOK, contracts.SearchSessionsResponse{
			SchemaVersion: 1, Status: contracts.StatusOK, RequestID: f.lastSearch.RequestID,
			Project:             &contracts.ProjectReference{ProjectID: f.project.ProjectID, Name: f.project.Name, Status: f.project.Status},
			QueryInterpretation: contracts.SearchQueryInterpretation{Query: f.lastSearch.Query}, Results: f.searchResults,
		})
	case strings.Contains(request.URL.Path, "/sessions"):
		f.sessionRequests++
		encode(http.StatusNotFound, fixtureError("not_found", contracts.StatusNotFound))
	default:
		encode(http.StatusNotFound, fixtureError("not_found", contracts.StatusNotFound))
	}
}

func (f *fakeCoconetServer) membership() contracts.Membership {
	return contracts.Membership{SchemaVersion: 1, ProjectID: f.project.ProjectID, Account: f.account, Role: f.role, Status: contracts.MembershipActive, CreatedAt: "2026-08-24T10:00:00Z", UpdatedAt: "2026-08-24T10:00:00Z"}
}

func (f *fakeCoconetServer) access(member contracts.Membership) contracts.ProjectAccess {
	project := f.project
	if f.accessProjectID != "" {
		project.ProjectID = f.accessProjectID
	}
	return contracts.ProjectAccess{
		SchemaVersion: 1, Status: contracts.StatusOK, Project: &project, Membership: &member,
		Capabilities: map[string]bool{"read": true, "write": true, "manage": false, "resume": true, "share": true},
	}
}

func fixtureError(code string, status contracts.Status) contracts.ErrorEnvelope {
	return contracts.ErrorEnvelope{SchemaVersion: 1, Status: status, RequestID: "request-fixture-error", Code: code, Message: "fixture request failed", Retryable: false}
}

func configureRuntimeTest(t *testing.T, home, cwd, serverURL string) {
	t.Helper()
	setRuntimeTestHome(t, home)
	t.Setenv("COCONET_CWD", cwd)
	setRuntimeTestServer(t, serverURL)
	t.Setenv("COCONET_CODEX_COMMAND", "coconet-test-no-codex")
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex-fixture-test-only"))
	t.Setenv("COCONET_CLAUDE_COMMAND", "coconet-test-no-claude")
	t.Setenv("COCONET_CLAUDE_NODE", "coconet-test-no-node")
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "tclaude-test-only"))
	t.Setenv("COCONET_DISABLE_BACKGROUND_SYNC", "1")
	paths, err := resolveRuntimePaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := saveCredential(paths, fixtureDeploymentTarget(serverURL), fixtureAccount("acct_fixture01", "Fixture Account"), "fixture-token-with-enough-entropy-1234"); err != nil {
		t.Fatal(err)
	}
}

func configureBoundRepository(t *testing.T, remote *fakeCoconetServer, serverURL string) (string, runtimePaths) {
	t.Helper()
	repositoryRoot := initTestRepository(t, "bound-project")
	home := t.TempDir()
	configureRuntimeTest(t, home, repositoryRoot, serverURL)
	paths, err := resolveRuntimePaths()
	if err != nil {
		t.Fatal(err)
	}
	target := fixtureDeploymentTarget(serverURL)
	if err := saveCredential(paths, target, remote.account, "fixture-token-with-enough-entropy-1234"); err != nil {
		t.Fatal(err)
	}
	if err := saveBinding(paths, fixtureBinding(repositoryRoot, remote.project.ProjectID, remote.account.AccountID, target)); err != nil {
		t.Fatal(err)
	}
	return repositoryRoot, paths
}

func initTestRepository(t *testing.T, name string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	for key, value := range map[string]string{"user.name": "Fixture Account", "user.email": "fixture@coconet.test"} {
		if output, err := exec.Command("git", "-C", root, "config", key, value).CombinedOutput(); err != nil {
			t.Fatalf("git config %s: %v: %s", key, err, output)
		}
	}
	return root
}
