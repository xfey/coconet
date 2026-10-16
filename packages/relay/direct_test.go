package relay

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contentstore"
	"github.com/xfey/coconet/packages/contracts"
)

type memoryDirect struct {
	mu     sync.Mutex
	data   map[string][]byte
	copies int
}

func (b *memoryDirect) Identity() string { return "isolated-test-objects" }
func (b *memoryDirect) UploadURL(_ context.Context, key string, _ int64, _ time.Duration) (string, error) {
	return "https://objects.invalid/" + key, nil
}
func (b *memoryDirect) DownloadURL(_ context.Context, key string, _ time.Duration) (string, error) {
	return "https://objects.invalid/" + key, nil
}
func (b *memoryDirect) Seal(_ context.Context, source, target string, size int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	data, ok := b.data[source]
	if !ok || int64(len(data)) != size {
		return errors.New("staging object missing or incomplete")
	}
	b.data[target] = bytes.Clone(data)
	b.copies++
	return nil
}
func (b *memoryDirect) Remove(_ context.Context, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.data, key)
	return nil
}

func TestDirectUploadImmutableFinalizationAndRecovery(t *testing.T) {
	ctx := context.Background()
	backend := &memoryDirect{data: map[string][]byte{}}
	root := t.TempDir()
	s, err := OpenWithOptions(root, Options{Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	body := []byte("fixed source")
	input := contracts.RelayTransferRequest{SchemaVersion: 1, SHA256: contentstore.ObjectDigest(body), Size: int64(len(body))}
	target, err := s.BeginUpload(ctx, "project", "owner", input)
	if err != nil || target.UploadID == "" || target.Proxy {
		t.Fatal(target, err)
	}
	input.UploadID = target.UploadID
	if _, err := s.FinishUpload(ctx, "project", "owner", input); err == nil {
		t.Fatal("missing bytes finalized")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenWithOptions(root, Options{Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	start := input
	start.UploadID = ""
	again, err := s.BeginUpload(ctx, "project", "owner", start)
	if err != nil || again.UploadID != target.UploadID {
		t.Fatal("reservation lost on restart", err)
	}
	if _, err := s.FinishUpload(ctx, "project", "other", input); err == nil {
		t.Fatal("foreign completion accepted")
	}
	staging := strings.TrimPrefix(target.URL, "https://objects.invalid/")
	backend.data[staging] = bytes.Clone(body)
	if result, err := s.FinishUpload(ctx, "project", "owner", input); err != nil || !result.Ready {
		t.Fatal(result, err)
	}
	backend.data[staging] = []byte("tamper later")
	if _, err := s.FinishUpload(ctx, "project", "owner", input); err != nil {
		t.Fatal(err)
	}
	read, err := s.DownloadTarget(ctx, "project", input.SHA256)
	if err != nil || !bytes.Equal(backend.data[strings.TrimPrefix(read.URL, "https://objects.invalid/")], body) || backend.copies != 1 {
		t.Fatal("old PUT capability altered published bytes", err)
	}
	if _, err := s.DownloadTarget(ctx, "other", input.SHA256); !errors.Is(err, ErrObject) {
		t.Fatal("cross-project content leaked", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if wrong, err := Open(root); err == nil {
		wrong.Close()
		t.Fatal("backend silently switched to filesystem")
	}
	s, err = OpenWithOptions(root, Options{Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentFinalizationCopiesOnce(t *testing.T) {
	ctx := context.Background()
	backend := &memoryDirect{data: map[string][]byte{}}
	s, err := OpenWithOptions(t.TempDir(), Options{Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	body := []byte("bytes")
	input := contracts.RelayTransferRequest{SchemaVersion: 1, SHA256: contentstore.ObjectDigest(body), Size: int64(len(body))}
	target, err := s.BeginUpload(ctx, "project", "owner", input)
	if err != nil {
		t.Fatal(err)
	}
	backend.data[strings.TrimPrefix(target.URL, "https://objects.invalid/")] = body
	input.UploadID = target.UploadID
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := s.FinishUpload(ctx, "project", "owner", input); err != nil && !errors.Is(err, ErrBusy) {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	if backend.copies != 1 {
		t.Fatal("multiple final copies", backend.copies)
	}
}
