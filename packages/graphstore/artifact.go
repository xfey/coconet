package graphstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/sessionartifact"
)

// SaveArtifact reads a fixed source without changing it. The root is a manifest
// of independently compressed chunks; unchanged prefix chunks are reused.
func (s *Store) SaveArtifact(path, mediaType string) (string, error) {
	release := s.PinContent()
	defer release()
	descriptor, err := sessionartifact.BuildVersion(path, mediaType, sessionartifact.CompressedChunkingVersion)
	if err != nil {
		return "", err
	}
	for _, chunk := range descriptor.Chunks {
		data, err := sessionartifact.EncodedChunk(path, chunk)
		if err != nil {
			return "", err
		}
		digest, err := s.PutObject(data)
		if err != nil {
			return "", err
		}
		if digest != sessionartifact.StoredHash(chunk.Chunk) {
			return "", errors.New("artifact chunk identity differs")
		}
	}
	data, err := json.Marshal(descriptor.Manifest)
	if err != nil {
		return "", err
	}
	return s.PutObject(data)
}

// SaveArtifactBytes is the bounded JSON debugging entry's convenience adapter.
// Native capture uses SaveArtifact directly on its fixed copy.
func (s *Store) SaveArtifactBytes(data []byte) (string, error) {
	if len(data) == 0 || len(data) > sessionartifact.MaxArtifactSize {
		return "", errors.New("artifact size is invalid")
	}
	file, err := os.CreateTemp(s.root, ".artifact-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(file.Name())
	_, err = io.Copy(file, bytes.NewReader(data))
	err = errors.Join(err, file.Close())
	if err != nil {
		return "", err
	}
	return s.SaveArtifact(file.Name(), "application/octet-stream")
}

func (s *Store) ArtifactManifest(digest string) (contracts.ArtifactManifest, error) {
	var manifest contracts.ArtifactManifest
	file, err := s.OpenObject(digest)
	if err != nil {
		return manifest, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
	if err != nil {
		return manifest, err
	}
	if len(data) > 1<<20 {
		return manifest, errors.New("artifact manifest exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return manifest, err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return manifest, errors.New("expected one artifact manifest")
	}
	return manifest, sessionartifact.ValidateManifest(manifest)
}

// ValidateArtifact requires a complete, decodable local closure before capture
// publication, including in offline mode without a replication validator.
func (s *Store) ValidateArtifact(digest string) error {
	release := s.PinContent()
	defer release()
	manifest, err := s.ArtifactManifest(digest)
	if err != nil {
		return err
	}
	hash := sha256.New()
	for _, chunk := range manifest.Chunks {
		input, err := s.OpenObject(sessionartifact.StoredHash(chunk))
		if err != nil {
			return err
		}
		err = sessionartifact.DecodeChunk(input, chunk, hash)
		input.Close()
		if err != nil {
			return err
		}
	}
	if hex.EncodeToString(hash.Sum(nil)) != manifest.ArtifactSHA256 {
		return errors.New("reconstructed artifact digest differs")
	}
	return nil
}

// ArtifactFile materializes an immutable local cache file for native Fork.
// Neither this method nor cache cleanup receives an original Agent path.
func (s *Store) ArtifactFile(digest string) (string, error) {
	release := s.PinContent()
	defer release()
	s.materializedMu.Lock()
	defer s.materializedMu.Unlock()
	manifest, err := s.ArtifactManifest(digest)
	if err != nil {
		return "", err
	}
	directory, err := s.materializedDirectory()
	if err != nil {
		return "", err
	}
	path := filepath.Join(directory, manifest.ArtifactSHA256)
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return "", errors.New("artifact cache is not a regular file")
		}
		if err := sessionartifact.ValidateFile(path, manifest); err != nil {
			return "", err
		}
		return path, s.touchMaterialized(path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if _, err := s.allowMaterialized(path, manifest.ArtifactSize); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(directory, ".artifact-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	hash := sha256.New()
	for _, chunk := range manifest.Chunks {
		input, err := s.OpenObject(sessionartifact.StoredHash(chunk))
		if err != nil {
			return "", err
		}
		err = sessionartifact.DecodeChunk(input, chunk, io.MultiWriter(file, hash))
		input.Close()
		if err != nil {
			return "", err
		}
	}
	if hex.EncodeToString(hash.Sum(nil)) != manifest.ArtifactSHA256 {
		return "", errors.New("reconstructed artifact digest differs")
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	if err := os.Link(file.Name(), path); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return "", err
		}
	} else {
		s.materializedBytes.Add(manifest.ArtifactSize)
	}
	dir, err := os.Open(directory)
	if err != nil {
		return "", err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return "", err
	}
	if err := sessionartifact.ValidateFile(path, manifest); err != nil {
		return "", err
	}
	return path, s.touchMaterialized(path)
}
