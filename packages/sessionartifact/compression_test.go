package sessionartifact

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
)

func TestCompressedArtifactRoundTripAndAppendReuse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "native-session")
	content := append(bytes.Repeat([]byte("full tool output\n"), CompressedChunkSize/17+1), []byte("tail")...)
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	first, err := BuildVersion(path, "application/x-ndjson", 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateManifest(first.Manifest); err != nil {
		t.Fatal(err)
	}
	var restored bytes.Buffer
	stored := int64(0)
	for _, location := range first.Chunks {
		encoded, err := EncodedChunk(path, location)
		if err != nil {
			t.Fatal(err)
		}
		stored += int64(len(encoded))
		if err := DecodeChunk(bytes.NewReader(encoded), location.Chunk, &restored); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(content, restored.Bytes()) || stored >= int64(len(content))/4 {
		t.Fatal("round trip or compression failed")
	}
	if err := ValidateFile(path, first.Manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(content, []byte("new turn")...), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := BuildVersion(path, "application/x-ndjson", 2)
	if err != nil {
		t.Fatal(err)
	}
	if first.Manifest.ArtifactSHA256 == second.Manifest.ArtifactSHA256 || first.Manifest.Chunks[0] != second.Manifest.Chunks[0] {
		t.Fatal("append did not reuse the stable block")
	}
	if err := ValidateFile(path, first.Manifest); err == nil {
		t.Fatal("changed original accepted")
	}
}

type shortChunkWriter struct{}

func (shortChunkWriter) Write(content []byte) (int, error) { return len(content) - 1, nil }

func TestCompressedManifestRejectsAmbiguousIdentityAndOversizedBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), 2*CompressedChunkSize), 0600); err != nil {
		t.Fatal(err)
	}
	descriptor, err := BuildVersion(path, "application/x-ndjson", 2)
	if err != nil {
		t.Fatal(err)
	}
	manifest := descriptor.Manifest
	if err := ValidateManifest(manifest); err != nil {
		t.Fatal("valid duplicate blocks rejected", err)
	}
	manifest.Chunks = append([]contracts.ArtifactChunk{}, manifest.Chunks...)
	manifest.Chunks[1].StoredSize++
	manifest.ManifestSHA256, _ = HashManifest(manifest)
	if err := ValidateManifest(manifest); err == nil {
		t.Fatal("ambiguous repeated hash accepted")
	}
	manifest = descriptor.Manifest
	manifest.Chunks = append([]contracts.ArtifactChunk{}, manifest.Chunks...)
	manifest.Chunks[0].RawSize++
	manifest.ArtifactSize++
	manifest.ManifestSHA256, _ = HashManifest(manifest)
	if err := ValidateManifest(manifest); err == nil {
		t.Fatal("oversized compressed block accepted")
	}
	encoded, err := EncodedChunk(path, descriptor.Chunks[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := DecodeChunk(bytes.NewReader(encoded), descriptor.Chunks[0].Chunk, shortChunkWriter{}); err != io.ErrShortWrite {
		t.Fatal("short write lost", err)
	}
}

func TestCompressedChunkRejectsCorruptionTrailingDataAndExpansion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session")
	if err := os.WriteFile(path, bytes.Repeat([]byte("original"), 1000), 0600); err != nil {
		t.Fatal(err)
	}
	descriptor, err := BuildVersion(path, "application/x-ndjson", 2)
	if err != nil {
		t.Fatal(err)
	}
	chunk := descriptor.Chunks[0]
	encoded, err := EncodedChunk(path, chunk)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"corrupt", "trailing", "expansion"} {
		t.Run(mode, func(t *testing.T) {
			candidate := append([]byte(nil), encoded...)
			expected := chunk.Chunk
			switch mode {
			case "corrupt":
				candidate[len(candidate)-8] ^= 1 // corrupt gzip CRC, not unused padding bits
			case "trailing":
				candidate = append(candidate, []byte("trailing")...)
			case "expansion":
				expected.RawSize = 10
			}
			// Even matching transport hashes cannot authorize invalid decoded data.
			hash := sha256.Sum256(candidate)
			expected.StoredSHA256 = hex.EncodeToString(hash[:])
			expected.StoredSize = int64(len(candidate))
			var output bytes.Buffer
			if err := DecodeChunk(bytes.NewReader(candidate), expected, &output); err == nil {
				t.Fatal("invalid chunk accepted")
			}
			if output.Len() != 0 {
				t.Fatal("unverified bytes published")
			}
		})
	}
}
