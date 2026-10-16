package main

import (
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
)

func TestClaudeHooksPinProjectAcrossDirectoriesAndResume(t *testing.T) {
	remote := newFakeCoconetServer()
	server := httptest.NewServer(remote)
	defer server.Close()
	root, paths := configureBoundRepository(t, remote, server.URL)
	targetForUpgrade, err := discoverDeployment(context.Background(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := persistBinding(paths, root, targetForUpgrade, remote.account.AccountID, remote.project.ProjectID, ""); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("CLAUDE_PROJECT_DIR", root)
	child := filepath.Join(root, "nested")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatal(err)
	}
	target := fixtureDeploymentTarget(server.URL)
	if err := saveBinding(paths, fixtureBinding(child, "coco_nestedfixture", remote.account.AccountID, target)); err != nil {
		t.Fatal(err)
	}
	run := func(name, cwd, id string) {
		t.Helper()
		var out, stderr bytes.Buffer
		payload := hookJSON(t, map[string]string{"hook_event_name": name, "session_id": "native-origin-fixture", "cwd": cwd, "event_id": id, "transcript_path": "/fixture/source.jsonl"})
		if code := runHook([]string{"--agent", "claude"}, bytes.NewReader(payload), &out, &stderr); code != 0 || stderr.Len() != 0 {
			t.Fatal("hook failed", code, stderr.String())
		}
	}
	run("SessionStart", root, "")
	// Relaunch from a different worktree, then enter a separately bound child.
	outside := t.TempDir()
	t.Setenv("CLAUDE_PROJECT_DIR", outside)
	run("SessionStart", outside, "")
	for i, cwd := range []string{child, filepath.Dir(root), outside} {
		run("Stop", cwd, []string{"event-origin-child", "event-origin-parent", "event-origin-worktree"}[i])
	}
	binding, _ := bindingAtRoot(paths, root)
	events, err := localDAGInbox(localDAGBindingRoot(paths, *binding))
	if err != nil || len(events) != 3 {
		t.Fatal("missing events", len(events), err)
	}
	for _, item := range events {
		event := item.Request
		if event.ProjectID != remote.project.ProjectID || !samePath(event.Root, root) || event.ConfigRoot == "" || event.TranscriptPath == "" {
			t.Fatal("origin drifted", event)
		}
	}
	// Disconnect/rebind must not silently route the old Session to the new Project.
	if _, err := removeBindingAtRoot(paths, root); err != nil {
		t.Fatal(err)
	}
	if binding, _, _, err := hookSessionBinding(paths, contracts.AgentClaude, hookEvent{SessionID: "native-origin-fixture", CWD: child}); err != nil || binding != nil {
		t.Fatal("disconnected origin revived", err)
	}
	if err := saveBinding(paths, fixtureBinding(root, "coco_reboundfixture", remote.account.AccountID, target)); err != nil {
		t.Fatal(err)
	}
	if binding, _, _, err := hookSessionBinding(paths, contracts.AgentClaude, hookEvent{SessionID: "native-origin-fixture", CWD: root}); err == nil || binding != nil {
		t.Fatal("rebound origin revived")
	}
	// Same native ID in another isolated Agent root is a separate local origin.
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("CLAUDE_PROJECT_DIR", child)
	binding, origin, _, err := hookSessionBinding(paths, contracts.AgentClaude, hookEvent{HookEventName: "SessionStart", SessionID: "native-origin-fixture", CWD: child})
	if err != nil || binding == nil || binding.ProjectID != "coco_nestedfixture" || !samePath(origin, child) {
		t.Fatal("config roots conflated", err)
	}
}

func TestClaudeUnconfirmedStopDoesNotPinDynamicDirectory(t *testing.T) {
	remote := newFakeCoconetServer()
	server := httptest.NewServer(remote)
	defer server.Close()
	root, paths := configureBoundRepository(t, remote, server.URL)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	binding, origin, _, err := hookSessionBinding(paths, contracts.AgentClaude, hookEvent{HookEventName: "Stop", SessionID: "native-unconfirmed", CWD: root})
	if err != nil || binding == nil || origin != "" {
		t.Fatal("dynamic cwd became authoritative", err)
	}
}
