package adapters

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
)

func textFixture(t *testing.T, messages []TextMessage) TextImportRequest {
	t.Helper()
	path := filepath.Join(t.TempDir(), "compact.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range messages {
		if err := json.NewEncoder(f).Encode(m); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()
	hash, _, err := HashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return TextImportRequest{AttemptDirectory: t.TempDir(), CompactPath: path, CompactSHA256: hash, CWD: t.TempDir(), ObservedAt: "2026-10-15T01:02:03Z"}
}

func TestTextImportLongHistoryIsDeterministicAndPreservesLiteralMessages(t *testing.T) {
	var messages []TextMessage
	for i := 0; i < 72; i++ {
		role := "user"
		if i%2 != 0 {
			role = "assistant"
		}
		messages = append(messages, TextMessage{ID: string(rune(0x4e00 + i)), Role: role, Text: "  中文🥥\n```go\n" + strings.Repeat("code\n", 500) + "```  "})
	}
	request := textFixture(t, messages)
	first, cleanup, err := PrepareClaudeText(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	again, done, err := PrepareClaudeText(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	if first.SourceArtifactSHA256 != again.SourceArtifactSHA256 || first.SourceNativeSessionID != again.SourceNativeSessionID {
		t.Fatal("retry changed identity or bytes")
	}
	f, _ := os.Open(first.SourceArtifactPath)
	defer f.Close()
	decoder := json.NewDecoder(f)
	var parent *string
	for _, want := range messages {
		var record struct {
			Type, UUID, SessionID, CWD string
			ParentUUID                 *string
			Message                    struct {
				Role    string
				Content []struct{ Type, Text string }
			}
		}
		if err := decoder.Decode(&record); err != nil {
			t.Fatal(err)
		}
		if record.Type != want.Role || record.Message.Role != want.Role || len(record.Message.Content) != 1 || record.Message.Content[0].Text != want.Text || record.CWD != request.CWD || record.SessionID != first.SourceNativeSessionID {
			t.Fatal("lost literal history")
		}
		if parent == nil && record.ParentUUID != nil || parent != nil && (record.ParentUUID == nil || *parent != *record.ParentUUID) {
			t.Fatal("broken message ancestry")
		}
		parent = &record.UUID
	}
	hash, _, _ := HashFile(request.CompactPath)
	if hash != request.CompactSHA256 {
		t.Fatal("input changed")
	}
}

func TestTextImportRejectsMalformedOrChangedSourceAndCleansPartialOutput(t *testing.T) {
	cases := [][]TextMessage{nil, {{ID: "1", Role: "tool", Text: "result"}}, {{ID: "1", Role: "user", Text: " "}}, {{ID: "1", Role: "user", Text: "a"}, {ID: "1", Role: "assistant", Text: "b"}}}
	for _, messages := range cases {
		request := textFixture(t, messages)
		if _, _, err := PrepareClaudeText(context.Background(), request); err == nil {
			t.Fatal("invalid source accepted")
		}
		files, _ := os.ReadDir(request.AttemptDirectory)
		if len(files) != 0 {
			t.Fatal("partial output leaked")
		}
	}
	request := textFixture(t, []TextMessage{{ID: "1", Role: "user", Text: "hello"}})
	request.CompactSHA256 = strings.Repeat("a", 64)
	if _, _, err := PrepareClaudeText(context.Background(), request); err == nil {
		t.Fatal("hash mismatch accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := PrepareClaudeText(ctx, request); err == nil {
		t.Fatal("cancel ignored")
	}
}

func TestImportedTextMustMatchExactlyBeforeInheritance(t *testing.T) {
	request := textFixture(t, []TextMessage{{ID: "1", Role: "assistant", Text: "决定\n保留原接口"}, {ID: "2", Role: "user", Text: "接下来继续"}})
	observation := contracts.SessionObservation{Normalized: contracts.NormalizedObservation{Turns: []contracts.NormalizedTurn{{Role: "assistant", Kind: "message", Text: "决定\n保留原接口"}, {Role: "user", Kind: "message", Text: "接下来继续"}}}}
	if err := VerifyImportedText(context.Background(), request, observation); err != nil {
		t.Fatal(err)
	}
	observation.Normalized.Turns[1].Text = "偷偷截断"
	if err := VerifyImportedText(context.Background(), request, observation); err == nil {
		t.Fatal("changed history accepted")
	}
}
