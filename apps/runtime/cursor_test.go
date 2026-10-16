package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/xfey/coconet/adapters"
	"github.com/xfey/coconet/packages/agentplugin"
	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/localdag"
	"path/filepath"
	"testing"
)

func TestCursorHookLifecycleAndEmptyEnd(t *testing.T) {
	for _, prompted := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "submitted"}[prompted], func(t *testing.T) {
			root, workspace, profile := t.TempDir(), t.TempDir(), t.TempDir()
			workspace, _ = filepath.EvalSymlinks(workspace)
			profile, _ = filepath.EvalSymlinks(profile)
			managedMCPConfig(t, root, []localDAGWorkspace{{Path: workspace, ProjectID: "project"}})
			t.Setenv("CURSOR_CONFIG_DIR", profile)
			t.Setenv("COCONET_DISABLE_BACKGROUND_SYNC", "1")
			id := "11111111-1111-4111-8111-111111111111"
			invoke := func(agent, name string) string {
				data, _ := json.Marshal(hookEvent{HookEventName: name, ConversationID: id, CursorVersion: "fixture", WorkspaceRoots: []string{workspace}, CWD: "/wrong-directory"})
				var out bytes.Buffer
				if code := runHook([]string{"--agent", agent}, bytes.NewReader(data), &out, &out); code != 0 {
					t.Fatal(code)
				}
				return out.String()
			}
			if out := invoke("claude", "sessionStart"); out != "" {
				t.Fatal("inherited Claude plugin emitted context")
			}
			out := invoke("cursor", "sessionStart")
			var payload map[string]any
			if json.Unmarshal([]byte(out), &payload) != nil || payload["additional_context"] == nil || payload["hookSpecificOutput"] != nil {
				t.Fatal("wrong Cursor context envelope", out)
			}
			if prompted {
				if out := invoke("cursor", "beforeSubmitPrompt"); out != "" {
					t.Fatal(out)
				}
			}
			if out := invoke("cursor", "sessionEnd"); out != "" {
				t.Fatal(out)
			}
			queued, err := localDAGInbox(root)
			if err != nil || len(queued) != 1 {
				t.Fatal(queued, err)
			}
			req := queued[0].Request
			if req.CWD != workspace || req.Agent != contracts.AgentCursor || req.NativeID != id || req.EmptyStart == prompted {
				t.Fatal(req)
			}
			store, err := graphstore.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			engine := &localdag.Engine{Store: store, Actor: "actor"}
			// Use the same Actor from the fixture's service configuration.
			config, err := loadLocalDAGService(root)
			if err != nil {
				t.Fatal(err)
			}
			engine.Actor = config.Actor
			_, err = prepareLocalDAGObservation(context.Background(), root, engine, req, nil)
			want := errEmptyNativeSession
			if prompted {
				want = adapters.ErrNativeSessionMissing
			}
			if !errors.Is(err, want) {
				t.Fatal(err)
			}
		})
	}
}
func TestCursorDefaultMigrationDoesNotRestoreRemovedHosts(t *testing.T) {
	paths := defaultSetupFixture(t)
	if err := atomicWriteJSON(filepath.Join(paths.root, defaultAgentsFilename), defaultAgentSetupState{SchemaVersion: 1}, 0600); err != nil {
		t.Fatal(err)
	}
	if err := saveAgentIntegrationState(paths, agentIntegrationState{Integrations: []agentIntegration{}}); err != nil {
		t.Fatal(err)
	}
	report := setupReport(t, 0)
	if len(report.Results) != 1 || report.Results[0].Host != agentplugin.HostCursor {
		t.Fatal(report)
	}
	state, _, _ := loadAgentIntegrationState(paths)
	removeAgentIntegration = func(context.Context, agentIntegration) agentplugin.UninstallResult {
		return agentplugin.UninstallResult{Status: agentplugin.StatusRemoved}
	}
	if runAgentRemove(context.Background(), []string{state.Integrations[0].ID}, &bytes.Buffer{}, &bytes.Buffer{}) != 0 {
		t.Fatal("remove failed")
	}
	if len(setupReport(t, 0).Results) != 0 {
		t.Fatal("restored explicitly removed Cursor")
	}
}
func TestCursorProfilesShareOneExtensionInstallation(t *testing.T) {
	paths := defaultSetupFixture(t)
	a, _ := newAgentIntegration(agentplugin.HostCursor, "agent", t.TempDir())
	b, _ := newAgentIntegration(agentplugin.HostCursor, "cursor-agent", t.TempDir())
	saveAgentIntegrationState(paths, agentIntegrationState{Integrations: []agentIntegration{a, b}})
	removed := 0
	removeAgentIntegration = func(context.Context, agentIntegration) agentplugin.UninstallResult {
		removed++
		return agentplugin.UninstallResult{Status: agentplugin.StatusRemoved}
	}
	if runAgentRemove(context.Background(), []string{a.ID}, &bytes.Buffer{}, &bytes.Buffer{}) != 0 || removed != 0 {
		t.Fatal("removed shared Cursor extensions")
	}
	if runAgentRemove(context.Background(), []string{b.ID}, &bytes.Buffer{}, &bytes.Buffer{}) != 0 || removed != 1 {
		t.Fatal("last Cursor extension remains")
	}
}

func TestCursorForkUsesRegisteredInstallation(t *testing.T) {
	paths := defaultSetupFixture(t)
	profile := t.TempDir()
	a, _ := newAgentIntegration(agentplugin.HostCursor, "/opt/cursor/bin/cursor-agent", profile)
	if err := saveAgentIntegrationState(paths, agentIntegrationState{Integrations: []agentIntegration{a}}); err != nil {
		t.Fatal(err)
	}
	if cmd, err := cursorForkCommand(profile, "agent"); err != nil || cmd != a.Command {
		t.Fatal(cmd, err)
	}
	if cmd, err := cursorForkCommand(t.TempDir(), "agent"); err != nil || cmd != "agent" {
		t.Fatal(cmd, err)
	}
	b, _ := newAgentIntegration(agentplugin.HostCursor, "/other/cursor-agent", profile)
	if err := saveAgentIntegrationState(paths, agentIntegrationState{Integrations: []agentIntegration{a, b}}); err != nil {
		t.Fatal(err)
	}
	if _, err := cursorForkCommand(profile, "agent"); err == nil {
		t.Fatal("ambiguous installation accepted")
	}
}
