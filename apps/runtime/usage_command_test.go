package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/localdag"
)

func usageCommandFixture(t *testing.T, root, id, project string, at time.Time) {
	t.Helper()
	s, err := graphstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Begin(graphstore.Run{ID: id, TaskID: id, ProjectID: project, Kind: "stage", Provider: "codex", Model: "fixture-model", Status: "running", StartedAt: at.Format(time.RFC3339Nano), Input: json.RawMessage(`{"prompt":"private fixture input"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveModelResponse(id, `{"visible":"private fixture output"}`, json.RawMessage(`{"total":{"inputTokens":100,"cachedInputTokens":60,"outputTokens":12}}`), "fixture-model"); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(id, "completed", "", at.Add(time.Second).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
}

func invokeUsage(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, stderr bytes.Buffer
	code := runUsage(context.Background(), args, &out, &stderr)
	if strings.Contains(out.String(), "private fixture") {
		t.Fatal("usage exposed conversation text")
	}
	return code, out.String(), stderr.String()
}

func TestUsageCommandReadsAllScopesWithoutConnectionOrMutation(t *testing.T) {
	home := t.TempDir()
	setRuntimeTestHome(t, home)
	paths, err := resolveRuntimePaths()
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(paths.root, "local-dag")
	one, two := filepath.Join(base, "scopes", "one"), filepath.Join(base, "scopes", "two")
	now := time.Now().UTC()
	usageCommandFixture(t, base, "old", "coco_legacy", now.Add(-40*24*time.Hour))
	usageCommandFixture(t, base, "middle", "coco_middle", now.Add(-20*24*time.Hour))
	usageCommandFixture(t, one, "same-run", "coco_shared", now)
	usageCommandFixture(t, two, "same-run", "coco_shared", now)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(one, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COCONET_LOCAL_DAG_HOME", alias)
	before := map[string][]byte{}
	for _, root := range []string{base, one, two} {
		data, err := os.ReadFile(filepath.Join(root, "graph.db"))
		if err != nil {
			t.Fatal(err)
		}
		before[root] = data
	}
	report, err := collectUsage(context.Background(), now)
	if err != nil || len(report.Warnings) != 0 || len(report.Periods) != 3 {
		t.Fatal(report, err)
	}
	for i, want := range []int{2, 3, 4} {
		period := report.Periods[i]
		if period.Totals.Attempts != want || period.Totals.Input.Tokens != int64(want*100) || len(period.Models) != 1 {
			t.Fatal(period)
		}
	}
	code, out, stderr := invokeUsage(t)
	if code != 0 || stderr != "" {
		t.Fatal(code, out, stderr)
	}
	for _, want := range []string{"Last 7 days", "Last 30 days", "All time", "codex / fixture-model", "Cache read", "Cache write"} {
		if !strings.Contains(out, want) {
			t.Fatal("missing", want, out)
		}
	}
	for root, data := range before {
		after, err := os.ReadFile(filepath.Join(root, "graph.db"))
		if err != nil || !bytes.Equal(after, data) {
			t.Fatal("report mutated database", root, err)
		}
		if _, err := os.Stat(filepath.Join(root, "control.json")); !os.IsNotExist(err) {
			t.Fatal("report started a worker", err)
		}
	}
	if _, err := os.Stat(paths.credentialPath); !os.IsNotExist(err) {
		t.Fatal("report signed in", err)
	}
	// A store outside the normal tree counts too, without duplicating aliases.
	external := filepath.Join(t.TempDir(), "external")
	usageCommandFixture(t, external, "external", "coco_external", now)
	t.Setenv("COCONET_LOCAL_DAG_HOME", external)
	report, err = collectUsage(context.Background(), now)
	if err != nil || report.Periods[0].Totals.Attempts != 3 || report.Periods[2].Totals.Attempts != 5 {
		t.Fatal(report, err)
	}
}

func TestUsageEmptyStateHelpAndInvalidArguments(t *testing.T) {
	home := filepath.Join(t.TempDir(), "not-created")
	setRuntimeTestHome(t, home)
	t.Setenv("COCONET_LOCAL_DAG_HOME", "")
	code, out, stderr := invokeUsage(t)
	if code != 0 || strings.Count(out, "No recorded usage.") != 3 || stderr != "" {
		t.Fatal(code, out, stderr)
	}
	for _, args := range [][]string{{"--days", "7"}, {"--group-by", "model"}, {"--project", "coco_p"}, {"--json"}, {"extra"}} {
		code, _, stderr := invokeUsage(t, args...)
		if code != 2 || !strings.Contains(stderr, "no options required") {
			t.Fatal(args, code, stderr)
		}
	}
	for _, args := range [][]string{{"usage", "--help"}, {"help", "usage"}} {
		var out, stderr bytes.Buffer
		if code := run(context.Background(), args, strings.NewReader(""), &out, &stderr); code != 0 || !strings.Contains(out.String(), "7 days, 30 days and all time") || strings.Contains(out.String(), "--group-by") {
			t.Fatal(code, out.String(), stderr.String())
		}
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("read-only command created local state", err)
	}
}

func TestUsageOldWorkerReportsPartialTotalsWithoutStoppingIt(t *testing.T) {
	setRuntimeTestHome(t, t.TempDir())
	t.Setenv("COCONET_LOCAL_DAG_HOME", "")
	paths, err := resolveRuntimePaths()
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(paths.root, "local-dag")
	old := filepath.Join(base, "scopes", "old")
	usageCommandFixture(t, base, "ok", "coco_ok", time.Now())
	usageCommandFixture(t, old, "old", "coco_old", time.Now())
	var requests []string
	var requestsMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestsMu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path)
		requestsMu.Unlock()
		if r.URL.Path == "/status" {
			io.WriteString(w, `{}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	if err := atomicWriteJSON(filepath.Join(old, "control.json"), localDAGEndpoint{URL: server.URL, Token: strings.Repeat("a", 64)}, 0600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := invokeUsage(t)
	if code != 1 || !strings.Contains(out, "totals are incomplete") || !strings.Contains(out, "fixture-model") || !strings.Contains(stderr, "coconet update") {
		t.Fatal(code, out, stderr)
	}
	requestsMu.Lock()
	defer requestsMu.Unlock()
	if strings.Join(requests, ",") != "GET /status,GET /usage" {
		t.Fatal("unexpected daemon operation", requests)
	}
}

func TestUsageReadsLiveWorkerWithoutModelCalls(t *testing.T) {
	setRuntimeTestHome(t, t.TempDir())
	root := t.TempDir()
	t.Setenv("COCONET_LOCAL_DAG_HOME", root)
	now := time.Now().UTC()
	usageCommandFixture(t, root, "recent", "coco_live", now)
	usageCommandFixture(t, root, "old", "coco_old", now.Add(-8*24*time.Hour))
	store, err := graphstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// No inference runner is configured: reading usage needs no Agent.
	engine := &localdag.Engine{Store: store, Actor: "fixture"}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serveLocalDAG(ctx, root, engine, 1, io.Discard) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("daemon did not stop")
		}
	}()
	control := filepath.Join(root, "control.json")
	deadline := time.Now().Add(5 * time.Second)
	var endpoint localDAGEndpoint
	for {
		if data, err := os.ReadFile(control); err == nil && json.Unmarshal(data, &endpoint) == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon did not start")
		}
		time.Sleep(time.Millisecond)
	}
	report, err := collectUsage(ctx, now)
	if err != nil || len(report.Warnings) > 0 || len(report.Periods) != 3 {
		t.Fatal(report, err)
	}
	if report.Periods[0].Totals.Attempts != 1 || report.Periods[1].Totals.Attempts != 2 || report.Periods[2].Totals.Attempts != 2 || report.Periods[0].Totals.CacheRead.Tokens != 60 {
		t.Fatal(report)
	}
	for _, tc := range []struct {
		method, token, origin, path string
		status                      int
	}{
		{"GET", "", "", "/usage", 401}, {"GET", endpoint.Token, "https://example.test", "/usage", 403},
		{"POST", endpoint.Token, "", "/usage", 405}, {"GET", endpoint.Token, "", "/usage?query=bad-date", 400},
	} {
		req, err := http.NewRequestWithContext(ctx, tc.method, endpoint.URL+tc.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+tc.token)
		req.Header.Set("Origin", tc.origin)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != tc.status {
			t.Fatal(tc, res.StatusCode)
		}
	}
}

func TestUsageTextShowsMissingCountersAndCancellation(t *testing.T) {
	setRuntimeTestHome(t, t.TempDir())
	root := t.TempDir()
	t.Setenv("COCONET_LOCAL_DAG_HOME", root)
	usageCommandFixture(t, root, "known", "coco_p", time.Now())
	s, err := graphstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Begin(graphstore.Run{ID: "unknown", TaskID: "unknown", ProjectID: "coco_p", Kind: "relations", Provider: "cursor", Status: "running", StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Input: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	code, out, stderr := invokeUsage(t)
	if code != 0 || stderr != "" {
		t.Fatal(code, out, stderr)
	}
	for _, want := range []string{"100", "—", "cursor / unknown", "Reported tokens only", "this device"} {
		if !strings.Contains(out, want) {
			t.Fatal("missing", want, out)
		}
	}
	if strings.ContainsAny(out, "?$") || strings.Contains(out, "Cache hit") || strings.Contains(out, "Estimated cost") {
		t.Fatal("report includes unwanted missing markers, rates or cost", out)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var output, errors bytes.Buffer
	if code := runUsage(ctx, nil, &output, &errors); code != 1 || !strings.Contains(errors.String(), "canceled") {
		t.Fatal(code, errors.String())
	}
}

func TestUsageFixedPeriodBoundaries(t *testing.T) {
	setRuntimeTestHome(t, t.TempDir())
	root := t.TempDir()
	t.Setenv("COCONET_LOCAL_DAG_HOME", root)
	now := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	for _, entry := range []struct {
		id string
		at time.Time
	}{
		{"seven", now.Add(-7 * 24 * time.Hour)},
		{"before-seven", now.Add(-7*24*time.Hour - time.Nanosecond)},
		{"thirty", now.Add(-30 * 24 * time.Hour)},
		{"before-thirty", now.Add(-30*24*time.Hour - time.Nanosecond)},
	} {
		usageCommandFixture(t, root, entry.id, "p", entry.at)
	}
	report, err := collectUsage(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []int{1, 3, 4} {
		if report.Periods[i].Totals.Attempts != want {
			t.Fatal(report)
		}
	}
}
