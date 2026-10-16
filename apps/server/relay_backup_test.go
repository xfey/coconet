package main

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/replication"
	"github.com/xfey/coconet/packages/workgraph"
)

func TestCompleteRelayDeploymentRestorePreservesIdentityAndFixedSources(t *testing.T) {
	t.Setenv("COCONET_RELAY_HOME", "")
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "source")
	var stdout, stderr bytes.Buffer
	if code := runServerCLI(ctx, operatorInitArgs(root), &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	configPath := filepath.Join(root, "server.json")
	config, identity, storage, objects, err := openConfiguredServer(ctx, configPath)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.close()
	a := newAuthority(storage, objects)
	access, err := a.bootstrapAccount(contracts.AccountBootstrapRequest{SchemaVersion: 1, DisplayName: "Restore fixture"})
	if err != nil {
		t.Fatal(err)
	}
	project, err := a.createProject(*access.Account, contracts.CreateProjectRequest{SchemaVersion: 1, RequestID: "backup-project", Name: "Recovery fixture"})
	if err != nil {
		t.Fatal(err)
	}
	producer, _, _ := publishedGraphFixture(t, a, newHandler("fixture", identity, a).(*serverHandler), *access.Account, project.Project.ProjectID, access.AccessToken)
	if err := producer.SyncContents(ctx, project.Project.ProjectID); err != nil {
		t.Fatal(err)
	}
	state, err := producer.Store.State(project.Project.ProjectID)
	if err != nil || len(state.Nodes) != 2 || len(state.Nodes["docs"].Parents) != 1 || len(state.Library) != 1 {
		t.Fatal("fixture must have a graph, relation and fixed Library entry", err)
	}
	if err := storage.close(); err != nil {
		t.Fatal(err)
	}
	before, _ := hashBackupFile(config.DataPath)
	backup := filepath.Join(t.TempDir(), "complete.db")
	if code := runServerCLI(ctx, []string{"backup", "--config", configPath, "--output", backup}, &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	manifest, err := readBackupManifest(backup)
	if err != nil || manifest.Format != 2 || manifest.Relay == nil {
		t.Fatal("backup omitted Relay", err)
	}
	if err := requireLegacyRestore(config, backup); err == nil {
		t.Fatal("allowed partial in-place restore")
	}
	destination := filepath.Join(t.TempDir(), "restored")
	if code := runServerCLI(ctx, []string{"restore", "--input", backup, "--data-dir", destination}, &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	after, _ := hashBackupFile(config.DataPath)
	if before != after {
		t.Fatal("backup/restore modified the source authority")
	}
	_, restoredIdentity, restored, restoredObjects, err := openConfiguredServer(ctx, filepath.Join(destination, "server.json"))
	if err != nil {
		t.Fatal("restored deployment cannot start", err)
	}
	defer restored.close()
	if restoredIdentity.deploymentID != identity.deploymentID || !bytes.Equal(restoredIdentity.privateKey, identity.privateKey) {
		t.Fatal("restore changed Deployment trust identity")
	}
	authority := newAuthority(restored, restoredObjects)
	actor, err := authority.authenticate(access.AccessToken)
	if err != nil || actor.AccountID != access.Account.AccountID {
		t.Fatal("restore changed account identity or token mapping", err)
	}
	server := httptest.NewTLSServer(newHandler("fixture", restoredIdentity, authority))
	defer server.Close()
	local, err := graphstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	consumer := &replication.Client{Store: local, Remote: replication.HTTP{BaseURL: server.URL, Token: access.AccessToken, Client: server.Client()}, Account: actor.AccountID, Device: "fresh-consumer"}
	if err := consumer.Sync(ctx, project.Project.ProjectID); err != nil {
		t.Fatal("fresh consumer cannot sync restored Relay", err)
	}
	recovered, _ := local.State(project.Project.ProjectID)
	if workgraph.Digest(recovered) != workgraph.Digest(state) {
		t.Fatal("graph, Library or fixed sources differ after recovery")
	}
	for _, node := range recovered.Nodes {
		if err := consumer.Pull(ctx, project.Project.ProjectID, node.Source, true); err != nil {
			t.Fatal("fixed native/compact source failed recovery", err)
		}
		if _, err := local.ArtifactFile(recovered.Snapshots[node.Source.Key()].Artifact); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := restoreDeployment(ctx, backup, destination); err == nil {
		t.Fatal("restore replaced an initialized target")
	}
	// Failure after authority/manifest verification leaves no startable config.
	files, _ := filepath.Glob(filepath.Join(backup+".relay", "objects", "*", "*"))
	if len(files) == 0 {
		t.Fatal("fixture lacks Relay objects")
	}
	if err := os.Remove(files[0]); err != nil {
		t.Fatal(err)
	}
	invalid := filepath.Join(t.TempDir(), "incomplete")
	if _, err := restoreDeployment(ctx, backup, invalid); err == nil {
		t.Fatal("missing Relay object restored")
	}
	if _, err := os.Stat(filepath.Join(invalid, "server.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partial restore can start")
	}
}

func TestRelayBackupFailureHasNoCompletionMarker(t *testing.T) {
	t.Setenv("COCONET_RELAY_HOME", "")
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "source")
	var out, logs bytes.Buffer
	if runServerCLI(ctx, operatorInitArgs(root), &out, &logs) != 0 {
		t.Fatal(logs.String())
	}
	config, _, storage, objects, err := openConfiguredServer(ctx, filepath.Join(root, "server.json"))
	if err != nil {
		t.Fatal(err)
	}
	storage.close()
	// A configured but damaged Relay root cannot be treated as an empty one.
	if err := os.Mkdir(config.DataPath+".relay", 0700); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "incomplete.db")
	if err := backupComplete(ctx, config, objects, backup); err == nil {
		t.Fatal("backup ignored missing Relay database")
	}
	if _, err := os.Stat(backup + ".manifest.json"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed backup has a completion marker")
	}
}
