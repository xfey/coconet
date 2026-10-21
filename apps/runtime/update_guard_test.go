package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// Simulate protocol-0 using the same persistent locks as the published worker.
// Native side effects must finish before stop; the lifetime lock must remain
// held by the updater until activation/rollback is finished.
func TestLegacyMigrationWaitsForHandoffAndHoldsStoppedWorker(t *testing.T) {
	setRuntimeTestHome(t, t.TempDir())
	paths, _ := resolveRuntimePaths()
	root := filepath.Join(paths.root, "local-dag")
	t.Setenv("COCONET_LOCAL_DAG_HOME", root) // duplicate discovery must not deadlock
	t.Setenv("COCONET_DISABLE_BACKGROUND_SYNC", "1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lifetime, err := acquireLocalFileLock(ctx, filepath.Join(root, "managed-daemon.lock"))
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	finishWorker := func() { once.Do(func() { lifetime() }) }
	defer finishWorker()
	handoff, err := acquireLocalFileLock(ctx, filepath.Join(root, "forks", "operations.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer handoff()
	stopped := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stop" {
			if _, err := os.Stat(filepath.Join(root, "fork-complete")); err != nil {
				t.Error("stop interrupted native handoff")
			}
			finishWorker()
			stopped <- struct{}{}
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"ok"}`)
	}))
	defer server.Close()
	if err := atomicWriteJSON(filepath.Join(root, "control.json"), localDAGEndpoint{Version: "0.30.1", URL: server.URL, Token: strings.Repeat("a", 64)}, 0600); err != nil {
		t.Fatal(err)
	}
	input, lease := io.Pipe()
	defer input.Close()
	defer lease.Close()
	reader, output := io.Pipe()
	defer reader.Close()
	code := make(chan int, 1)
	go func() { code <- runInstallGuard(ctx, []string{t.TempDir()}, input, output, io.Discard); output.Close() }()
	ready := make(chan bool, 1)
	go func() {
		var v map[string]bool
		err := json.NewDecoder(reader).Decode(&v)
		ready <- err == nil && v["ready"]
	}()
	select {
	case <-stopped:
		t.Fatal("stopped before native result was durable")
	case <-time.After(100 * time.Millisecond):
	}
	if err := os.WriteFile(filepath.Join(root, "fork-complete"), []byte("durable receipt"), 0600); err != nil {
		t.Fatal(err)
	}
	handoff()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("no activation lease")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("migration did not drain")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("worker never stopped")
	}
	for _, name := range []string{"managed-daemon.lock", "launch.lock", filepath.Join("forks", "operations.lock"), "inbox-processing.lock"} {
		wait, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
		unlock, err := acquireLocalFileLock(wait, filepath.Join(root, name))
		cancel()
		if err == nil {
			unlock()
			t.Fatalf("activation did not hold %s", name)
		}
	}
	lease.Close()
	if got := <-code; got != 0 {
		t.Fatal(got)
	}
	for _, name := range []string{"managed-daemon.lock", "launch.lock", filepath.Join("forks", "operations.lock"), "inbox-processing.lock"} {
		wait, cancel := context.WithTimeout(ctx, time.Second)
		unlock, err := acquireLocalFileLock(wait, filepath.Join(root, name))
		cancel()
		if err != nil {
			t.Fatal("migration leaked lock", name, err)
		}
		unlock()
	}
}

func TestCancelledLegacyMigrationDoesNotStopHandoff(t *testing.T) {
	setRuntimeTestHome(t, t.TempDir())
	paths, _ := resolveRuntimePaths()
	root := filepath.Join(paths.root, "local-dag")
	t.Setenv("COCONET_LOCAL_DAG_HOME", "")
	t.Setenv("COCONET_DISABLE_BACKGROUND_SYNC", "1")
	handoff, err := acquireLocalFileLock(context.Background(), filepath.Join(root, "forks", "operations.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer handoff()
	stopped := make(chan bool, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stop" {
			stopped <- true
		}
		io.WriteString(w, `{}`)
	}))
	defer server.Close()
	atomicWriteJSON(filepath.Join(root, "control.json"), localDAGEndpoint{URL: server.URL, Token: strings.Repeat("b", 64)}, 0600)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	input, lease := io.Pipe()
	defer input.Close()
	defer lease.Close()
	done := make(chan int, 1)
	go func() { done <- runInstallGuard(ctx, []string{t.TempDir()}, input, io.Discard, io.Discard) }()
	// Wait for the migration to own launch.lock before cancelling it.
	deadline := time.Now().Add(3 * time.Second)
	for {
		probe, stop := context.WithTimeout(ctx, 20*time.Millisecond)
		unlock, err := acquireLocalFileLock(probe, filepath.Join(root, "launch.lock"))
		stop()
		if err != nil {
			break
		}
		unlock()
		if time.Now().After(deadline) {
			t.Fatal("migration never started")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if got := <-done; got != 1 {
		t.Fatal(got)
	}
	select {
	case <-stopped:
		t.Fatal("cancelled migration stopped active handoff")
	default:
	}
}

func TestInstallGuardRechecksVersionBeforeTouchingWorkers(t *testing.T) {
	setRuntimeTestHome(t, t.TempDir())
	t.Setenv("COCONET_LOCAL_DAG_HOME", "")
	previous := version
	version = "1.0.0"
	defer func() { version = previous }()
	paths, _ := resolveRuntimePaths()
	// A malformed worker proves equal/newer candidates skip all worker handling.
	atomicWriteJSON(filepath.Join(paths.root, "local-dag", "control.json"), map[string]string{"broken": "endpoint"}, 0600)
	for _, installed := range []string{"1.0.0", "2.0.0", "dev"} {
		root := t.TempDir()
		os.MkdirAll(filepath.Join(root, "current"), 0700)
		atomicWriteJSON(filepath.Join(root, "current", "manifest.json"), map[string]string{"product": "coconet", "version": installed}, 0644)
		input, lease := io.Pipe()
		reader, output := io.Pipe()
		done := make(chan int, 1)
		go func() {
			done <- runInstallGuard(context.Background(), []string{root, "--if-newer"}, input, output, io.Discard)
			output.Close()
		}()
		var ready map[string]bool
		if err := json.NewDecoder(reader).Decode(&ready); err != nil || !ready["ready"] || !ready["current"] {
			t.Fatal(installed, ready, err)
		}
		lease.Close()
		if got := <-done; got != 0 {
			t.Fatal(got)
		}
		input.Close()
		reader.Close()
	}
}

func TestConcurrentInstallGuardsRecheckAfterWaiting(t *testing.T) {
	setRuntimeTestHome(t, t.TempDir())
	t.Setenv("COCONET_LOCAL_DAG_HOME", "")
	previous := version
	version = "1.0.0"
	defer func() { version = previous }()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "current"), 0700)
	writeVersion := func(v string) {
		t.Helper()
		if err := atomicWriteJSON(filepath.Join(root, "current", "manifest.json"), map[string]string{"product": "coconet", "version": v}, 0644); err != nil {
			t.Fatal(err)
		}
	}
	writeVersion("0.9.0")
	start := func() (io.WriteCloser, chan bool, chan int) {
		input, lease := io.Pipe()
		reader, output := io.Pipe()
		ready := make(chan bool, 1)
		done := make(chan int, 1)
		go func() {
			done <- runInstallGuard(context.Background(), []string{root, "--if-newer"}, input, output, io.Discard)
			output.Close()
			input.Close()
		}()
		go func() {
			var v map[string]bool
			err := json.NewDecoder(reader).Decode(&v)
			reader.Close()
			if err != nil || !v["ready"] {
				ready <- false
				return
			}
			ready <- v["current"]
		}()
		return lease, ready, done
	}
	first, ready1, done1 := start()
	defer first.Close()
	if current := <-ready1; current {
		t.Fatal("first upgrade skipped")
	}
	second, ready2, done2 := start()
	defer second.Close()
	select {
	case <-ready2:
		t.Fatal("concurrent installer crossed held lease")
	case <-time.After(50 * time.Millisecond):
	}
	writeVersion("2.0.0") // the first installation selected a newer version
	first.Close()
	if code := <-done1; code != 0 {
		t.Fatal(code)
	}
	if current := <-ready2; !current {
		t.Fatal("waiting installer would overwrite newer version")
	}
	second.Close()
	if code := <-done2; code != 0 {
		t.Fatal(code)
	}
}
