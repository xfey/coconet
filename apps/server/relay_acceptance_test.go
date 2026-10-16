package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contentstore"
	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/conversation"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/localdag"
	"github.com/xfey/coconet/packages/replication"
	"github.com/xfey/coconet/packages/workgraph"
)

type simultaneousRelayHTTP struct {
	replication.HTTP
	first     bool
	arrived   *sync.WaitGroup
	gate      <-chan struct{}
	conflicts *atomic.Int64
}

func (h *simultaneousRelayHTTP) Append(ctx context.Context, project string, c contracts.RelayCommit) (contracts.RelayReceipt, error) {
	if !h.first {
		h.first = true
		h.arrived.Done()
		select {
		case <-h.gate:
		case <-ctx.Done():
			return contracts.RelayReceipt{}, ctx.Err()
		}
	}
	r, err := h.HTTP.Append(ctx, project, c)
	if errors.Is(err, replication.ErrConflict) {
		h.conflicts.Add(1)
	}
	return r, err
}

func TestRelayHTTPSimultaneousWritersAndCheckpointReaders(t *testing.T) {
	_, handler, account, project, token := dashboardFixture(t)
	server := httptest.NewServer(handler)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	transport := replication.HTTP{BaseURL: server.URL, Token: token, Client: server.Client()}
	const devices, stages = 4, 6
	var arrived sync.WaitGroup
	arrived.Add(devices)
	gate := make(chan struct{})
	go func() { arrived.Wait(); close(gate) }()
	var conflicts atomic.Int64
	clients := make([]*replication.Client, devices)
	create := func(device string) *replication.Client {
		store, err := graphstore.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { store.Close() })
		return &replication.Client{Store: store, Remote: transport, Account: account.AccountID, Device: device}
	}
	done := make(chan error, devices)
	for i := range clients {
		client := create(fmt.Sprintf("device_%d", i))
		clients[i] = client
		client.Remote = &simultaneousRelayHTTP{HTTP: transport, arrived: &arrived, gate: gate, conflicts: &conflicts}
		go func(i int, client *replication.Client) {
			run := func() error {
				engine := &localdag.Engine{Store: client.Store, Publisher: client, Actor: client.Writer()}
				publish := func(configure func(workgraph.State, *workgraph.Commit)) error {
					for retry := 0; retry < 80; retry++ {
						state, err := client.Store.State(project.ProjectID)
						if err != nil {
							return err
						}
						id := fmt.Sprintf("commit_%d_%d_%d", i, state.Revision, retry)
						commit := workgraph.Commit{Schema: 1, ID: id, ProjectID: project.ProjectID, Actor: client.Writer(), Base: state.Revision, RunID: "run_" + id, At: "2026-10-09T00:00:00Z"}
						configure(state, &commit)
						if err := client.Publish(ctx, commit); errors.Is(err, workgraph.ErrConflict) {
							continue
						} else if err != nil {
							return err
						}
						_, err = client.Store.Apply(commit, false)
						return err
					}
					return errors.New("publication retry bound exceeded")
				}
				for step := 0; step < stages; step++ {
					name := fmt.Sprintf("work_%d_%d", i, step)
					capture := localdag.Capture{ProjectID: project.ProjectID, Agent: "fixture", NativeID: name, Entries: []conversation.Entry{{Turn: contracts.NormalizedTurn{TurnID: "turn", Role: "user", Kind: "message", Text: "Complete " + name}}}, Artifact: []byte("original " + name)}
					source, err := engine.Capture(ctx, capture)
					for retry := 0; errors.Is(err, workgraph.ErrConflict) && retry < 80; retry++ {
						err = engine.Recover(ctx, project.ProjectID)
						if err == nil {
							source, err = engine.Capture(ctx, capture)
						}
					}
					if err != nil {
						return fmt.Errorf("capture %s: %w", name, err)
					}
					if err := publish(func(state workgraph.State, c *workgraph.Commit) {
						c.Stage = &workgraph.StageDecision{Source: source, Action: "start", SessionRevision: state.Sessions[source.SessionID].Revision, NewNodeID: name, Text: &workgraph.Text{Objective: name}, Reason: "Distinct outcome"}
					}); err != nil {
						return err
					}
					if err := publish(func(state workgraph.State, c *workgraph.Commit) {
						node := state.Nodes[name]
						c.Relations = &workgraph.RelationDecision{NodeID: name, NodeRevision: node.Revision, Through: node.EndTurn, Complete: true, Reason: "Checked result use"}
						if step > 0 {
							parent := state.Nodes[fmt.Sprintf("work_%d_%d", i, step-1)]
							c.Relations.Parents = []workgraph.Parent{{NodeID: parent.ID, Revision: parent.Revision, Source: parent.Source, Reason: "Uses preceding result", Evidence: []string{"turn"}}}
						}
					}); err != nil {
						return err
					}
					if err := client.PublishCheckpoint(ctx, project.ProjectID); err != nil {
						return err
					}
				}
				return nil
			}
			done <- run()
		}(i, client)
	}
	for range clients {
		if err := <-done; err != nil {
			t.Error(err)
		}
	}
	if t.Failed() {
		return
	}
	if conflicts.Load() < devices-1 {
		t.Fatal("fixture did not exercise simultaneous CAS conflicts")
	}
	var digest string
	for _, client := range clients {
		if err := client.Sync(ctx, project.ProjectID); err != nil {
			t.Fatal(err)
		}
		state, err := client.Store.State(project.ProjectID)
		if err != nil {
			t.Fatal(err)
		}
		if err := workgraph.ValidateState(state); err != nil {
			t.Fatal(err)
		}
		if state.Revision != devices*stages*3 || len(state.Nodes) != devices*stages {
			t.Fatal("lost or duplicate work")
		}
		edges := 0
		for _, node := range state.Nodes {
			edges += len(node.Parents)
		}
		if edges != devices*(stages-1) {
			t.Fatal("lost dependency")
		}
		if digest != "" && digest != workgraph.Digest(state) {
			t.Fatal("replicas diverged")
		}
		digest = workgraph.Digest(state)
		if err := client.PublishCheckpoint(ctx, project.ProjectID); err != nil {
			t.Fatal(err)
		}
	}
	// Multiple local readers racing a cold bootstrap cannot overwrite each other.
	fresh := create("fresh")
	readers := make(chan error, 8)
	for i := 0; i < cap(readers); i++ {
		go func() { readers <- fresh.Sync(ctx, project.ProjectID) }()
	}
	for i := 0; i < cap(readers); i++ {
		if err := <-readers; err != nil {
			t.Fatal(err)
		}
	}
	state, err := fresh.Store.Replay(project.ProjectID)
	if err != nil || workgraph.Digest(state) != digest {
		t.Fatalf("bootstrap/replay diverged: %v", err)
	}
	history, err := fresh.Store.History(project.ProjectID)
	if err != nil || len(history) != 0 {
		t.Fatalf("checkpoint unexpectedly replayed history: %v", err)
	}
	for _, snapshot := range state.Snapshots {
		if _, err := fresh.Store.Object(snapshot.Conversation); err == nil {
			t.Fatal("bootstrap downloaded transcript")
		}
	}
	t.Logf("writers=%d commits=%d nodes=%d edges=%d CAS_conflicts=%d concurrent_readers=%d", devices, state.Revision, len(state.Nodes), devices*(stages-1), conflicts.Load(), cap(readers))
}

func TestRelayHTTPCheckpointAndObjectAccessLifecycle(t *testing.T) {
	a, handler, account, project, token := dashboardFixture(t)
	invoke := func(method, suffix string, body []byte, credential string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, "/v1/projects/"+project.ProjectID+"/relay/"+suffix, bytes.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+credential)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	payload := []byte("opaque checkpoint")
	digest := contentstore.ObjectDigest(payload)
	commit := contracts.RelayCommit{SchemaVersion: 1, DeviceID: "device", ID: "commit", Codec: "coconet-json-v2", Payload: payload, SHA256: digest, StateSHA256: digest, Objects: []string{}}
	body, _ := json.Marshal(commit)
	if response := invoke("POST", "commits", body, token); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	cp := contracts.RelayCheckpoint{SchemaVersion: 1, DeviceID: "device", Revision: 1, CommitID: "commit", Codec: "coconet-state-json-v1", Payload: payload, SHA256: digest}
	checkpoint, _ := json.Marshal(cp)
	check, _ := json.Marshal(contracts.RelayObjectCheck{SchemaVersion: 1, Objects: []contracts.RelayObject{{SHA256: digest, Size: int64(len(payload))}}})
	requests := []struct {
		method, suffix string
		body           []byte
	}{{"POST", "checkpoint", checkpoint}, {"GET", "checkpoint", nil}, {"PUT", "objects/" + digest, payload}, {"GET", "objects/" + digest, nil}, {"POST", "objects/check", check}}
	for _, request := range requests {
		if response := invoke(request.method, request.suffix, request.body, ""); response.Code != 401 {
			t.Fatalf("unauthenticated %s %s: %d", request.method, request.suffix, response.Code)
		}
		if response := invoke(request.method, request.suffix, request.body, token); response.Code != 200 {
			t.Fatalf("active %s %s: %d %s", request.method, request.suffix, response.Code, response.Body.String())
		}
	}
	if err := a.store.update(func(state *databaseState) error {
		p := state.Projects.Get(project.ProjectID)
		p.Status = contracts.ProjectArchived
		state.Projects.Set(p.ProjectID, p)
		key := membershipKey(project.ProjectID, account.AccountID)
		m := state.Memberships.Get(key)
		m.Status = contracts.MembershipArchived
		state.Memberships.Set(key, m)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, request := range requests {
		want := 409
		if request.method == "GET" {
			want = 200
		}
		if response := invoke(request.method, request.suffix, request.body, token); response.Code != want {
			t.Fatalf("archived %s %s: %d", request.method, request.suffix, response.Code)
		}
	}
	if err := a.store.update(func(state *databaseState) error {
		p := state.Projects.Get(project.ProjectID)
		p.Status = contracts.ProjectActive
		state.Projects.Set(p.ProjectID, p)
		state.Memberships.Delete(membershipKey(project.ProjectID, account.AccountID))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, request := range requests {
		if response := invoke(request.method, request.suffix, request.body, token); response.Code != 403 {
			t.Fatalf("revoked %s %s: %d", request.method, request.suffix, response.Code)
		}
	}
}
