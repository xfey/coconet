package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestFilesystemArtifactTransferThroughServer(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	artifacts, err := openFilesystemArtifactStore(root)
	if err != nil {
		t.Fatal(err)
	}
	storage, err := openStore(filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.close() })
	deployment, err := generateDeploymentIdentity()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newHandler("test", deployment, newAuthority(storage, artifacts)))
	t.Cleanup(server.Close)

	content := []byte("filesystem artifact fixture\n")
	digest := sha256.Sum256(content)
	object := artifactObject{Key: "objects/chunk_filesystem01", SHA256: hex.EncodeToString(digest[:]), Size: int64(len(content))}
	upload, err := artifacts.AuthorizeUpload(context.Background(), object, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(upload.URL, artifactTransferPrefix) || strings.HasPrefix(upload.URL, "http") || upload.Headers[artifactCapabilityHeader] == "" {
		t.Fatalf("unexpected filesystem upload target %#v", upload)
	}
	putArtifact(t, server.URL, upload, content, http.StatusNoContent)
	putArtifact(t, server.URL, upload, content, http.StatusNoContent)

	info, err := artifacts.Stat(context.Background(), object.Key)
	if err != nil || info.Size != object.Size {
		t.Fatalf("unexpected stored object %#v %v", info, err)
	}
	reader, err := artifacts.Open(context.Background(), object.Key)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := io.ReadAll(reader)
	closeErr := reader.Close()
	if err != nil || closeErr != nil || !bytes.Equal(stored, content) {
		t.Fatalf("stored bytes differ: %q %v %v", stored, err, closeErr)
	}

	download, err := artifacts.AuthorizeDownload(context.Background(), object, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodGet, server.URL+download.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range download.Headers {
		request.Header.Set(name, value)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	downloaded, readErr := io.ReadAll(response.Body)
	closeErr = response.Body.Close()
	if response.StatusCode != http.StatusOK || readErr != nil || closeErr != nil || !bytes.Equal(downloaded, content) {
		t.Fatalf("unexpected download status=%d content=%q read=%v close=%v", response.StatusCode, downloaded, readErr, closeErr)
	}
	if response.Header.Get("Cache-Control") != "private, no-store" || response.Header.Get("ETag") != `"`+object.SHA256+`"` {
		t.Fatalf("unexpected download headers %#v", response.Header)
	}

	if runtime.GOOS != "windows" {
		for _, path := range []string{root, filepath.Join(root, "objects"), filepath.Join(root, ".tmp")} {
			if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o700 {
				t.Fatalf("unexpected directory permission %s %#v %v", path, info, err)
			}
		}
		if info, err := os.Stat(filepath.Join(root, object.Key)); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("unexpected object permission %#v %v", info, err)
		}
	}
}

func TestFilesystemArtifactCapabilitiesEnforceMethodIntegrityAndExpiry(t *testing.T) {
	artifacts, err := openFilesystemArtifactStore(filepath.Join(t.TempDir(), "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	artifacts.now = func() time.Time { return now }
	content := []byte("capability fixture")
	digest := sha256.Sum256(content)
	object := artifactObject{Key: "objects/chunk_capability01", SHA256: hex.EncodeToString(digest[:]), Size: int64(len(content))}
	upload, err := artifacts.AuthorizeUpload(context.Background(), object, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPut, upload.URL, bytes.NewReader(content))
	assertArtifactErrorCode(t, artifacts.ServeArtifactTransfer(httptest.NewRecorder(), request), "artifact_capability_invalid")

	request = httptest.NewRequest(http.MethodGet, upload.URL, nil)
	request.Header.Set(artifactCapabilityHeader, upload.Headers[artifactCapabilityHeader])
	assertArtifactErrorCode(t, artifacts.ServeArtifactTransfer(httptest.NewRecorder(), request), "artifact_capability_invalid")

	request = httptest.NewRequest(http.MethodPut, artifactTransferPrefix+"chunk_other", bytes.NewReader(content))
	request.Header.Set(artifactCapabilityHeader, upload.Headers[artifactCapabilityHeader])
	assertArtifactErrorCode(t, artifacts.ServeArtifactTransfer(httptest.NewRecorder(), request), "artifact_capability_invalid")

	request = httptest.NewRequest(http.MethodPut, upload.URL, bytes.NewReader([]byte(strings.Repeat("x", len(content)))))
	request.Header.Set(artifactCapabilityHeader, upload.Headers[artifactCapabilityHeader])
	assertArtifactErrorCode(t, artifacts.ServeArtifactTransfer(httptest.NewRecorder(), request), "artifact_upload_integrity_mismatch")

	now = now.Add(time.Minute + time.Second)
	request = httptest.NewRequest(http.MethodPut, upload.URL, bytes.NewReader(content))
	request.Header.Set(artifactCapabilityHeader, upload.Headers[artifactCapabilityHeader])
	assertArtifactErrorCode(t, artifacts.ServeArtifactTransfer(httptest.NewRecorder(), request), "artifact_capability_invalid")
}

func TestFilesystemArtifactStoreRejectsUnsafePathsAndSymlinks(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	artifacts, err := openFilesystemArtifactStore(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"", "../chunk_bad", "objects/../chunk_bad", "/objects/chunk_bad", `objects\\chunk_bad`, "objects/not-a-chunk"} {
		if _, err := artifacts.Stat(context.Background(), key); err == nil {
			t.Fatalf("unsafe object key was accepted: %q", key)
		}
	}
	if runtime.GOOS == "windows" {
		t.Skip("symbolic-link semantics require Unix test permissions")
	}
	external := filepath.Join(t.TempDir(), "external")
	if err := os.WriteFile(external, []byte("external"), 0o600); err != nil {
		t.Fatal(err)
	}
	objectPath := filepath.Join(root, "objects", "chunk_symlink01")
	if err := os.Symlink(external, objectPath); err != nil {
		t.Fatal(err)
	}
	if _, err := artifacts.Stat(context.Background(), "objects/chunk_symlink01"); err == nil {
		t.Fatal("symbolic-link object was accepted")
	}
	linkedRoot := filepath.Join(t.TempDir(), "linked-root")
	if err := os.Symlink(root, linkedRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := openFilesystemArtifactStore(linkedRoot); err == nil {
		t.Fatal("symbolic-link artifact root was accepted")
	}
}

func TestArtifactBackendBindingRejectsSilentStorageChanges(t *testing.T) {
	storage, err := openStore(filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer storage.close()
	first := artifactBackendIdentity("filesystem", "/srv/coconet/artifacts-a")
	second := artifactBackendIdentity("filesystem", "/srv/coconet/artifacts-b")
	if err := storage.bindArtifactBackend(first); err != nil {
		t.Fatal(err)
	}
	if err := storage.bindArtifactBackend(first); err != nil {
		t.Fatalf("same backend identity was not idempotent: %v", err)
	}
	if err := storage.bindArtifactBackend(second); err == nil {
		t.Fatal("metadata accepted a different artifact backend")
	}
	stored, err := storage.artifactBackendID()
	if err != nil || stored != first {
		t.Fatalf("unexpected bound backend %q %v", stored, err)
	}
}

func putArtifact(t *testing.T, serverURL string, target artifactTransferTarget, content []byte, expectedStatus int) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPut, serverURL+target.URL, bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range target.Headers {
		request.Header.Set(name, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != expectedStatus {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("artifact PUT returned %d: %s", response.StatusCode, body)
	}
}

func assertArtifactErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	var domain *authorityError
	if !errors.As(err, &domain) || domain.Code != code {
		t.Fatalf("expected artifact error %q, got %v", code, err)
	}
}
