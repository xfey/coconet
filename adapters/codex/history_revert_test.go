package codex

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestRevertedHistoryKeepsSameSessionWithDistinctPhysicalSegments(t *testing.T) {
	adapter := New("codex")
	adapter.ConfigRoot = t.TempDir()
	root := filepath.Join(adapter.ConfigRoot, "sessions")
	parent := filepath.Join(root, "rollout-old-"+historyParentID+".jsonl")
	before := writeTestRollout(t, parent, historyParentID, "paginated", nil, "keep B")
	// A later discarded suffix remains physically present in the old file.
	f, err := os.OpenFile(parent, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("discarded C must not be transported\n")
	_ = f.Close()
	base := historyPosition{historyParentID, 2, int64(len(before))}
	child := filepath.Join(root, "rollout-new-"+historyParentID+"_"+historyChildID+".jsonl")
	childBytes := writeTestRollout(t, child, historyParentID, "paginated", &base, "keep D")
	// Revert again after part of the rewritten branch, preserving both bases.
	leaf := filepath.Join(root, "rollout-newer-"+historyParentID+"_"+historyLeafID+".jsonl")
	leafBase := historyPosition{historyParentID, 4, int64(len(childBytes))}
	leafBytes := writeTestRollout(t, leaf, historyParentID, "paginated", &leafBase, "keep E")
	client := historyClient{paths: map[string]string{historyParentID: leaf}}
	artifact, err := adapter.historyArtifact(client, leaf, historyParentID)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(artifact.Path)
	if err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(file)
	_, _ = reader.Next()
	var manifest historyManifest
	if err := json.NewDecoder(reader).Decode(&manifest); err != nil || manifest.SchemaVersion != 2 {
		t.Fatal("revert requires segment-aware bundle", err)
	}
	for _, want := range [][]byte{before, childBytes, leafBytes} {
		if _, err := reader.Next(); err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(reader)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatal("transport changed original segment bytes", err)
		}
	}
	_ = file.Close()
	request := testForkRequest(t, artifact.Path, historyParentID)
	for _, path := range []string{parent, child, leaf} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	imported, err := adapter.prepareForkHistory(request)
	if err != nil {
		t.Fatal(err)
	}
	defer imported.cleanup()
	ids := append(append([]string{}, imported.Ancestors...), imported.NativeID)
	seen := map[string]bool{}
	for i, id := range ids {
		if seen[id] {
			t.Fatal("physical segments reused one import identity")
		}
		seen[id] = true
		path, err := findArtifact(filepath.Dir(imported.Path), id+".jsonl")
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		meta, _, err := readRolloutMetadata(bytes.NewReader(data))
		if err != nil || meta.ID != id || bytes.Contains(data, []byte("discarded C")) {
			t.Fatal("invalid imported effective history", err)
		}
		if i > 0 && (meta.HistoryBase == nil || meta.HistoryBase.ThreadID != ids[i-1]) {
			t.Fatal("self reference was not mapped to its physical parent")
		}
	}
	// A child captured after importing remains independently transferable.
	paths := map[string]string{}
	for _, id := range ids {
		paths[id], _ = findArtifact(filepath.Dir(imported.Path), id+".jsonl")
	}
	if _, err := adapter.historyArtifact(historyClient{paths}, imported.Path, imported.NativeID); err != nil {
		t.Fatal("imported chain cannot be captured", err)
	}
}

func TestRevertedHistoryRejectsAmbiguousPhysicalAncestors(t *testing.T) {
	adapter := New("codex")
	adapter.ConfigRoot = t.TempDir()
	root := filepath.Join(adapter.ConfigRoot, "sessions")
	parent := filepath.Join(root, "rollout-old-"+historyParentID+".jsonl")
	data := writeTestRollout(t, parent, historyParentID, "paginated", nil, "before")
	duplicate := filepath.Join(root, "rollout-duplicate-"+historyParentID+"_"+historyLeafID+".jsonl")
	if err := os.WriteFile(duplicate, data, 0o600); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(root, "rollout-new-"+historyParentID+"_"+historyChildID+".jsonl")
	base := historyPosition{historyParentID, 2, int64(len(data))}
	writeTestRollout(t, child, historyParentID, "paginated", &base, "after")
	if _, err := adapter.historyArtifact(historyClient{map[string]string{historyParentID: child}}, child, historyParentID); err == nil {
		t.Fatal("ambiguous ancestor accepted")
	}
}
