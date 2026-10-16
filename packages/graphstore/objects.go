package graphstore

import (
	"errors"
	"io"
	"os"
	"sync"

	"github.com/xfey/coconet/packages/contentstore"
)

const MaxObjectBytes = contentstore.MaxObjectBytes

type ObjectRead struct {
	*os.File
	once    sync.Once
	release func()
}

func (r *ObjectRead) Close() error {
	var err error
	r.once.Do(func() { err = r.File.Close(); r.release() })
	return err
}
func (s *Store) OpenObject(digest string) (*ObjectRead, error) {
	release := s.PinContent()
	file, err := s.objects.OpenObject(digest)
	if err != nil {
		release()
		return nil, err
	}
	if err := s.objects.Touch(digest); err != nil {
		file.Close()
		release()
		return nil, err
	}
	return &ObjectRead{File: file, release: release}, nil
}
func ObjectDigest(data []byte) string { return contentstore.ObjectDigest(data) }
func (s *Store) Object(digest string) ([]byte, error) {
	file, err := s.OpenObject(digest)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, MaxObjectBytes+1))
}
func (s *Store) PutObject(data []byte) (string, error) {
	release := s.PinContent()
	defer release()
	s.objectQuotaMu.Lock()
	defer s.objectQuotaMu.Unlock()
	digest := ObjectDigest(data)
	old, err := s.objects.Stat(digest)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	added := int64(len(data))
	if old != nil {
		added = 0
	}
	if added > 0 && added > s.objectLimit.Load()-s.objectBytes.Load() {
		return "", ErrLocalObjectQuota
	}
	digest, err = s.objects.PutObject(data)
	if err != nil {
		return "", err
	}
	s.objectBytes.Add(added)
	return digest, s.objects.Touch(digest)
}

type countingObjectReader struct {
	reader io.Reader
	read   int64
	limit  int64
}

func (r *countingObjectReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.read += int64(n)
	if r.read > r.limit {
		return n, ErrLocalObjectQuota
	}
	return n, err
}
func (s *Store) Receive(reader io.Reader, digest string) error {
	release := s.PinContent()
	defer release()
	s.objectQuotaMu.Lock()
	defer s.objectQuotaMu.Unlock()
	old, err := s.objects.Stat(digest)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	oldSize := int64(0)
	if old != nil {
		oldSize = old.Size()
	}
	available := max(0, s.objectLimit.Load()-s.objectBytes.Load()+oldSize)
	counted := &countingObjectReader{reader: io.LimitReader(reader, min(available, MaxObjectBytes)+1), limit: available}
	err = s.objects.Receive(counted, digest)
	if err != nil {
		if counted.read > available {
			return ErrLocalObjectQuota
		}
		return err
	}
	info, err := s.objects.Stat(digest)
	if err != nil {
		return err
	}
	s.objectBytes.Add(info.Size() - oldSize)
	return s.objects.Touch(digest)
}
