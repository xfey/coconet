package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUnboundHookIgnoresLegacyDeclarationAndStaysSilent(t *testing.T) {
	repositoryRoot := initTestRepository(t, "hook-project")
	legacyDirectory := filepath.Join(repositoryRoot, ".coconet")
	if err := os.MkdirAll(legacyDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDirectory, "project.json"), []byte(`{"schema_version":2,"project_id":"coco_fixture01","secret":"ignored"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	setRuntimeTestHome(t, t.TempDir())
	setRuntimeTestServer(t, "https://coconet.test")

	for _, eventName := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "Stop"} {
		payload := hookJSON(t, map[string]string{"hook_event_name": eventName, "cwd": repositoryRoot})
		var output, errors bytes.Buffer
		if code := runHook([]string{"--agent", "codex"}, bytes.NewReader(payload), &output, &errors); code != 0 || errors.Len() != 0 || output.Len() != 0 {
			t.Fatalf("unexpected %s hook code=%d stdout=%q stderr=%q", eventName, code, output.String(), errors.String())
		}
	}
}

func TestHookAlwaysFailsOpenOnMalformedArgumentsAndInput(t *testing.T) {
	for _, test := range []struct {
		args  []string
		input string
	}{
		{[]string{"--unknown"}, `{}`},
		{[]string{"--agent", "other"}, `{}`},
		{[]string{"--agent", "claude"}, `{not-json`},
	} {
		var output, errors bytes.Buffer
		if code := runHook(test.args, strings.NewReader(test.input), &output, &errors); code != 0 || output.Len() != 0 || errors.Len() != 0 {
			t.Fatalf("hook did not fail open: code=%d stdout=%q stderr=%q", code, output.String(), errors.String())
		}
	}
}

func TestRecallHookEventsAreIgnoredWithoutSearchContextOrLocalState(t *testing.T) {
	remote := newFakeCoconetServer()
	remote.searchDelay = 5 * time.Second
	server := httptest.NewServer(remote)
	t.Cleanup(server.Close)
	repositoryRoot, paths := configureBoundRepository(t, remote, server.URL)
	target := fixtureDeploymentTarget(server.URL)

	for _, eventName := range []string{"UserPromptSubmit", "PreToolUse"} {
		payload := hookJSON(t, map[string]any{
			"hook_event_name": eventName,
			"session_id":      "ignored-recall-session",
			"cwd":             repositoryRoot,
			"prompt":          "This prompt must not be searched",
			"tool_name":       "Edit",
			"tool_input":      map[string]string{"file_path": filepath.Join(repositoryRoot, "apps", "runtime", "hook.go")},
		})
		started := time.Now()
		var output, errors bytes.Buffer
		if code := runHook([]string{"--agent", "claude"}, bytes.NewReader(payload), &output, &errors); code != 0 || output.Len() != 0 || errors.Len() != 0 {
			t.Fatalf("%s was not ignored: code=%d stdout=%q stderr=%q", eventName, code, output.String(), errors.String())
		}
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Fatalf("%s attempted blocking work: %s", eventName, elapsed)
		}
	}
	if remote.lastSearch.Query != "" {
		t.Fatalf("a removed recall Hook issued Search: %#v", remote.lastSearch)
	}
	if _, err := os.Stat(deploymentSyncPath(paths, target.DeploymentID)); !os.IsNotExist(err) {
		t.Fatalf("removed recall Hooks created local Runtime state: %v", err)
	}
}

func hookJSON(t *testing.T, value any) []byte {
	t.Helper()
	content, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return content
}
