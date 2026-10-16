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
	"github.com/xfey/coconet/packages/inference"
	"github.com/xfey/coconet/packages/localdag"
)

type blockingLocalModel struct {
	Started chan struct{}
	Release chan struct{}
}

func (m *blockingLocalModel) Infer(ctx context.Context, _ inference.Request) (inference.Response, error) {
	select {
	case m.Started <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return inference.Response{}, ctx.Err()
	case <-m.Release:
	}
	text := `{"action":"none","stage":null,"items":{"add":[],"revise":[],"complete":[],"withdraw":[]},"reason":"no visible contribution","evidence":[]}`
	return inference.Response{Text: text, Visible: text}, nil
}

func TestLocalDaemonCapturesAndServesReadersDuringModelExecution(t *testing.T) {
	root := t.TempDir()
	store, err := graphstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	model := &blockingLocalModel{Started: make(chan struct{}, 1), Release: make(chan struct{})}
	engine := &localdag.Engine{Store: store, Runner: model, Actor: "device"}
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
			t.Error("daemon did not shut down")
		}
	}()
	var endpoint localDAGEndpoint
	deadline := time.Now().Add(3 * time.Second)
	for {
		data, err := os.ReadFile(filepath.Join(root, "control.json"))
		if err == nil && json.Unmarshal(data, &endpoint) == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	request := func(method, path, token string, body []byte) *http.Response {
		t.Helper()
		r, _ := http.NewRequest(method, endpoint.URL+path, bytes.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	response := request("GET", "/status", "", nil)
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("daemon accepted unauthenticated caller")
	}
	entries := []conversation.Entry{{Turn: contracts.NormalizedTurn{TurnID: "user", Ordinal: 0, Role: "user", Kind: "message", Text: "检查失败处理。"}}}
	body, _ := json.Marshal(localdag.Capture{ProjectID: "project", Agent: "fixture", NativeID: "native", Entries: entries})
	response = request("POST", "/capture?project=project", endpoint.Token, body)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("capture status %d", response.StatusCode)
	}
	select {
	case <-model.Started:
	case <-time.After(3 * time.Second):
		t.Fatal("model did not start")
	}
	// The CLI must query IPC rather than block on the open database. Capture
	// also remains available while the model is blocked.
	var output, diagnostics bytes.Buffer
	if code := runLocalDAG(context.Background(), []string{"state", "--root", root, "--project", "project"}, &output, &diagnostics); code != 0 {
		t.Fatalf("live read failed: %s", diagnostics.String())
	}
	body, _ = json.Marshal(localdag.Capture{ProjectID: "project", Agent: "fixture", NativeID: "second", Entries: entries})
	response = request("POST", "/capture?project=project", endpoint.Token, body)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("capture was blocked by inference")
	}
	close(model.Release)
	deadline = time.Now().Add(3 * time.Second)
	for {
		response = request("GET", "/status", endpoint.Token, nil)
		var status struct {
			Attempts int  `json:"attempts"`
			Paused   bool `json:"paused"`
		}
		json.NewDecoder(response.Body).Decode(&status)
		response.Body.Close()
		if status.Paused && status.Attempts == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("attempt budget not enforced")
		}
		time.Sleep(10 * time.Millisecond)
	}
	response = request("POST", "/stop", endpoint.Token, nil)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("stop failed")
	}
}

func TestLocalDaemonClientRejectsNonLoopbackEndpoint(t *testing.T) {
	root := t.TempDir()
	if err := atomicWriteJSON(filepath.Join(root, "control.json"), localDAGEndpoint{URL: "https://example.com", Token: "credential"}, 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if handled, code := localDAGClient(context.Background(), root, "state", localDAGClientOptions{Project: "project"}, &output, &output); !handled || code == 0 {
		t.Fatal("unsafe endpoint was accepted")
	}
}
