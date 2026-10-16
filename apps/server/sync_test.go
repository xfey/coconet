package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contracts"
)

type memoryArtifactStore struct {
	server  *httptest.Server
	client  *http.Client
	mutex   sync.Mutex
	objects map[string][]byte
}

func newMemoryArtifactStore(t *testing.T) *memoryArtifactStore {
	t.Helper()
	store := &memoryArtifactStore{objects: map[string][]byte{}, client: &http.Client{Timeout: 5 * time.Second}}
	store.server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if !strings.HasPrefix(request.URL.Path, "/object/") {
			response.WriteHeader(http.StatusNotFound)
			return
		}
		key, _ := url.PathUnescape(strings.TrimPrefix(request.URL.Path, "/object/"))
		if request.Method == http.MethodGet {
			store.mutex.Lock()
			content, ok := store.objects[key]
			store.mutex.Unlock()
			if !ok {
				response.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = response.Write(content)
			return
		}
		if request.Method != http.MethodPut {
			response.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		content, err := io.ReadAll(request.Body)
		if err != nil {
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		store.mutex.Lock()
		store.objects[key] = content
		store.mutex.Unlock()
		response.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(store.server.Close)
	return store
}

func (*memoryArtifactStore) Driver() string { return "memory" }

func (*memoryArtifactStore) BackendIdentity() string { return "memory:fixture" }

func (store *memoryArtifactStore) AuthorizeUpload(_ context.Context, object artifactObject, _ time.Duration) (artifactTransferTarget, error) {
	return artifactTransferTarget{URL: store.server.URL + "/object/" + url.PathEscape(object.Key)}, nil
}

func (store *memoryArtifactStore) AuthorizeDownload(_ context.Context, object artifactObject, _ time.Duration) (artifactTransferTarget, error) {
	return artifactTransferTarget{URL: store.server.URL + "/object/" + url.PathEscape(object.Key)}, nil
}

func (store *memoryArtifactStore) Stat(_ context.Context, key string) (storedObject, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	content, ok := store.objects[key]
	if !ok {
		return storedObject{}, os.ErrNotExist
	}
	return storedObject{Size: int64(len(content))}, nil
}

func (store *memoryArtifactStore) Open(_ context.Context, key string) (io.ReadCloser, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	content, ok := store.objects[key]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), content...))), nil
}

func newSyncTestAuthority(t *testing.T) (*authority, contracts.Account, contracts.Project, *memoryArtifactStore) {
	t.Helper()
	objects := newMemoryArtifactStore(t)
	storage, err := openStore(filepath.Join(t.TempDir(), "server.db"), objects)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.close() })
	authority := newAuthority(storage, objects)
	identity, err := authority.bootstrapAccount(contracts.AccountBootstrapRequest{SchemaVersion: 1, DisplayName: "Sync Fixture", Email: "sync@coconet.test"})
	if err != nil || identity.Account == nil {
		t.Fatal(err)
	}
	access, err := authority.createProject(*identity.Account, contracts.CreateProjectRequest{SchemaVersion: 1, RequestID: "request-sync-project", Name: "sync"})
	if err != nil || access.Project == nil {
		t.Fatal(err)
	}
	return authority, *identity.Account, *access.Project, objects
}

func (store *memoryArtifactStore) Write(_ context.Context, object artifactObject, data []byte) error {
	if int64(len(data)) != object.Size || hashToken(string(data)) != object.SHA256 {
		return errors.New("invalid fixture content")
	}
	store.mutex.Lock()
	defer store.mutex.Unlock()
	if old, ok := store.objects[object.Key]; ok && !bytes.Equal(old, data) {
		return errors.New("immutable fixture conflict")
	}
	store.objects[object.Key] = bytes.Clone(data)
	return nil
}
func (store *memoryArtifactStore) Delete(_ context.Context, key string) error {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	delete(store.objects, key)
	return nil
}
