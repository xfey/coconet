package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/xfey/coconet/adapters"
	"github.com/xfey/coconet/adapters/claude"
	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/localdag"
)

func TestEmptySessionEndRequiresTrackedStartupAndNoInteraction(t *testing.T) {
	for _, kind := range []string{"empty", "prompt", "stop", "resume", "old-plugin", "unknown-start", "late-end", "existing-file", "captured", "pending-baseline"} {
		t.Run(kind, func(t *testing.T) {
			root, workspace, profile := t.TempDir(), t.TempDir(), t.TempDir()
			workspace, _ = filepath.EvalSymlinks(workspace)
			profile, _ = filepath.EvalSymlinks(profile)
			t.Setenv("COCONET_LOCAL_DAG_HOME", root)
			t.Setenv("CLAUDE_CONFIG_DIR", profile)
			t.Setenv("CLAUDE_PROJECT_DIR", "")
			t.Setenv("COCONET_DISABLE_BACKGROUND_SYNC", "1")
			t.Setenv("COCONET_HOOK_LIFECYCLE", "1")
			if kind == "old-plugin" {
				t.Setenv("COCONET_HOOK_LIFECYCLE", "")
			}
			config := localDAGServiceConfig{Schema: 1, Actor: "reader", Workspaces: []localDAGWorkspace{{Path: workspace, ProjectID: "project"}}}
			event := hookEvent{HookEventName: "SessionStart", Source: "startup", SessionID: "source", CWD: workspace, TranscriptPath: filepath.Join(profile, "projects", "workspace", "source.jsonl")}
			if kind == "unknown-start" {
				event.Source = ""
			}
			if kind == "existing-file" {
				if err := os.MkdirAll(filepath.Dir(event.TranscriptPath), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(event.TranscriptPath, []byte("old content"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			invoke := func() localDAGObservationRequest {
				source, found, err := localDAGHookSource(root, config, contracts.AgentClaude, event)
				if err != nil || !found {
					t.Fatal(found, err)
				}
				return source
			}
			invoke()
			if kind == "prompt" {
				event.HookEventName = "UserPromptSubmit"
				invoke()
			}
			if kind == "stop" {
				event.HookEventName = "Stop"
				invoke()
			}
			if kind == "resume" {
				event.Source = "resume"
				invoke()
			}
			event.HookEventName = "SessionEnd"
			request := invoke()
			request.Event = "SessionEnd"
			request.TranscriptPath = event.TranscriptPath
			if kind == "late-end" {
				event.HookEventName = "UserPromptSubmit"
				invoke()
			}
			if kind == "existing-file" {
				if err := os.Remove(event.TranscriptPath); err != nil {
					t.Fatal(err)
				}
			}
			store, err := graphstore.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			engine := &localdag.Engine{Store: store, Actor: "reader"}
			if kind == "captured" {
				if _, err := engine.Capture(context.Background(), localdag.Capture{ProjectID: request.ProjectID, Agent: "claude", NativeID: request.NativeID}); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "pending-baseline" {
				normalized, _ := adapters.ApplyCheckpoint(nil, adapters.ObservationCheckpoint{})
				visible := &contracts.VisibleTextProjection{Version: 1, SourceCursor: normalized.ToCursor, Turns: []contracts.VisibleTextTurn{}}
				if _, err := engine.QueueInheritedObservation(context.Background(), localdag.Observation{ProjectID: request.ProjectID, Agent: "claude", NativeID: request.NativeID, Inherited: true, Normalized: normalized, Visible: visible}); err != nil {
					t.Fatal(err)
				}
			}
			empty, err := confirmedEmptySessionEnd(root, engine, request)
			if err != nil || empty != (kind == "empty") {
				t.Fatal("incorrect empty evidence", kind, empty, err)
			}
			// Exercise the real helper with an isolated SDK; missing history is checked
			// before SDK metadata calls, and no Agent/model process is invoked.
			sdk := filepath.Join(profile, "sdk.mjs")
			if err := os.WriteFile(sdk, []byte("export {};"), 0600); err != nil {
				t.Fatal(err)
			}
			adapter := claude.New("")
			adapter.ConfigRoot = profile
			adapter.SDKEntry = sdk
			_, err = prepareLocalDAGObservation(context.Background(), root, engine, request, adapter)
			expected := adapters.ErrNativeSessionMissing
			if kind == "empty" {
				expected = errEmptyNativeSession
			}
			if !errors.Is(err, expected) {
				t.Fatal("wrong observation outcome", kind, err)
			}
			if kind == "empty" {
				t.Setenv("COCONET_CLAUDE_SDK_ENTRY", sdk)
				if err := enqueueLocalDAGObservation(root, request); err != nil {
					t.Fatal(err)
				}
				if err := drainLocalDAGInbox(context.Background(), root, engine); err != nil {
					t.Fatal(err)
				}
				queue, _ := localDAGInbox(root)
				runs, _ := store.Runs("project")
				state, _ := store.State("project")
				if len(queue) != 0 || len(runs) != 1 || runs[0].Status != "skipped" || len(state.Sessions) != 0 {
					t.Fatal("empty end became work/failure", queue, runs)
				}
			}
		})
	}
}

func TestPromptLifecycleHookIsSilentAndDoesNotQueueWork(t *testing.T) {
	root, workspace, profile := t.TempDir(), t.TempDir(), t.TempDir()
	workspace, _ = filepath.EvalSymlinks(workspace)
	profile, _ = filepath.EvalSymlinks(profile)
	managedMCPConfig(t, root, []localDAGWorkspace{{Path: workspace, ProjectID: "project"}})
	t.Setenv("CLAUDE_CONFIG_DIR", profile)
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	t.Setenv("COCONET_HOOK_LIFECYCLE", "1")
	data, _ := json.Marshal(hookEvent{HookEventName: "UserPromptSubmit", SessionID: "source", CWD: workspace})
	var out bytes.Buffer
	if code := runHook([]string{"--agent", "claude"}, bytes.NewReader(data), &out, &out); code != 0 || out.Len() != 0 {
		t.Fatal("prompt hook interrupted work", out.String())
	}
	for _, file := range []string{"inbox", "graph.db", "control.json", "daemon.log"} {
		if _, err := os.Stat(filepath.Join(root, file)); !os.IsNotExist(err) {
			t.Fatal("prompt hook did extra work", file)
		}
	}
}
