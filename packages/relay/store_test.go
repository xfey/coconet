package relay

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/xfey/coconet/packages/contentstore"
	"github.com/xfey/coconet/packages/contracts"
	bolt "go.etcd.io/bbolt"
)

func fixture(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestReopenReclaimsInterruptedUploadReservation(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("interrupted upload")
	digest := contentstore.ObjectDigest(data)
	if err := s.db.Update(func(tx *bolt.Tx) error {
		value, _ := json.Marshal(objectRecord{Size: MaxProjectBytes, Lease: "previous-process"})
		if err := tx.Bucket([]byte("usage")).Put([]byte("project"), encodedNumber(MaxProjectBytes)); err != nil {
			return err
		}
		return tx.Bucket([]byte("objects")).Put(key("project", digest), value)
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutObject("project", digest, int64(len(data)), bytes.NewReader(data)); err != nil {
		t.Fatal("stale reservation blocks retry", err)
	}
}
func proposal(id string, base int64) contracts.RelayCommit {
	// Deliberately not a DAG value: the relay must keep business payload opaque.
	payload := []byte(`{"opaque":"client-owned content"}`)
	return contracts.RelayCommit{SchemaVersion: 1, ID: id, DeviceID: "device", Base: base, Codec: "coconet-json-v2", Payload: payload, SHA256: contentstore.ObjectDigest(payload), Objects: []string{}}
}
func TestRelayCASIdempotencyAndWriterBinding(t *testing.T) {
	s := fixture(t)
	input := proposal("first", 0)
	first, err := s.Append("project", "account", input)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Append("project", "account", input)
	if err != nil || again.Revision != first.Revision {
		t.Fatal("idempotent receipt lost")
	}
	if _, err := s.Append("project", "another", input); err == nil {
		t.Fatal("another actor claimed the original receipt")
	}
	if _, err := s.Append("project", "account", proposal("stale", 0)); !errors.Is(err, ErrConflict) {
		t.Fatal("stale project commit accepted")
	}
	input.Payload = []byte("changed")
	input.SHA256 = contentstore.ObjectDigest(input.Payload)
	if _, err := s.Append("project", "account", input); err == nil {
		t.Fatal("transaction identity was overwritten")
	}
}
func TestRelayConcurrentWritersHaveOneWinner(t *testing.T) {
	s := fixture(t)
	results := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	start := make(chan struct{})
	for _, id := range []string{"one", "two"} {
		go func(id string) {
			ready.Done()
			<-start
			_, err := s.Append("project", "account", proposal(id, 0))
			results <- err
		}(id)
	}
	ready.Wait()
	close(start)
	a, b := <-results, <-results
	if !(a == nil && errors.Is(b, ErrConflict) || b == nil && errors.Is(a, ErrConflict)) {
		t.Fatalf("CAS outcomes: %v %v", a, b)
	}
	page, err := s.Since("project", 0, 10)
	if err != nil || page.Head != 1 || len(page.Entries) != 1 {
		t.Fatal("concurrent append diverged")
	}
}
func TestRelayObjectsAreValidatedAndScopedToProject(t *testing.T) {
	s := fixture(t)
	data := []byte("fixed snapshot")
	digest := contentstore.ObjectDigest(data)
	input := proposal("first", 0)
	input.Objects = []string{digest}
	if _, err := s.Append("project", "account", input); err != nil {
		t.Fatal("content availability blocked graph identity publication", err)
	}
	if err := s.PutObject("project", digest, int64(len(data)), bytes.NewReader([]byte("wrong"))); err == nil {
		t.Fatal("bad upload accepted")
	}
	if err := s.PutObject("project", digest, int64(len(data)), bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Object("other", digest); !errors.Is(err, ErrObject) {
		t.Fatal("object readable across projects")
	}
	file, err := s.Object("project", digest)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(file)
	file.Close()
	if !bytes.Equal(got, data) {
		t.Fatal("content changed")
	}
	if _, err := s.Append("project", "account", input); err != nil {
		t.Fatal(err)
	}
}
func TestRelayLogPersistsAndPaginates(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append("project", "account", proposal("one", 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append("project", "account", proposal("two", 1)); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	page, err := s.Since("project", 0, 1)
	if err != nil || page.Head != 2 || len(page.Entries) != 1 || page.Entries[0].Revision != 1 {
		t.Fatal("first page invalid")
	}
	page, err = s.Since("project", 1, 1)
	if err != nil || len(page.Entries) != 1 || page.Entries[0].Revision != 2 {
		t.Fatal("second page invalid")
	}
	if _, err := s.Since("project", 3, 1); err == nil {
		t.Fatal("ahead cursor accepted")
	}
}

type blockingReader struct {
	started chan struct{}
	release chan struct{}
	body    *bytes.Reader
}

func (r *blockingReader) Read(p []byte) (int, error) {
	select {
	case r.started <- struct{}{}:
	default:
	}
	<-r.release
	return r.body.Read(p)
}
func TestUploadDoesNotHoldDatabaseTransaction(t *testing.T) {
	s := fixture(t)
	body := []byte("content")
	reader := &blockingReader{started: make(chan struct{}, 1), release: make(chan struct{}), body: bytes.NewReader(body)}
	done := make(chan error, 1)
	go func() { done <- s.PutObject("project", contentstore.ObjectDigest(body), int64(len(body)), reader) }()
	<-reader.started
	// A metadata-only commit can progress while an unrelated object is arriving.
	_, err := s.Append("project", "account", proposal("unrelated", 0))
	close(reader.release)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
