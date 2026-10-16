package graphstore

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecoveryExcludesLiveOwnerAndKeepsUnrelatedFiles(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{}
	for _, prefix := range []string{".native-copy-", ".artifact-"} {
		file, err := os.CreateTemp(root, prefix)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteString("unfinished local bytes"); err != nil {
			t.Fatal(err)
		}
		file.Close()
		paths = append(paths, file.Name())
	}
	unrelated := filepath.Join(root, ".artifact-user-notes")
	if err := os.WriteFile(unrelated, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if second, err := Open(root); err == nil {
		second.Close()
		t.Fatal("concurrent graph owner accepted")
	}
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("live temporary removed", err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, path := range paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("interrupted temporary not removed")
		}
	}
	if data, err := os.ReadFile(unrelated); err != nil || string(data) != "keep" {
		t.Fatal("unrelated local file changed", err)
	}
}

func TestStorageUsageIncludesDatabaseAndPrivateLayoutWithoutFollowingLinks(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Begin(Run{ID: "run", TaskID: "task", ProjectID: "project", Kind: "stage", Status: "running", StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Input: json.RawMessage(`{"secret":"private-prompt"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject([]byte("content")); err != nil {
		t.Fatal(err)
	}
	for _, category := range []string{"inbox", "history", "forks", "inference-work", "inference-sources"} {
		dir := filepath.Join(root, category)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "fixture"), []byte("12345"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	external := t.TempDir()
	if err := os.WriteFile(filepath.Join(external, "original"), []byte(strings.Repeat("x", 4096)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(root, "external-history")); err != nil {
		t.Fatal(err)
	}
	usage, err := s.StorageUsage(context.Background())
	if err != nil || usage.Symlinks != 1 || usage.Buckets["runs"].Records != 1 || usage.Buckets["runs"].Bytes <= 0 || usage.Layout["graph.db"].Bytes <= 0 || usage.Layout["objects"].Bytes != 7 {
		t.Fatal("incomplete storage usage", usage, err)
	}
	var total int64
	for category, row := range usage.Layout {
		if category == "external-history" {
			t.Fatal("followed external history")
		}
		total += row.Bytes
	}
	if total != usage.TotalFileBytes || usage.Layout["history"].Bytes != 5 || usage.Layout["inference-sources"].Bytes != 5 {
		t.Fatal("layout totals differ")
	}
	encoded, _ := json.Marshal(usage)
	if strings.Contains(string(encoded), "private-prompt") || strings.Contains(string(encoded), "original") {
		t.Fatal("inspection exposed file/database content")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.StorageUsage(ctx); err == nil {
		t.Fatal("cancelled storage inspection continued")
	}
}
