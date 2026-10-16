package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/lifecycle"
	"github.com/minio/minio-go/v7/pkg/signer"
	"github.com/xfey/coconet/packages/contentstore"
	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/conversation"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/localdag"
	"github.com/xfey/coconet/packages/relay"
	"github.com/xfey/coconet/packages/replication"
	"github.com/xfey/coconet/packages/workgraph"
	bolt "go.etcd.io/bbolt"
)

// This test is explicitly opt-in. Configuration and the exact cleanup journal
// must be private files outside the repository. It never lists/deletes a bucket
// or changes lifecycle rules; only keys created for its random project are used.
type liveRelayBackend struct {
	*relayS3Backend
	project, journal string
	mu               sync.Mutex
	keys             map[string]bool
	copies           atomic.Int64
}

func (b *liveRelayBackend) remember(keys ...string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, key := range keys {
		if !strings.HasPrefix(key, b.project+"/") || strings.Contains(key, "..") {
			return errors.New("live test key escaped its project")
		}
		b.keys[key] = true
	}
	data, err := json.Marshal(b.keys)
	if err != nil {
		return err
	}
	return os.WriteFile(b.journal, data, 0600)
}

func (b *liveRelayBackend) UploadURL(ctx context.Context, key string, size int64, ttl time.Duration) (string, error) {
	if err := b.remember(key); err != nil {
		return "", err
	}
	return b.relayS3Backend.UploadURL(ctx, key, size, ttl)
}

func (b *liveRelayBackend) Seal(ctx context.Context, source, target string, size int64) error {
	if err := b.remember(source, target); err != nil {
		return err
	}
	if err := b.relayS3Backend.Seal(ctx, source, target, size); err != nil {
		return err
	}
	b.copies.Add(1)
	return nil
}

func (b *liveRelayBackend) Remove(ctx context.Context, key string) error {
	if err := b.remember(key); err != nil {
		return err
	}
	if err := b.relayS3Backend.Remove(ctx, key); err != nil {
		return err
	}
	return nil
}

func TestLiveRelayObjectStoreAcceptance(t *testing.T) {
	configPath := os.Getenv("COCONET_LIVE_RELAY_S3_CONFIG")
	if configPath == "" {
		t.Skip("requires explicitly supplied private object-store configuration")
	}
	info, err := os.Lstat(configPath)
	if err != nil || !filepath.IsAbs(configPath) || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		t.Fatal("live object-store config must be an absolute private regular file")
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal("cannot read private object-store config")
	}
	var cfg map[string]string
	if json.Unmarshal(data, &cfg) != nil {
		t.Fatal("invalid private object-store config")
	}
	endpoint, err := url.Parse(cfg["ENDPOINT"])
	if err != nil || endpoint.Scheme != "https" {
		t.Fatal("live object-store test requires HTTPS")
	}
	evidence := os.Getenv("COCONET_LIVE_RELAY_EVIDENCE")
	if info, err := os.Lstat(evidence); err != nil || !filepath.IsAbs(evidence) || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		t.Fatal("live evidence directory must be absolute and private")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	artifact, err := openS3ArtifactStore(ctx, s3Config{Endpoint: cfg["ENDPOINT"], Region: cfg["REGION"], Bucket: cfg["BUCKET"], AccessKey: cfg["ACCESS_KEY"], SecretKey: cfg["SECRET_KEY"]})
	if err != nil {
		t.Fatal("live bucket initialization failed; check configuration and bucket access")
	}
	liveInspectLifecycle(t, ctx, cfg)
	a, handler, actor, project, token := dashboardFixture(t)
	backend := &liveRelayBackend{relayS3Backend: &relayS3Backend{store: artifact.(*s3ArtifactStore)}, project: project.ProjectID, journal: filepath.Join(evidence, "created-keys.json"), keys: map[string]bool{}}
	if _, err := os.Lstat(backend.journal); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("use a fresh evidence directory for each live run")
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 2*time.Minute)
		defer stop()
		for key := range backend.keys {
			if err := backend.relayS3Backend.Remove(cleanup, key); err != nil {
				t.Error("exact live object cleanup failed; retain the private journal")
				continue
			}
			_, err := backend.store.client.StatObject(cleanup, backend.store.bucket, relayObjectPrefix+key, minio.StatObjectOptions{})
			code := minio.ToErrorResponse(err).Code
			if code != "NoSuchKey" && code != "NoSuchObject" && code != "NotFound" {
				t.Error("removed live object did not return not-found")
			}
		}
		t.Logf("exact cleanup checked %d test keys", len(backend.keys))
	})
	root := t.TempDir()
	options := relay.Options{Backend: backend, ContentTTL: time.Hour, TransferCapacity: 16 << 20}
	storage, err := relay.OpenWithOptions(root, options)
	if err != nil {
		t.Fatal(err)
	}
	a.store.relay = storage
	var bulk, requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if (r.Method == "PUT" || r.Method == "GET") && strings.Contains(r.URL.Path, "/relay/objects/") {
			bulk.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	newClient := func(device string) (*replication.Client, *localdag.Engine) {
		s, err := graphstore.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		c := &replication.Client{Store: s, Account: actor.AccountID, Device: device, Remote: replication.HTTP{BaseURL: server.URL, Token: token, Client: &http.Client{Timeout: 30 * time.Second}}}
		return c, &localdag.Engine{Store: s, Publisher: c, Actor: c.Writer()}
	}
	owner, engine := newClient("origin")
	peer, reader := newClient("replica")
	native := make([]byte, 512<<10)
	if _, err := rand.Read(native); err != nil {
		t.Fatal(err)
	}
	entries := []conversation.Entry{{Turn: contracts.NormalizedTurn{TurnID: "one", Ordinal: 0, Role: "user", Kind: "message", Text: "先把临时存储和本地副本打通，保持接口简单。"}}}
	first, err := engine.Capture(ctx, localdag.Capture{ProjectID: project.ProjectID, Agent: "fixture", NativeID: "live-replica-source", Artifact: native, Entries: entries})
	if err != nil {
		t.Fatal(err)
	}
	// Pin the earlier version to a node, so the cumulative later version cannot
	// replace its exact evidence. No model is invoked in this storage test.
	state, _ := owner.Store.State(project.ProjectID)
	commit := workgraph.Commit{Schema: 1, ID: "live-stage", ProjectID: project.ProjectID, Actor: owner.Writer(), Base: state.Revision, RunID: "live-run", At: time.Now().UTC().Format(time.RFC3339Nano), Stage: &workgraph.StageDecision{Source: first, Action: "start", Text: &workgraph.Text{Objective: "验证临时存储"}, NewNodeID: "live-node", Reason: "固定验收来源"}}
	if err := owner.Publish(ctx, commit); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Store.Apply(commit, false); err != nil {
		t.Fatal(err)
	}
	if err := owner.SyncContents(ctx, project.ProjectID); err != nil {
		t.Fatal("initial live upload:", err)
	}
	initialCopies := backend.copies.Load()
	entries = append(entries, conversation.Entry{Turn: contracts.NormalizedTurn{TurnID: "two", Ordinal: 1, Role: "assistant", Kind: "message", Text: "新增后续证据 second-version-only，不能挂回早期节点。"}})
	native2 := append(bytes.Clone(native), []byte("second-version-only")...)
	second, err := engine.Capture(ctx, localdag.Capture{ProjectID: project.ProjectID, Agent: "fixture", NativeID: "live-replica-source", Artifact: native2, Entries: entries})
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.SyncContents(ctx, project.ProjectID); err != nil {
		t.Fatal("incremental live upload:", err)
	}
	incrementalCopies := backend.copies.Load() - initialCopies
	if incrementalCopies >= initialCopies {
		t.Fatal("cumulative version did not reuse earlier chunks")
	}
	if err := owner.PublishCheckpoint(ctx, project.ProjectID); err != nil {
		t.Fatal(err)
	}
	if err := peer.Sync(ctx, project.ProjectID); err != nil {
		t.Fatal(err)
	}
	if err := peer.SyncContents(ctx, project.ProjectID); err != nil {
		t.Fatal("live replica download:", err)
	}
	state, err = peer.Store.State(project.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	for _, sample := range []struct {
		source workgraph.Source
		raw    []byte
	}{{first, native}, {second, native2}} {
		file, err := peer.Store.ArtifactFile(state.Snapshots[sample.source.Key()].Artifact)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(file)
		if err != nil || !bytes.Equal(got, sample.raw) {
			t.Fatal("live native reconstruction differs")
		}
	}
	if coverage, err := peer.Store.IndexProject(ctx, project.ProjectID, 64); err != nil || !coverage.Complete {
		t.Fatal("live replica index incomplete", err)
	}
	beforeRead := requests.Load()
	selected, err := reader.ReadSelectedNode(ctx, project.ProjectID, "live-node", first, true)
	if err != nil {
		t.Fatal(err)
	}
	text, err := os.ReadFile(selected.CompactPath)
	if err != nil || bytes.Contains(text, []byte("second-version-only")) {
		t.Fatal("fixed version was replaced by later text")
	}
	hits, err := reader.Search(project.ProjectID, localdag.WorkSearch{Query: "second-version-only"})
	if err != nil || len(hits.ContentMatches) != 1 || hits.ContentMatches[0].Source != second || requests.Load() != beforeRead {
		t.Fatal("local fixed read/search needed network or returned wrong version", err)
	}

	// Actual provider enforcement: wrong signed size cannot upload, and a stale
	// staging capability cannot overwrite finalized bytes.
	liveSignedCapabilities(t, ctx, storage, backend, project.ProjectID, actor.AccountID)
	if os.Getenv("COCONET_LIVE_RELAY_CROSS_DEVICE") == "1" {
		liveCrossDeviceProbe(t, ctx, evidence, storage, peer.Store, project.ProjectID)
	}
	// Reserving half the total capacity charges both staging and final bytes.
	// Existing objects must prevent this request before expiry; no large body is
	// uploaded for the quota probe.
	quota := contracts.RelayTransferRequest{SchemaVersion: 1, SHA256: contentstore.ObjectDigest([]byte("quota reservation only")), Size: 8 << 20}
	if _, err := storage.BeginUpload(ctx, project.ProjectID, actor.AccountID, quota); !errors.Is(err, relay.ErrQuota) {
		t.Fatal("capacity accepted a reservation beyond its bound", err)
	}
	// No active request remains. Advance ONLY this disposable store's deadlines,
	// then reopen through the real backend and run production maintenance. This
	// validates real DELETE/restart/capacity, not days of provider lifecycle time.
	expire := func() int {
		if err := storage.Close(); err != nil {
			t.Fatal(err)
		}
		a.store.relay = nil
		liveExpireFixture(t, root)
		storage, err = relay.OpenWithOptions(root, options)
		if err != nil {
			t.Fatal(err)
		}
		a.store.relay = storage
		removed, err := storage.MaintainTemporary(ctx, 64)
		if err != nil || removed == 0 {
			t.Fatal("live expiry maintenance did not delete objects", err)
		}
		return removed
	}
	removed := expire()
	if _, err := storage.DownloadTarget(ctx, project.ProjectID, state.Snapshots[first.Key()].Conversation); !errors.Is(err, relay.ErrObject) {
		t.Fatal("expired source still advertised", err)
	}
	if target, err := storage.BeginUpload(ctx, project.ProjectID, actor.AccountID, quota); err != nil || target.URL == "" {
		t.Fatal("expiry did not release capacity for the previously rejected reservation", err)
	}
	expire() // Release the quota-only reservation before peer repair.
	t.Log("PASS capacity rejected overflow, released expired bytes and reclaimed an unfinished reservation")
	// The original source is never used again. The peer with a complete replica
	// alone repairs all missing manifests/chunks for a late third client.
	late, _ := newClient("late")
	if err := late.Sync(ctx, project.ProjectID); err != nil {
		t.Fatal(err)
	}
	repaired := false
	for range 32 {
		err := late.Pull(ctx, project.ProjectID, first, true)
		if err == nil {
			repaired = true
			break
		}
		if !errors.Is(err, replication.ErrWaitingForReplica) {
			t.Fatal("late client did not report waiting", err)
		}
		if err := peer.SyncContents(ctx, project.ProjectID); err != nil {
			t.Fatal("live holder repair failed", err)
		}
	}
	if !repaired {
		t.Fatal("live peer repair did not converge")
	}
	file, err := late.Store.ArtifactFile(state.Snapshots[first.Key()].Artifact)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(got, native) {
		t.Fatal("late client's exact raw version differs")
	}
	final, err := late.Store.State(project.ProjectID)
	if err != nil || workgraph.Digest(final) != workgraph.Digest(state) || bulk.Load() != 0 {
		t.Fatal("repair changed the graph or proxied body traffic")
	}
	t.Logf("PASS live direct transfer: first_objects=%d incremental_objects=%d raw_bytes=%d expired_generations=%d graph_revision=%d application_body_requests=%d; exact version, local index and peer-only repair passed", initialCopies, incrementalCopies, len(native2), removed, final.Revision, bulk.Load())
}

func liveSignedCapabilities(t *testing.T, ctx context.Context, s *relay.Store, b *liveRelayBackend, project, account string) {
	t.Helper()
	raw := []byte("fixed published bytes")
	in := contracts.RelayTransferRequest{SchemaVersion: 1, SHA256: contentstore.ObjectDigest(raw), Size: int64(len(raw))}
	target, err := s.BeginUpload(ctx, project, account, in)
	if err != nil {
		t.Fatal(err)
	}
	put := func(data []byte) int {
		r, _ := http.NewRequestWithContext(ctx, "PUT", target.URL, bytes.NewReader(data))
		response, err := (&http.Client{Timeout: 20 * time.Second}).Do(r)
		if err != nil {
			t.Fatal("live signed upload transport failed")
		}
		io.Copy(io.Discard, io.LimitReader(response.Body, 65536))
		response.Body.Close()
		return response.StatusCode
	}
	if code := put(append(bytes.Clone(raw), 'x')); code < 400 {
		t.Fatal("provider accepted an upload with the wrong signed size")
	}
	if code := put(raw); code != 200 {
		t.Fatalf("provider rejected correctly signed upload: %d", code)
	}
	staging := project + "/" + target.UploadID + "/upload"
	info, err := b.store.client.StatObject(ctx, b.store.bucket, relayObjectPrefix+staging, minio.StatObjectOptions{})
	if err != nil {
		t.Fatal("cannot inspect test staging object")
	}
	in.UploadID = target.UploadID
	if _, err := s.FinishUpload(ctx, project, account, in); err != nil {
		t.Fatal(err)
	}
	copies := b.copies.Load()
	if _, err := s.FinishUpload(ctx, project, account, in); err != nil || b.copies.Load() != copies {
		t.Fatal("duplicate completion copied again", err)
	}
	changed := bytes.Repeat([]byte("z"), len(raw))
	if code := put(changed); code != 200 {
		t.Fatalf("staging rewrite unexpectedly failed: %d", code)
	}
	rejected := project + "/" + target.UploadID + "/rejected-copy"
	if err := b.remember(rejected); err != nil {
		t.Fatal(err)
	}
	_, err = b.store.client.CopyObject(ctx, minio.CopyDestOptions{Bucket: b.store.bucket, Object: relayObjectPrefix + rejected}, minio.CopySrcOptions{Bucket: b.store.bucket, Object: relayObjectPrefix + staging, MatchETag: info.ETag})
	if minio.ToErrorResponse(err).Code != "PreconditionFailed" {
		t.Fatal("provider did not reject a stale source ETag")
	}
	_, err = b.store.client.StatObject(ctx, b.store.bucket, relayObjectPrefix+rejected, minio.StatObjectOptions{})
	if code := minio.ToErrorResponse(err).Code; code != "NoSuchKey" && code != "NoSuchObject" && code != "NotFound" {
		t.Fatal("rejected conditional copy created a destination")
	}
	download, err := s.DownloadTarget(ctx, project, in.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := http.NewRequestWithContext(ctx, "GET", download.URL, nil)
	response, err := (&http.Client{Timeout: 20 * time.Second}).Do(r)
	if err != nil {
		t.Fatal("live signed download transport failed")
	}
	got, readErr := io.ReadAll(io.LimitReader(response.Body, 65536))
	response.Body.Close()
	if response.StatusCode != 200 || readErr != nil || !bytes.Equal(got, raw) {
		t.Fatal("staging rewrite affected finalized content")
	}
	t.Log("PASS provider signed-size enforcement, stale ETag rejection, immutable final copy and duplicate completion")
}

func liveInspectLifecycle(t *testing.T, ctx context.Context, cfg map[string]string) {
	t.Helper()
	// Use standard header-signed S3 GET ?lifecycle. MinIO's convenience method
	// adds the withUpdatedAt extension; bucket operations also need not support
	// presigned URLs even when ordinary object transfers do.
	u, err := url.Parse(cfg["ENDPOINT"])
	if err != nil {
		t.Fatal("invalid lifecycle inspection endpoint")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + cfg["BUCKET"]
	u.RawQuery = "lifecycle="
	r, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	r.Header.Set("X-Amz-Content-Sha256", contentstore.ObjectDigest(nil))
	r = signer.SignV4(*r, cfg["ACCESS_KEY"], cfg["SECRET_KEY"], "", cfg["REGION"])
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(r)
	if err != nil {
		t.Log("UNVERIFIED lifecycle inspection: transport failed; inspect rules before deployment")
		return
	}
	defer response.Body.Close()
	decoder := xml.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode != http.StatusOK {
		var failure struct{ Code string }
		if decoder.Decode(&failure) == nil && failure.Code == "NoSuchLifecycleConfiguration" {
			t.Log("PASS lifecycle inspection: no bucket lifecycle configured")
			return
		}
		t.Logf("UNVERIFIED lifecycle inspection: HTTP %d; inspect rules before deployment", response.StatusCode)
		return
	}
	var rules lifecycle.Configuration
	if err := decoder.Decode(&rules); err != nil {
		t.Fatal("could not decode bucket lifecycle inspection")
	}
	for _, rule := range rules.Rules {
		if rule.Status != "Enabled" || rule.Expiration.IsNull() {
			continue
		}
		prefix := rule.Prefix
		if rule.RuleFilter.Prefix != "" {
			prefix = rule.RuleFilter.Prefix
		} else if rule.RuleFilter.And.Prefix != "" {
			prefix = rule.RuleFilter.And.Prefix
		}
		if strings.HasPrefix(relayObjectPrefix, prefix) || strings.HasPrefix(prefix, relayObjectPrefix) {
			t.Fatal("bucket age-based expiration overlaps the Relay prefix; review before uploading")
		}
	}
	t.Logf("PASS lifecycle inspection: %d rules, no enabled age-based expiration overlaps Relay", len(rules.Rules))
}

func liveCrossDeviceProbe(t *testing.T, ctx context.Context, evidence string, s *relay.Store, local *graphstore.Store, project string) {
	t.Helper()
	page, err := s.ObjectCatalog(project, "", 512)
	if err != nil {
		t.Fatal(err)
	}
	var targets []contracts.RelayTransferTarget
	for _, item := range page.Objects {
		// Exclude the standalone capability test which is not a Session object.
		f, err := local.OpenObject(item.SHA256)
		if err != nil {
			continue
		}
		f.Close()
		target, err := s.DownloadTarget(ctx, project, item.SHA256)
		if err != nil {
			t.Fatal(err)
		}
		targets = append(targets, target)
		if len(targets) == 2 {
			break
		}
	}
	if len(targets) != 2 {
		t.Fatal("missing cross-device probe objects")
	}
	data, _ := json.Marshal(targets)
	if err := os.WriteFile(filepath.Join(evidence, "download-probes.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("CROSS_DEVICE_READY: signed capabilities saved in private evidence directory (50 second deadline)")
	timer := time.NewTimer(50 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if data, err := os.ReadFile(filepath.Join(evidence, "cross-device-ok.json")); err == nil {
			var hosts []string
			if json.Unmarshal(data, &hosts) != nil || len(hosts) != 2 || hosts[0] != "macos" || hosts[1] != "anydev" {
				t.Fatal("invalid cross-device receipt")
			}
			t.Log("PASS actual macOS and AnyDev direct downloads with hash verification")
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-timer.C:
			t.Fatal("cross-device receipt deadline exceeded")
		case <-ticker.C:
		}
	}
}

func liveExpireFixture(t *testing.T, root string) {
	t.Helper()
	db, err := bolt.Open(filepath.Join(root, "relay.db"), 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	past := time.Now().UTC().Add(-2 * time.Hour)
	err = db.Update(func(tx *bolt.Tx) error {
		for _, name := range []string{"expiry", "download-guards", "read-leases"} {
			if err := tx.DeleteBucket([]byte(name)); err != nil {
				return err
			}
			if _, err := tx.CreateBucket([]byte(name)); err != nil {
				return err
			}
		}
		for _, name := range []string{"objects", "garbage"} {
			bucket := tx.Bucket([]byte(name))
			if err := bucket.ForEach(func(key, value []byte) error {
				var record map[string]json.RawMessage
				if err := json.Unmarshal(value, &record); err != nil {
					return err
				}
				for _, field := range []string{"upload_until", "expires_at"} {
					record[field], _ = json.Marshal(past.Format(time.RFC3339Nano))
				}
				record["maintenance_at"], _ = json.Marshal(past.UnixNano())
				data, err := json.Marshal(record)
				if err != nil {
					return err
				}
				if err := bucket.Put(key, data); err != nil {
					return err
				}
				index := append([]byte(fmt.Sprintf("%020d/%s/", past.UnixNano(), name)), key...)
				return tx.Bucket([]byte("expiry")).Put(index, []byte{1})
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
