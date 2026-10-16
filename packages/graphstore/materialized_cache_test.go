package graphstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/conversation"
)

func TestDerivedCacheQuotaExpiryAndRecoveryPreserveSources(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	if err := s.SetStorageLimits(32<<20, 16<<20); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(t.TempDir(), "original-agent-fixture")
	content := bytes.Repeat([]byte("a"), 16<<20)
	if err := os.WriteFile(original, content, 0600); err != nil {
		t.Fatal(err)
	}
	artifact, err := s.SaveArtifact(original, "application/octet-stream")
	if err != nil {
		t.Fatal(err)
	}
	path, err := s.ArtifactFile(artifact)
	if err != nil {
		t.Fatal(err)
	}
	if usage := s.CacheUsage(); usage.ExportsBytes != 16<<20 || usage.ExportsLimit != 16<<20 {
		t.Fatal(usage)
	}
	if duplicate, err := s.ArtifactFile(artifact); err != nil || duplicate != path {
		t.Fatal(duplicate, err)
	}
	prepared, err := conversation.Build([]conversation.Entry{{Turn: contracts.NormalizedTurn{TurnID: "turn", Ordinal: 0, Role: "user", Kind: "message", Text: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range prepared.Objects {
		if _, err := s.PutObject(data); err != nil {
			t.Fatal(err)
		}
	}
	body, _ := json.Marshal(prepared.Manifest)
	manifest, err := s.PutObject(body)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConversationFile(context.Background(), manifest, 1); !errors.Is(err, ErrMaterializedQuota) {
		t.Fatal("full cache allowed new export", err)
	}
	temp := filepath.Join(root, "materialized", ".conversation-interrupted")
	if err := os.WriteFile(temp, []byte("interrupted"), 0600); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(temp); !os.IsNotExist(err) {
		t.Fatal("crash remnant remains", err)
	}
	if usage := s.CacheUsage(); usage.ExportsBytes != 16<<20 || usage.ObjectsLimit != 32<<20 || usage.ExportsLimit != 16<<20 {
		t.Fatal(usage)
	}
	past := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatal(err)
	}
	release := s.PinContent()
	if result, err := s.PruneCache(context.Background(), time.Now(), "", CacheRoots{Artifacts: []string{artifact}}); err != nil || !result.Busy {
		t.Fatal(result, err)
	}
	release()
	if result, err := s.PruneCache(context.Background(), time.Now(), "", CacheRoots{Artifacts: []string{artifact}}); err != nil || result.Removed != 1 || result.Bytes != 16<<20 {
		t.Fatal(result, err)
	}
	if err := s.ValidateArtifact(artifact); err != nil {
		t.Fatal("original blocks lost", err)
	}
	if got, err := os.ReadFile(original); err != nil || !bytes.Equal(got, content) {
		t.Fatal("Agent fixture changed", err)
	}
	compact, err := s.ConversationFile(context.Background(), manifest, 1)
	if err != nil {
		t.Fatal(err)
	}
	before := s.CacheUsage().ExportsBytes
	// Quota pressure does not prevent reading an already exported compact file.
	s.materializedLimit.Store(before)
	if again, err := s.ConversationFile(context.Background(), manifest, 1); err != nil || again != compact {
		t.Fatal(again, err)
	}
	if usage := s.CacheUsage(); usage.ExportsBytes != before {
		t.Fatal("duplicate export charged twice", usage)
	}
	if err := s.SetStorageLimits(0, 32<<20); err != nil {
		t.Fatal(err)
	}
	if usage := s.CacheUsage(); usage.ObjectsLimit != 32<<20 || usage.ExportsLimit != 32<<20 {
		t.Fatal(usage)
	}
}

func TestDerivedCacheRejectsRedirectedDirectoryAndBoundsTemporaryOutput(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "materialized")); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(root); err == nil {
		s.Close()
		t.Fatal("symlink accepted")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatal(entries, err)
	}
	var output bytes.Buffer
	writer := boundedMaterializedWriter{writer: &output, limit: 4}
	if _, err := writer.Write([]byte("1234")); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("5")); err == nil || output.String() != "1234" {
		t.Fatal("temporary export grew past bound", err)
	}
}
