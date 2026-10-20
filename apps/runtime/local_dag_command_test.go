package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/conversation"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/localdag"
	"github.com/xfey/coconet/packages/workgraph"
)

func TestLocalDAGPullResolvesOnlyAcceptedNodeSources(t *testing.T) {
	root := t.TempDir()
	store, err := graphstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	e := &localdag.Engine{Store: store, Actor: "device"}
	source, err := e.Capture(context.Background(), localdag.Capture{ProjectID: "project", Agent: "fixture", NativeID: "native", Entries: []conversation.Entry{{Turn: contracts.NormalizedTurn{TurnID: "user", Ordinal: 0, Role: "user", Kind: "message", Text: "完善停止条件。"}}}, Artifact: []byte("original copy")})
	if err != nil {
		t.Fatal(err)
	}
	state, _ := store.State("project")
	c := workgraph.Commit{Schema: 1, ID: "stage", RunID: "run-stage", Actor: "device", ProjectID: "project", Base: state.Revision, At: "2026-10-09T00:00:00Z", Stage: &workgraph.StageDecision{Source: source, SessionRevision: state.Sessions[source.SessionID].Revision, Action: "start", NewNodeID: "node", Text: &workgraph.Text{Objective: "完善停止条件"}, Reason: "accepted work", Evidence: []string{"e1"}}}
	if _, err := store.Apply(c, false); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	var output, diagnostics bytes.Buffer
	args := []string{"pull", "--root", root, "--project", "project", "--node", "node"}
	if code := runLocalDAG(context.Background(), args, &output, &diagnostics); code != 0 {
		t.Fatal(diagnostics.String())
	}
	var result localdag.NodeRead
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Source != source || result.NativePath != "" {
		t.Fatal("default pull included raw artifact or wrong source")
	}
	if _, err := os.Stat(result.CompactPath); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	diagnostics.Reset()
	if code := runLocalDAG(context.Background(), append(args, "--native"), &output, &diagnostics); code != 0 {
		t.Fatal(diagnostics.String())
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(result.NativePath)
	if err != nil || string(data) != "original copy" {
		t.Fatal("explicit native read failed", err)
	}
}

func TestLocalDAGCaptureIsIdempotentAndReplayable(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(t.TempDir(), "capture.json")
	data, _ := json.Marshal(localdag.Capture{ProjectID: "project", Agent: "fixture", NativeID: "session"})
	if err := os.WriteFile(input, data, 0600); err != nil {
		t.Fatal(err)
	}
	var output, diagnostics bytes.Buffer
	args := []string{"local-dag", "capture", "--root", root, "--project", "project", "--actor", "device", "--input", input}
	for i := 0; i < 2; i++ {
		if code := run(context.Background(), args, nil, &output, &diagnostics); code != 0 {
			t.Fatalf("capture failed: %d %s", code, diagnostics.String())
		}
	}
	current, _ := os.ReadFile(input)
	if !bytes.Equal(data, current) {
		t.Fatal("capture input was changed")
	}
	output.Reset()
	if code := run(context.Background(), []string{"local-dag", "replay", "--root", root, "--project", "project"}, nil, &output, &diagnostics); code != 0 {
		t.Fatalf("replay: %s", diagnostics.String())
	}
	var state workgraph.State
	if err := json.Unmarshal(output.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.Revision != 1 || len(state.Snapshots) != 1 {
		t.Fatal("duplicate capture created a version")
	}
}

func TestInternalInferenceHookDoesNotReadInputOrCreateState(t *testing.T) {
	t.Setenv("COCONET_INTERNAL_INFERENCE", "1")
	root := filepath.Join(t.TempDir(), "absent")
	setRuntimeTestHome(t, root)
	var output bytes.Buffer
	if code := runHook([]string{"--agent", "codex"}, nil, &output, &output); code != 0 || output.Len() != 0 {
		t.Fatal("internal hook was not silent")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("internal inference created capture state")
	}
}
