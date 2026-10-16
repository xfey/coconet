package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/agentplugin"
	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/inference"
	"github.com/xfey/coconet/packages/localdag"
	"github.com/xfey/coconet/packages/workgraph"
)

func TestAutomaticInferenceRoutesExactSourceProfile(t *testing.T) {
	paths, _, _ := relayConfigurationFixture(t)
	root := t.TempDir()
	engine := &localdag.Engine{Actor: "device"}
	entries := []agentIntegration{}
	for _, agent := range []string{"codex", "claude", "cursor"} {
		for _, label := range []string{"first", "second"} {
			profile, _ := canonicalPath(t.TempDir())
			command := filepath.Join(t.TempDir(), agent+"-fixture")
			if err := os.WriteFile(command, []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
				t.Fatal(err)
			}
			entry, err := newAgentIntegration(agentplugin.Host(agent), command, profile)
			if err != nil {
				t.Fatal(err)
			}
			entry.Status = agentIntegrationConfigured
			entries = append(entries, entry)
			request := localDAGObservationRequest{ProjectID: "project", Agent: contracts.AgentType(agent), NativeID: agent + label, ConfigRoot: profile}
			if err := rememberLocalInferenceSource(root, engine, request); err != nil {
				t.Fatal(err)
			}
			if err := saveAgentIntegrationState(paths, agentIntegrationState{Integrations: entries}); err != nil {
				t.Fatal(err)
			}
			session := workgraph.Session{ID: localdag.ObservationSessionID("project", engine.Actor, agent, agent+label), Agent: agent}
			runner, err := automaticInferenceResolver(root)("project", session)
			if err != nil {
				t.Fatal(err)
			}
			config := runner.(inference.Local).Config
			if config.Provider != agent || config.Command != command || config.ConfigRoot != profile || config.Model != "" || config.Timeout != 120*time.Second {
				t.Fatal(config)
			}
			// The same profile may be reached through a symlink, but cannot be replaced.
			alias := filepath.Join(t.TempDir(), "alias")
			if err := os.Symlink(profile, alias); err != nil {
				t.Fatal(err)
			}
			request.ConfigRoot = alias
			if err := rememberLocalInferenceSource(root, engine, request); err != nil {
				t.Fatal(err)
			}
			request.ConfigRoot = t.TempDir()
			if err := rememberLocalInferenceSource(root, engine, request); err == nil {
				t.Fatal("source profile changed")
			}
			info, err := os.Stat(localInferenceSourcePath(root, "project", session.ID))
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal(info, err)
			}
		}
	}
	// Removing the integration must fail explicitly, never choose another profile.
	last := entries[len(entries)-1]
	if err := saveAgentIntegrationState(paths, agentIntegrationState{Integrations: entries[:len(entries)-1]}); err != nil {
		t.Fatal(err)
	}
	session := workgraph.Session{ID: localdag.ObservationSessionID("project", engine.Actor, "cursor", "cursorsecond"), Agent: "cursor"}
	if _, err := automaticInferenceResolver(root)("project", session); err == nil || !strings.Contains(err.Error(), "agent list") {
		t.Fatal(err)
	}
	// Multiple executables claiming one profile cannot be chosen arbitrarily.
	duplicate, _ := newAgentIntegration(last.Type, os.Args[0], last.ConfigRoot)
	if err := saveAgentIntegrationState(paths, agentIntegrationState{Integrations: append(entries, duplicate)}); err != nil {
		t.Fatal(err)
	}
	if _, err := automaticInferenceResolver(root)("project", session); err == nil {
		t.Fatal("ambiguous installation accepted")
	}
	session.Agent = "codex"
	if _, err := automaticInferenceResolver(root)("project", session); err == nil {
		t.Fatal("source provider mismatch accepted")
	}
	if _, err := automaticInferenceResolver(root)("other-project", session); err == nil {
		t.Fatal("cross-project source accepted")
	}
}
