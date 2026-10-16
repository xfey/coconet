package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

const (
	artifactAuthorizationTTL = 15 * time.Minute
	artifactCapabilityHeader = "X-Coconet-Artifact-Capability"
	artifactTransferPrefix   = "/v1/artifacts/objects/"
)

var artifactObjectKeyPattern = regexp.MustCompile(`^objects/chunk_[A-Za-z0-9_-]+$`)

type storedObject struct {
	Size int64
}

type artifactObject struct {
	Key    string
	SHA256 string
	Size   int64
}

type artifactTransferTarget struct {
	URL     string
	Headers map[string]string
}

type artifactStore interface {
	Driver() string
	BackendIdentity() string
	AuthorizeUpload(context.Context, artifactObject, time.Duration) (artifactTransferTarget, error)
	AuthorizeDownload(context.Context, artifactObject, time.Duration) (artifactTransferTarget, error)
	Stat(context.Context, string) (storedObject, error)
	Open(context.Context, string) (io.ReadCloser, error)
}

type artifactTransferStore interface {
	ServeArtifactTransfer(http.ResponseWriter, *http.Request) error
}

type s3ArtifactStore struct {
	client   *minio.Client
	bucket   string
	identity string
}

type s3Config struct {
	Endpoint  string
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
}

func openS3ArtifactStore(ctx context.Context, config s3Config) (artifactStore, error) {
	if config.Endpoint == "" {
		return nil, errors.New("S3 endpoint is required")
	}
	parsed, err := url.Parse(strings.TrimRight(config.Endpoint, "/"))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, errors.New("S3 endpoint must be an absolute HTTP or HTTPS URL")
	}
	parsed.Path = ""
	if config.Bucket == "" || config.AccessKey == "" || config.SecretKey == "" {
		return nil, errors.New("S3 bucket and credentials are required")
	}
	client, err := minio.New(parsed.Host, &minio.Options{
		Creds:  credentials.NewStaticV4(config.AccessKey, config.SecretKey, ""),
		Secure: parsed.Scheme == "https",
		Region: config.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("create S3 client: %w", err)
	}
	exists, err := client.BucketExists(ctx, config.Bucket)
	if err != nil {
		return nil, fmt.Errorf("check S3 bucket: %w", err)
	}
	if !exists {
		return nil, errors.New("configured S3 bucket does not exist; create it before starting coconet-server")
	}
	identity := artifactBackendIdentity("s3", parsed.String(), strings.TrimSpace(config.Region), config.Bucket)
	return &s3ArtifactStore{client: client, bucket: config.Bucket, identity: identity}, nil
}

func (store *s3ArtifactStore) Driver() string { return "s3" }

func (store *s3ArtifactStore) BackendIdentity() string { return store.identity }

func (store *s3ArtifactStore) AuthorizeUpload(ctx context.Context, object artifactObject, expiry time.Duration) (artifactTransferTarget, error) {
	if err := validateArtifactObject(object); err != nil {
		return artifactTransferTarget{}, err
	}
	target, err := store.client.PresignedPutObject(ctx, store.bucket, object.Key, expiry)
	if err != nil {
		return artifactTransferTarget{}, fmt.Errorf("authorize chunk upload: %w", err)
	}
	return artifactTransferTarget{URL: target.String()}, nil
}

func (store *s3ArtifactStore) AuthorizeDownload(ctx context.Context, object artifactObject, expiry time.Duration) (artifactTransferTarget, error) {
	if err := validateArtifactObject(object); err != nil {
		return artifactTransferTarget{}, err
	}
	target, err := store.client.PresignedGetObject(ctx, store.bucket, object.Key, expiry, nil)
	if err != nil {
		return artifactTransferTarget{}, fmt.Errorf("authorize chunk download: %w", err)
	}
	return artifactTransferTarget{URL: target.String()}, nil
}

func (store *s3ArtifactStore) Stat(ctx context.Context, objectKey string) (storedObject, error) {
	if !validArtifactObjectKey(objectKey) {
		return storedObject{}, errors.New("invalid object key")
	}
	info, err := store.client.StatObject(ctx, store.bucket, objectKey, minio.StatObjectOptions{})
	if err != nil {
		return storedObject{}, err
	}
	return storedObject{Size: info.Size}, nil
}

func (store *s3ArtifactStore) Open(ctx context.Context, objectKey string) (io.ReadCloser, error) {
	if !validArtifactObjectKey(objectKey) {
		return nil, errors.New("invalid object key")
	}
	object, err := store.client.GetObject(ctx, store.bucket, objectKey, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	return object, nil
}

type filesystemArtifactStore struct {
	root        string
	objectsPath string
	temporary   string
	identity    string
	secret      []byte
	now         func() time.Time
	writeMutex  sync.Mutex
}

type artifactCapability struct {
	Version   int    `json:"v"`
	Method    string `json:"method"`
	ObjectKey string `json:"object_key"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	ExpiresAt int64  `json:"expires_at"`
}

func openFilesystemArtifactStore(root string) (*filesystemArtifactStore, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) == filepath.VolumeName(filepath.Clean(root))+string(filepath.Separator) {
		return nil, errors.New("filesystem artifact path must be an absolute dedicated directory")
	}
	root = filepath.Clean(root)
	if err := secureArtifactDirectory(root); err != nil {
		return nil, fmt.Errorf("prepare filesystem artifact root: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("canonicalize filesystem artifact root: %w", err)
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return nil, err
	}
	objectsPath := filepath.Join(canonical, "objects")
	temporary := filepath.Join(canonical, ".tmp")
	if err := secureArtifactDirectory(objectsPath); err != nil {
		return nil, fmt.Errorf("prepare filesystem object directory: %w", err)
	}
	if err := secureArtifactDirectory(temporary); err != nil {
		return nil, fmt.Errorf("prepare filesystem temporary directory: %w", err)
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("create artifact capability secret: %w", err)
	}
	return &filesystemArtifactStore{
		root: canonical, objectsPath: objectsPath, temporary: temporary,
		identity: artifactBackendIdentity("filesystem", canonical), secret: secret, now: time.Now,
	}, nil
}

func (store *filesystemArtifactStore) Driver() string { return "filesystem" }

func (store *filesystemArtifactStore) BackendIdentity() string { return store.identity }

func (store *filesystemArtifactStore) AuthorizeUpload(_ context.Context, object artifactObject, expiry time.Duration) (artifactTransferTarget, error) {
	return store.authorize(http.MethodPut, object, expiry)
}

func (store *filesystemArtifactStore) AuthorizeDownload(_ context.Context, object artifactObject, expiry time.Duration) (artifactTransferTarget, error) {
	return store.authorize(http.MethodGet, object, expiry)
}

func (store *filesystemArtifactStore) authorize(method string, object artifactObject, expiry time.Duration) (artifactTransferTarget, error) {
	if err := validateArtifactObject(object); err != nil {
		return artifactTransferTarget{}, err
	}
	if expiry <= 0 {
		return artifactTransferTarget{}, errors.New("artifact authorization expiry must be positive")
	}
	payload := artifactCapability{
		Version: 1, Method: method, ObjectKey: object.Key, SHA256: object.SHA256,
		Size: object.Size, ExpiresAt: store.now().Add(expiry).Unix(),
	}
	content, err := json.Marshal(payload)
	if err != nil {
		return artifactTransferTarget{}, err
	}
	encoded := base64.RawURLEncoding.EncodeToString(content)
	signature := hmac.New(sha256.New, store.secret)
	_, _ = signature.Write([]byte(encoded))
	token := encoded + "." + base64.RawURLEncoding.EncodeToString(signature.Sum(nil))
	name := strings.TrimPrefix(object.Key, "objects/")
	return artifactTransferTarget{
		URL:     artifactTransferPrefix + name,
		Headers: map[string]string{artifactCapabilityHeader: token},
	}, nil
}

func (store *filesystemArtifactStore) Stat(ctx context.Context, objectKey string) (storedObject, error) {
	if err := ctx.Err(); err != nil {
		return storedObject{}, err
	}
	file, err := store.openObject(objectKey)
	if err != nil {
		return storedObject{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return storedObject{}, err
	}
	return storedObject{Size: info.Size()}, nil
}

func (store *filesystemArtifactStore) Open(ctx context.Context, objectKey string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return store.openObject(objectKey)
}

func (store *filesystemArtifactStore) ServeArtifactTransfer(response http.ResponseWriter, request *http.Request) error {
	objectKey, ok := artifactObjectKeyFromTransferPath(request.URL.Path)
	if !ok || request.URL.RawQuery != "" {
		return notFound("artifact_route_not_found", "artifact transfer route was not found")
	}
	capability, err := store.verifyCapability(request.Header.Get(artifactCapabilityHeader), request.Method, objectKey)
	if err != nil {
		return forbidden("artifact_capability_invalid", "a valid artifact transfer capability is required")
	}
	switch request.Method {
	case http.MethodPut:
		return store.receiveObject(response, request, capability)
	case http.MethodGet:
		return store.sendObject(response, capability)
	default:
		return forbidden("artifact_capability_invalid", "a valid artifact transfer capability is required")
	}
}

func (store *filesystemArtifactStore) verifyCapability(token, method, objectKey string) (artifactCapability, error) {
	if token == "" || len(token) > 4096 {
		return artifactCapability{}, errors.New("artifact capability is missing")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return artifactCapability{}, errors.New("artifact capability is malformed")
	}
	provided, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return artifactCapability{}, errors.New("artifact capability is malformed")
	}
	signature := hmac.New(sha256.New, store.secret)
	_, _ = signature.Write([]byte(parts[0]))
	if !hmac.Equal(provided, signature.Sum(nil)) {
		return artifactCapability{}, errors.New("artifact capability signature is invalid")
	}
	content, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return artifactCapability{}, errors.New("artifact capability is malformed")
	}
	var capability artifactCapability
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&capability); err != nil {
		return artifactCapability{}, errors.New("artifact capability is malformed")
	}
	if capability.Version != 1 || capability.Method != method || capability.ObjectKey != objectKey || capability.ExpiresAt <= store.now().Unix() {
		return artifactCapability{}, errors.New("artifact capability does not authorize this request")
	}
	if err := validateArtifactObject(artifactObject{Key: capability.ObjectKey, SHA256: capability.SHA256, Size: capability.Size}); err != nil {
		return artifactCapability{}, errors.New("artifact capability is malformed")
	}
	return capability, nil
}

func (store *filesystemArtifactStore) receiveObject(response http.ResponseWriter, request *http.Request, capability artifactCapability) error {
	if request.ContentLength >= 0 && request.ContentLength != capability.Size {
		return integrity("artifact_upload_size_mismatch", "uploaded artifact chunk has the wrong size")
	}
	if err := store.ensureDirectories(); err != nil {
		return unavailable("artifact_write_failed", "filesystem artifact storage is unavailable")
	}
	temporary, err := os.CreateTemp(store.temporary, ".chunk-*")
	if err != nil {
		return unavailable("artifact_write_failed", "filesystem artifact storage is unavailable")
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return unavailable("artifact_write_failed", "filesystem artifact storage is unavailable")
	}
	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temporary, hasher), io.LimitReader(request.Body, capability.Size+1))
	if copyErr != nil {
		_ = temporary.Close()
		return unavailable("artifact_write_failed", "filesystem artifact storage is unavailable")
	}
	if written != capability.Size || hex.EncodeToString(hasher.Sum(nil)) != capability.SHA256 {
		_ = temporary.Close()
		return integrity("artifact_upload_integrity_mismatch", "uploaded artifact chunk failed integrity validation")
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return unavailable("artifact_write_failed", "filesystem artifact storage is unavailable")
	}
	if err := temporary.Close(); err != nil {
		return unavailable("artifact_write_failed", "filesystem artifact storage is unavailable")
	}

	store.writeMutex.Lock()
	defer store.writeMutex.Unlock()
	destination, err := store.objectPath(capability.ObjectKey)
	if err != nil {
		return integrity("artifact_object_path_invalid", "artifact object path is invalid")
	}
	if _, err := os.Lstat(destination); err == nil {
		matches, verifyErr := store.existingObjectMatches(capability)
		if verifyErr != nil {
			return unavailable("artifact_read_failed", "filesystem artifact storage is unavailable")
		}
		if !matches {
			return conflict("artifact_object_conflict", "artifact object already exists with different content")
		}
		response.WriteHeader(http.StatusNoContent)
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return unavailable("artifact_write_failed", "filesystem artifact storage is unavailable")
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return unavailable("artifact_write_failed", "filesystem artifact storage is unavailable")
	}
	if err := os.Chmod(destination, 0o600); err != nil {
		return unavailable("artifact_write_failed", "filesystem artifact storage is unavailable")
	}
	directory, err := os.Open(store.objectsPath)
	if err != nil {
		return unavailable("artifact_write_failed", "filesystem artifact storage is unavailable")
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil || closeErr != nil {
		return unavailable("artifact_write_failed", "filesystem artifact storage is unavailable")
	}
	response.WriteHeader(http.StatusNoContent)
	return nil
}

func (store *filesystemArtifactStore) sendObject(response http.ResponseWriter, capability artifactCapability) error {
	file, err := store.openObject(capability.ObjectKey)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return notFound("artifact_object_not_found", "artifact object was not found")
		}
		return unavailable("artifact_read_failed", "filesystem artifact storage is unavailable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() != capability.Size {
		return integrity("artifact_object_integrity_mismatch", "stored artifact chunk failed integrity validation")
	}
	response.Header().Set("Content-Type", "application/octet-stream")
	response.Header().Set("Content-Length", fmt.Sprintf("%d", capability.Size))
	response.Header().Set("Cache-Control", "private, no-store")
	response.Header().Set("ETag", `"`+capability.SHA256+`"`)
	response.WriteHeader(http.StatusOK)
	if _, err := io.Copy(response, file); err != nil {
		return unavailable("artifact_read_failed", "filesystem artifact storage is unavailable")
	}
	return nil
}

func (store *filesystemArtifactStore) existingObjectMatches(capability artifactCapability) (bool, error) {
	file, err := store.openObject(capability.ObjectKey)
	if err != nil {
		return false, err
	}
	defer file.Close()
	hasher := sha256.New()
	written, err := io.Copy(hasher, io.LimitReader(file, capability.Size+1))
	if err != nil {
		return false, err
	}
	return written == capability.Size && hex.EncodeToString(hasher.Sum(nil)) == capability.SHA256, nil
}

func (store *filesystemArtifactStore) openObject(objectKey string) (*os.File, error) {
	if err := store.ensureDirectories(); err != nil {
		return nil, err
	}
	path, err := store.objectPath(objectKey)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("artifact object must be a regular file")
	}
	if info.Mode().Perm() != 0o600 {
		return nil, errors.New("artifact object permissions are unsafe")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		_ = file.Close()
		return nil, errors.New("artifact object changed while opening")
	}
	return file, nil
}

func (store *filesystemArtifactStore) objectPath(objectKey string) (string, error) {
	if !validArtifactObjectKey(objectKey) {
		return "", errors.New("invalid object key")
	}
	name := strings.TrimPrefix(objectKey, "objects/")
	path := filepath.Join(store.objectsPath, name)
	if filepath.Dir(path) != store.objectsPath {
		return "", errors.New("object path escapes artifact root")
	}
	return path, nil
}

func (store *filesystemArtifactStore) ensureDirectories() error {
	for _, path := range []string{store.root, store.objectsPath, store.temporary} {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
			return errors.New("artifact storage directory was replaced")
		}
	}
	return nil
}

func secureArtifactDirectory(path string) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("artifact storage path must be a directory and not a symbolic link")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

func artifactObjectKeyFromTransferPath(path string) (string, bool) {
	if !strings.HasPrefix(path, artifactTransferPrefix) {
		return "", false
	}
	name := strings.TrimPrefix(path, artifactTransferPrefix)
	if name == "" || strings.Contains(name, "/") {
		return "", false
	}
	key := "objects/" + name
	return key, validArtifactObjectKey(key)
}

func validateArtifactObject(object artifactObject) error {
	if !validArtifactObjectKey(object.Key) || object.Size <= 0 {
		return errors.New("invalid artifact object")
	}
	hash, err := hex.DecodeString(object.SHA256)
	if err != nil || len(hash) != sha256.Size || object.SHA256 != strings.ToLower(object.SHA256) {
		return errors.New("invalid artifact object hash")
	}
	return nil
}

func validArtifactObjectKey(objectKey string) bool {
	return artifactObjectKeyPattern.MatchString(objectKey) && filepath.ToSlash(objectKey) == objectKey
}

func artifactBackendIdentity(driver string, values ...string) string {
	hasher := sha256.New()
	_, _ = io.WriteString(hasher, driver)
	for _, value := range values {
		_, _ = hasher.Write([]byte{0})
		_, _ = io.WriteString(hasher, value)
	}
	return driver + ":" + hex.EncodeToString(hasher.Sum(nil))
}
