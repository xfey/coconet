package cursor

import (
	"context"
	"encoding/json"
	"github.com/xfey/coconet/adapters"
	"os"
	"path/filepath"
	"testing"
)

func TestForkFixedSnapshotPreservesSourceAndRetries(t *testing.T) {
	a, cwd, id, db := fixtureDB(t)
	setRoot(t, db, id, appendField(nil, 1, putMessage(t, db, "user", "fixed source")))
	original, _, err := a.Observe(context.Background(), cwd, id, adapters.ObservationCheckpoint{})
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(original.RawArtifact.Path)
	// The live source advances after the chosen version; Fork must use the fixed copy.
	setRoot(t, db, id, appendField(nil, 1, putMessage(t, db, "user", "newer live content")))
	req := adapters.ForkRequest{AttemptDirectory: t.TempDir(), SourceArtifactPath: original.RawArtifact.Path, SourceArtifactSHA256: original.RawArtifact.SHA256, SourceNativeSessionID: id, CWD: t.TempDir()}
	first, err := a.Fork(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	texts, _ := observeFixture(t, a, req.CWD, first.NativeSessionID, adapters.ObservationCheckpoint{})
	if len(texts) != 1 || texts[0] != "fixed source" {
		t.Fatal(texts)
	}
	// Publication survived but the attempt marker did not.
	if err = os.Remove(filepath.Join(req.AttemptDirectory, "native-fork.json")); err != nil {
		t.Fatal(err)
	}
	again, err := a.Fork(context.Background(), req)
	if err != nil || again != first {
		t.Fatal(again, err)
	}
	hash, _, _ := adapters.HashFile(original.RawArtifact.Path)
	if hash != req.SourceArtifactSHA256 {
		t.Fatal("source was changed")
	}
	live, _ := observeFixture(t, a, cwd, id, adapters.ObservationCheckpoint{})
	if live[0] != "newer live content" {
		t.Fatal("live history was changed")
	}
	req.SourceArtifactSHA256 = "bad"
	if _, err = a.Fork(context.Background(), req); err == nil {
		t.Fatal("accepted bad hash")
	}
}

func TestPortableImportExactTextAndIdempotency(t *testing.T) {
	a := Adapter{ConfigRoot: t.TempDir(), Command: "cursor-agent"}
	path := filepath.Join(t.TempDir(), "dialogue.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	e := json.NewEncoder(f)
	for _, m := range []adapters.TextMessage{{ID: "a", Role: "user", Text: "  多语言😀\n<user_query>保留字面量</user_query>\n保留换行  "}, {ID: "b", Role: "assistant", Text: "已完成"}, {ID: "c", Role: "assistant", Text: "补充说明"}, {ID: "d", Role: "user", Text: "continue"}, {ID: "e", Role: "user", Text: "continue"}} {
		if err = e.Encode(m); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()
	hash, _, _ := adapters.HashFile(path)
	req := adapters.TextImportRequest{AttemptDirectory: t.TempDir(), CompactPath: path, CompactSHA256: hash, CWD: t.TempDir(), ObservedAt: "2026-10-16T00:00:00Z"}
	first, err := a.ImportText(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	o, _, err := a.Observe(context.Background(), req.CWD, first.NativeSessionID, adapters.ObservationCheckpoint{})
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(o.RawArtifact.Path)
	if err = adapters.VerifyImportedText(context.Background(), req, o); err != nil {
		t.Fatal(err)
	}
	second, err := a.ImportText(context.Background(), req)
	if err != nil || first != second {
		t.Fatal(second, err)
	}
	db, err := openReadOnly(o.RawArtifact.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := readState(context.Background(), db, first.NativeSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.fields[8]) != 3 {
		t.Fatal("UI turns missing")
	}
	req.CompactSHA256 = "invalid"
	if _, err = a.ImportText(context.Background(), req); err == nil {
		t.Fatal("accepted bad dialogue")
	}
}

func TestWorkspaceReplacementPreservesUnknownWireFields(t *testing.T) {
	source := []byte{16, 99, 29, 1, 2, 3, 4}
	source = appendField(source, 9, []byte("old"))
	source = appendField(source, 42, []byte("unknown"))
	updated, err := replaceBytesField(source, 9, []byte("new"))
	if err != nil {
		t.Fatal(err)
	}
	fields, err := wireFields(updated)
	if err != nil || string(fields[9][0]) != "new" || string(fields[42][0]) != "unknown" {
		t.Fatal(fields, err)
	}
	for i := 0; i < 7; i++ {
		if updated[i] != source[i] {
			t.Fatal("changed unknown wire fields")
		}
	}
}
