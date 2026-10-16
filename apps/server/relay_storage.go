package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/relay"
	"github.com/xfey/coconet/packages/storageguard"
)

// New Relay credentials are independent of the historical artifact backend.
// A bound metadata directory cannot silently switch buckets or key prefixes.
func openRelayStorage(root string, guards ...*storageguard.Guard) (*relay.Store, error) {
	var guard *storageguard.Guard
	if len(guards) > 0 {
		guard = guards[0]
	}
	history := 168 * time.Hour
	if value := os.Getenv("COCONET_RELAY_HISTORY_RETENTION"); value != "" {
		var err error
		history, err = time.ParseDuration(value)
		if err != nil || history < 0 || history > 0 && history < time.Hour {
			return nil, errors.New("invalid Relay history retention")
		}
	}
	endpoint := os.Getenv("COCONET_RELAY_S3_ENDPOINT")
	if endpoint == "" {
		for _, name := range []string{"REGION", "BUCKET", "ACCESS_KEY", "SECRET_KEY"} {
			if os.Getenv("COCONET_RELAY_S3_"+name) != "" {
				return nil, errors.New("Relay S3 endpoint is required when object storage is configured")
			}
		}
		if os.Getenv("COCONET_RELAY_CONTENT_TTL") != "" || os.Getenv("COCONET_RELAY_TRANSFER_BYTES") != "" {
			return nil, errors.New("temporary content policy requires object storage")
		}
		return relay.OpenWithOptions(root, relay.Options{Guard: guard, HistoryRetention: history})
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, errors.New("invalid Relay object endpoint")
	}
	ip := net.ParseIP(parsed.Hostname())
	local := parsed.Hostname() == "localhost" || ip != nil && ip.IsLoopback()
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && local) {
		return nil, errors.New("Relay object storage requires HTTPS except loopback development")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store, err := openS3ArtifactStore(ctx, s3Config{Endpoint: endpoint, Region: os.Getenv("COCONET_RELAY_S3_REGION"), Bucket: os.Getenv("COCONET_RELAY_S3_BUCKET"), AccessKey: os.Getenv("COCONET_RELAY_S3_ACCESS_KEY"), SecretKey: os.Getenv("COCONET_RELAY_S3_SECRET_KEY")})
	if err != nil {
		return nil, errors.New("Relay object storage initialization failed; check endpoint, bucket and credentials")
	}
	s3, ok := store.(*s3ArtifactStore)
	if !ok {
		return nil, errors.New("Relay object storage type differs")
	}
	ttl := 7 * 24 * time.Hour
	if value := os.Getenv("COCONET_RELAY_CONTENT_TTL"); value != "" {
		ttl, err = time.ParseDuration(value)
		if err != nil {
			return nil, errors.New("invalid temporary content TTL")
		}
	}
	var capacity int64
	if value := os.Getenv("COCONET_RELAY_TRANSFER_BYTES"); value != "" {
		capacity, err = strconv.ParseInt(value, 10, 64)
		if err != nil || capacity <= 0 {
			return nil, errors.New("invalid temporary transfer capacity")
		}
	}
	return relay.OpenWithOptions(root, relay.Options{Guard: guard, HistoryRetention: history, Backend: &relayS3Backend{store: s3}, ContentTTL: ttl, TransferCapacity: capacity})
}

type relayS3Backend struct{ store *s3ArtifactStore }

const relayObjectPrefix = "coconet-relay-v2/"

func (b *relayS3Backend) Identity() string {
	return b.store.BackendIdentity() + "/" + relayObjectPrefix
}
func (b *relayS3Backend) UploadURL(ctx context.Context, key string, size int64, expiry time.Duration) (string, error) {
	u, err := b.store.client.PresignHeader(ctx, http.MethodPut, b.store.bucket, relayObjectPrefix+key, expiry, nil, http.Header{"Content-Length": []string{strconv.FormatInt(size, 10)}})
	if err != nil {
		return "", errors.New("could not authorize object upload")
	}
	return u.String(), nil
}
func (b *relayS3Backend) DownloadURL(ctx context.Context, key string, expiry time.Duration) (string, error) {
	_, checkErr := b.store.client.StatObject(ctx, b.store.bucket, relayObjectPrefix+key, minio.StatObjectOptions{})
	if checkErr != nil {
		code := minio.ToErrorResponse(checkErr).Code
		if code == "NoSuchKey" || code == "NoSuchObject" || code == "NotFound" {
			return "", relay.ErrObject
		}
		return "", errors.New("could not verify temporary object availability")
	}
	u, err := b.store.client.PresignedGetObject(ctx, b.store.bucket, relayObjectPrefix+key, expiry, nil)
	if err != nil {
		return "", errors.New("could not authorize object download")
	}
	return u.String(), nil
}
func (b *relayS3Backend) Seal(ctx context.Context, staging, target string, size int64) error {
	info, err := b.store.client.StatObject(ctx, b.store.bucket, relayObjectPrefix+staging, minio.StatObjectOptions{})
	if err != nil || info.Size != size {
		return errors.New("uploaded object is missing or its size differs")
	}
	// Copy takes place inside COS. The final key has no client PUT capability.
	_, err = b.store.client.CopyObject(ctx, minio.CopyDestOptions{Bucket: b.store.bucket, Object: relayObjectPrefix + target}, minio.CopySrcOptions{Bucket: b.store.bucket, Object: relayObjectPrefix + staging, MatchETag: info.ETag})
	if err != nil {
		return errors.New("could not finalize uploaded object")
	}
	return nil
}
func (b *relayS3Backend) Remove(ctx context.Context, key string) error {
	if key == "" || strings.Contains(key, "..") {
		return errors.New("invalid Relay object key")
	}
	if err := b.store.client.RemoveObject(ctx, b.store.bucket, relayObjectPrefix+key, minio.RemoveObjectOptions{}); err != nil {
		return errors.New("could not remove temporary object")
	}
	return nil
}

func (h *serverHandler) serveRelayTransfer(w http.ResponseWriter, r *http.Request, requestID string, actor contracts.Account, project string, parts []string, storage *relay.Store, failure func(error)) bool {
	if len(parts) == 1 && parts[0] == "content-policy" && r.Method == http.MethodGet {
		h.writeJSON(w, 200, storage.ContentPolicy())
		return true
	}
	if len(parts) != 2 || parts[0] != "transfers" {
		return false
	}
	if r.Method != http.MethodPost {
		h.methodNotAllowed(w, requestID, "POST")
		return true
	}
	var input contracts.RelayTransferRequest
	if err := decodeRelayRetention(w, r, &input); err != nil {
		failure(err)
		return true
	}
	var result contracts.RelayTransferTarget
	var err error
	switch parts[1] {
	case "upload":
		result, err = storage.BeginUpload(r.Context(), project, actor.AccountID, input)
	case "complete":
		result, err = storage.FinishUpload(r.Context(), project, actor.AccountID, input)
	case "download":
		if input.SchemaVersion != 1 || input.UploadID != "" {
			failure(relay.ErrInvalid)
			return true
		}
		result, err = storage.DownloadTarget(r.Context(), project, input.SHA256)
	default:
		return false
	}
	if err != nil {
		failure(err)
	} else {
		h.writeJSON(w, 200, result)
	}
	return true
}
