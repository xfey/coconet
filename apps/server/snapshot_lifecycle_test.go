package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/xfey/coconet/adapters"
	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/sessionartifact"
)

// An authorized download begun after candidate marking must win over GC.

func TestCompleteBackupValidatesClosureAndIdentity(t *testing.T) {
	a, actor, project, objects := newSyncTestAuthority(t)
	id := "sess_legacy_backup"
	seedContextSession(t, a, contextSessionFixture{ProjectID: project.ProjectID, ServerSessionID: id, Author: actor, AgentType: contracts.AgentCodex, Versions: 1})
	indexContextTurns(t, a, actor, project.ProjectID, id, 1, "fixture", []contracts.NormalizedTurn{contextTurn("backup", 0, "user", "message", "recoverable evidence")}, adapters.ObservationCheckpoint{})
	path := filepath.Join(t.TempDir(), "native.jsonl")
	body := []byte("{\"native\":\"retained original\"}\n")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	descriptor, err := sessionartifact.Build(path, "application/x-ndjson")
	if err != nil {
		t.Fatal(err)
	}
	objectKey := "objects/chunk_backup"
	if err := objects.Write(context.Background(), artifactObject{Key: objectKey, SHA256: descriptor.Manifest.ArtifactSHA256, Size: int64(len(body))}, body); err != nil {
		t.Fatal(err)
	}
	if err := a.store.update(func(state *databaseState) error {
		snapshot := state.Snapshots.Get(versionKey(id, 1))
		snapshot.Manifest = descriptor.Manifest
		state.Snapshots.Set(versionKey(id, 1), snapshot)
		chunk := descriptor.Manifest.Chunks[0]
		state.Chunks.Set(artifactStoredChunkKey(project.ProjectID, chunk), chunkRecord{ProjectID: project.ProjectID, SHA256: chunk.SHA256, RawSize: chunk.RawSize, ObjectKey: objectKey})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	identity, err := generateDeploymentIdentity()
	if err != nil {
		t.Fatal(err)
	}
	identityPath := filepath.Join(t.TempDir(), "identity.json")
	if err := saveDeploymentIdentity(identityPath, identity); err != nil {
		t.Fatal(err)
	}
	if err := a.store.bindDeployment(identity.deploymentID); err != nil {
		t.Fatal(err)
	}

	dataPath := a.store.db.Path()
	if err := a.store.close(); err != nil {
		t.Fatal(err)
	}
	config := serverConfig{DataPath: dataPath, IdentityPath: identityPath, MetadataDriver: "bbolt"}
	output := filepath.Join(t.TempDir(), "complete.db")
	if err := backupComplete(context.Background(), config, objects, output); err != nil {
		t.Fatal(err)
	}
	// Simulate complete object loss, then restore from the self-contained bundle.
	objects.mutex.Lock()
	objects.objects = map[string][]byte{}
	objects.mutex.Unlock()
	if err := restoreCompleteObjects(context.Background(), output, identity.deploymentID, objects); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output + ".manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var m completeBackupManifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Identity) == 0 || len(m.Objects) < 2 {
		t.Fatal("incomplete backup")
	}
	m.Objects = m.Objects[1:]
	modified, _ := json.Marshal(m)
	if err := os.WriteFile(output+".manifest.json", modified, 0600); err != nil {
		t.Fatal(err)
	}
	if err := restoreCompleteObjects(context.Background(), output, identity.deploymentID, objects); err == nil {
		t.Fatal("missing closure passed restore validation")
	}
	if err := os.WriteFile(output+".manifest.json", data, 0600); err != nil {
		t.Fatal(err)
	}
	compact := filepath.Join(t.TempDir(), "compact.db")
	if err := compactBbolt(dataPath, compact); err != nil {
		t.Fatal(err)
	}
	if err := validateBboltSnapshotReadOnly(compact, identity.deploymentID); err != nil {
		t.Fatal(err)
	}
	if _, err := restoreBbolt(dataPath, compact, identity.deploymentID); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(dataPath); err != nil {
		t.Fatal(err)
	}
	if rollback, err := restoreBbolt(dataPath, output, identity.deploymentID); err != nil || rollback != "" {
		t.Fatal("restore to missing metadata failed", rollback, err)
	}
	restored, err := openStore(dataPath, objects)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.close()
	if err := restored.view(func(s databaseState) error {
		got, err := reconstructNormalizedTurns(s, id, 1)
		if err != nil || len(got) != 1 || got[0].Text != "recoverable evidence" {
			t.Fatal("restore lost content", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
