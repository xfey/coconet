package contentstore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveOnlyExactRegularOwnedObject(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := s.PutObject([]byte("owned bytes"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(digest); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(digest); err != nil {
		t.Fatal("absent retry", err)
	}
	for _, input := range []string{"../history", "", root} {
		if err := s.Remove(input); err == nil {
			t.Fatal("non-digest accepted")
		}
	}
	native := filepath.Join(t.TempDir(), "history")
	if err := os.WriteFile(native, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	path, _ := s.objectPath(digest)
	if err := os.Symlink(native, path); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(digest); err == nil {
		t.Fatal("symlink accepted")
	}
	if data, err := os.ReadFile(native); err != nil || string(data) != "original" {
		t.Fatal("original changed", err)
	}
}

func TestObjectOperationsRejectSubstitutedDirectory(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	digest := ObjectDigest([]byte("external data"))
	if err := os.Mkdir(filepath.Join(external, digest[:2]), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(external, digest[:2], digest)
	if err := os.WriteFile(path, []byte("external data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(root, "objects")); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(digest); err == nil {
		t.Fatal("followed substituted directory for removal")
	}
	if _, err := s.OpenObject(digest); err == nil {
		t.Fatal("followed substituted directory for reading")
	}
	if _, err := s.PutObject([]byte("external data")); err == nil {
		t.Fatal("followed substituted directory for writing")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "external data" {
		t.Fatal("unrelated file changed", err)
	}
}
