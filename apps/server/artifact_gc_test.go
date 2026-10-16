package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestArtifactGarbageFilesystemDeletionRejectsLinksAndOtherKeys(t *testing.T) {
	objects, err := openFilesystemArtifactStore(filepath.Join(t.TempDir(), "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	path, err := objects.objectPath("objects/chunk_test")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("owned orphan"), 0600); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := objects.Delete(context.Background(), "objects/chunk_test"); err != nil {
			t.Fatal(err)
		}
	}
	if err := objects.Delete(context.Background(), "../outside"); err == nil {
		t.Fatal("outside key accepted")
	}
	native := filepath.Join(t.TempDir(), "native")
	if err := os.WriteFile(native, []byte("native"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(native, path); err != nil {
		t.Fatal(err)
	}
	if err := objects.Delete(context.Background(), "objects/chunk_test"); err == nil {
		t.Fatal("symlink accepted")
	}
	if _, err := os.Stat(native); err != nil {
		t.Fatal("native file removed", err)
	}
}

// A retry of the same idempotent request gets fresh keys, even while old
// uploads are quarantined for deletion. The observation identity is fixed.
