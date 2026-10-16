package sessionartifact

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/xfey/coconet/packages/contracts"
)

const (
	ChunkingSchemaVersion         = 1
	DefaultChunkSize              = 1 << 20
	MaxChunkCount                 = 1024
	CompressedChunkSize           = 256 << 10
	CompressedChunkingVersion     = 2
	CapabilityCompressedArtifacts = "session_artifact_gzip_v2"
	MaxArtifactSize               = 256 << 20
)

type ChunkLocation struct {
	Chunk  contracts.ArtifactChunk
	Offset int64
}

type Descriptor struct {
	Manifest contracts.ArtifactManifest
	Chunks   []ChunkLocation
}

type manifestContent struct {
	SchemaVersion         int                       `json:"schema_version"`
	ArtifactSHA256        string                    `json:"artifact_sha256"`
	ArtifactSize          int64                     `json:"artifact_size"`
	ArtifactMediaType     string                    `json:"artifact_media_type"`
	ChunkingSchemaVersion int                       `json:"chunking_schema_version"`
	Chunks                []contracts.ArtifactChunk `json:"chunks"`
}

func Build(path, mediaType string) (Descriptor, error) {
	return BuildVersion(path, mediaType, ChunkingSchemaVersion)
}

func BuildVersion(path, mediaType string, format int) (Descriptor, error) {
	chunkSize, maxChunks := DefaultChunkSize, 256
	if format == CompressedChunkingVersion {
		chunkSize, maxChunks = CompressedChunkSize, MaxChunkCount
	} else if format != ChunkingSchemaVersion {
		return Descriptor{}, errors.New("unsupported artifact format")
	}
	if path == "" || mediaType == "" {
		return Descriptor{}, errors.New("artifact path and media type are required")
	}
	file, err := os.Open(path)
	if err != nil {
		return Descriptor{}, fmt.Errorf("open artifact: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Descriptor{}, fmt.Errorf("inspect artifact: %w", err)
	}
	if info.Size() > MaxArtifactSize {
		return Descriptor{}, errors.New("artifact exceeds the supported capacity")
	}

	artifactHasher := sha256.New()
	buffer := make([]byte, chunkSize)
	var size int64
	locations := make([]ChunkLocation, 0)
	for ordinal := 0; ; ordinal++ {
		count, readErr := io.ReadFull(file, buffer)
		if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
			return Descriptor{}, fmt.Errorf("read artifact: %w", readErr)
		}
		if count == 0 {
			break
		}
		content := buffer[:count]
		_, _ = artifactHasher.Write(content)
		digest := sha256.Sum256(content)
		chunk := contracts.ArtifactChunk{Ordinal: ordinal, SHA256: hex.EncodeToString(digest[:]), RawSize: int64(count)}
		if format == CompressedChunkingVersion {
			encoded, err := compressChunk(content)
			if err != nil {
				return Descriptor{}, err
			}
			storedDigest := sha256.Sum256(encoded)
			chunk.Encoding, chunk.StoredSize, chunk.StoredSHA256 = "gzip", int64(len(encoded)), hex.EncodeToString(storedDigest[:])
		}
		locations = append(locations, ChunkLocation{Chunk: chunk, Offset: size})
		size += int64(count)
		if size > MaxArtifactSize || len(locations) > maxChunks {
			return Descriptor{}, errors.New("artifact exceeds the supported capacity")
		}
		if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
			break
		}
	}
	if size == 0 {
		return Descriptor{}, errors.New("artifact must not be empty")
	}

	chunks := make([]contracts.ArtifactChunk, len(locations))
	for index := range locations {
		chunks[index] = locations[index].Chunk
	}
	manifest := contracts.ArtifactManifest{
		SchemaVersion:         contracts.SchemaVersion,
		ArtifactSHA256:        hex.EncodeToString(artifactHasher.Sum(nil)),
		ArtifactSize:          size,
		ArtifactMediaType:     mediaType,
		ChunkingSchemaVersion: format,
		Chunks:                chunks,
	}
	manifest.ManifestSHA256, err = HashManifest(manifest)
	if err != nil {
		return Descriptor{}, err
	}
	return Descriptor{Manifest: manifest, Chunks: locations}, nil
}

func HashManifest(manifest contracts.ArtifactManifest) (string, error) {
	content, err := json.Marshal(manifestContent{
		SchemaVersion:         manifest.SchemaVersion,
		ArtifactSHA256:        manifest.ArtifactSHA256,
		ArtifactSize:          manifest.ArtifactSize,
		ArtifactMediaType:     manifest.ArtifactMediaType,
		ChunkingSchemaVersion: manifest.ChunkingSchemaVersion,
		Chunks:                manifest.Chunks,
	})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:]), nil
}

func ValidateManifest(manifest contracts.ArtifactManifest) error {
	if manifest.SchemaVersion != contracts.SchemaVersion || manifest.ChunkingSchemaVersion != ChunkingSchemaVersion && manifest.ChunkingSchemaVersion != CompressedChunkingVersion {
		return errors.New("unsupported artifact manifest version")
	}
	if manifest.ArtifactMediaType == "" || !validDigest(manifest.ArtifactSHA256) || !validDigest(manifest.ManifestSHA256) {
		return errors.New("artifact manifest metadata is incomplete")
	}
	if manifest.ArtifactSize <= 0 || len(manifest.Chunks) == 0 {
		return errors.New("artifact manifest has no content")
	}
	chunkSize, maxChunks := int64(DefaultChunkSize), 256
	if manifest.ChunkingSchemaVersion == CompressedChunkingVersion {
		chunkSize, maxChunks = CompressedChunkSize, MaxChunkCount
	}
	if manifest.ArtifactSize > MaxArtifactSize || len(manifest.Chunks) > maxChunks {
		return errors.New("artifact manifest exceeds the supported capacity")
	}
	var total int64
	identities := make(map[string]contracts.ArtifactChunk)
	for index, chunk := range manifest.Chunks {
		if chunk.Ordinal != index || chunk.RawSize <= 0 || chunk.RawSize > chunkSize || !validDigest(chunk.SHA256) {
			return fmt.Errorf("artifact chunk %d is invalid", index)
		}
		if manifest.ChunkingSchemaVersion == CompressedChunkingVersion {
			if chunk.Encoding != "gzip" || !validDigest(chunk.StoredSHA256) || chunk.StoredSize <= 0 || chunk.StoredSize > chunk.RawSize+1024 {
				return errors.New("invalid compressed artifact chunk")
			}
		} else if chunk.Encoding != "" || chunk.StoredSize != 0 || chunk.StoredSHA256 != "" {
			return errors.New("legacy artifact has compressed metadata")
		}
		identity := chunk
		identity.Ordinal = 0
		if prior, exists := identities[chunk.SHA256]; exists && prior != identity {
			return errors.New("repeated chunk has inconsistent metadata")
		}
		identities[chunk.SHA256] = identity
		total += chunk.RawSize
	}
	if total != manifest.ArtifactSize {
		return errors.New("artifact manifest size does not match chunks")
	}
	digest, err := HashManifest(manifest)
	if err != nil {
		return err
	}
	if digest != manifest.ManifestSHA256 {
		return errors.New("artifact manifest hash mismatch")
	}
	return nil
}

func OpenChunk(path string, chunk ChunkLocation) (*io.SectionReader, *os.File, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	return io.NewSectionReader(file, chunk.Offset, chunk.Chunk.RawSize), file, nil
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func compressChunk(content []byte) ([]byte, error) {
	var encoded bytes.Buffer
	writer, err := gzip.NewWriterLevel(&encoded, gzip.BestSpeed)
	if err != nil {
		return nil, err
	}
	if _, err := writer.Write(content); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return encoded.Bytes(), nil
}

func StoredSize(chunk contracts.ArtifactChunk) int64 {
	if chunk.Encoding != "" {
		return chunk.StoredSize
	}
	return chunk.RawSize
}
func StoredHash(chunk contracts.ArtifactChunk) string {
	if chunk.Encoding != "" {
		return chunk.StoredSHA256
	}
	return chunk.SHA256
}

// ValidateFile checks the original bytes, independent of compressor versions.
// Downloaded sources must not need recompression to prove their identity.
func ValidateFile(path string, manifest contracts.ArtifactManifest) error {
	if err := ValidateManifest(manifest); err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() != manifest.ArtifactSize {
		return errors.New("artifact size differs from manifest")
	}
	whole := sha256.New()
	for _, chunk := range manifest.Chunks {
		hasher := sha256.New()
		written, err := io.CopyN(io.MultiWriter(whole, hasher), file, chunk.RawSize)
		if err != nil || written != chunk.RawSize || hex.EncodeToString(hasher.Sum(nil)) != chunk.SHA256 {
			return errors.New("artifact chunk differs from manifest")
		}
	}
	if hex.EncodeToString(whole.Sum(nil)) != manifest.ArtifactSHA256 {
		return errors.New("artifact hash differs from manifest")
	}
	return nil
}

// EncodedChunk prepares at most one bounded block, not an entire Session.
func EncodedChunk(path string, location ChunkLocation) ([]byte, error) {
	reader, file, err := OpenChunk(path, location)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(content)
	if int64(len(content)) != location.Chunk.RawSize || hex.EncodeToString(hash[:]) != location.Chunk.SHA256 {
		return nil, errors.New("artifact changed after preparation")
	}
	if location.Chunk.Encoding == "gzip" {
		content, err = compressChunk(content)
		if err != nil {
			return nil, err
		}
	}
	hash = sha256.Sum256(content)
	if int64(len(content)) != StoredSize(location.Chunk) || hex.EncodeToString(hash[:]) != StoredHash(location.Chunk) {
		return nil, errors.New("encoded artifact differs from prepared manifest")
	}
	return content, nil
}

// DecodeChunk verifies both stored and original bytes, rejects trailing data,
// and bounds decompression before writing original bytes to the destination.
func DecodeChunk(source io.Reader, chunk contracts.ArtifactChunk, destination io.Writer) error {
	size := StoredSize(chunk)
	if size <= 0 || size > DefaultChunkSize+1024 || chunk.RawSize <= 0 || chunk.RawSize > DefaultChunkSize {
		return errors.New("invalid chunk size")
	}
	content, err := io.ReadAll(io.LimitReader(source, size+1))
	if err != nil {
		return err
	}
	digest := sha256.Sum256(content)
	if int64(len(content)) != size || hex.EncodeToString(digest[:]) != StoredHash(chunk) {
		return errors.New("stored chunk failed integrity validation")
	}
	raw := content
	if chunk.Encoding == "gzip" {
		buffer := bytes.NewReader(content)
		reader, err := gzip.NewReader(buffer)
		if err != nil {
			return err
		}
		reader.Multistream(false)
		raw, err = io.ReadAll(io.LimitReader(reader, chunk.RawSize+1))
		closeErr := reader.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if buffer.Len() != 0 {
			return errors.New("compressed chunk has trailing data")
		}
	} else if chunk.Encoding != "" {
		return errors.New("unsupported chunk encoding")
	}
	digest = sha256.Sum256(raw)
	if int64(len(raw)) != chunk.RawSize || hex.EncodeToString(digest[:]) != chunk.SHA256 {
		return errors.New("raw chunk failed integrity validation")
	}
	written, err := destination.Write(raw)
	if err == nil && written != len(raw) {
		return io.ErrShortWrite
	}
	return err
}
