package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/conversation"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/localdag"
	"github.com/xfey/coconet/packages/workgraph"
)

type delayedDiscoveryPublisher struct {
	started chan struct{}
	release chan struct{}
	store   *graphstore.Store
	commits []workgraph.Commit
}

func (p *delayedDiscoveryPublisher) Publish(context.Context, workgraph.Commit) error {
	return errors.New("reader must not publish")
}

func (p *delayedDiscoveryPublisher) Sync(ctx context.Context, _ string) error {
	select {
	case p.started <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.release:
	}
	for _, commit := range p.commits {
		if _, err := p.store.Apply(commit, false); err != nil {
			return err
		}
	}
	return nil
}

func discoveryCommits(t *testing.T) []workgraph.Commit {
	t.Helper()
	store, err := graphstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e := &localdag.Engine{Store: store, Actor: "producer"}
	source, err := e.Capture(context.Background(), localdag.Capture{ProjectID: "project", Agent: "fixture", NativeID: "fresh",
		Entries: []conversation.Entry{{Turn: contracts.NormalizedTurn{TurnID: "one", Ordinal: 0, Role: "user", Kind: "message", Text: "retry documentation"}}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Apply(workgraph.Commit{Schema: 1, ID: "stage", ProjectID: "project", Actor: "producer", RunID: "stage", Base: 1, At: time.Now().UTC().Format(time.RFC3339Nano),
		Stage: &workgraph.StageDecision{Source: source, Action: "start", NewNodeID: "node", Text: &workgraph.Text{Objective: "retry documentation", Summary: "Clarify stopping conditions."}, Reason: "fixture"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	history, err := store.History("project")
	if err != nil {
		t.Fatal(err)
	}
	var commits []workgraph.Commit
	for _, entry := range history {
		commits = append(commits, entry.Commit)
	}
	return commits
}

func TestLocalDiscoveryWaitsForFirstSyncWithoutFetchingContent(t *testing.T) {
	for _, action := range []string{"graph", "search"} {
		t.Run(action, func(t *testing.T) {
			root := t.TempDir()
			store, err := graphstore.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := store.WatchProject("project"); err != nil {
				t.Fatal(err)
			}
			remote := &delayedDiscoveryPublisher{started: make(chan struct{}, 1), release: make(chan struct{}), store: store, commits: discoveryCommits(t)}
			engine := &localdag.Engine{Store: store, Actor: "reader", Publisher: remote}
			ctx, cancel := context.WithCancel(context.Background())
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
					t.Error("daemon failed to stop")
				}
			}()
			select {
			case <-remote.started:
			case <-time.After(3 * time.Second):
				t.Fatal("sync did not start")
			}
			type result struct {
				code                int
				output, diagnostics string
			}
			read := make(chan result, 1)
			go func() {
				var output, diagnostics bytes.Buffer
				_, code := localDAGClient(ctx, root, action, localDAGClientOptions{Project: "project", Query: "retry"}, &output, &diagnostics)
				read <- result{code, output.String(), diagnostics.String()}
			}()
			select {
			case r := <-read:
				t.Fatalf("fresh replica returned before sync: %+v", r)
			case <-time.After(75 * time.Millisecond):
			}
			close(remote.release)
			select {
			case r := <-read:
				if r.code != 0 || !strings.Contains(r.output, "retry documentation") {
					t.Fatalf("discovery missed synchronized work: %+v", r)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("read did not resume")
			}
			budget, err := store.InferenceBudget(time.Now(), 1)
			if err != nil || budget.Used != 0 {
				t.Fatal("discovery spent model quota", err)
			}
			objects, err := os.ReadDir(filepath.Join(root, "objects"))
			if err != nil && !os.IsNotExist(err) || len(objects) != 0 {
				t.Fatal("discovery downloaded content", err)
			}
		})
	}
}

func TestLocalDiscoveryDistinguishesUnsyncedEmptyAndOfflineReplica(t *testing.T) {
	store, err := graphstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	engine := &localdag.Engine{Store: store}
	s := newLocalDAGDiscoverySync()
	if err := s.wait(context.Background(), engine, "project"); err != nil {
		t.Fatal("offline mode blocked", err)
	}
	engine.Publisher = &delayedDiscoveryPublisher{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.wait(ctx, engine, "project"); err == nil || !strings.Contains(err.Error(), "still pending") {
		t.Fatal("unsynced empty was reported as ready", err)
	}
	s.report("project", errors.New("Relay unavailable"))
	if err := s.wait(context.Background(), engine, "project"); err == nil || !strings.Contains(err.Error(), "Relay unavailable") {
		t.Fatal("sync failure hidden", err)
	}
	s.report("project", nil)
	if err := s.wait(context.Background(), engine, "project"); err != nil {
		t.Fatal("confirmed empty project blocked", err)
	}
	for _, commit := range discoveryCommits(t) {
		if _, err := store.Apply(commit, false); err != nil {
			t.Fatal(err)
		}
	}
	s = newLocalDAGDiscoverySync() // New daemon, Relay not yet available.
	if err := s.wait(ctx, engine, "project"); err != nil {
		t.Fatal("verified offline replica blocked", err)
	}
}
