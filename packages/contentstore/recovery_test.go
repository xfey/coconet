package contentstore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInterruptedStreamsRemoveOnlyGeneratedRegularTemporaries(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := s.PutObject([]byte("published content"))
	if err != nil {
		t.Fatal(err)
	}
	path, _ := s.objectPath(digest)
	temporary := filepath.Join(filepath.Dir(path), ".pending-123456789")
	// A crash after link but before unlink leaves two names for the same bytes.
	if err := os.Link(path, temporary); err != nil {
		t.Fatal(err)
	}
	if err := s.CleanupInterruptedStreams(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(temporary); !os.IsNotExist(err) {
		t.Fatal("interrupted stream remained")
	}
	if data, err := s.Object(digest); err != nil || string(data) != "published content" {
		t.Fatal("published object changed", err)
	}
	external := filepath.Join(t.TempDir(), "original")
	if err := os.WriteFile(external, []byte("keep original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, temporary); err != nil {
		t.Fatal(err)
	}
	if err := s.CleanupInterruptedStreams(); err == nil {
		t.Fatal("temporary symlink accepted")
	}
	if data, err := os.ReadFile(external); err != nil || string(data) != "keep original" {
		t.Fatal("original was changed", err)
	}
}
