package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contracts"
)

func setRuntimeTestHome(t *testing.T, root string) {
	t.Helper()
	previous := runtimeConfigDirectory
	runtimeConfigDirectory = func() (string, bool, error) { return root, true, nil }
	t.Cleanup(func() { runtimeConfigDirectory = previous })
}

func setRuntimeTestServer(t *testing.T, server string) {
	t.Helper()
	paths, err := resolveRuntimePaths()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := saveDefaultServer(paths, server); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialsAndRuntimeStateAreIsolatedByDeployment(t *testing.T) {
	home := t.TempDir()
	setRuntimeTestHome(t, home)
	paths, err := resolveRuntimePaths()
	if err != nil {
		t.Fatal(err)
	}
	first := deploymentTarget{DeploymentID: "dpl_isolated01", ServerURL: "https://one.coconet.test", ServerPublicKeySHA256: strings.Repeat("1", 64)}
	second := deploymentTarget{DeploymentID: "dpl_isolated02", ServerURL: "https://two.coconet.test", ServerPublicKeySHA256: strings.Repeat("2", 64)}
	account := fixtureAccount("acct_isolated01", "Isolated")
	if err := saveCredential(paths, first, account, "first-token-with-enough-entropy-1234"); err != nil {
		t.Fatal(err)
	}
	if err := saveCredential(paths, second, account, "second-token-with-enough-entropy-1234"); err != nil {
		t.Fatal(err)
	}
	firstCredential, firstExists, err := loadCredential(paths, first)
	if err != nil || !firstExists || firstCredential.AccessToken != "first-token-with-enough-entropy-1234" {
		t.Fatalf("unexpected first credential %#v %v %v", firstCredential, firstExists, err)
	}
	secondCredential, secondExists, err := loadCredential(paths, second)
	if err != nil || !secondExists || secondCredential.AccessToken != "second-token-with-enough-entropy-1234" {
		t.Fatalf("unexpected second credential %#v %v %v", secondCredential, secondExists, err)
	}
	changedEndpoint := first
	changedEndpoint.ServerURL = "https://moved.coconet.test"
	if credential, exists, err := loadCredential(paths, changedEndpoint); err != nil || !exists || credential.Account.AccountID != account.AccountID {
		t.Fatalf("same Deployment credential was not reusable through a verified local route: %#v %v %v", credential, exists, err)
	}

	if deploymentSyncPath(paths, first.DeploymentID) == deploymentSyncPath(paths, second.DeploymentID) {
		t.Fatal("deployment storage paths collide")
	}
}

func TestCredentialAndBindingFilesAreUserOnlyAndScopeByLongestRoot(t *testing.T) {
	home := t.TempDir()
	setRuntimeTestHome(t, home)
	paths, err := resolveRuntimePaths()
	if err != nil {
		t.Fatal(err)
	}
	account := fixtureAccount("acct_fixture01", "Fixture")
	target := fixtureDeploymentTarget("https://coconet.test")
	if err := saveCredential(paths, target, account, "fixture-access-token-with-enough-entropy"); err != nil {
		t.Fatal(err)
	}
	credential, exists, err := loadCredential(paths, target)
	if err != nil || !exists || credential.AccessToken == "" {
		t.Fatalf("load credential: %#v %v %v", credential, exists, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(paths.credentialPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("credential mode is %o", info.Mode().Perm())
		}
	}

	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(filepath.Join(nested, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	bindings := []contracts.LocalBinding{
		fixtureBinding(root, "coco_parent001", account.AccountID),
		fixtureBinding(nested, "coco_nested001", account.AccountID),
	}
	for _, binding := range bindings {
		if err := saveBinding(paths, binding); err != nil {
			t.Fatal(err)
		}
	}
	selected, err := bindingForAnyCWD(paths, filepath.Join(nested, "child"))
	if err != nil || selected == nil || selected.ProjectID != "coco_nested001" {
		t.Fatalf("unexpected nested binding %#v %v", selected, err)
	}
	selected, err = bindingForAnyCWD(paths, filepath.Join(nested, "child"))
	if err != nil || selected == nil || selected.ProjectID != "coco_nested001" {
		t.Fatalf("unexpected deployment-independent nested binding %#v %v", selected, err)
	}
	if _, err := removeBindingAtRoot(paths, nested); err != nil {
		t.Fatal(err)
	}
	selected, err = bindingForAnyCWD(paths, filepath.Join(nested, "child"))
	if err != nil || selected == nil || selected.ProjectID != "coco_parent001" {
		t.Fatalf("unexpected fallback binding %#v %v", selected, err)
	}
}

func TestGitRepositoryDiscoverySupportsSymlinkAndIgnoresLegacyDeclaration(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repository")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("git", "init", "-q", root)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	legacyDirectory := filepath.Join(root, ".coconet")
	if err := os.MkdirAll(legacyDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDirectory, "project.json"), []byte(`{"schema_version":1,"project_id":"bad","secret":"ignored"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(t.TempDir(), "repository-link")
	if err := os.Symlink(root, symlink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	repository, err := discoverGitRepository(symlink)
	if err != nil {
		t.Fatal(err)
	}
	canonical, _ := canonicalPath(root)
	if repository.root != canonical {
		t.Fatalf("unexpected repository %#v", repository)
	}
	if _, err := discoverGitRepository(root); err != nil {
		t.Fatalf("legacy declaration affected Git discovery: %v", err)
	}
}

func TestWorktreeDiscoveryAndMovedRepositoryKeepLocalBindingIdentity(t *testing.T) {
	root := initTestRepository(t, "worktree-source")
	command := exec.Command("git", "-C", root, "config", "user.name", "Fixture")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git config: %v: %s", err, output)
	}
	command = exec.Command("git", "-C", root, "config", "user.email", "fixture@coconet.test")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git config: %v: %s", err, output)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	command = exec.Command("git", "-C", root, "add", "README.md")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, output)
	}
	command = exec.Command("git", "-C", root, "commit", "-q", "-m", "fixture")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, output)
	}
	worktree := filepath.Join(t.TempDir(), "worktree")
	command = exec.Command("git", "-C", root, "worktree", "add", "-q", "-b", "fixture-worktree", worktree)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git worktree: %v: %s", err, output)
	}
	discovered, err := discoverGitRepository(worktree)
	if err != nil || !samePath(discovered.root, worktree) {
		t.Fatalf("worktree root mismatch: %#v %v", discovered, err)
	}

	movableParent := t.TempDir()
	movable := filepath.Join(movableParent, "before")
	if output, err := exec.Command("git", "init", "-q", movable).CombinedOutput(); err != nil {
		t.Fatalf("git init movable: %v: %s", err, output)
	}
	home := t.TempDir()
	setRuntimeTestHome(t, home)
	paths, _ := resolveRuntimePaths()
	account := fixtureAccount("acct_fixture01", "Fixture")
	if err := saveBinding(paths, fixtureBinding(movable, "coco_movable001", account.AccountID)); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(movableParent, "after")
	if err := os.Rename(movable, moved); err != nil {
		t.Fatal(err)
	}
	movedRepository, err := discoverGitRepository(moved)
	if err != nil {
		t.Fatalf("moved repository identity mismatch: %#v %v", movedRepository, err)
	}
	if err := saveBinding(paths, fixtureBinding(moved, "coco_movable001", account.AccountID)); err != nil {
		t.Fatal(err)
	}
	state, err := loadBindingState(paths)
	if err != nil || len(state.Bindings) != 1 || !samePath(state.Bindings[0].CollaborationRoot, movedRepository.root) {
		t.Fatalf("stale moved binding was not migrated: %#v %v", state, err)
	}
}

func fixtureAccount(id, name string) contracts.Account {
	return contracts.Account{
		SchemaVersion: contracts.SchemaVersion,
		AccountID:     id,
		DisplayName:   name,
		Email:         "fixture@coconet.test",
		CreatedAt:     "2026-08-24T10:00:00Z",
	}
}

func fixtureBinding(root, projectID, accountID string, targets ...deploymentTarget) contracts.LocalBinding {
	target := fixtureDeploymentTarget("https://coconet.test")
	if len(targets) > 0 {
		target = targets[0]
	}
	return contracts.LocalBinding{
		SchemaVersion: contracts.LocalBindingSchemaVersion, DeploymentID: target.DeploymentID,
		LocalServerURL: target.ServerURL, ServerPublicKeySHA256: target.ServerPublicKeySHA256,
		ProjectID: projectID, AccountID: accountID, CollaborationRoot: root,
		ConnectedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
}
