package codex

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xfey/coconet/adapters"
)

const (
	historyParentID = "01980000-0000-7000-8000-000000000001"
	historyChildID  = "01980000-0000-7000-8000-000000000002"
	historyLeafID   = "01980000-0000-7000-8000-000000000003"
)

func writeTestRollout(t *testing.T, path, id, mode string, base *historyPosition, texts ...string) []byte {
	t.Helper()
	meta := map[string]any{"id": id, "cwd": filepath.Dir(path), "history_mode": mode, "base_instructions": map[string]any{"text": "native instructions"}, "future_metadata": "preserve me"}
	ordinal := uint64(0)
	if base != nil {
		meta["history_base"] = base
		ordinal = base.EndOrdinalExclusive
	}
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	_ = encoder.Encode(map[string]any{"type": "session_meta", "ordinal": ordinal, "payload": meta})
	for _, text := range texts {
		ordinal++
		_ = encoder.Encode(map[string]any{"type": "response_item", "ordinal": ordinal, "payload": map[string]any{"type": "function_call_output", "call_id": "fixture-call", "output": text}})
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, output.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

type historyClient struct {
	paths map[string]string
}

func (client historyClient) Request(method string, params, target any) error {
	arguments := params.(map[string]any)
	id, _ := arguments["threadId"].(string)
	path, found := client.paths[id]
	if method != "thread/read" || arguments["includeTurns"] != false || !found {
		return errors.New("unexpected dependency read")
	}
	content, _ := json.Marshal(map[string]any{"thread": map[string]any{"id": id, "path": path}})
	return json.Unmarshal(content, target)
}

func (historyClient) Close() error { return nil }

type historyFixture struct {
	adapter Adapter
	client  historyClient
	parent  string
	child   string
	base    historyPosition
	bytes   [][]byte
}

func newHistoryFixture(t *testing.T) historyFixture {
	t.Helper()
	adapter := New("codex-fixture")
	adapter.ConfigRoot = t.TempDir()
	parent := filepath.Join(adapter.ConfigRoot, "sessions", "parent.jsonl")
	child := filepath.Join(adapter.ConfigRoot, "sessions", "child.jsonl")
	parentBytes := writeTestRollout(t, parent, historyParentID, "paginated", nil, "parent tool evidence", "parent compaction evidence")
	base := historyPosition{ThreadID: historyParentID, EndByteOffset: int64(len(parentBytes)), EndOrdinalExclusive: 3}
	childBytes := writeTestRollout(t, child, historyChildID, "paginated", &base, "child tool evidence")
	return historyFixture{adapter: adapter, client: historyClient{paths: map[string]string{historyParentID: parent, historyChildID: child}}, parent: parent, child: child, base: base, bytes: [][]byte{parentBytes, childBytes}}
}

func testForkRequest(t *testing.T, path, id string) adapters.ForkRequest {
	t.Helper()
	hash, _, err := adapters.HashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return adapters.ForkRequest{AttemptDirectory: t.TempDir(), CWD: t.TempDir(), SourceArtifactPath: path, SourceArtifactSHA256: hash, SourceNativeSessionID: id}
}

func TestHistoryBundlePreservesNativeBytesAndFixedAncestorPrefixes(t *testing.T) {
	f := newHistoryFixture(t)
	nativeTime := time.Unix(1700000000, 0)
	if err := os.Chtimes(f.child, nativeTime, nativeTime); err != nil {
		t.Fatal(err)
	}
	first, err := f.adapter.historyArtifact(f.client, f.child, historyChildID)
	if err != nil || first.MediaType != historyMediaType {
		t.Fatal("could not capture native history closure", err)
	}
	info, err := os.Stat(first.Path)
	if err != nil || !info.ModTime().Equal(nativeTime) {
		t.Fatal("bundle creation changed the SessionEnd source write time", err)
	}
	file, err := os.Open(first.Path)
	if err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(file)
	_, _ = reader.Next()
	for index, expected := range f.bytes {
		header, err := reader.Next()
		if err != nil || header.Name != historyMemberName(index) {
			t.Fatal("native artifact member missing", err)
		}
		actual, err := io.ReadAll(reader)
		if err != nil || !bytes.Equal(actual, expected) {
			t.Fatal("transport changed raw native evidence", err)
		}
	}
	_ = file.Close()
	appendFile, err := os.OpenFile(f.parent, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = appendFile.WriteString("later source work must stay outside the checkpoint\n")
	_ = appendFile.Close()
	second, err := f.adapter.historyArtifact(f.client, f.child, historyChildID)
	if err != nil || first.SHA256 != second.SHA256 || first.Size != second.Size {
		t.Fatal("ancestor growth changed a fixed history snapshot", err)
	}
	request := testForkRequest(t, second.Path, historyChildID)
	// Only the transported bundle remains available to the importing account.
	if err := os.Remove(f.parent); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.child); err != nil {
		t.Fatal(err)
	}
	imported, err := f.adapter.prepareForkHistory(request)
	if err != nil {
		t.Fatal(err)
	}
	defer imported.cleanup()
	content, _ := os.ReadFile(imported.Path)
	meta, header, err := readRolloutMetadata(bytes.NewReader(content))
	mappedParent, _ := snapshotNativeID(request.SourceArtifactSHA256, historyParentID)
	mappedChild, _ := snapshotNativeID(request.SourceArtifactSHA256, historyChildID)
	if err != nil || meta.ID != mappedChild || meta.HistoryMode != "paginated" || meta.HistoryBase == nil || meta.HistoryBase.ThreadID != mappedParent || !bytes.Contains(header, []byte(`"future_metadata":"preserve me"`)) {
		t.Fatal("native import lost source metadata or dependency identity", err)
	}
	if len(header) != bytes.IndexByte(f.bytes[1], '\n')+1 || !bytes.Equal(content[len(header):], f.bytes[1][len(header):]) {
		t.Fatal("identity remapping changed history offsets or opaque payloads")
	}
	parentPath, err := findArtifact(filepath.Dir(imported.Path), mappedParent+".jsonl")
	if err != nil {
		t.Fatal(err)
	}
	parentContent, _ := os.ReadFile(parentPath)
	parentHeaderLength := bytes.IndexByte(f.bytes[0], '\n') + 1
	if len(parentContent) != len(f.bytes[0]) || !bytes.Equal(parentContent[parentHeaderLength:], f.bytes[0][parentHeaderLength:]) || bytes.Contains(parentContent, []byte("later source work")) {
		t.Fatal("import changed native evidence or included later work")
	}
	entries, _ := os.ReadDir(request.AttemptDirectory)
	if len(entries) != 0 {
		t.Fatal("temporary dependency files leaked")
	}

}

func TestHistoryBundleIncludesTransitiveArchivedAncestors(t *testing.T) {
	f := newHistoryFixture(t)
	archived := filepath.Join(f.adapter.ConfigRoot, "archived_sessions", "parent.jsonl")
	if err := os.MkdirAll(filepath.Dir(archived), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(f.parent, archived); err != nil {
		t.Fatal(err)
	}
	f.client.paths[historyParentID] = archived
	base := historyPosition{ThreadID: historyChildID, EndByteOffset: int64(len(f.bytes[1])), EndOrdinalExclusive: 5}
	leaf := filepath.Join(f.adapter.ConfigRoot, "sessions", "leaf.jsonl")
	writeTestRollout(t, leaf, historyLeafID, "paginated", &base, "leaf evidence")
	artifact, err := f.adapter.historyArtifact(f.client, leaf, historyLeafID)
	if err != nil {
		t.Fatal(err)
	}
	imported, err := f.adapter.prepareForkHistory(testForkRequest(t, artifact.Path, historyLeafID))
	if err != nil {
		t.Fatal(err)
	}
	defer imported.cleanup()
	entries, _ := os.ReadDir(filepath.Dir(imported.Path))
	if len(entries) != 3 {
		t.Fatal("transitive dependencies missing")
	}
	var content []byte
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(filepath.Dir(imported.Path), entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		content = append(content, data...)
	}
	for _, evidence := range []string{"parent tool evidence", "child tool evidence", "leaf evidence"} {
		if !bytes.Contains(content, []byte(evidence)) {
			t.Fatal("transitive native history missing", evidence)
		}
	}

}

func TestHistoryCaptureRejectsIncompleteOrUntrustedDependencies(t *testing.T) {
	for _, name := range []string{"missing", "cycle", "truncated", "inside-record", "ordinal", "identity", "external-path", "symlink", "future-mode"} {
		t.Run(name, func(t *testing.T) {
			f := newHistoryFixture(t)
			switch name {
			case "missing":
				delete(f.client.paths, historyParentID)
			case "cycle":
				base := historyPosition{ThreadID: historyChildID, EndByteOffset: int64(len(f.bytes[1])), EndOrdinalExclusive: 5}
				writeTestRollout(t, f.child, historyChildID, "paginated", &base, "cycle")
			case "truncated":
				_ = os.Truncate(f.parent, int64(len(f.bytes[0])-1))
			case "inside-record":
				f.base.EndByteOffset--
				writeTestRollout(t, f.child, historyChildID, "paginated", &f.base, historyChildID)
			case "ordinal":
				f.base.EndOrdinalExclusive++
				writeTestRollout(t, f.child, historyChildID, "paginated", &f.base, historyChildID)
			case "identity":
				writeTestRollout(t, f.parent, "another-parent", "paginated", nil, "changed identity")
			case "external-path", "symlink":
				external := filepath.Join(t.TempDir(), "parent.jsonl")
				_ = os.WriteFile(external, f.bytes[0], 0o600)
				if name == "external-path" {
					f.client.paths[historyParentID] = external
				} else {
					_ = os.Remove(f.parent)
					_ = os.Symlink(external, f.parent)
				}
			case "future-mode":
				writeTestRollout(t, f.child, historyChildID, "future-mode", &f.base, "unknown")
			}
			if _, err := f.adapter.historyArtifact(f.client, f.child, historyChildID); err == nil {
				t.Fatal("invalid dependency produced a successful artifact")
			}
		})
	}
}

func TestForkRejectsOldSingleFileWithMissingDependency(t *testing.T) {
	f := newHistoryFixture(t)
	request := testForkRequest(t, f.child, historyChildID)
	started := false
	f.adapter.start = func(context.Context, string, string, string) (appServerClient, error) {
		started = true
		return nil, errors.New("must not launch")
	}
	if _, err := f.adapter.Fork(context.Background(), request); err == nil || started {
		t.Fatal("incomplete old artifact reached native Fork", err)
	}
	entries, _ := os.ReadDir(request.AttemptDirectory)
	if len(entries) != 0 {
		t.Fatal("failed import leaked files")
	}
}

func TestHistoryImportsKeepNativePayloadsAndMultipleFixedVersions(t *testing.T) {
	for _, mode := range []string{"legacy", "paginated"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source.jsonl")
			first := writeTestRollout(t, path, historyParentID, mode, nil, "first fixed version")
			adapter := New("codex-fixture")
			adapter.ConfigRoot = t.TempDir()
			request1 := testForkRequest(t, path, historyParentID)
			import1, err := adapter.prepareForkHistory(request1)
			if err != nil {
				t.Fatal(err)
			}
			defer import1.cleanup()
			second := writeTestRollout(t, path, historyParentID, mode, nil, "first fixed version", "second version only")
			import2, err := adapter.prepareForkHistory(testForkRequest(t, path, historyParentID))
			if err != nil {
				t.Fatal(err)
			}
			defer import2.cleanup()
			one, _ := os.ReadFile(import1.Path)
			two, _ := os.ReadFile(import2.Path)
			source, _ := os.ReadFile(path)
			if bytes.Contains(one, []byte("second version only")) || !bytes.Contains(two, []byte("second version only")) || !bytes.Equal(source, second) {
				t.Fatal("independent fixed imports collided or changed the source")
			}
			if mode == "legacy" && (!bytes.Equal(one, first) || !bytes.Equal(two, second)) {
				t.Fatal("legacy import changed native bytes")
			}
		})
	}
}

func TestHistoryBundleRejectsArchiveTamperingAndCleansPartialImports(t *testing.T) {
	for _, name := range []string{"path-traversal", "symlink", "extra-member", "missing-member", "wrong-root", "wrong-size", "duplicate-id", "missing-ancestor", "invalid-boundary"} {
		t.Run(name, func(t *testing.T) {
			f := newHistoryFixture(t)
			manifest := historyManifest{Format: historyFormat, SchemaVersion: 1, RootNativeID: historyChildID, Members: []historyMember{{NativeID: historyParentID, Size: int64(len(f.bytes[0]))}, {NativeID: historyChildID, Size: int64(len(f.bytes[1]))}}}
			switch name {
			case "wrong-root":
				manifest.RootNativeID = "different-child"
			case "wrong-size":
				manifest.Members[1].Size++
			case "duplicate-id":
				manifest.Members[1].NativeID = historyParentID
			case "missing-ancestor":
				f.bytes[0] = writeTestRollout(t, f.parent, historyParentID, "paginated", &historyPosition{ThreadID: "missing", EndByteOffset: 50, EndOrdinalExclusive: 1}, historyParentID)
				manifest.Members[0].Size = int64(len(f.bytes[0]))
			case "invalid-boundary":
				f.base.EndOrdinalExclusive++
				f.bytes[1] = writeTestRollout(t, f.child, historyChildID, "paginated", &f.base, historyChildID)
				manifest.Members[1].Size = int64(len(f.bytes[1]))
			}
			var buffer bytes.Buffer
			writer := tar.NewWriter(&buffer)
			content, _ := json.Marshal(manifest)
			_ = writer.WriteHeader(&tar.Header{Name: "manifest.json", Size: int64(len(content)), Mode: 0o600})
			_, _ = writer.Write(content)
			for index, data := range f.bytes {
				if name == "missing-member" && index == 1 {
					break
				}
				header := tar.Header{Name: historyMemberName(index), Size: int64(len(data)), Mode: 0o600}
				if index == 1 && name == "path-traversal" {
					header.Name = "../escaped.jsonl"
				}
				if index == 1 && name == "symlink" {
					header.Typeflag, header.Linkname, header.Size = tar.TypeSymlink, "/outside", 0
				}
				_ = writer.WriteHeader(&header)
				_, _ = writer.Write(data)
			}
			if name == "extra-member" {
				_ = writer.WriteHeader(&tar.Header{Name: "unexpected", Size: 0})
			}
			_ = writer.Close()
			path := filepath.Join(t.TempDir(), "artifact.tar")
			_ = os.WriteFile(path, buffer.Bytes(), 0o600)
			request := testForkRequest(t, path, historyChildID)
			if _, err := f.adapter.prepareForkHistory(request); err == nil {
				t.Fatal("corrupt archive produced an import")
			}
			entries, _ := os.ReadDir(request.AttemptDirectory)
			if len(entries) != 0 {
				t.Fatal("partial archive import leaked files")
			}
		})
	}
}

func TestForkVerificationFailureReusesRecordedChild(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.jsonl")
	writeTestRollout(t, path, "source", "legacy", nil, "fixed history")
	request := testForkRequest(t, path, "source")
	client := &forkFakeClient{sourceID: "source", childID: historyChildID}
	adapter := New("codex-fixture")
	starts := 0
	adapter.start = func(context.Context, string, string, string) (appServerClient, error) {
		starts++
		if starts == 2 {
			return nil, errors.New("new process unavailable")
		}
		return client, nil
	}
	if _, err := adapter.Fork(context.Background(), request); err == nil || !strings.Contains(err.Error(), "verify independent") {
		t.Fatal("verification failure was reported as success", err)
	}
	result, err := adapter.Fork(context.Background(), request)
	if err != nil || result.NativeSessionID != historyChildID || client.forkCalls != 1 {
		t.Fatal("retry created another native child", err)
	}
}

func TestNativeIdentityRemappingPreservesOffsetsAndOpaqueContent(t *testing.T) {
	mapped, err := snapshotNativeID(strings.Repeat("a", 64), historyParentID)
	if err != nil || mapped == historyParentID || mapped[14] != '7' || mapped[:13] != historyParentID[:13] {
		t.Fatal("import identity lost its UUIDv7 timestamp", err)
	}
	identities := map[string]string{historyParentID: mapped}
	for _, kind := range []string{"session_meta", "event_msg", "token_usage_record", "response_item"} {
		record := []byte(`{"ordinal":9007199254740993,"type":"` + kind + `","payload":{"id":"` + historyParentID + `","thread_id":"` + historyParentID + `","session_id":"` + historyParentID + `","item":{"thread_id":"` + historyParentID + `","text":"原文保持不变","large_output":"` + strings.Repeat("tool evidence ", 10000) + `"}}}`)
		updated, err := remapHistoryRecord(record, identities)
		if err != nil || len(updated) != len(record) {
			t.Fatal("remapping moved native byte boundaries", err)
		}
		var original, imported struct {
			Payload map[string]json.RawMessage `json:"payload"`
		}
		_ = json.Unmarshal(record, &original)
		_ = json.Unmarshal(updated, &imported)
		if !bytes.Equal(original.Payload["item"], imported.Payload["item"]) || !bytes.Contains(updated, []byte("9007199254740993")) {
			t.Fatal("remapping changed nested evidence or numeric precision")
		}
		if kind == "response_item" && !bytes.Equal(record, updated) {
			t.Fatal("model-visible response item changed")
		}
		if kind != "response_item" && !bytes.Contains(updated, []byte(mapped)) {
			t.Fatal("native storage identity was not remapped")
		}
	}
}

func TestHistoryImportPreservesCRLFByteOffsets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.jsonl")
	content := writeTestRollout(t, path, historyParentID, "paginated", nil, "opaque tool output")
	content = bytes.ReplaceAll(content, []byte{'\n'}, []byte{'\r', '\n'})
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	segment, err := openRolloutSegment(path, historyParentID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer segment.file.Close()
	var output bytes.Buffer
	if err := copyRemappedHistory(&output, segment, map[string]string{historyParentID: historyChildID}); err != nil {
		t.Fatal(err)
	}
	want := bytes.Replace(content, []byte(historyParentID), []byte(historyChildID), 1)
	if !bytes.Equal(output.Bytes(), want) {
		t.Fatal("import changed native line endings or moved byte boundaries")
	}
}

func TestIdentityRemappingDoesNotInterpretUnknownKeysAsNestedPaths(t *testing.T) {
	record := []byte(`{"type":"session_meta","payload":{"id":"` + historyParentID + `","history_base.thread_id":"` + historyParentID + `","history_base/thread_id":"` + historyParentID + `","history_base":{"thread_id":"` + historyParentID + `"}}}`)
	updated, err := remapHistoryRecord(record, map[string]string{historyParentID: historyChildID})
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.Replace(record, []byte(`"id":"`+historyParentID), []byte(`"id":"`+historyChildID), 1)
	want = bytes.Replace(want, []byte(`"thread_id":"`+historyParentID), []byte(`"thread_id":"`+historyChildID), 1)
	if !bytes.Equal(updated, want) {
		t.Fatal("remapping interpreted an opaque field name as a metadata path")
	}
}

func TestConcurrentSnapshotImportsAreIdempotentAndRejectLocalChanges(t *testing.T) {
	f := newHistoryFixture(t)
	artifact, err := f.adapter.historyArtifact(f.client, f.child, historyChildID)
	if err != nil {
		t.Fatal(err)
	}
	requests := make([]adapters.ForkRequest, 8)
	for i := range requests {
		requests[i] = testForkRequest(t, artifact.Path, historyChildID)
	}
	results := make([]preparedForkHistory, len(requests))
	failures := make([]error, len(requests))
	var workers sync.WaitGroup
	for i := range requests {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			results[i], failures[i] = f.adapter.prepareForkHistory(requests[i])
		}(i)
	}
	workers.Wait()
	for i, err := range failures {
		if err != nil || results[i].Path != results[0].Path || results[i].NativeID != results[0].NativeID || len(results[i].Ancestors) != 1 {
			t.Fatal("concurrent fixed imports diverged", err)
		}
	}
	ids, err := f.adapter.importedHistoryIDs()
	if err != nil || len(ids) != 2 || ids[historyParentID] || ids[historyChildID] {
		t.Fatal("import inventory did not distinguish local aliases from original Sessions", err)
	}
	root := results[0].Path
	if err := os.WriteFile(root, []byte("changed local history\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.adapter.prepareForkHistory(requests[0]); err == nil {
		t.Fatal("an altered import was silently overwritten or accepted")
	}
	content, _ := os.ReadFile(root)
	if string(content) != "changed local history\n" {
		t.Fatal("existing local history was overwritten")
	}
}

func TestDiscoveryExcludesManagedHistoryAliases(t *testing.T) {
	f := newHistoryFixture(t)
	artifact, err := f.adapter.historyArtifact(f.client, f.child, historyChildID)
	if err != nil {
		t.Fatal(err)
	}
	imported, err := f.adapter.prepareForkHistory(testForkRequest(t, artifact.Path, historyChildID))
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	fake := &fakeClient{responses: map[string]any{"thread/list": map[string]any{"data": []thread{
		{ID: imported.NativeID, CWD: cwd}, {ID: imported.Ancestors[0], CWD: cwd}, {ID: historyChildID, CWD: cwd},
	}}}}
	f.adapter.start = func(context.Context, string, string, string) (appServerClient, error) { return fake, nil }
	sessions, err := f.adapter.Discover(context.Background(), cwd)
	if err != nil || len(sessions) != 1 || sessions[0].NativeSessionID != historyChildID {
		t.Fatal("native import dependencies leaked into Session discovery", err)
	}
}

type preparationClient struct {
	parent string
	closed bool
	bad    bool
}

func (client *preparationClient) Close() error { client.closed = true; return nil }

func (client *preparationClient) Request(method string, params, target any) error {
	args := params.(map[string]any)
	if method != "thread/fork" || args["threadId"] != client.parent || args["ephemeral"] != true || args["excludeTurns"] != true || args["config"].(map[string]any)["features.hooks"] != false {
		return errors.New("ancestor preparation changed its non-persistent scope")
	}
	content, _ := json.Marshal(map[string]any{"thread": thread{ID: historyLeafID, ForkedFromID: client.parent, Ephemeral: !client.bad}})
	return json.Unmarshal(content, target)
}

func TestAncestorPreparationClosesEachTemporaryContextAndFailsClosed(t *testing.T) {
	for _, fail := range []bool{false, true} {
		adapter := New("codex-fixture")
		history := preparedForkHistory{Ancestors: []string{historyParentID, historyChildID}}
		var clients []*preparationClient
		adapter.start = func(context.Context, string, string, string) (appServerClient, error) {
			if len(clients) > 0 && !clients[len(clients)-1].closed {
				t.Fatal("ancestor contexts accumulated in memory")
			}
			client := &preparationClient{parent: history.Ancestors[len(clients)], bad: fail}
			clients = append(clients, client)
			return client, nil
		}
		err := adapter.prepareImportedHistory(context.Background(), t.TempDir(), history)
		if (err != nil) != fail || !clients[len(clients)-1].closed || !fail && len(clients) != 2 {
			t.Fatal("native preparation did not enforce its temporary boundary", err)
		}
	}
}
