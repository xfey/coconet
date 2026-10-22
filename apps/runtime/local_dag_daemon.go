package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xfey/coconet/packages/distribution"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/inference"
	"github.com/xfey/coconet/packages/localdag"
	"github.com/xfey/coconet/packages/workgraph"
)

type localDAGEndpoint struct {
	UpdateProtocol int    `json:"update_protocol,omitempty"`
	Version        string `json:"version,omitempty"`
	URL            string `json:"url"`
	Token          string `json:"token"`
}
type limitedInference struct {
	Runner inference.Runner
	Store  *graphstore.Store
	Limit  int
}

func (r *limitedInference) Infer(ctx context.Context, input inference.Request) (inference.Response, error) {
	budget, reserved, err := r.Store.ReserveInference(time.Now(), r.Limit)
	if err != nil {
		return inference.Response{}, err
	}
	if !reserved {
		next, err := time.Parse(time.RFC3339Nano, budget.ResumesAt)
		if err != nil {
			return inference.Response{}, err
		}
		return inference.Response{}, &inference.Deferred{Until: next}
	}
	if err := reserveHistoryCall(ctx); err != nil {
		return inference.Response{}, err
	}
	return r.Runner.Infer(ctx, input)
}
func (r *limitedInference) budget() (graphstore.InferenceBudget, error) {
	return r.Store.InferenceBudget(time.Now(), r.Limit)
}
func (r *limitedInference) Configuration() any {
	if described, ok := r.Runner.(interface{ Configuration() any }); ok {
		return described.Configuration()
	}
	return nil
}

// serveLocalDAG owns the open store for its lifetime. CLI readers use this
// authenticated loopback API instead of racing the daemon's bbolt file lock.
// The explicit budget bounds attempts over a persisted rolling 24-hour window.
func serveLocalDAG(ctx context.Context, root string, engine *localdag.Engine, budget int, stdout io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	projects, err := localDAGActiveProjects(root, engine.Store)
	if err != nil {
		return err
	}
	for _, project := range projects {
		if err := engine.RecoverLocal(ctx, project); err != nil {
			return err
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return err
	}
	endpoint := localDAGEndpoint{UpdateProtocol: 1, Version: version, URL: "http://" + listener.Addr().String(), Token: hex.EncodeToString(secret[:])}
	control := filepath.Join(root, "control.json")
	if err := atomicWriteJSON(control, endpoint, 0600); err != nil {
		return err
	}
	defer os.Remove(control)
	releaseRuntimeStartup(ctx)
	processingEnabled := engine.Runner != nil || engine.ResolveRunner != nil
	runner := &limitedInference{Runner: engine.Runner, Store: engine.Store, Limit: budget}
	if _, err := runner.budget(); err != nil {
		return err
	}
	if resolver := engine.ResolveRunner; resolver != nil {
		engine.ResolveRunner = func(project string, session workgraph.Session) (inference.Runner, error) {
			selected, err := resolver(project, session)
			if err != nil {
				return nil, err
			}
			return &limitedInference{Runner: selected, Store: engine.Store, Limit: budget}, nil
		}
	} else {
		engine.Runner = runner
	}
	engine.TaskContext = localHistoryTaskContext(root, engine)
	var statusMu sync.RWMutex
	currentErrors := map[string]string{}
	discoverySync := newLocalDAGDiscoverySync()
	report := func(scope string, err error) {
		statusMu.Lock()
		defer statusMu.Unlock()
		if err != nil {
			currentErrors[scope] = err.Error()
		} else {
			delete(currentErrors, scope)
		}
	}
	if config, managed, err := loadConnectedLocalDAG(root); managed {
		if err != nil {
			report("configuration", err)
		} else if config.PauseReason != "" {
			report("configuration", errors.New(config.PauseReason))
		}
	}
	wake := make(chan struct{}, 1)
	notify := func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	activity := func(work func()) bool {
		release, err := beginRuntimeActivity(ctx, root)
		if err != nil {
			return false
		}
		defer release()
		work()
		return true
	}
	workerDone := make(chan struct{})
	syncDone := make(chan struct{})
	inboxDone := make(chan struct{})
	maintenanceDone := make(chan struct{})
	go func() {
		defer close(maintenanceDone)
		timer := time.NewTicker(time.Minute)
		defer timer.Stop()
		for {
			if !activity(func() {
				report("diagnostic-retention", engine.Store.PruneDiagnostics(time.Now()))
				report("transfer-retention", engine.Store.PruneTransfers(time.Now()))
				_, cacheErr := maintainLocalDAGCache(ctx, root, engine)
				report("local-content-retention", cacheErr)
				projects, err := localDAGActiveProjects(root, engine.Store)
				if err != nil {
					report("retention", err)
					return
				}
				for _, project := range projects {
					if ctx.Err() != nil {
						return
					}
					attempt, stop := context.WithTimeout(ctx, 45*time.Second)
					err := maintainLocalDAG(attempt, engine, project)
					stop()
					report("retention/"+project, err)
				}
			}) {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
		}
	}()
	contentDone := make(chan struct{})
	go func() {
		defer close(contentDone)
		remote, canSync := engine.Publisher.(interface {
			SyncContents(context.Context, string) error
		})
		timer := time.NewTicker(time.Second)
		defer timer.Stop()
		polls := map[string]*projectPoll{}
		for {
			if !activity(func() {
				projects, err := localDAGActiveProjects(root, engine.Store)
				if err != nil {
					report("content", err)
					return
				}
				for _, project := range projects {
					before, err := engine.Store.AcceptedRevision(project)
					if err != nil {
						report("content/"+project, err)
						continue
					}
					poll := polls[project]
					if poll == nil {
						poll = &projectPoll{}
						polls[project] = poll
					}
					if !poll.due(time.Now(), before) {
						continue
					}
					attempt, stop := context.WithTimeout(ctx, 45*time.Second)
					if canSync {
						if background, ok := engine.Publisher.(interface {
							SyncContentsBackground(context.Context, string) error
						}); ok {
							err = background.SyncContentsBackground(attempt, project)
						} else {
							err = remote.SyncContents(attempt, project)
						}
					}
					stop()
					report("content/"+project, err)
					index, indexErr := engine.Store.IndexProject(ctx, project, 16)
					report("indexing/"+project, indexErr)
					coverage, coverageErr := engine.Store.ReplicaCoverage(project, engine.Actor)
					report("replica-state/"+project, coverageErr)
					pending := canSync && (coverage.NativeReady < coverage.Expected || coverage.TextReady < coverage.Expected || coverage.PendingUploads > 0) || !index.Complete
					poll.complete(time.Now(), before, coverage.Revision, pending, err)
				}
			}) {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
		}
	}()
	go func() {
		defer close(inboxDone)
		timer := time.NewTicker(time.Second)
		defer timer.Stop()
		for {
			if !activity(func() {
				report("history", tickLocalHistory(ctx, root, engine))
				report("capture", drainLocalDAGInbox(ctx, root, engine))
			}) {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
		}
	}()
	go func() {
		defer close(syncDone)
		if engine.Publisher == nil {
			return
		}
		timer := time.NewTicker(time.Second)
		defer timer.Stop()
		polls := map[string]*projectPoll{}
		for {
			if !activity(func() {
				projects, err := localDAGActiveProjects(root, engine.Store)
				if err != nil {
					report("replication", err)
					return
				}
				for _, project := range projects {
					if ctx.Err() != nil {
						return
					}
					before, readErr := engine.Store.AcceptedRevision(project)
					if readErr != nil {
						report("replication/"+project, readErr)
						continue
					}
					poll := polls[project]
					if poll == nil {
						poll = &projectPoll{}
						polls[project] = poll
					}
					if !poll.due(time.Now(), before) {
						continue
					}
					err := syncLocalDAGBackground(ctx, engine, project)
					report("replication/"+project, err)
					discoverySync.report(project, err)
					if remote, ok := engine.Publisher.(projectDocumentReader); ok {
						attempt, stop := context.WithTimeout(ctx, 15*time.Second)
						report("project-document/"+project, syncProjectDocument(attempt, root, project, remote))
						stop()
					}
					after, readErr := engine.Store.AcceptedRevision(project)
					if readErr != nil {
						report("replication/"+project, readErr)
						continue
					}
					published, _ := engine.Store.PublishedCheckpoint(project)
					poll.complete(time.Now(), before, after, published < after, err)
					if err == nil && after != before {
						notify()
					}
				}
			}) {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
		}
	}()
	go func() {
		defer close(workerDone)
		timer := time.NewTicker(time.Second)
		defer timer.Stop()
		for {
			if !activity(func() {
				projects, err := localDAGActiveProjects(root, engine.Store)
				if err != nil {
					report("processing", err)
					return
				}
				for _, project := range projects {
					if ctx.Err() != nil {
						return
					}
					report("publication/"+project, engine.Flush(ctx, project))
				}
				// Deterministic lifecycle-only work needs no model quota. The runner
				// reserves actual calls atomically; exhausted tasks wait with NextAt.
				for _, project := range projects {
					if ctx.Err() != nil {
						return
					}
					if processingEnabled {
						report("processing/"+project, engine.Drain(ctx, project, 1))
					}
				}
			}) {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-wake:
			case <-timer.C:
			}
		}
	}()
	defer func() {
		cancel()
		<-workerDone
		<-syncDone
		<-inboxDone
		<-maintenanceDone
		<-contentDone
	}()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+endpoint.Token)) != 1 {
			http.Error(w, "local authorization required", 401)
			return
		}
		// Browser pages cannot call this service with ambient credentials.
		if r.Header.Get("Origin") != "" {
			http.Error(w, "browser origin is not enabled for this endpoint", 403)
			return
		}
		if r.URL.Path != "/status" && r.URL.Path != "/stop" {
			release, err := beginRuntimeActivity(r.Context(), root)
			if err != nil {
				http.Error(w, "Runtime update is waiting; retry this operation.", 503)
				return
			}
			defer release()
		}
		project := r.URL.Query().Get("project")
		if project != "" {
			projects, err := localDAGActiveProjects(root, engine.Store)
			if err != nil || localDAGIsConnectedRoot(root) && !slices.Contains(projects, project) {
				http.Error(w, "project is not available in this Runtime", 403)
				return
			}
		}
		var value any
		var failure error
		switch {
		case r.Method == "GET" && r.URL.Path == "/document":
			value, failure = readProjectDocument(root, project)
		case r.Method == "GET" && r.URL.Path == "/status":
			statusMu.RLock()
			activeErrors := map[string]string{}
			messages := []string{}
			for scope, message := range currentErrors {
				activeErrors[scope] = message
				messages = append(messages, scope+": "+message)
			}
			statusMu.RUnlock()
			sort.Strings(messages)
			remaining, err := runner.budget()
			failure = err
			capture, captureErr := localDAGInboxHealth(root)
			if captureErr != nil {
				failure = errors.Join(failure, captureErr)
			}
			mode := "offline"
			if engine.Publisher != nil {
				mode = "relay"
			}
			value = map[string]any{"status": "running", "runtime_version": version, "sync_mode": mode, "attempts": remaining.Used, "attempt_limit": remaining.Limit, "paused": !processingEnabled || remaining.Limit > 0 && remaining.Used >= remaining.Limit, "inference_configured": processingEnabled, "resumes_at": remaining.ResumesAt, "budget_window_hours": 24, "last_error": strings.Join(messages, "; "), "errors": activeErrors, "capture": capture}
			if budget == 0 {
				status := value.(map[string]any)
				delete(status, "attempt_limit")
				delete(status, "resumes_at")
				delete(status, "budget_window_hours")
				status["inference_mode"] = "automatic"
			}
			if project != "" {
				operations, err := localOperations(r.Context(), root, engine, project)
				failure = errors.Join(failure, err)
				value.(map[string]any)["project"] = operations
			}
		case r.Method == "POST" && r.URL.Path == "/stop":
			caller := r.Header.Get("X-Coconet-Version")
			order, known := distribution.CompareVersions(caller, version)
			if caller != version && (!known || order < 0) {
				http.Error(w, "A newer Runtime is running. Reopen the Agent session before managing it.", http.StatusConflict)
				return
			}
			value = map[string]bool{"stopping": true}
			defer cancel()
		case r.Method == "GET" && r.URL.Path == "/inbox":
			value, failure = localDAGInbox(root)
		case r.Method == "POST" && r.URL.Path == "/retry-inbox":
			failure = retryLocalDAGInboxProject(root, r.URL.Query().Get("task"), project)
			value = map[string]bool{"queued": failure == nil}
		case strings.HasPrefix(r.URL.Path, "/history-"):
			if r.Method != "POST" || project == "" {
				http.Error(w, "history requires POST and a project", 400)
				return
			}
			var request localHistoryRequest
			data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
			failure = err
			if failure == nil {
				failure = decodeStrict(data, &request)
			}
			if failure == nil {
				value, failure = traceLocalHistoryAction(r.Context(), root, engine, project, strings.TrimPrefix(r.URL.Path, "/"), request)
				notify()
			}
		case strings.HasPrefix(r.URL.Path, "/storage-"):
			action := strings.TrimPrefix(r.URL.Path, "/")
			if action == "storage-status" && r.Method != "GET" || action != "storage-status" && r.Method != "POST" {
				http.Error(w, "invalid storage method", 405)
				return
			}
			options := localDAGClientOptions{Project: project, Input: r.URL.Query().Get("file")}
			if action == "storage-configure" {
				options.ObjectLimit, failure = strconv.ParseInt(r.URL.Query().Get("bytes"), 10, 64)
				if failure == nil {
					options.ExportLimit, failure = strconv.ParseInt(r.URL.Query().Get("exports_bytes"), 10, 64)
				}
			}
			if failure == nil {
				value, failure = localStorage(r.Context(), root, engine, action, options)
			}
		case r.URL.Path == "/usage":
			if r.Method != "GET" {
				http.Error(w, "usage requires GET", 405)
				return
			}
			var since time.Time
			if raw := r.URL.Query().Get("query"); raw != "" {
				since, failure = time.Parse(time.RFC3339Nano, raw)
			}
			if failure == nil {
				value, failure = engine.Store.ModelUsage(since)
			}
		case strings.HasPrefix(r.URL.Path, "/diagnostics-"):
			action := strings.TrimPrefix(r.URL.Path, "/")
			read := action == "diagnostics-status" || action == "diagnostics-list" || action == "diagnostics-export"
			if read && r.Method != "GET" || !read && r.Method != "POST" {
				http.Error(w, "invalid diagnostic method", 405)
				return
			}
			options := localDAGClientOptions{Project: project, Task: r.URL.Query().Get("task"), IncludeContent: r.URL.Query().Get("include_content") == "true", Query: r.URL.Query().Get("query"), Limit: 50}
			if action == "diagnostics-list" && r.URL.Query().Has("limit") {
				options.Limit, failure = strconv.Atoi(r.URL.Query().Get("limit"))
			}
			if action == "diagnostics-configure" {
				var policy graphstore.DiagnosticPolicy
				data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
				failure = err
				if failure == nil {
					failure = decodeStrict(data, &policy)
				}
				options.DiagnosticPolicy = &policy
			}
			if failure == nil {
				value, failure = localDiagnostics(engine.Store, action, options)
			}
		case project == "":
			http.Error(w, "project is required", 400)
			return
		case r.Method == "GET" && r.URL.Path == "/state":
			value, failure = engine.Store.State(project)
		case r.Method == "GET" && (r.URL.Path == "/graph" || r.URL.Path == "/library"):
			limit := 0
			if raw := r.URL.Query().Get("limit"); raw != "" {
				limit, failure = strconv.Atoi(raw)
			}
			if failure == nil {
				failure = discoverySync.wait(r.Context(), engine, project)
			}
			if failure == nil {
				value, failure = engine.GraphPage(project, localdag.GraphQuery{Limit: limit, Cursor: r.URL.Query().Get("cursor")}, r.URL.Path == "/library")
			}
		case r.Method == "GET" && r.URL.Path == "/search":
			limit := 0
			if raw := r.URL.Query().Get("limit"); raw != "" {
				limit, failure = strconv.Atoi(raw)
			}
			if failure == nil {
				failure = discoverySync.wait(r.Context(), engine, project)
			}
			if failure == nil {
				value, failure = engine.Search(project, localdag.WorkSearch{Query: r.URL.Query().Get("query"), Limit: limit, Cursor: r.URL.Query().Get("cursor")})
			}
		case r.Method == "POST" && (r.URL.Path == "/collect" || r.URL.Path == "/uncollect"):
			var request localdag.LibraryRequest
			data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
			failure = err
			if failure == nil {
				failure = decodeStrict(data, &request)
			}
			if failure == nil {
				if r.URL.Path == "/uncollect" {
					value, failure = engine.Uncollect(r.Context(), project, request)
				} else {
					value, failure = engine.Collect(r.Context(), project, request)
				}
				notify()
			}
		case r.Method == "POST" && r.URL.Path == "/fork":
			var request localNativeForkRequest
			data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
			failure = err
			if failure == nil {
				failure = decodeStrict(data, &request)
			}
			if failure == nil && request.ProjectID != project {
				failure = errors.New("native Fork project differs")
			}
			if failure == nil {
				attempt, stop := context.WithTimeout(r.Context(), localNativeForkTimeout)
				value, failure = forkLocalDAG(attempt, root, engine, request, nil)
				stop()
				notify()
			}
		case r.Method == "POST" && r.URL.Path == "/pull":
			if r.URL.Query().Has("session") || r.URL.Query().Has("version") {
				version, err := strconv.Atoi(r.URL.Query().Get("version"))
				if err != nil || version < 1 || r.URL.Query().Get("session") == "" {
					failure = errors.New("pull requires a complete fixed source")
				} else {
					value, failure = engine.ReadSelectedNode(r.Context(), project, r.URL.Query().Get("node"), workgraph.Source{SessionID: r.URL.Query().Get("session"), Version: version}, r.URL.Query().Get("native") == "true")
				}
			} else {
				value, failure = engine.ReadNode(r.Context(), project, r.URL.Query().Get("node"), r.URL.Query().Get("native") == "true")
			}
		case r.Method == "POST" && (r.URL.Path == "/index" || r.URL.Path == "/index-rebuild"):
			if r.URL.Path == "/index-rebuild" {
				failure = engine.Store.ResetSearchIndex(r.Context(), project)
			}
			if failure == nil {
				value, failure = engine.Store.IndexProject(r.Context(), project, 64)
			}
		case r.Method == "GET" && r.URL.Path == "/logs":
			value, failure = engine.Store.Runs(project)
		case r.Method == "GET" && r.URL.Path == "/tasks":
			value, failure = engine.Store.Tasks(project)
		case r.Method == "GET" && r.URL.Path == "/proposals":
			value, failure = engine.Store.Proposals(project)
		case r.Method == "GET" && r.URL.Path == "/transfers":
			value, failure = engine.Store.Transfers(project, 100)
		case r.Method == "POST" && r.URL.Path == "/sync":
			failure = syncLocalDAG(r.Context(), engine, project)
			if failure == nil {
				failure = engine.Flush(r.Context(), project)
			}
			value, _ = engine.Store.State(project)
		case r.Method == "GET" && r.URL.Path == "/replay":
			value, failure = engine.Store.Replay(project)
		case r.Method == "POST" && r.URL.Path == "/capture":
			var capture localdag.Capture
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<20))
			decoder.DisallowUnknownFields()
			failure = decoder.Decode(&capture)
			if failure == nil && decoder.Decode(&struct{}{}) != io.EOF {
				failure = errors.New("expected one capture value")
			}
			if failure == nil && capture.ProjectID != project {
				failure = errors.New("capture project differs")
			}
			if failure == nil {
				value, failure = captureLocalDAG(r.Context(), root, engine, capture)
				if failure == nil {
					notify()
				}
			}
		case r.Method == "POST" && r.URL.Path == "/observe":
			var request localDAGObservationRequest
			data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
			failure = err
			if failure == nil {
				failure = decodeStrict(data, &request)
			}
			if failure == nil && request.ProjectID != project {
				failure = errors.New("observation project differs")
			}
			if failure == nil {
				value, failure = observeLocalDAG(r.Context(), root, engine, request)
				notify()
			}

		case r.Method == "POST" && r.URL.Path == "/retry":
			value, failure = engine.Retry(project, r.URL.Query().Get("task"))
			if failure == nil {
				notify()
			}
		default:
			http.Error(w, "unknown local endpoint", 404)
			return
		}
		if failure != nil {
			http.Error(w, localDAGFailureMessage(failure), 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(value)
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	if err := json.NewEncoder(stdout).Encode(map[string]any{"status": "running", "url": endpoint.URL, "attempt_limit": budget}); err != nil {
		server.Close()
		return err
	}
	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		shutdown, cancelShutdown := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancelShutdown()
		if err := server.Shutdown(shutdown); err != nil {
			server.Close()
		}
		<-done
		return nil
	}
}

// Returns handled=false only if no live daemon owns this root. A rejected
// authenticated request must not fall back to bypassing the running service.
type localDAGClientOptions struct {
	History          *localHistoryRequest
	ObjectLimit      int64
	ExportLimit      int64
	Cursor           string
	Library          *localdag.LibraryRequest
	Fork             *localNativeForkRequest
	DiagnosticPolicy *graphstore.DiagnosticPolicy
	IncludeContent   bool
	Project          string
	Input            string
	Task             string
	Node             string
	Native           bool
	Source           *workgraph.Source
	Query            string
	Limit            int
}

func localDAGClient(ctx context.Context, root, action string, options localDAGClientOptions, stdout, stderr io.Writer) (handled bool, code int) {
	data, err := readLocalPrivateFile(filepath.Join(root, "control.json"), 4096, true)
	if errors.Is(err, os.ErrNotExist) {
		return false, 0
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return true, 1
	}
	var endpoint localDAGEndpoint
	if decodeStrict(data, &endpoint) != nil {
		fmt.Fprintln(stderr, "invalid local daemon endpoint")
		return true, 1
	}
	parsed, err := url.Parse(endpoint.URL)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.User != nil || parsed.RawQuery != "" || parsed.Path != "" || parsed.Fragment != "" || len(endpoint.Token) != 64 {
		fmt.Fprintln(stderr, "invalid local daemon endpoint")
		return true, 1
	}
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if action == "storage-export" || action == "storage-restore" {
		client.Timeout = 15 * time.Minute
	}
	if action == "fork" || strings.HasPrefix(action, "history-") {
		client.Timeout = localNativeForkTimeout
	}
	probe, err := http.NewRequestWithContext(ctx, "GET", endpoint.URL+"/status", nil)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return true, 1
	}
	probe.Header.Set("Authorization", "Bearer "+endpoint.Token)
	probe.Header.Set("X-Coconet-Version", version)
	response, err := client.Do(probe)
	if err != nil {
		var network *net.OpError
		if errors.As(err, &network) && network.Op == "dial" {
			return false, 0
		}
		fmt.Fprintln(stderr, err)
		return true, 1
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			fmt.Fprintln(stderr, "local daemon authorization failed; run coconet status --verbose to inspect the local connection")
		} else {
			fmt.Fprintf(stderr, "local daemon health check returned HTTP %d; run coconet status --verbose and coconet diagnostics list\n", response.StatusCode)
		}
		return true, 1
	}
	method := "GET"
	var body io.Reader
	switch action {
	case "document", "state", "graph", "search", "library", "logs", "tasks", "replay", "status", "proposals", "transfers", "inbox", "diagnostics-status", "diagnostics-list", "diagnostics-export", "storage-status", "usage":
	case "history-preview", "history-list", "history-status", "history-start", "history-pause", "history-resume", "history-retry", "history-forget", "collect", "uncollect", "fork":
		method = "POST"
		var payload any = options.Library
		if action == "fork" {
			payload = options.Fork
		}
		if strings.HasPrefix(action, "history-") {
			payload = options.History
		}
		data, err := json.Marshal(payload)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return true, 1
		}
		body = strings.NewReader(string(data))
	case "index", "index-rebuild", "retry", "retry-inbox", "stop", "sync", "pull", "diagnostics-pin", "diagnostics-unpin", "diagnostics-prune", "storage-configure", "storage-prune", "storage-reindex", "storage-export", "storage-restore":
		method = "POST"
	case "diagnostics-configure":
		method = "POST"
		data, err := json.Marshal(options.DiagnosticPolicy)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return true, 1
		}
		body = strings.NewReader(string(data))
	case "capture", "observe":
		method = "POST"
		file, err := os.Open(options.Input)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return true, 1
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || info.Size() > 64<<20 {
			fmt.Fprintln(stderr, "capture exceeds size limit")
			return true, 1
		}
		body = file
	default:
		fmt.Fprintln(stderr, "local daemon owns this store; use its tasks or stop it before offline processing")
		return true, 1
	}
	query := url.Values{"project": []string{options.Project}, "task": []string{options.Task}, "node": []string{options.Node}}
	if action == "storage-export" || action == "storage-restore" {
		query.Set("file", options.Input)
	}
	if action == "storage-configure" {
		query.Set("bytes", strconv.FormatInt(options.ObjectLimit, 10))
		query.Set("exports_bytes", strconv.FormatInt(options.ExportLimit, 10))
	}
	if options.Native {
		query.Set("native", "true")
	}
	if options.Source != nil {
		query.Set("session", options.Source.SessionID)
		query.Set("version", strconv.Itoa(options.Source.Version))
	}
	if action == "search" || action == "diagnostics-list" || action == "graph" || action == "library" || action == "usage" {
		query.Set("query", options.Query)
		query.Set("limit", strconv.Itoa(options.Limit))
		query.Set("cursor", options.Cursor)
	}
	if options.IncludeContent {
		query.Set("include_content", "true")
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint.URL+"/"+action+"?"+query.Encode(), body)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return true, 1
	}
	request.Header.Set("Authorization", "Bearer "+endpoint.Token)
	request.Header.Set("X-Coconet-Version", version)
	response, err = client.Do(request)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return true, 1
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		fmt.Fprintln(stderr, strings.TrimSpace(string(message)))
		return true, 1
	}
	_, err = io.Copy(stdout, response.Body)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return true, 1
	}
	return true, 0
}
