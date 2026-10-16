package localdag

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/conversation"
	"github.com/xfey/coconet/packages/workgraph"
)

func TestReadSelectedNodeRejectsStaleSelectionBeforeDownloading(t *testing.T) {
	e, store := fixture(t)
	capture(t, e, "session", 1, "完善重试文档。")
	e.Runner = output(stage("start", &workgraph.Text{Objective: "完善重试文档"}, "e1"))
	process(t, e, "stage", "session")
	state, _ := store.State("project")
	id := state.Sessions["session"].Tail
	old := state.Nodes[id].Source
	capture(t, e, "session", 2, "完善重试文档。", "好的，再把停止提示改清楚。")
	e.Runner = output(stage("continue", nil, "e2"))
	process(t, e, "stage", "session")
	fetcher := &readPublisher{}
	e.Publisher = fetcher
	for _, source := range []workgraph.Source{old, {}, {SessionID: "another", Version: 2}} {
		if _, err := e.ReadSelectedNode(context.Background(), "project", id, source, false); !errors.Is(err, ErrSelectionChanged) {
			t.Fatalf("expected changed-source error, got %v", err)
		}
	}
	if len(fetcher.sources) != 0 {
		t.Fatal("stale selection triggered downloads")
	}
	state, _ = store.State("project")
	result, err := e.ReadSelectedNode(context.Background(), "project", id, state.Nodes[id].Source, false)
	if err != nil || result.Source != state.Nodes[id].Source {
		t.Fatal("fresh selection failed", err)
	}
	runs, _ := store.Runs("project")
	found := false
	for _, run := range runs {
		if run.Kind == "read" && run.Status == "failed" {
			var input struct {
				Expected workgraph.Source `json:"expected_source"`
			}
			if err := json.Unmarshal(run.Input, &input); err != nil {
				t.Fatal(err)
			}
			found = found || input.Expected == old
		}
	}
	if !found {
		t.Fatal("stale selection was not retained in read diagnostics")
	}
}

type readPublisher struct {
	native  []bool
	sources []workgraph.Source
	pull    func(workgraph.Source, bool) error
}

func (*readPublisher) Publish(context.Context, workgraph.Commit) error { return nil }
func (p *readPublisher) Pull(_ context.Context, _ string, s workgraph.Source, native bool) error {
	p.native = append(p.native, native)
	p.sources = append(p.sources, s)
	if p.pull != nil {
		return p.pull(s, native)
	}
	return nil
}

func TestReadNodeUsesFixedSourcesAndNativeContentIsExplicit(t *testing.T) {
	e, store := fixture(t)
	visible := "已完善停止条件文档。"
	entries := []conversation.Entry{{Turn: contracts.NormalizedTurn{TurnID: "one", Ordinal: 0, Role: "assistant", Kind: "message", Text: visible + " opaque tool body"}, Visible: &visible}}
	c := Capture{ProjectID: "project", Agent: "fixture", NativeID: "native", Entries: entries, Artifact: []byte("original native copy")}
	source, err := e.Capture(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	e.Runner = output(stage("start", &workgraph.Text{Objective: "完善文档"}, "e1"))
	process(t, e, "stage", source.SessionID)
	state, _ := store.State("project")
	id := state.Sessions[source.SessionID].Tail
	c.Entries = append(c.Entries, conversation.Entry{Turn: contracts.NormalizedTurn{TurnID: "two", Ordinal: 1, Role: "user", Kind: "message", Text: "接下来检查大文件导入。"}})
	c.Artifact = []byte("later native copy")
	if _, err := e.Capture(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	e.Runner = output(stage("start", &workgraph.Text{Objective: "检查大文件导入"}, "e2"))
	process(t, e, "stage", source.SessionID)
	fetcher := &readPublisher{}
	e.Publisher = fetcher
	read, err := e.ReadNode(context.Background(), "project", id, false)
	if err != nil {
		t.Fatal(err)
	}
	text, err := os.ReadFile(read.CompactPath)
	if err != nil {
		t.Fatal(err)
	}
	if read.Source != source || read.NativePath != "" || len(fetcher.native) != 0 || strings.Contains(string(text), "opaque") || strings.Contains(string(text), "大文件") || !strings.Contains(string(text), visible) {
		t.Fatal("compact read was not confined to the node's visible fixed source")
	}
	read, err = e.ReadNode(context.Background(), "project", id, true)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := os.ReadFile(read.NativePath)
	if err != nil || string(artifact) != "original native copy" || len(fetcher.native) != 0 {
		t.Fatal("native read did not preserve selected source", err)
	}
	if _, err := e.ReadNode(context.Background(), "project", source.Key(), false); err == nil {
		t.Fatal("arbitrary version bypassed node selection")
	}
	runs, _ := store.Runs("project")
	success, failed := 0, 0
	for _, run := range runs {
		if run.Kind == "read" {
			if run.Status == "completed" {
				success++
			}
			if run.Status == "failed" {
				failed++
			}
		}
	}
	if success != 2 || failed != 1 {
		t.Fatal("read outcomes were not archived")
	}
	if err := os.WriteFile(read.CompactPath, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ReadNode(context.Background(), "project", id, false); err == nil {
		t.Fatal("corrupt compact cache accepted")
	}
	if len(fetcher.sources) != 0 {
		t.Fatal("local or corrupt content caused a remote read")
	}
}

func TestReadNodeDownloadsOnlyMissingContent(t *testing.T) {
	e, store := fixture(t)
	capture(t, e, "session", 1, "完善重试文档。")
	e.Runner = output(stage("start", &workgraph.Text{Objective: "完善重试文档"}, "e1"))
	process(t, e, "stage", "session")
	state, _ := store.State("project")
	node := state.Nodes[state.Sessions["session"].Tail]
	snapshot := state.Snapshots[node.Source.Key()]
	data, err := store.Object(snapshot.Conversation)
	if err != nil {
		t.Fatal(err)
	}
	file, err := store.OpenObject(snapshot.Conversation)
	if err != nil {
		t.Fatal(err)
	}
	path := file.Name()
	file.Close()
	// Keep a private copy without changing the accepted graph or source pointer.
	missing := filepath.Join(t.TempDir(), "manifest")
	if err := os.Rename(path, missing); err != nil {
		t.Fatal(err)
	}
	fetcher := &readPublisher{pull: func(workgraph.Source, bool) error { return errors.New("relay unavailable") }}
	e.Publisher = fetcher
	if _, err := e.ReadNode(context.Background(), "project", node.ID, false); err == nil || !strings.Contains(err.Error(), "relay unavailable") {
		t.Fatal("missing content incorrectly reported as an offline success", err)
	}
	fetcher.pull = func(source workgraph.Source, native bool) error {
		if source != node.Source || native {
			t.Fatal("download changed the fixed selection or requested native bytes")
		}
		return os.WriteFile(path, data, 0600)
	}
	if _, err := e.ReadNode(context.Background(), "project", node.ID, false); err != nil {
		t.Fatal(err)
	}
	fetcher.pull = func(workgraph.Source, bool) error { t.Fatal("cached read contacted Relay"); return nil }
	if _, err := e.ReadNode(context.Background(), "project", node.ID, false); err != nil {
		t.Fatal(err)
	}
	if len(fetcher.sources) != 2 {
		t.Fatal("unexpected download count", len(fetcher.sources))
	}
}
