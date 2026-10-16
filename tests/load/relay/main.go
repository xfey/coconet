// Bounded HTTP load fixture for an explicitly initialized, isolated loopback server.
// Synthetic opaque commits measure Relay transport/storage, not client replay or model quality.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/workgraph"
	bolt "go.etcd.io/bbolt"
)

type project struct {
	ID, Token, Account, Cookie string
	Head                       int64
	Nodes                      int
	mu                         sync.Mutex
}
type fixture struct {
	URL, Deployment string
	Projects        []*project
}
type measurements struct {
	mu     sync.Mutex
	By     map[string]*metric
	Errors []string
	Failed int
}
type metric struct {
	Count          int
	Codes          map[int]int
	Received, Sent int64
	MS             []float64
}

var stats = measurements{By: map[string]*metric{}}
var client = &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{MaxIdleConns: 256, MaxIdleConnsPerHost: 256, MaxConnsPerHost: 256}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
var root, mode string
var f fixture
var seq atomic.Int64

func must(e error) {
	if e != nil {
		panic(e)
	}
}
func encoded(v any) []byte   { b, e := json.Marshal(v); must(e); return b }
func digest(b []byte) string { x := sha256.Sum256(b); return hex.EncodeToString(x[:]) }
func record(name string, code int, ms float64, sent, received int64, err error) {
	stats.mu.Lock()
	defer stats.mu.Unlock()
	m := stats.By[name]
	if m == nil {
		m = &metric{Codes: map[int]int{}}
		stats.By[name] = m
	}
	m.Count++
	m.Codes[code]++
	m.MS = append(m.MS, ms)
	m.Sent += sent
	m.Received += received
	if err != nil {
		stats.Failed++
		if len(stats.Errors) < 8 {
			stats.Errors = append(stats.Errors, err.Error())
		}
	}
}
func request(ctx context.Context, name, method, path string, p *project, body []byte, extra map[string]string, accepted ...int) ([]byte, http.Header, int, error) {
	target := f.URL + path
	if strings.HasPrefix(path, "http://") {
		u, e := url.Parse(path)
		if e != nil || u.Hostname() != "127.0.0.1" {
			return nil, nil, 0, fmt.Errorf("non-loopback transfer rejected")
		}
		target = path
	}
	req, e := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if e != nil {
		return nil, nil, 0, e
	}
	if p != nil {
		req.Header.Set("Authorization", "Bearer "+p.Token)
		if strings.HasPrefix(path, "/v1/dashboard/") {
			req.Header.Set("Cookie", p.Cookie)
		}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("Origin", strings.Replace(f.URL, "http:", "https:", 1))
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	start := time.Now()
	resp, e := client.Do(req)
	var b []byte
	var h http.Header
	code := 0
	if e == nil {
		code = resp.StatusCode
		h = resp.Header
		b, e = io.ReadAll(io.LimitReader(resp.Body, 24<<20))
		ce := resp.Body.Close()
		if e == nil {
			e = ce
		}
		ok := false
		for _, v := range accepted {
			ok = ok || code == v
		}
		if !ok && e == nil {
			e = fmt.Errorf("%s HTTP %d", name, code)
		}
	}
	record(name, code, float64(time.Since(start).Microseconds())/1000, int64(len(body)), int64(len(b)), e)
	return b, h, code, e
}
func api(ctx context.Context, name, method, path string, p *project, in, out any, accepted ...int) (int, error) {
	var body []byte
	if in != nil {
		body = encoded(in)
	}
	b, _, code, e := request(ctx, name, method, path, p, body, nil, accepted...)
	if e == nil && out != nil {
		e = json.Unmarshal(b, out)
	}
	return code, e
}
func route(p *project, s string) string { return "/v1/projects/" + p.ID + "/relay/" + s }
func save()                             { must(os.WriteFile(filepath.Join(root, "fixture.json"), encoded(f), 0600)) }
func load() {
	b, e := os.ReadFile(filepath.Join(root, "fixture.json"))
	must(e)
	must(json.Unmarshal(b, &f))
	u, e := url.Parse(f.URL)
	must(e)
	if u.Scheme != "http" || u.Hostname() != "127.0.0.1" {
		panic("fixture must use isolated loopback")
	}
	var d struct {
		ID string `json:"deployment_id"`
	}
	_, e = api(context.Background(), "identity", "GET", "/v1/deployment?challenge=capacity_owned_fixture_1234", nil, nil, &d, 200)
	must(e)
	if d.ID != f.Deployment || d.ID == "" {
		panic("fixture identity mismatch")
	}
}
func state(p *project, revision int64) []byte {
	s := workgraph.New(p.ID)
	s.Revision = revision
	actor := contracts.RelayWriter(p.Account, "capacity_device")
	at := "2026-10-11T00:00:00Z"
	hash := digest([]byte("synthetic conversation reference"))
	for i := 0; i < p.Nodes; i++ {
		id := fmt.Sprintf("session_%06d", i)
		node := fmt.Sprintf("node_%06d", i)
		src := workgraph.Source{SessionID: id, Version: 1}
		s.Sessions[id] = workgraph.Session{ID: id, Owner: actor, Agent: "fixture", NativeID: id, Latest: 1, ProcessedThrough: 1, ConsumedTurns: 2, Tail: node, Revision: 1, Items: []workgraph.Item{}}
		s.Snapshots[src.Key()] = workgraph.Snapshot{Source: src, Owner: actor, Agent: "fixture", NativeID: id, Digest: hash, Conversation: hash, Turns: 2, Ended: true, ObservedAt: at}
		n := workgraph.Node{ID: node, Text: workgraph.Text{Objective: fmt.Sprintf("Synthetic work item %d", i), Summary: strings.Repeat("Implemented and verified a bounded change. ", 8), Approach: strings.Repeat("Keep independent work separate; reuse accepted results. ", 4)}, Source: src, State: "sealed", Revision: 1, StartTurn: 0, EndTurn: 2, Parents: []workgraph.Edge{}, Relations: "ready", RelationThrough: 2, CreatedAt: at, UpdatedAt: at, RunID: "capacity_run"}
		if i > 0 && i%5 != 0 {
			n.Parents = append(n.Parents, workgraph.Edge{Parent: fmt.Sprintf("node_%06d", i-1), Source: workgraph.Source{SessionID: fmt.Sprintf("session_%06d", i-1), Version: 1}, Reason: "Uses preceding synthetic result", Evidence: []string{"turn_1"}, RunID: "capacity_run"})
		}
		s.Nodes[node] = n
	}
	must(workgraph.ValidateState(s))
	return encoded(s)
}
func appendCommit(ctx context.Context, p *project, payloadBytes int, publish, quota bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	next := p.Head + 1
	id := fmt.Sprintf("capacity_%d_%d", time.Now().UnixNano(), seq.Add(1))
	payload := encoded(map[string]any{"fixture": "opaque transport benchmark", "sequence": next, "padding": strings.Repeat("x", payloadBytes)})
	var cp []byte
	if publish {
		cp = state(p, next)
	}
	c := contracts.RelayCommit{SchemaVersion: 1, ID: id, DeviceID: "capacity_device", Base: p.Head, Codec: "coconet-json-v2", Payload: payload, SHA256: digest(payload), Objects: []string{}}
	if publish {
		c.StateSHA256 = digest(cp)
	}
	accepted := []int{200}
	if quota {
		accepted = append(accepted, 507)
	}
	var receipt contracts.RelayReceipt
	b, _, code, e := request(ctx, "commit", "POST", route(p, "commits"), p, encoded(c), nil, accepted...)
	if e != nil {
		return e
	}
	if code == 507 {
		if !bytes.Contains(b, []byte("relay_log_quota")) {
			return fmt.Errorf("unexpected quota error")
		}
		return errQuota
	}
	if e = json.Unmarshal(b, &receipt); e != nil {
		return e
	}
	if receipt.Revision != next {
		return fmt.Errorf("unexpected commit revision")
	}
	p.Head = next
	if publish {
		checkpoint := contracts.RelayCheckpoint{SchemaVersion: 1, DeviceID: c.DeviceID, Revision: next, CommitID: id, Codec: "coconet-state-json-v1", SHA256: c.StateSHA256, Payload: cp}
		_, e = api(ctx, "checkpoint_write", "POST", route(p, "checkpoint"), p, checkpoint, nil, 200)
	}
	return e
}

var errQuota = fmt.Errorf("project log quota reached")

func setup(serverURL string) {
	f.URL = serverURL
	u, e := url.Parse(f.URL)
	must(e)
	if u.Scheme != "http" || u.Hostname() != "127.0.0.1" {
		panic("loopback required")
	}
	if _, e = os.Stat(filepath.Join(root, "fixture.json")); !os.IsNotExist(e) {
		panic("refuse existing fixture")
	}
	b, e := os.ReadFile(filepath.Join(root, "tokens.json"))
	must(e)
	var tokens []string
	must(json.Unmarshal(b, &tokens))
	if len(tokens) < 2 {
		panic("need fixture accounts")
	}
	ctx := context.Background()
	var d struct {
		ID string `json:"deployment_id"`
	}
	_, e = api(ctx, "identity", "GET", "/v1/deployment?challenge=capacity_owned_fixture_1234", nil, nil, &d, 200)
	must(e)
	f.Deployment = d.ID
	owners := []*project{}
	for _, token := range tokens {
		p := &project{Token: token}
		var a contracts.Account
		_, e = api(ctx, "account", "GET", "/v1/account", p, nil, &a, 200)
		must(e)
		p.Account = a.AccountID
		_, h, _, e := request(ctx, "dashboard_login", "POST", "/v1/dashboard/session", p, encoded(map[string]any{"schema_version": 1, "account_key": token}), nil, 200)
		must(e)
		p.Cookie = strings.Split(h.Get("Set-Cookie"), ";")[0]
		if p.Cookie == "" {
			panic("cookie absent")
		}
		owners = append(owners, p)
	}
	for i := len(f.Projects); i < 20; i++ {
		owner := owners[i%len(owners)]
		p := &project{Token: owner.Token, Account: owner.Account, Cookie: owner.Cookie, Nodes: 50}
		if i == 18 {
			p.Nodes = 3000
		}
		if i == 19 {
			p.Nodes = 0
		}
		var a contracts.ProjectAccess
		_, e = api(ctx, "project_create", "POST", "/v1/projects", p, contracts.CreateProjectRequest{SchemaVersion: 1, RequestID: fmt.Sprintf("capacity_project_%d", i), Name: fmt.Sprintf("Isolated capacity %d", i)}, &a, 201)
		must(e)
		p.ID = a.Project.ProjectID
		f.Projects = append(f.Projects, p)
		must(appendCommit(ctx, p, 2048, true, false))
		save()
	}
}
func summary(start time.Time) {
	stats.mu.Lock()
	defer stats.mu.Unlock()
	out := map[string]any{"mode": mode, "elapsed_seconds": time.Since(start).Seconds(), "unexpected_errors": stats.Failed, "errors": stats.Errors}
	m := map[string]any{}
	for name, v := range stats.By {
		sort.Float64s(v.MS)
		p := func(q float64) float64 {
			if len(v.MS) == 0 {
				return 0
			}
			return v.MS[int(float64(len(v.MS)-1)*q)]
		}
		m[name] = map[string]any{"count": v.Count, "status": v.Codes, "p50_ms": p(.5), "p95_ms": p(.95), "p99_ms": p(.99), "max_ms": p(1), "sent_bytes": v.Sent, "received_bytes": v.Received}
	}
	out["requests"] = m
	must(json.NewEncoder(os.Stdout).Encode(out))
}
func runLoad(duration time.Duration, rate, concurrency int) {
	ctx, cancel := context.WithTimeout(context.Background(), duration+20*time.Second)
	defer cancel()
	jobs := make(chan int)
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				p := f.Projects[j%18]
				var e error
				switch mode {
				case "idle":
					p.mu.Lock()
					head := p.Head
					p.mu.Unlock()
					_, e = api(ctx, "idle_sync", "GET", route(p, fmt.Sprintf("commits?after=%d&limit=100", head)), p, nil, nil, 200)
				case "mixed":
					switch j % 10 {
					case 0:
						e = appendCommit(ctx, p, 8192, true, false)
					case 1, 2:
						e = appendCommit(ctx, p, 8192, false, false)
					case 3:
						_, e = api(ctx, "dashboard_read", "GET", "/v1/dashboard/projects/"+p.ID+"/published-graph", p, nil, nil, 200)
					default:
						p.mu.Lock()
						head := p.Head
						p.mu.Unlock()
						_, e = api(ctx, "idle_sync", "GET", route(p, fmt.Sprintf("commits?after=%d&limit=100", head)), p, nil, nil, 200)
					}
				case "large-read":
					p = f.Projects[18]
					_, e = api(ctx, "large_checkpoint_read", "GET", route(p, "checkpoint"), p, nil, nil, 200)
				case "catchup":
					_, e = api(ctx, "catchup_page", "GET", route(p, "commits?after=0&limit=100"), p, nil, nil, 200)
				case "write":
					e = appendCommit(ctx, p, 8192, false, false)
				case "churn":
					e = appendCommit(ctx, f.Projects[18], 2048, true, false)
				default:
					panic("unknown load mode")
				}
				if e != nil && ctx.Err() == nil {
					cancel()
				}
			}
		}()
	}
	deadline := time.NewTimer(duration)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Second / time.Duration(rate))
	defer ticker.Stop()
loop:
	for j := 0; ; j++ {
		select {
		case <-deadline.C:
			break loop
		case <-ctx.Done():
			break loop
		case <-ticker.C:
			select {
			case jobs <- j:
			case <-ctx.Done():
				break loop
			}
		}
	}
	close(jobs)
	wg.Wait()
	save()
}
func conflicts() {
	ctx := context.Background()
	p := f.Projects[0]
	before := p.Head
	payload := []byte(`{"fixture":"race"}`)
	var wg sync.WaitGroup
	var wins atomic.Int64
	var winning contracts.RelayCommit
	var mu sync.Mutex
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := contracts.RelayCommit{SchemaVersion: 1, ID: fmt.Sprintf("race_%d_%d", time.Now().UnixNano(), i), DeviceID: "capacity_device", Base: before, Codec: "coconet-json-v2", Payload: payload, SHA256: digest(payload), Objects: []string{}}
			_, _, code, e := request(ctx, "conflicting_commit", "POST", route(p, "commits"), p, encoded(c), nil, 200, 409)
			must(e)
			if code == 200 {
				wins.Add(1)
				mu.Lock()
				winning = c
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if wins.Load() != 1 {
		panic("CAS lost single winner")
	}
	p.Head++
	var r contracts.RelayReceipt
	_, e := api(ctx, "duplicate_commit", "POST", route(p, "commits"), p, winning, &r, 200)
	must(e)
	if r.Revision != p.Head {
		panic("duplicate advanced head")
	}
	save()
}
func objects() {
	ctx := context.Background()
	p := f.Projects[0]
	for i := 0; i < 32; i++ {
		data := bytes.Repeat([]byte{byte(i)}, 2<<20)
		r := contracts.RelayTransferRequest{SchemaVersion: 1, SHA256: digest(data), Size: int64(len(data))}
		var t contracts.RelayTransferTarget
		_, e := api(ctx, "upload_reserve", "POST", route(p, "transfers/upload"), p, r, &t, 200)
		must(e)
		if t.Ready {
			panic("fixture object existed")
		}
		_, _, _, e = request(ctx, "object_direct_put", "PUT", t.URL, nil, data, nil, 200)
		must(e)
		r.UploadID = t.UploadID
		_, e = api(ctx, "upload_complete", "POST", route(p, "transfers/complete"), p, r, nil, 200)
		must(e)
		r.UploadID = ""
		_, e = api(ctx, "download_authorize", "POST", route(p, "transfers/download"), p, r, &t, 200)
		must(e)
		b, _, _, e := request(ctx, "object_direct_get", "GET", t.URL, nil, nil, nil, 200)
		must(e)
		if digest(b) != r.SHA256 {
			panic("content mismatch")
		}
	}
}
func verify() {
	for _, p := range f.Projects {
		var page contracts.RelayCheckpointHead
		_, e := api(context.Background(), "verify_head", "GET", route(p, "checkpoint-head"), p, nil, &page, 200)
		must(e)
		if page.Head != p.Head {
			panic("durable head differs")
		}
		var cp contracts.RelayCheckpointPage
		_, e = api(context.Background(), "verify_checkpoint", "GET", route(p, "checkpoint"), p, nil, &cp, 200)
		must(e)
		if cp.Checkpoint == nil || digest(cp.Checkpoint.Checkpoint.Payload) != cp.Checkpoint.Checkpoint.SHA256 {
			panic("invalid checkpoint checksum")
		}
		var s workgraph.State
		must(json.Unmarshal(cp.Checkpoint.Checkpoint.Payload, &s))
		must(workgraph.ValidateState(s))
	}
}
func events(duration time.Duration, concurrency int) {
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()
	p := f.Projects[0]
	var wg sync.WaitGroup
	var opened, frames atomic.Int64
	streamClient := &http.Client{Transport: client.Transport}
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequestWithContext(ctx, "GET", f.URL+"/v1/dashboard/projects/"+p.ID+"/events", nil)
			req.Header.Set("Cookie", p.Cookie)
			req.Header.Set("X-Forwarded-Proto", "https")
			start := time.Now()
			r, e := streamClient.Do(req)
			code := 0
			if r != nil {
				code = r.StatusCode
			}
			if e == nil && code != 200 {
				e = fmt.Errorf("event status %d", code)
			}
			record("event_open", code, float64(time.Since(start).Microseconds())/1000, 0, 0, e)
			if e != nil {
				return
			}
			defer r.Body.Close()
			opened.Add(1)
			scan := bufio.NewScanner(r.Body)
			for scan.Scan() {
				if strings.HasPrefix(scan.Text(), "data:") {
					frames.Add(1)
				}
			}
		}()
	}
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			fmt.Fprintf(os.Stdout, "{\"event_connections\":%d,\"data_frames\":%d}\n", opened.Load(), frames.Load())
			if opened.Load() != int64(concurrency) || frames.Load() < int64(concurrency) {
				panic("SSE fixture incomplete")
			}
			save()
			return
		case <-tick.C:
			must(appendCommit(context.Background(), p, 2048, true, false))
		}
	}
}
func audit(path string) {
	db, e := bolt.Open(path, 0600, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	must(e)
	defer db.Close()
	out := map[string]any{}
	must(db.View(func(tx *bolt.Tx) error {
		for e := range tx.Check() {
			must(e)
		}
		return tx.ForEach(func(k []byte, b *bolt.Bucket) error {
			var size int64
			must(b.ForEach(func(k, v []byte) error { size += int64(len(k) + len(v)); return nil }))
			out[string(k)] = map[string]any{"keys": b.Stats().KeyN, "logical_bytes": size}
			return nil
		})
	}))
	info, e := os.Stat(path)
	must(e)
	must(json.NewEncoder(os.Stdout).Encode(map[string]any{"file_bytes": info.Size(), "buckets": out, "structural_check": "ok"}))
}
func main() {
	var duration time.Duration
	var rate, concurrency int
	var serverURL, path string
	flag.StringVar(&root, "root", "", "owned fixture directory")
	flag.StringVar(&mode, "mode", "", "bucket/setup/idle/mixed/write/churn/large-read/catchup/conflicts/objects/quota/verify/audit")
	flag.StringVar(&serverURL, "url", "http://127.0.0.1:18971", "isolated server")
	flag.StringVar(&path, "db", "", "offline audit database")
	flag.DurationVar(&duration, "duration", 60*time.Second, "bounded duration")
	flag.IntVar(&rate, "rate", 10, "operations per second ceiling")
	flag.IntVar(&concurrency, "concurrency", 10, "maximum inflight operations")
	flag.Parse()
	if duration <= 0 || duration > 10*time.Minute || rate < 1 || rate > 200 || concurrency < 1 || concurrency > 200 || root == "" {
		panic("invalid bounded fixture configuration")
	}
	if _, e := os.Stat(filepath.Join(root, ".capacity-fixture")); e != nil {
		panic("owned fixture marker required")
	}
	if mode == "audit" {
		audit(path)
		return
	}
	if mode == "bucket" {
		c, e := minio.New("127.0.0.1:18972", &minio.Options{Creds: credentials.NewStaticV4(os.Getenv("MINIO_ROOT_USER"), os.Getenv("MINIO_ROOT_PASSWORD"), ""), Secure: false})
		must(e)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		must(c.MakeBucket(ctx, "capacity-fixture", minio.MakeBucketOptions{}))
		return
	}
	start := time.Now()
	defer summary(start)
	if mode == "setup" {
		setup(serverURL)
		return
	}
	load()
	switch mode {
	case "verify":
		verify()
	case "events":
		events(duration, concurrency)
	case "conflicts":
		conflicts()
	case "objects":
		objects()
	case "quota":
		for i := 0; i < 300; i++ {
			e := appendCommit(context.Background(), f.Projects[19], (1<<20)-1024, false, true)
			if e == errQuota {
				save()
				return
			}
			must(e)
		}
		panic("log quota did not stop growth")
	default:
		runLoad(duration, rate, concurrency)
	}
	if stats.Failed > 0 {
		panic("unexpected HTTP errors; see summary")
	}
}
