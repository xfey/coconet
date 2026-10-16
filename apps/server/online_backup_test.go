package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/relay"
	"github.com/xfey/coconet/packages/workgraph"
)

type backupExternalFixture struct{}

func (backupExternalFixture) Identity() string { return "backup-external-fixture" }
func (backupExternalFixture) UploadURL(context.Context, string, int64, time.Duration) (string, error) {
	return "", errors.New("unused")
}
func (backupExternalFixture) DownloadURL(context.Context, string, time.Duration) (string, error) {
	return "", errors.New("unused")
}
func (backupExternalFixture) Seal(context.Context, string, string, int64) error {
	return errors.New("unused")
}
func (backupExternalFixture) Remove(context.Context, string) error { return nil }

func TestOnlineBackupSocketRestoresCompactedGraphAndIdentity(t *testing.T) {
	t.Setenv("COCONET_RELAY_HOME", "")
	ctx := context.Background()
	// Unix socket paths have a small OS length limit; keep the fixture short.
	root, err := os.MkdirTemp("", "coco-backup-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	dataRoot := filepath.Join(root, "data")
	var stdout, stderr bytes.Buffer
	if code := runServerCLI(ctx, operatorInitArgs(dataRoot), &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	config, identity, s, objects, err := openConfiguredServer(ctx, filepath.Join(dataRoot, "server.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	if err := s.configureCapacity(); err != nil {
		t.Fatal(err)
	}
	s.relay, err = relay.OpenWithOptions(config.DataPath+".relay", relay.Options{Backend: backupExternalFixture{}, Guard: s.capacity})
	if err != nil {
		t.Fatal(err)
	}
	a := newAuthority(s, objects)
	account, err := a.bootstrapAccount(contracts.AccountBootstrapRequest{SchemaVersion: 1, DisplayName: "Online backup"})
	if err != nil {
		t.Fatal(err)
	}
	project, err := a.createProject(*account.Account, contracts.CreateProjectRequest{SchemaVersion: 1, RequestID: "project-backup", Name: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	producer, _, _ := publishedGraphFixture(t, a, newHandler("fixture", identity, a).(*serverHandler), *account.Account, project.Project.ProjectID, account.AccessToken)
	state, _ := producer.Store.State(project.Project.ProjectID)
	if n, err := s.relay.CompactHistory(ctx, 0, 1024); err != nil || n == 0 {
		t.Fatal(n, err)
	}
	stop, err := startOperatorSocket(ctx, config, s)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	path := filepath.Join(dataRoot, "operator.sock")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}}
	defer client.CloseIdleConnections()
	for _, name := range []string{"../escape", "valid"} {
		body, _ := json.Marshal(map[string]string{"name": name})
		response, err := client.Post("http://operator/backup", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		want := 200
		if name != "valid" {
			want = 400
		}
		if response.StatusCode != want {
			t.Fatal(response.StatusCode)
		}
	}
	backup := filepath.Join(dataRoot, "backups", "automatic", "valid", "server.db")
	restoredPath, err := restoreDeployment(ctx, backup, filepath.Join(root, "restored"))
	if err != nil {
		t.Fatal(err)
	}
	config2, identity2, restored, objects2, err := openConfiguredServer(ctx, restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.close()
	if identity2.deploymentID != identity.deploymentID {
		t.Fatal("identity changed")
	}
	if _, err := newAuthority(restored, objects2).authenticate(account.AccessToken); err != nil {
		t.Fatal("login mapping changed", err)
	}
	restored.relay, err = relay.OpenWithOptions(config2.DataPath+".relay", relay.Options{Backend: backupExternalFixture{}})
	if err != nil {
		t.Fatal(err)
	}
	page, err := restored.relay.Checkpoint(project.Project.ProjectID)
	if err != nil || page.Checkpoint == nil {
		t.Fatal(err)
	}
	var recovered workgraph.State
	if err := json.Unmarshal(page.Checkpoint.Checkpoint.Payload, &recovered); err != nil || workgraph.Digest(recovered) != workgraph.Digest(state) {
		t.Fatal("DAG differs", err)
	}
	if _, err := restored.relay.Since(project.Project.ProjectID, 0, 100); !errors.Is(err, contracts.ErrRelayHistoryCompacted) {
		t.Fatal("history floor lost", err)
	}
	if _, err := a.createProject(*account.Account, contracts.CreateProjectRequest{SchemaVersion: 1, RequestID: "after-backup", Name: "still writable"}); err != nil {
		t.Fatal("backup left writes locked", err)
	}
}
