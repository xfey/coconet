// Package contentstore stores immutable, content-addressed byte objects.
package contentstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const MaxObjectBytes int64 = 256 << 20

// ErrInput describes rejected caller-provided bytes, not a storage failure.
var ErrInput = errors.New("invalid content input")

type Store struct{ root string }

func Open(root string) (*Store, error) {
	if root == "" {
		return nil, errors.New("object root is required")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("object root must be a real directory")
	}
	return &Store{root: root}, nil
}
func ObjectDigest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func (s *Store) objectPath(digest string) (string, error) {
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != sha256.Size || hex.EncodeToString(decoded) != digest {
		return "", fmt.Errorf("%w: invalid object digest", ErrInput)
	}
	return filepath.Join(s.root, "objects", digest[:2], digest), nil
}

// Never follow a substituted cache directory into an unrelated user path.
func (s *Store) objectDirectory(path string, create bool) error {
	for _, directory := range []string{filepath.Join(s.root, "objects"), filepath.Dir(path)} {
		if create {
			if err := os.Mkdir(directory, 0700); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
		}
		info, err := os.Lstat(directory)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("content directory must be a real owned directory")
		}
	}
	return nil
}

func (s *Store) Stat(digest string) (os.FileInfo, error) {
	path, err := s.objectPath(digest)
	if err != nil {
		return nil, err
	}
	if err := s.objectDirectory(path, false); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxObjectBytes {
		return nil, errors.New("invalid content object")
	}
	return info, nil
}

func (s *Store) Touch(digest string) error {
	path, err := s.objectPath(digest)
	if err != nil {
		return err
	}
	if _, err := s.Stat(digest); err != nil {
		return err
	}
	return os.Chtimes(path, time.Now(), time.Now())
}
func (s *Store) PutObject(data []byte) (string, error) {
	digest := ObjectDigest(data)
	path, err := s.objectPath(digest)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(path); err == nil {
		f, err := s.OpenObject(digest)
		if err != nil {
			return digest, err
		}
		return digest, f.Close()
	} else if !errors.Is(err, os.ErrNotExist) {
		return digest, err
	}
	return digest, s.PutStream(bytes.NewReader(data), digest, int64(len(data)))
}

// PutStream verifies length and digest before publication, using bounded memory.
func (s *Store) PutStream(reader io.Reader, digest string, size int64) error {
	if size < 0 || size > MaxObjectBytes {
		return fmt.Errorf("%w: object exceeds size limit", ErrInput)
	}
	return s.receive(reader, digest, size)
}

// Receive verifies an unknown-length download with a strict size ceiling.
func (s *Store) Receive(reader io.Reader, digest string) error {
	return s.receive(reader, digest, -1)
}

func (s *Store) receive(reader io.Reader, digest string, size int64) error {
	path, err := s.objectPath(digest)
	if err != nil {
		return err
	}
	if err := s.objectDirectory(path, true); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".pending-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	hash := sha256.New()
	limit := size
	if limit < 0 {
		limit = MaxObjectBytes
	}
	n, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(reader, limit+1))
	if err == nil && (n > limit || size >= 0 && n != size || hex.EncodeToString(hash.Sum(nil)) != digest) {
		err = fmt.Errorf("%w: content object integrity mismatch", ErrInput)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	// Link instead of rename: immutable identities must not replace existing bytes.
	if err := os.Link(f.Name(), path); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	verified, err := s.OpenObject(digest)
	if err != nil {
		return err
	}
	verified.Close()
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (s *Store) OpenObject(digest string) (*os.File, error) {
	path, err := s.objectPath(digest)
	if err != nil {
		return nil, err
	}
	info, err := s.Stat(digest)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxObjectBytes {
		return nil, errors.New("invalid content object")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(f, MaxObjectBytes+1))
	if err != nil || n != info.Size() || hex.EncodeToString(hash.Sum(nil)) != digest {
		f.Close()
		return nil, errors.New("content object integrity mismatch")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
func (s *Store) Object(digest string) ([]byte, error) {
	f, err := s.OpenObject(digest)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, MaxObjectBytes+1))
}

// Remove deletes exactly one validated object. The caller owns reference,
// lease and writer exclusion; native Session paths are never accepted here.
func (s *Store) Remove(digest string) error {
	path, err := s.objectPath(digest)
	if err != nil {
		return err
	}
	if err := s.objectDirectory(path, false); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("content deletion requires a regular object")
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
