package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/conversation"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/localdag"
	"github.com/xfey/coconet/packages/workgraph"
)

func startTestDAGDaemon(t *testing.T, root string, engine *localdag.Engine) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serveLocalDAG(ctx, root, engine, 1, io.Discard) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("daemon did not stop")
		}
	})
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
	return ctx
}
func TestManagedLibraryMCPAndCLIUseFixedLocalTransaction(t *testing.T) {
	root, workspace := t.TempDir(), t.TempDir()
	managedMCPConfig(t, root, []localDAGWorkspace{{Path: workspace, ProjectID: "project"}})
	chdirMCPTest(t, workspace)
	store, err := graphstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	engine := &localdag.Engine{Store: store, Actor: "reader"}
	source, err := engine.Capture(context.Background(), localdag.Capture{ProjectID: "project", Agent: "codex", NativeID: "native-fixture", Entries: []conversation.Entry{{Turn: contracts.NormalizedTurn{TurnID: "turn", Ordinal: 0, Role: "user", Kind: "message", Text: "完善提示"}}}})
	if err != nil {
		t.Fatal(err)
	}
	state, _ := store.State("project")
	if _, err := store.Apply(workgraph.Commit{Schema: 1, ID: "stage", ProjectID: "project", Actor: engine.Actor, Base: state.Revision, RunID: "fixture", At: time.Now().UTC().Format(time.RFC3339Nano), Stage: &workgraph.StageDecision{Source: source, Action: "start", NewNodeID: "node", Text: &workgraph.Text{Objective: "完善提示"}, Reason: "fixture"}}, false); err != nil {
		t.Fatal(err)
	}
	ctx := startTestDAGDaemon(t, root, engine)
	args := localdag.LibraryRequest{RequestID: "explicit-library-request", NodeID: "node", Source: source, Note: "后续参考"}
	result := decodeMCPToolCall[localdag.LibraryResult](t, localMCPCall(t, "add_session_to_library", args))
	if result.IsError || result.Structured.State != "collected" {
		t.Fatal(result)
	}
	again := decodeMCPToolCall[localdag.LibraryResult](t, localMCPCall(t, "add_session_to_library", args))
	if again.IsError || again.Structured.RunID != result.Structured.RunID {
		t.Fatal("request did not replay", again)
	}
	listed := decodeMCPToolCall[localdag.GraphView](t, localMCPCall(t, "list_library_entries", map[string]any{"limit": 1}))
	if listed.IsError || len(listed.Structured.Nodes) != 1 || listed.Structured.Nodes[0].Source != source || listed.Structured.Nodes[0].Library.Note != args.Note {
		t.Fatal(listed)
	}
	var out, stderr bytes.Buffer
	if code := runLibraryCommand(ctx, []string{"list", "--json"}, &out, &stderr, nil); code != 0 || !bytes.Contains(out.Bytes(), []byte("后续参考")) {
		t.Fatal(code, out.String(), stderr.String())
	}
	out.Reset()
	stderr.Reset()
	if code := runLibraryCommand(ctx, []string{"add", "--node", "node", "--session", source.SessionID, "--version", "1", "--request-id", args.RequestID, "--note", args.Note, "--json"}, &out, &stderr, nil); code != 0 {
		t.Fatal(code, stderr.String())
	}
	args.RequestID, args.Note = "explicit-remove-request", ""
	removed := decodeMCPToolCall[localdag.LibraryResult](t, localMCPCall(t, "remove_session_from_library", args))
	if removed.IsError || removed.Structured.State != "removed" {
		t.Fatal(removed)
	}
	out.Reset()
	stderr.Reset()
	if code := runLibraryCommand(ctx, []string{"remove", "--node", "node", "--session", source.SessionID, "--version", "1", "--request-id", args.RequestID, "--json"}, &out, &stderr, nil); code != 0 || !bytes.Contains(out.Bytes(), []byte(`"state":"removed"`)) {
		t.Fatal("CLI removal retry failed", stderr.String(), out.String())
	}
	state, _ = store.State("project")
	if len(state.Library) != 0 || state.Nodes["node"].State != "current" || state.Nodes["node"].Source != source {
		t.Fatal("Library removal changed preserved work")
	}
	if _, err := os.Stat(filepath.Join(root, "materialized")); !os.IsNotExist(err) {
		t.Fatal("Library operations downloaded conversations")
	}
	budget, err := store.InferenceBudget(time.Now(), 1)
	if err != nil || budget.Used != 0 {
		t.Fatal("Library operation invoked inference", budget, err)
	}
}
