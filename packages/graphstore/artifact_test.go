package graphstore

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/xfey/coconet/packages/sessionartifact"
)

func TestArtifactChunksReusePrefixAndReconstructExactCopies(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	path := filepath.Join(t.TempDir(), "native-copy")
	first := bytes.Repeat([]byte("abcd"), sessionartifact.CompressedChunkSize/2+123)
	if err := os.WriteFile(path, first, 0600); err != nil {
		t.Fatal(err)
	}
	one, err := s.SaveArtifact(path, "application/jsonl")
	if err != nil {
		t.Fatal(err)
	}
	current, _ := os.ReadFile(path)
	if !bytes.Equal(current, first) {
		t.Fatal("source was modified")
	}
	second := append(bytes.Clone(first), []byte("\nnext user turn")...)
	if err := os.WriteFile(path, second, 0600); err != nil {
		t.Fatal(err)
	}
	two, err := s.SaveArtifact(path, "application/jsonl")
	if err != nil {
		t.Fatal(err)
	}
	a, _ := s.ArtifactManifest(one)
	b, _ := s.ArtifactManifest(two)
	if one == two || len(a.Chunks) < 3 || a.Chunks[0] != b.Chunks[0] || a.Chunks[1] != b.Chunks[1] {
		t.Fatal("append rewrote complete prefix chunks")
	}
	for digest, expected := range map[string][]byte{one: first, two: second} {
		if err := s.ValidateArtifact(digest); err != nil {
			t.Fatal(err)
		}
		file, err := s.ArtifactFile(digest)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(file)
		if err != nil || !bytes.Equal(got, expected) {
			t.Fatal("version reconstruction changed original bytes")
		}
	}
	other, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	manifest, err := s.Object(one)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.PutObject(manifest); err != nil {
		t.Fatal(err)
	}
	if err := other.ValidateArtifact(one); err == nil {
		t.Fatal("incomplete artifact closure accepted")
	}
}
