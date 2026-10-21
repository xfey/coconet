package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/conversation"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/localdag"
)

func TestRuntimeUpdateWaitsForPersistedInferenceAndBlocksNewWork(t *testing.T) {
	root := t.TempDir()
	store, err := graphstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	model := &blockingLocalModel{Started: make(chan struct{}, 1), Release: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- serveLocalDAG(ctx, root, &localdag.Engine{Store: store, Runner: model, Actor: "device"}, 1, io.Discard)
	}()
	defer func() { cancel(); <-done }()
	endpoint := waitManagedControl(t, root, "")
	capture := localdag.Capture{ProjectID: "project", Agent: "fixture", NativeID: "native", Entries: []conversation.Entry{{Turn: contracts.NormalizedTurn{TurnID: "u", Ordinal: 0, Role: "user", Kind: "message", Text: "Check durable update behavior"}}}}
	data, _ := json.Marshal(capture)
	req, _ := http.NewRequest("POST", endpoint.URL+"/capture?project=project", bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+endpoint.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	select {
	case <-model.Started:
	case <-time.After(3 * time.Second):
		t.Fatal("inference never started")
	}
	entered := make(chan func(), 1)
	go func() {
		release, err := acquireRuntimeUpdate(ctx, root)
		if err == nil {
			entered <- release
		}
	}()
	select {
	case release := <-entered:
		release()
		t.Fatal("activation interrupted inference")
	case <-time.After(80 * time.Millisecond):
	}
	close(model.Release)
	var release func()
	select {
	case release = <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("completed inference did not drain")
	}
	defer func() {
		if release != nil {
			release()
		}
	}()
	// The no-contribution model result has been accepted and published before
	// activation can proceed. It is not merely an HTTP request that finished.
	state, err := store.State("project")
	if err != nil || len(state.Sessions) != 1 {
		t.Fatal("capture was not durable", err)
	}
	for _, session := range state.Sessions {
		if session.ProcessedThrough != session.Latest {
			t.Fatal("inference result was not durable before activation", session)
		}
	}
	newWork := make(chan struct{})
	go func() {
		unlock, err := beginRuntimeActivity(ctx, root)
		if err == nil {
			unlock()
			close(newWork)
		}
	}()
	select {
	case <-newWork:
		t.Fatal("new work crossed activation barrier")
	case <-time.After(60 * time.Millisecond):
	}
	release()
	release = nil
	select {
	case <-newWork:
	case <-time.After(time.Second):
		t.Fatal("work did not resume")
	}
}

func TestCancelledUpdateReleasesIntentAndDoesNotCancelWork(t *testing.T) {
	root := t.TempDir()
	work, err := beginRuntimeActivity(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		release, err := acquireRuntimeUpdate(ctx, root)
		if err == nil {
			release()
		}
		done <- err
	}()
	cancel()
	if err := <-done; err == nil {
		t.Fatal("cancellation ignored")
	}
	work()
	next, err := beginRuntimeActivity(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	next()
}

func TestNewerRuntimeIsNeverClassifiedForReplacement(t *testing.T) {
	previous := version
	version = "1.0.0"
	defer func() { version = previous }()
	root := t.TempDir()
	for _, v := range []string{"2.0.0", "1.0.0", "dev"} {
		if err := atomicWriteJSON(filepath.Join(root, "control.json"), localDAGEndpoint{Version: v, UpdateProtocol: 1}, 0600); err != nil {
			t.Fatal(err)
		}
		old, err := localDAGOlderProcess(root)
		if err != nil || old {
			t.Fatal(v, old, err)
		}
	}
	_ = os.Remove(filepath.Join(root, "control.json"))
}

func TestInstallGuardLeaseAndCancellation(t *testing.T) {
	setRuntimeTestHome(t, t.TempDir())
	t.Setenv("COCONET_LOCAL_DAG_HOME", "")
	paths, _ := resolveRuntimePaths()
	input, lease := io.Pipe()
	reader, output := io.Pipe()
	done := make(chan int, 1)
	go func() {
		code := runInstallGuard(context.Background(), []string{t.TempDir()}, input, output, io.Discard)
		output.Close()
		done <- code
	}()
	var ready struct {
		Ready bool `json:"ready"`
	}
	if err := json.NewDecoder(reader).Decode(&ready); err != nil || !ready.Ready {
		t.Fatal(ready, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	if unlock, err := beginRuntimeActivity(ctx, filepath.Join(paths.root, "local-dag")); err == nil {
		unlock()
		t.Error("work crossed held installation lease")
	}
	cancel()
	lease.Close()
	if code := <-done; code != 0 {
		t.Fatal(code)
	}
	unlock, err := beginRuntimeActivity(context.Background(), filepath.Join(paths.root, "local-dag"))
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	reader.Close()
	input.Close()
}
