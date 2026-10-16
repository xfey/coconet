package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/conversation"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/localdag"
)

func TestLocalCacheMaintenanceProtectsPausedInboxArtifactClosure(t *testing.T) {
	root := t.TempDir()
	store, err := graphstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	artifact, err := store.SaveArtifactBytes([]byte("fixed pending native fixture"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(localdag.Observation{ProjectID: "project", ArtifactManifest: artifact})
	prepared, err := store.PutObject(body)
	if err != nil {
		t.Fatal(err)
	}
	orphan, err := store.PutObject([]byte("unused fixture"))
	if err != nil {
		t.Fatal(err)
	}
	event := localDAGInboxEvent{Schema: 1, ID: "pending", Prepared: prepared, Paused: true, Request: localDAGObservationRequest{ProjectID: "project"}}
	if err := atomicWriteJSON(filepath.Join(root, "inbox", "pending.json"), event, 0600); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-8 * 24 * time.Hour)
	if err := filepath.WalkDir(filepath.Join(root, "objects"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		return os.Chtimes(path, past, past)
	}); err != nil {
		t.Fatal(err)
	}
	result, err := maintainLocalDAGCache(context.Background(), root, &localdag.Engine{Store: store, Actor: "device"})
	if err != nil || result.Removed != 1 {
		t.Fatal(result, err)
	}
	if _, err := store.Object(orphan); !os.IsNotExist(err) {
		t.Fatal("unused object remains", err)
	}
	if _, err := store.Object(prepared); err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateArtifact(artifact); err != nil {
		t.Fatal("paused input lost its closure", err)
	}
}
func TestStorageCLIControlsExistingPrivateStore(t *testing.T) {
	root := t.TempDir()
	store, err := graphstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutObject([]byte("cache fixture")); err != nil {
		t.Fatal(err)
	}
	store.Close()
	invoke := func(args ...string) (int, []byte, string) {
		var out, stderr bytes.Buffer
		args = append(args, "--root", root)
		code := runStorage(context.Background(), args, &out, &stderr)
		return code, out.Bytes(), stderr.String()
	}
	if code, data, err := invoke("configure", "--max-mib", "32", "--exports-mib", "16"); code != 0 {
		t.Fatal(code, err)
	} else {
		var usage graphstore.CacheUsage
		if err := json.Unmarshal(data, &usage); err != nil || usage.ObjectsLimit != 32<<20 || usage.ExportsLimit != 16<<20 {
			t.Fatal(string(data), err)
		}
	}
	if code, data, err := invoke("configure", "--exports-mib", "64"); code != 0 {
		t.Fatal(code, err)
	} else {
		var usage graphstore.CacheUsage
		if err := json.Unmarshal(data, &usage); err != nil || usage.ObjectsLimit != 32<<20 || usage.ExportsLimit != 64<<20 {
			t.Fatal(string(data), err)
		}
	}
	if code, _, err := invoke("status"); code != 0 {
		t.Fatal(code, err)
	}
	if code, _, err := invoke("prune"); code != 0 {
		t.Fatal(code, err)
	}
}

func TestStorageCLIConfiguresBothLimitsThroughDaemon(t *testing.T) {
	root := t.TempDir()
	store, err := graphstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- serveLocalDAG(ctx, root, &localdag.Engine{Store: store, Actor: "device"}, 1, io.Discard)
	}()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("daemon did not stop")
		}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(root, "control.json")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon did not start")
		}
		time.Sleep(time.Millisecond)
	}
	for _, args := range [][]string{{"configure", "--max-mib", "32", "--exports-mib", "64"}, {"configure", "--exports-mib", "16"}} {
		var out, stderr bytes.Buffer
		if code := runStorage(ctx, append(args, "--root", root), &out, &stderr); code != 0 {
			t.Fatal(code, stderr.String())
		}
	}
	if usage := store.CacheUsage(); usage.ObjectsLimit != 32<<20 || usage.ExportsLimit != 16<<20 {
		t.Fatal(usage)
	}
}

func TestStorageArchiveThroughDaemonAndOfflineRestore(t *testing.T) {
	root := t.TempDir()
	store, err := graphstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine := &localdag.Engine{Store: store, Actor: "device"}
	_, err = engine.Capture(ctx, localdag.Capture{ProjectID: "project", Agent: "claude", NativeID: "native", Entries: []conversation.Entry{{Turn: contracts.NormalizedTurn{TurnID: "t", Role: "user", Kind: "message", Text: "保存可恢复的项目副本"}}}, Artifact: []byte("raw session copy")})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- serveLocalDAG(ctx, root, engine, 1, io.Discard) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("daemon did not stop")
		}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(root, "control.json")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon missing")
		}
		time.Sleep(time.Millisecond)
	}
	archive := filepath.Join(t.TempDir(), "backup.tar")
	var out, stderr bytes.Buffer
	if code := runStorage(ctx, []string{"export", "--root", root, "--project", "project", "--file", archive}, &out, &stderr); code != 0 {
		t.Fatal(code, stderr.String())
	}
	target := filepath.Join(t.TempDir(), "restored")
	out.Reset()
	stderr.Reset()
	if code := runStorage(ctx, []string{"restore", "--root", target, "--project", "project", "--file", archive}, &out, &stderr); code != 0 {
		t.Fatal(code, stderr.String())
	}
	var result graphstore.ReplicaArchiveResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || !result.Offline || result.Sources != 1 {
		t.Fatal(out.String(), err)
	}
	out.Reset()
	stderr.Reset()
	if code := runStorage(ctx, []string{"reindex", "--root", target, "--project", "project"}, &out, &stderr); code != 0 {
		t.Fatal(code, stderr.String())
	}
}
