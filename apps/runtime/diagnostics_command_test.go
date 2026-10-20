package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/localdag"
)

func TestDiagnosticMetadataDoesNotExportPendingRecoveryInput(t *testing.T) {
	_, store := diagnosticStoreFixture(t)
	defer store.Close()
	if err := store.Begin(graphstore.Run{ID: "pending", TaskID: "pending", ProjectID: "project", Kind: "stage", Status: "running", Input: json.RawMessage(`{}`), RecoveryInput: &contracts.ProcessingSpec{Payload: json.RawMessage(`{"secret":"fixed private input"}`)}}); err != nil {
		t.Fatal(err)
	}
	result, err := localDiagnostics(store, "diagnostics-export", localDAGClientOptions{Project: "project", Task: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(result)
	if bytes.Contains(data, []byte("fixed private input")) {
		t.Fatal("metadata export included recovery input")
	}
	run, err := store.Run("project", "pending")
	if err != nil || run.RecoveryInput == nil {
		t.Fatal("export removed durable recovery state", err)
	}
}

func diagnosticStoreFixture(t *testing.T) (string, *graphstore.Store) {
	t.Helper()
	root := t.TempDir()
	store, err := graphstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if err := store.Begin(graphstore.Run{ID: "trace", TaskID: "task", ProjectID: "project", Kind: "stage", Status: "running", StartedAt: stamp, Input: json.RawMessage(`{"prompt":"private fixture conversation"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveResponse("trace", "private fixture response", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Finish("trace", "completed", "", stamp); err != nil {
		t.Fatal(err)
	}
	return root, store
}
func TestDiagnosticsCLIExportsOnlyExplicitContentWithoutOverwriting(t *testing.T) {
	root, store := diagnosticStoreFixture(t)
	store.Close()
	invoke := func(args ...string) (int, string, string) {
		t.Helper()
		var out, stderr bytes.Buffer
		args = append(args, "--root", root, "--project", "project")
		code := runDiagnostics(context.Background(), args, &out, &stderr)
		return code, out.String(), stderr.String()
	}
	if code, out, err := invoke("list"); code != 0 || !strings.Contains(out, "trace") || strings.Contains(out, "private fixture") {
		t.Fatal(code, out, err)
	}
	meta := filepath.Join(t.TempDir(), "metadata.json")
	if code, _, err := invoke("export", "--run", "trace", "--output", meta); code != 0 {
		t.Fatal(code, err)
	}
	data, err := os.ReadFile(meta)
	if err != nil || strings.Contains(string(data), "private fixture") {
		t.Fatal("implicit content export", err)
	}
	if code, _, _ := invoke("export", "--run", "trace", "--output", meta, "--include-content"); code == 0 {
		t.Fatal("existing export overwritten")
	}
	preserved, _ := os.ReadFile(meta)
	if !bytes.Equal(data, preserved) {
		t.Fatal("existing export changed")
	}
	full := filepath.Join(t.TempDir(), "full.json")
	if code, _, err := invoke("export", "--run", "trace", "--output", full, "--include-content"); code != 0 {
		t.Fatal(code, err)
	}
	data, err = os.ReadFile(full)
	if err != nil || !strings.Contains(string(data), "private fixture response") {
		t.Fatal("explicit content missing", err)
	}
	info, _ := os.Stat(full)
	if info.Mode().Perm() != 0600 {
		t.Fatal("diagnostic export is not private")
	}
	if code, _, err := invoke("pin", "--run", "trace"); code != 0 {
		t.Fatal(code, err)
	}
	if code, out, err := invoke("status"); code != 0 || !strings.Contains(out, `"pinned":1`) {
		t.Fatal(code, out, err)
	}
	if code, _, err := invoke("configure", "--days", "2", "--max-mib", "32"); code != 0 {
		t.Fatal(code, err)
	}
}
func TestDiagnosticsCLIUsesDaemonAndRejectsForeignRun(t *testing.T) {
	root, store := diagnosticStoreFixture(t)
	defer store.Close()
	engine := &localdag.Engine{Store: store, Actor: "device"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
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
			t.Fatal("daemon did not start")
		}
		time.Sleep(time.Millisecond)
	}
	for _, action := range []string{"status", "list", "pin", "unpin"} {
		args := []string{action, "--root", root, "--project", "project", "--run", "trace"}
		var out, stderr bytes.Buffer
		if code := runDiagnostics(ctx, args, &out, &stderr); code != 0 {
			t.Fatal(action, code, stderr.String())
		}
	}
	var out, stderr bytes.Buffer
	if code := runDiagnostics(ctx, []string{"pin", "--root", root, "--project", "foreign", "--run", "trace"}, &out, &stderr); code == 0 {
		t.Fatal("foreign diagnostic mutation accepted")
	}
	if !strings.Contains(stderr.String(), "another project") {
		t.Fatal(stderr.String())
	}
}
