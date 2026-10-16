package sessionartifact

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
)

func TestBuildCreatesDeterministicVerifiableManifest(t *testing.T) {
	content := bytes.Repeat([]byte("coconet-artifact\n"), DefaultChunkSize/8)
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := Build(path, "application/x-ndjson")
	if err != nil {
		t.Fatal(err)
	}
	second, err := Build(path, "application/x-ndjson")
	if err != nil {
		t.Fatal(err)
	}
	if first.Manifest.ManifestSHA256 != second.Manifest.ManifestSHA256 || len(first.Chunks) < 2 {
		t.Fatalf("manifest is not deterministic or chunked: %#v", first.Manifest)
	}
	if err := ValidateManifest(first.Manifest); err != nil {
		t.Fatal(err)
	}

	var rebuilt bytes.Buffer
	for _, chunk := range first.Chunks {
		reader, file, err := OpenChunk(path, chunk)
		if err != nil {
			t.Fatal(err)
		}
		_, copyErr := rebuilt.ReadFrom(reader)
		_ = file.Close()
		if copyErr != nil {
			t.Fatal(copyErr)
		}
	}
	if !bytes.Equal(content, rebuilt.Bytes()) {
		t.Fatal("manifest chunks did not rebuild the original artifact")
	}
}

func TestValidateManifestRejectsMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	descriptor, err := Build(path, "application/x-ndjson")
	if err != nil {
		t.Fatal(err)
	}
	descriptor.Manifest.Chunks[0].RawSize++
	if err := ValidateManifest(descriptor.Manifest); err == nil {
		t.Fatal("mutated manifest was accepted")
	}
}

func TestBuildAndValidateManifestEnforceCapacity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversized.jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(MaxArtifactSize + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(path, "application/x-ndjson"); err == nil {
		t.Fatal("Build accepted an artifact above the supported capacity")
	}

	fixturePath := filepath.Join(t.TempDir(), "fixture.jsonl")
	if err := os.WriteFile(fixturePath, []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	descriptor, err := Build(fixturePath, "application/x-ndjson")
	if err != nil {
		t.Fatal(err)
	}

	tooLarge := descriptor.Manifest
	tooLarge.ArtifactSize = MaxArtifactSize + 1
	tooLarge.ManifestSHA256, err = HashManifest(tooLarge)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateManifest(tooLarge); err == nil {
		t.Fatal("ValidateManifest accepted an oversized artifact")
	}

	tooManyChunks := descriptor.Manifest
	tooManyChunks.Chunks = make([]contracts.ArtifactChunk, MaxChunkCount+1)
	for index := range tooManyChunks.Chunks {
		tooManyChunks.Chunks[index] = descriptor.Manifest.Chunks[0]
		tooManyChunks.Chunks[index].Ordinal = index
	}
	tooManyChunks.ArtifactSize = int64(len(tooManyChunks.Chunks)) * descriptor.Manifest.Chunks[0].RawSize
	tooManyChunks.ManifestSHA256, err = HashManifest(tooManyChunks)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateManifest(tooManyChunks); err == nil {
		t.Fatal("ValidateManifest accepted too many chunks")
	}
}
