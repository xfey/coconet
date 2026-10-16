package cursor

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xfey/coconet/adapters"
)

func fixtureDB(t *testing.T) (Adapter, string, string, *sql.DB) {
	t.Helper()
	profile := t.TempDir()
	cwd := t.TempDir()
	id := "11111111-1111-4111-8111-111111111111"
	dir := chatDirectory(profile, cwd, id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec("PRAGMA journal_mode=WAL; PRAGMA user_version=1; CREATE TABLE blobs(id TEXT PRIMARY KEY,data BLOB); CREATE TABLE meta(key TEXT PRIMARY KEY,value TEXT)"); err != nil {
		t.Fatal(err)
	}
	return Adapter{ConfigRoot: profile, Command: "cursor-fixture"}, cwd, id, db
}
func putBlob(t *testing.T, db *sql.DB, data []byte) []byte {
	t.Helper()
	sum := sha256.Sum256(data)
	if _, err := db.Exec("INSERT OR REPLACE INTO blobs VALUES(?,?)", hex.EncodeToString(sum[:]), data); err != nil {
		t.Fatal(err)
	}
	return sum[:]
}
func putMessage(t *testing.T, db *sql.DB, role, text string) []byte {
	t.Helper()
	content, _ := json.Marshal(map[string]any{"role": role, "content": []any{map[string]string{"type": "text", "text": text}}, "providerOptions": map[string]any{"cursor": map[string]string{"requestId": "fixture"}}})
	return putBlob(t, db, content)
}
func setRoot(t *testing.T, db *sql.DB, id string, root []byte) {
	t.Helper()
	hash := putBlob(t, db, root)
	meta, _ := json.Marshal(map[string]any{"agentId": id, "latestRootBlobId": hex.EncodeToString(hash), "createdAt": 1, "name": "Fixture", "mode": "default"})
	if _, err := db.Exec("INSERT OR REPLACE INTO meta VALUES('0',?)", hex.EncodeToString(meta)); err != nil {
		t.Fatal(err)
	}
}
func observeFixture(t *testing.T, a Adapter, cwd, id string, prior adapters.ObservationCheckpoint) ([]string, adapters.ObservationCheckpoint) {
	t.Helper()
	o, next, err := a.Observe(context.Background(), cwd, id, prior)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(o.RawArtifact.Path)
	if !o.RawArtifact.Temporary {
		t.Fatal("snapshot ownership missing")
	}
	if len(prior.TurnFingerprints) == 0 {
		if err := adapters.ValidateVisibleText(o.VisibleText, o.Normalized.Turns, o.Normalized.ToCursor); err != nil {
			t.Fatal(err)
		}
	}
	var texts []string
	for _, turn := range o.Normalized.Turns {
		texts = append(texts, turn.Text)
	}
	return texts, next
}

func TestCaptureUsesCommittedWALAndOnlyVisibleMainDialogue(t *testing.T) {
	a, cwd, id, db := fixtureDB(t)
	user := putMessage(t, db, "user", "<timestamp>fixture</timestamp>\n<user_query>实现任务队列😀</user_query>")
	assistant := putBlob(t, db, []byte(`{"role":"assistant","content":[{"type":"reasoning","text":"private reasoning"},{"type":"text","text":"队列已完成"},{"type":"tool-call","toolName":"Shell","args":{"command":"secret command"}}]}`))
	preamble := putBlob(t, db, []byte(`{"role":"user","content":"<user_info>workspace injection</user_info>"}`))
	tool := putBlob(t, db, []byte(`{"role":"tool","content":[{"type":"tool-result","result":"private tool output"}]}`))
	root := []byte{}
	for _, ref := range [][]byte{preamble, user, assistant, tool} {
		root = appendField(root, 1, ref)
	}
	setRoot(t, db, id, root)
	source := filepath.Join(chatDirectory(a.ConfigRoot, cwd, id), "store.db")
	before, _, _ := adapters.HashFile(source)
	walBefore, _, _ := adapters.HashFile(source + "-wal")
	texts, _ := observeFixture(t, a, cwd, id, adapters.ObservationCheckpoint{})
	if strings.Join(texts, "|") != "实现任务队列😀|队列已完成" {
		t.Fatalf("unexpected visible text: %q", texts)
	}
	after, _, _ := adapters.HashFile(source)
	walAfter, _, _ := adapters.HashFile(source + "-wal")
	if before != after || walBefore != walAfter {
		t.Fatal("native history was modified")
	}
}

func TestCompactionAndRewindFollowCurrentRoot(t *testing.T) {
	a, cwd, id, db := fixtureDB(t)
	u := putMessage(t, db, "user", "A")
	a1 := putMessage(t, db, "assistant", "B")
	a2 := putMessage(t, db, "assistant", "C")
	root := appendField(appendField(appendField(nil, 1, u), 1, a1), 1, a2)
	setRoot(t, db, id, root)
	_, before := observeFixture(t, a, cwd, id, adapters.ObservationCheckpoint{})
	archive := putBlob(t, db, appendField(appendField(nil, 1, u), 1, a1))
	summary := putBlob(t, db, []byte(`{"role":"user","content":"internal summary","providerOptions":{"cursor":{"isSummary":true}}}`))
	compacted := appendField(appendField(appendField(nil, 13, archive), 1, summary), 1, a2)
	setRoot(t, db, id, compacted)
	texts, after := observeFixture(t, a, cwd, id, before)
	if len(texts) != 0 || before.Cursor != after.Cursor {
		t.Fatal("compaction changed main dialogue")
	}
	// C remains physically present in blobs, but is no longer active.
	setRoot(t, db, id, appendField(appendField(nil, 1, u), 1, a1))
	texts, rewound := observeFixture(t, a, cwd, id, after)
	if strings.Join(texts, "|") != "A|B" || rewound.SourceEpoch != after.SourceEpoch+1 {
		t.Fatalf("rewind did not reset active branch: %q", texts)
	}
}

func TestIncompleteAndCorruptHistoryNeverBecomesEmptyCapture(t *testing.T) {
	for _, damage := range []string{"missing", "corrupt", "schema", "identity", "wire"} {
		t.Run(damage, func(t *testing.T) {
			a, cwd, id, db := fixtureDB(t)
			ref := putMessage(t, db, "user", "keep this message")
			setRoot(t, db, id, appendField(nil, 1, ref))
			switch damage {
			case "missing":
				db.Exec("DELETE FROM blobs WHERE id=?", hex.EncodeToString(ref))
			case "corrupt":
				db.Exec("UPDATE blobs SET data='bad' WHERE id=?", hex.EncodeToString(ref))
			case "schema":
				db.Exec("PRAGMA user_version=2")
			case "identity":
				setRoot(t, db, "other-session", appendField(nil, 1, ref))
			case "wire":
				setRoot(t, db, id, []byte{10, 255})
			}
			if _, _, err := a.Observe(context.Background(), cwd, id, adapters.ObservationCheckpoint{}); err == nil {
				t.Fatal("invalid native state accepted")
			}
		})
	}
}

func TestIdenticalRepeatedMessagesRemainDistinct(t *testing.T) {
	a, cwd, id, db := fixtureDB(t)
	ref := putMessage(t, db, "user", "continue")
	setRoot(t, db, id, appendField(appendField(nil, 1, ref), 1, ref))
	texts, _ := observeFixture(t, a, cwd, id, adapters.ObservationCheckpoint{})
	if len(texts) != 2 {
		t.Fatal("repeated user turns collapsed")
	}
}

func TestNativeReserializationDoesNotRewindImportedDialogue(t *testing.T) {
	a, cwd, id, db := fixtureDB(t)
	first := putBlob(t, db, []byte(`{"content":[{"text":"keep","type":"text"}],"providerOptions":{"cursor":{"requestId":"same"}},"role":"user"}`))
	setRoot(t, db, id, appendField(nil, 1, first))
	_, checkpoint := observeFixture(t, a, cwd, id, adapters.ObservationCheckpoint{})
	reordered := putBlob(t, db, []byte(`{"role":"user","content":[{"type":"text","text":"keep"}],"providerOptions":{"cursor":{"requestId":"same","additionalNativeMetadata":true}}}`))
	next := putMessage(t, db, "assistant", "new reply")
	setRoot(t, db, id, appendField(appendField(nil, 1, reordered), 1, next))
	texts, after := observeFixture(t, a, cwd, id, checkpoint)
	if len(texts) != 1 || texts[0] != "new reply" || after.SourceEpoch != checkpoint.SourceEpoch {
		t.Fatal("reserialization looked like rewind", texts)
	}
}

func TestDiscoveryIncludesNativeNestedWorkspaceButNotOtherProjects(t *testing.T) {
	a, cwd, id, db := fixtureDB(t)
	root := appendField(appendField(nil, 1, putMessage(t, db, "user", "nested work")), 9, []byte("file://"+cwd))
	setRoot(t, db, id, root)
	found, err := a.Discover(context.Background(), filepath.Dir(cwd))
	if err != nil || len(found) != 1 || found[0].CWD != cwd {
		t.Fatal(found, err)
	}
	found, err = a.Discover(context.Background(), t.TempDir())
	if err != nil || len(found) != 0 {
		t.Fatal("foreign workspace leaked", found, err)
	}
}
