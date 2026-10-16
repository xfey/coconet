package graphstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/conversation"
)

func TestPartialConversationCannotPublishPartialIndex(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	entries := []conversation.Entry{}
	for i := 0; i < 3; i++ {
		entries = append(entries, conversation.Entry{Turn: contracts.NormalizedTurn{TurnID: fmt.Sprint("turn-", i), Ordinal: i, Role: "user", Kind: "message", Text: strings.Repeat("检索内容与临时存储 ", 5000)}})
	}
	prepared, err := conversation.Build(entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Manifest.Blocks) < 2 {
		t.Fatal("fixture must span blocks")
	}
	manifest, _ := json.Marshal(prepared.Manifest)
	root, err := s.PutObject(manifest)
	if err != nil {
		t.Fatal(err)
	}
	first := prepared.Manifest.Blocks[0].SHA256
	if _, err := s.PutObject(prepared.Objects[first]); err != nil {
		t.Fatal(err)
	}
	c := snapshotCommit()
	c.Snapshot.Conversation = root
	c.Snapshot.Turns = len(entries)
	if _, err := s.Apply(c, false); err != nil {
		t.Fatal(err)
	}
	coverage, err := s.IndexProject(ctx, "project", 16)
	if err != nil || coverage.Indexed != 0 || coverage.Complete {
		t.Fatal(coverage, err)
	}
	db, err := s.searchDB()
	if err != nil {
		t.Fatal(err)
	}
	var documents int
	if err := db.QueryRow(`SELECT COUNT(*) FROM documents`).Scan(&documents); err != nil || documents != 0 {
		t.Fatal("partial source leaked indexed rows", documents, err)
	}
	for _, data := range prepared.Objects {
		if _, err := s.PutObject(data); err != nil {
			t.Fatal(err)
		}
	}
	coverage, err = s.IndexProject(ctx, "project", 16)
	if err != nil || !coverage.Complete {
		t.Fatal(coverage, err)
	}
	state, _ := s.State("project")
	result, err := s.SearchContent(ctx, state, "临时存储", 0, 20, coverage)
	if err != nil || len(result.Matches) != 3 {
		t.Fatal(result, err)
	}
}
