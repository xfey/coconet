package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/projectlink"
	bolt "go.etcd.io/bbolt"
)

func TestOperatorInitServeStatusAndOpenBootstrap(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "operator")
	address := unusedServerAddress(t)
	serverURL := "http://" + address
	var output, stderr bytes.Buffer
	if code := runServerCLI(context.Background(), []string{
		"init", "--data-dir", dataDir, "--listen", address, "--advertised-endpoint", serverURL,
	}, &output, &stderr); code != 0 {
		t.Fatalf("init failed code=%d stderr=%q", code, stderr.String())
	}
	configPath := filepath.Join(dataDir, "server.json")
	config, err := loadServerConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if config.SchemaVersion != serverConfigSchemaVersion || config.AdvertisedEndpoint != serverURL || config.RegistrationTokenSHA256 != "" || config.ArtifactDriver != "filesystem" || config.ArtifactPath != filepath.Join(dataDir, "artifacts") {
		t.Fatalf("unexpected initialized config %#v", config)
	}
	if content := string(mustReadFile(t, configPath)); strings.Contains(content, "registration_token") || strings.Contains(content, "require_artifact_store") {
		t.Fatalf("schema 3 retained a legacy credential field: %s", content)
	}
	for _, path := range []string{configPath, filepath.Join(dataDir, "deployment.json"), filepath.Join(dataDir, "server.db")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode is %o", path, info.Mode().Perm())
		}
	}
	if code := runServerCLI(context.Background(), []string{"init", "--data-dir", dataDir, "--advertised-endpoint", serverURL}, &output, &stderr); code == 0 {
		t.Fatal("init replaced an existing Deployment")
	}

	var accountOutput, accountErrors bytes.Buffer
	if code := runServerAccount(context.Background(), []string{"create", "--config", configPath, "--name", "Operator"}, &accountOutput, &accountErrors); code != 0 {
		t.Fatalf("create local account: %s", accountErrors.String())
	}
	creatorToken := strings.TrimSpace(accountOutput.String())
	serveCtx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		done <- runServerCLI(serveCtx, []string{"serve", "--config", configPath}, &bytes.Buffer{}, &bytes.Buffer{})
	}()
	waitForServerHealth(t, serverURL)

	var created contracts.ProjectAccess
	requestJSON(t, http.MethodPost, serverURL+"/v1/projects", creatorToken, contracts.CreateProjectRequest{
		SchemaVersion: contracts.SchemaVersion, RequestID: "request-operator-project", Name: "operator-project",
	}, http.StatusCreated, &created)

	tracked := []string{configPath, config.IdentityPath, config.DataPath}
	before := snapshotFiles(t, tracked)
	output.Reset()
	stderr.Reset()
	if code := runServerCLI(context.Background(), []string{"status", "--config", configPath}, &output, &stderr); code != 0 || !strings.Contains(output.String(), "Coconet Server status: ready") || !strings.Contains(output.String(), "Process / endpoint: reachable") {
		t.Fatalf("status failed code=%d stdout=%q stderr=%q", code, output.String(), stderr.String())
	}
	after := snapshotFiles(t, tracked)
	if !maps.Equal(before, after) {
		t.Fatalf("read-only status changed persistent Server files:\nbefore=%#v\nafter=%#v", before, after)
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("serve exited with %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop")
	}
}

func TestOperatorBboltBackupAndRestore(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "operator")
	var output, stderr bytes.Buffer
	if code := runServerCLI(context.Background(), operatorInitArgs(dataDir), &output, &stderr); code != 0 {
		t.Fatalf("init failed: %s", stderr.String())
	}
	configPath := filepath.Join(dataDir, "server.json")
	config, err := loadServerConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	storage, err := openStore(config.DataPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newAuthority(storage).bootstrapAccount(contracts.AccountBootstrapRequest{SchemaVersion: 1, DisplayName: "Before Backup", Email: "before@coconet.test"}); err != nil {
		t.Fatal(err)
	}
	if err := storage.close(); err != nil {
		t.Fatal(err)
	}

	backupPath := filepath.Join(t.TempDir(), "backup", "server.db")
	if code := runServerCLI(context.Background(), []string{"backup", "--config", configPath, "--output", backupPath}, &output, &stderr); code != 0 {
		t.Fatalf("backup failed code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(output.String(), "Complete backup") || !strings.Contains(output.String(), "native objects") {
		t.Fatalf("backup boundary was not explicit: %q", output.String())
	}
	storage, err = openStore(config.DataPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newAuthority(storage).bootstrapAccount(contracts.AccountBootstrapRequest{SchemaVersion: 1, DisplayName: "After Backup", Email: "after@coconet.test"}); err != nil {
		t.Fatal(err)
	}
	if err := storage.close(); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	stderr.Reset()
	if code := runServerCLI(context.Background(), []string{"restore", "--config", configPath, "--input", backupPath}, &output, &stderr); code != 0 {
		t.Fatalf("restore failed code=%d stderr=%q", code, stderr.String())
	}
	rollback := strings.TrimSpace(strings.Split(output.String(), "retained at ")[1])
	if _, err := os.Stat(rollback); err != nil {
		t.Fatalf("restore rollback is missing: %v", err)
	}
	storage, err = openStore(config.DataPath)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.close()
	if err := storage.view(func(state databaseState) error {
		if state.Accounts.Len() != 1 {
			t.Fatalf("restore retained post-backup mutations: %d accounts", state.Accounts.Len())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestOperatorRestorePreservesLegacySchemaSnapshot(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "operator-legacy-restore")
	var output, stderr bytes.Buffer
	if code := runServerCLI(context.Background(), operatorInitArgs(dataDir), &output, &stderr); code != 0 {
		t.Fatalf("init failed: %s", stderr.String())
	}
	configPath := filepath.Join(dataDir, "server.json")
	config, err := loadServerConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	legacySnapshot := filepath.Join(t.TempDir(), "schema-one.db")
	if err := backupBbolt(config.DataPath, legacySnapshot); err != nil {
		t.Fatal(err)
	}
	legacy, err := bolt.Open(legacySnapshot, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Update(func(tx *bolt.Tx) error {
		return setFixtureSchema(t, tx, []byte{1})
	}); err != nil {
		_ = legacy.Close()
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(legacySnapshot)
	if err != nil {
		t.Fatal(err)
	}
	output.Reset()
	stderr.Reset()
	identity, err := loadExistingDeploymentIdentityReadOnly(config.IdentityPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restoreBbolt(config.DataPath, legacySnapshot, identity.deploymentID); err != nil {
		t.Fatal(err)
	}

	after, err := os.ReadFile(legacySnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("restore validation mutated its schema 1 input snapshot")
	}
	live, err := bolt.Open(config.DataPath, 0o600, &bolt.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := live.View(func(tx *bolt.Tx) error {
		if version := tx.Bucket(metaBucket).Get(schemaKey); len(version) != 1 || version[0] != 1 {
			t.Fatalf("restore rewrote legacy schema marker: %v", version)
		}
		return nil
	}); err != nil {
		_ = live.Close()
		t.Fatal(err)
	}
	if err := live.Close(); err != nil {
		t.Fatal(err)
	}
	storage, err := openStore(config.DataPath)
	if err != nil {
		t.Fatalf("current Server could not upgrade the preserved schema 1 restore: %v", err)
	}
	if deploymentID, err := storage.deploymentID(); err != nil || deploymentID == "" {
		_ = storage.close()
		t.Fatalf("restored Deployment identity is invalid: %q %v", deploymentID, err)
	}
	if err := storage.close(); err != nil {
		t.Fatal(err)
	}
}

func TestOperatorConfigSchemaAndEndpointValidation(t *testing.T) {
	validEndpoints := []string{
		projectlink.HostedEndpoint,
		"https://coconet.example.com",
		"https://coconet.example.com:8443",
		"https://192.0.2.10:8443",
		"https://[2001:db8::10]:8443",
		"http://192.0.2.10:7788",
		"http://coconet.example.com:7788",
		"http://127.0.0.1:8787",
		"http://[::1]:8787",
	}
	for index, endpoint := range validEndpoints {
		t.Run("valid-"+strings.ReplaceAll(endpoint, ":", "_"), func(t *testing.T) {
			dataDir := filepath.Join(t.TempDir(), string(rune('a'+index)))
			var output, stderr bytes.Buffer
			if code := runServerCLI(context.Background(), []string{"init", "--data-dir", dataDir, "--advertised-endpoint", endpoint}, &output, &stderr); code != 0 {
				t.Fatalf("endpoint %q rejected: %s", endpoint, stderr.String())
			}
			config, err := loadServerConfig(filepath.Join(dataDir, "server.json"))
			if err != nil || config.AdvertisedEndpoint != endpoint {
				t.Fatalf("endpoint was not retained canonically: %#v %v", config, err)
			}
			if config.Listen != "127.0.0.1:7788" {
				t.Fatalf("unexpected default listen address: %q", config.Listen)
			}
		})
	}
	for _, endpoint := range []string{"", "https://coconet.example.com/path", "HTTPS://coconet.example.com", "coconet.example.com"} {
		t.Run("invalid-"+strings.ReplaceAll(endpoint, ":", "_"), func(t *testing.T) {
			var output, stderr bytes.Buffer
			arguments := []string{"init", "--data-dir", filepath.Join(t.TempDir(), "invalid")}
			if endpoint != "" {
				arguments = append(arguments, "--advertised-endpoint", endpoint)
			}
			if code := runServerCLI(context.Background(), arguments, &output, &stderr); code != 2 {
				t.Fatalf("invalid endpoint %q returned %d", endpoint, code)
			}
		})
	}

	t.Run("schema 3 rejects legacy fields", func(t *testing.T) {
		dataDir := filepath.Join(t.TempDir(), "strict")
		var output, stderr bytes.Buffer
		if code := runServerCLI(context.Background(), operatorInitArgs(dataDir), &output, &stderr); code != 0 {
			t.Fatal(stderr.String())
		}
		configPath := filepath.Join(dataDir, "server.json")
		content := string(mustReadFile(t, configPath))
		content = strings.Replace(content, `"listen":`, "\"registration_token_sha256\": \"\",\n  \"listen\":", 1)
		if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadServerConfigReadOnly(configPath); err == nil || !strings.Contains(err.Error(), "legacy field") {
			t.Fatalf("schema 3 accepted registration field: %v", err)
		}
	})
}

func TestOperatorLegacyConfigReadCompatibilityAndServeBoundary(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "legacy")
	var output, stderr bytes.Buffer
	if code := runServerCLI(context.Background(), operatorInitArgs(dataDir), &output, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	configPath := filepath.Join(dataDir, "server.json")
	config, err := loadServerConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	config.SchemaVersion = artifactServerConfigSchemaVersion
	config.AdvertisedEndpoint = ""
	config.RegistrationTokenSHA256 = hashToken("legacy-registration-token")
	if err := atomicWriteServerJSON(configPath, config, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadServerConfigReadOnly(configPath); err != nil {
		t.Fatalf("schema 2 config is no longer readable: %v", err)
	}
	before := snapshotFiles(t, []string{configPath, config.IdentityPath, config.DataPath})
	if code := runServerCLI(context.Background(), []string{"serve", "--config", configPath}, &output, &stderr); code == 0 {
		t.Fatal("serve accepted legacy config without advertised_endpoint")
	}
	after := snapshotFiles(t, []string{configPath, config.IdentityPath, config.DataPath})
	if !maps.Equal(before, after) {
		t.Fatalf("rejected legacy serve mutated persistent state: before=%#v after=%#v", before, after)
	}
}

func TestOperatorStatusNeverCreatesOrRepairsPersistentState(t *testing.T) {
	t.Run("missing state stays missing", func(t *testing.T) {
		dataDir := filepath.Join(t.TempDir(), "missing")
		configPath := filepath.Join(dataDir, "server.json")
		config := serverConfig{
			SchemaVersion: serverConfigSchemaVersion, Listen: "127.0.0.1:8080", AdvertisedEndpoint: projectlink.HostedEndpoint,
			MetadataDriver: "bbolt", DataPath: filepath.Join(dataDir, "server.db"), IdentityPath: filepath.Join(dataDir, "deployment.json"),
			ArtifactDriver: "filesystem", ArtifactPath: filepath.Join(dataDir, "artifacts"),
		}
		if err := atomicWriteServerJSON(configPath, config, 0o600); err != nil {
			t.Fatal(err)
		}
		before := snapshotFiles(t, []string{configPath})
		var output, stderr bytes.Buffer
		if code := runServerCLI(context.Background(), []string{"status", "--config", configPath}, &output, &stderr); code == 0 {
			t.Fatal("status accepted missing Deployment state")
		}
		if after := snapshotFiles(t, []string{configPath}); !maps.Equal(before, after) {
			t.Fatalf("status changed the only existing file: before=%#v after=%#v", before, after)
		}
		for _, path := range []string{config.IdentityPath, config.DataPath, config.ArtifactPath} {
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatalf("status created missing path %s: %v", path, err)
			}
		}
	})

	t.Run("unsafe permissions are reported but not repaired", func(t *testing.T) {
		dataDir := filepath.Join(t.TempDir(), "permissions")
		var output, stderr bytes.Buffer
		if code := runServerCLI(context.Background(), operatorInitArgs(dataDir), &output, &stderr); code != 0 {
			t.Fatal(stderr.String())
		}
		identityPath := filepath.Join(dataDir, "deployment.json")
		if err := os.Chmod(identityPath, 0o644); err != nil {
			t.Fatal(err)
		}
		if code := runServerCLI(context.Background(), []string{"status", "--config", filepath.Join(dataDir, "server.json")}, &output, &stderr); code == 0 || !strings.Contains(stderr.String(), "broader than 0600") {
			t.Fatalf("status accepted unsafe identity permissions code=%d stderr=%q", code, stderr.String())
		}
		if info, err := os.Stat(identityPath); err != nil || info.Mode().Perm() != 0o644 {
			t.Fatalf("status repaired identity permissions: %#v %v", info, err)
		}
	})
}

func TestOperatorArtifactConfigAndBackendBinding(t *testing.T) {
	t.Setenv("COCONET_S3_ENDPOINT", "")
	t.Setenv("COCONET_S3_BUCKET", "")
	t.Setenv("COCONET_S3_ACCESS_KEY", "")
	t.Setenv("COCONET_S3_SECRET_KEY", "")

	t.Run("custom filesystem path is initialized", func(t *testing.T) {
		dataDir := filepath.Join(t.TempDir(), "custom-data")
		artifactPath := filepath.Join(t.TempDir(), "custom-artifacts")
		var output, stderr bytes.Buffer
		arguments := append(operatorInitArgs(dataDir), "--artifact-path", artifactPath)
		if code := runServerCLI(context.Background(), arguments, &output, &stderr); code != 0 {
			t.Fatalf("custom filesystem init failed: %s", stderr.String())
		}
		config, err := loadServerConfig(filepath.Join(dataDir, "server.json"))
		if err != nil || config.ArtifactDriver != "filesystem" || config.ArtifactPath != artifactPath {
			t.Fatalf("unexpected custom filesystem config %#v %v", config, err)
		}
		for _, path := range []string{artifactPath, filepath.Join(artifactPath, "objects"), filepath.Join(artifactPath, ".tmp")} {
			if info, err := os.Stat(path); err != nil || !info.IsDir() {
				t.Fatalf("custom artifact directory is missing: %s %#v %v", path, info, err)
			}
		}
	})

	t.Run("explicit s3 never falls back", func(t *testing.T) {
		dataDir := filepath.Join(t.TempDir(), "s3")
		var output, stderr bytes.Buffer
		arguments := append(operatorInitArgs(dataDir), "--artifact-store", "s3")
		if code := runServerCLI(context.Background(), arguments, &output, &stderr); code != 0 {
			t.Fatalf("s3 init failed: %s", stderr.String())
		}
		config, err := loadServerConfig(filepath.Join(dataDir, "server.json"))
		if err != nil || config.ArtifactDriver != "s3" || config.ArtifactPath != "" {
			t.Fatalf("unexpected s3 config %#v %v", config, err)
		}
		if _, _, err := inspectConfiguredArtifactStore(context.Background(), config); err == nil || !strings.Contains(err.Error(), "COCONET_S3_ENDPOINT is required") {
			t.Fatalf("explicit s3 silently fell back: %v", err)
		}
	})

	t.Run("filesystem path change is rejected", func(t *testing.T) {
		dataDir := filepath.Join(t.TempDir(), "binding")
		var output, stderr bytes.Buffer
		if code := runServerCLI(context.Background(), operatorInitArgs(dataDir), &output, &stderr); code != 0 {
			t.Fatalf("init failed: %s", stderr.String())
		}
		configPath := filepath.Join(dataDir, "server.json")
		config, err := loadServerConfig(configPath)
		if err != nil {
			t.Fatal(err)
		}
		config.ArtifactPath = filepath.Join(dataDir, "different-artifacts")
		if _, err := openFilesystemArtifactStore(config.ArtifactPath); err != nil {
			t.Fatal(err)
		}
		if err := atomicWriteServerJSON(configPath, config, 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, storage, _, err := openConfiguredServer(context.Background(), configPath)
		if storage != nil {
			_ = storage.close()
		}
		if err == nil || !strings.Contains(err.Error(), "different artifact backend") {
			t.Fatalf("backend identity change was accepted: %v", err)
		}
	})
}

func TestOperatorRemovedCommandsStayOutsidePublicSurface(t *testing.T) {
	for _, command := range []string{"check", "health", "registration-token"} {
		var output, stderr bytes.Buffer
		if code := runServerCLI(context.Background(), []string{command}, &output, &stderr); code != 2 {
			t.Fatalf("removed command %q returned %d", command, code)
		}
		if strings.Contains(stderr.String(), command+"|") || strings.Contains(stderr.String(), "|"+command) {
			t.Fatalf("removed command %q remains in usage: %q", command, stderr.String())
		}
	}
}

type fileSnapshot struct {
	SHA256  [sha256.Size]byte
	Size    int64
	Mode    os.FileMode
	ModTime int64
}

func snapshotFiles(t *testing.T, paths []string) map[string]fileSnapshot {
	t.Helper()
	result := make(map[string]fileSnapshot, len(paths))
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		result[path] = fileSnapshot{SHA256: sha256.Sum256(content), Size: info.Size(), Mode: info.Mode(), ModTime: info.ModTime().UnixNano()}
	}
	return result
}

func operatorInitArgs(dataDir string) []string {
	return []string{"init", "--data-dir", dataDir, "--advertised-endpoint", projectlink.HostedEndpoint}
}

func unusedServerAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	return address
}

func waitForServerHealth(t *testing.T, serverURL string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(serverURL + "/healthz")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("Server did not become ready")
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value any
	if err := json.Unmarshal(content, &value); err != nil {
		t.Fatal(err)
	}
	return content
}
