package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/xfey/coconet/packages/agentplugin"
	"github.com/xfey/coconet/packages/distribution"
)

func TestInstallRunsWithoutConfirmation(t *testing.T) {
	restoreBootstrapStubs(t)
	loadBundleManifest = func(string) (distribution.Manifest, error) {
		return distribution.Manifest{Version: "0.7.0", OS: runtime.GOOS, Architecture: runtime.GOARCH}, nil
	}
	called := false
	installBundle = func(distribution.InstallOptions) (distribution.InstallResult, error) {
		called = true
		return distribution.InstallResult{Version: "0.7.0"}, nil
	}
	var stdout, stderr bytes.Buffer
	exit := run([]string{"install", "--bundle", "/fixture", "--root", "/install", "--bin-dir", "/bin"}, &stdout, &stderr)
	if exit != 0 || !called || stdout.String() != "✓ Installed Coconet 0.7.0\n" || stderr.Len() != 0 {
		t.Fatalf("unexpected install exit=%d called=%v stdout=%q stderr=%q", exit, called, stdout.String(), stderr.String())
	}
}

func TestInstallReturnsStructuredResult(t *testing.T) {
	restoreBootstrapStubs(t)
	loadBundleManifest = func(path string) (distribution.Manifest, error) {
		if path != "/fixture" {
			t.Fatalf("unexpected bundle path %q", path)
		}
		return distribution.Manifest{Version: "0.7.0", OS: runtime.GOOS, Architecture: runtime.GOARCH}, nil
	}
	installBundle = func(options distribution.InstallOptions) (distribution.InstallResult, error) {
		if options.InstallRoot != "/install" || options.BinDirectory != "/bin" || !options.SkipBinLink || options.CurrentOS != runtime.GOOS || options.CurrentArch != runtime.GOARCH {
			t.Fatalf("unexpected install options %#v", options)
		}
		return distribution.InstallResult{Version: "0.7.0", ReleasePath: "/install/versions/0.7.0", Executable: "/install/current/bin/coconet", AlreadyExists: true}, nil
	}
	var stdout, stderr bytes.Buffer
	exit := run([]string{"install", "--bundle", "/fixture", "--root", "/install", "--bin-dir", "/bin", "--no-bin-link", "--json"}, &stdout, &stderr)
	if exit != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), `"status":"ok"`) || !strings.Contains(stdout.String(), `"already_exists":true`) {
		t.Fatalf("unexpected install exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
}

func TestInstallReportsManifestAndInstallFailures(t *testing.T) {
	restoreBootstrapStubs(t)
	loadBundleManifest = func(string) (distribution.Manifest, error) {
		return distribution.Manifest{}, errors.New("bad manifest")
	}
	var stdout, stderr bytes.Buffer
	if exit := run([]string{"install"}, &stdout, &stderr); exit != 1 || !strings.Contains(stderr.String(), "bad manifest") {
		t.Fatalf("unexpected manifest failure exit=%d stderr=%q", exit, stderr.String())
	}
	restoreBootstrapStubs(t)
	loadBundleManifest = func(string) (distribution.Manifest, error) {
		return distribution.Manifest{Version: "0.7.0", OS: runtime.GOOS, Architecture: runtime.GOARCH}, nil
	}
	installBundle = func(distribution.InstallOptions) (distribution.InstallResult, error) {
		return distribution.InstallResult{}, errors.New("install failed")
	}
	stdout.Reset()
	stderr.Reset()
	if exit := run([]string{"install"}, &stdout, &stderr); exit != 1 || !strings.Contains(stderr.String(), "install failed") {
		t.Fatalf("unexpected install failure exit=%d stderr=%q", exit, stderr.String())
	}
}

func restoreBootstrapStubs(t *testing.T) {
	t.Helper()
	originalLoad := loadBundleManifest
	originalVerify := verifyBundle
	originalInstall := installBundle
	originalSetup := setupAgents
	originalGuard := guardInstallation
	guardInstallation = func(context.Context, string, string, bool, io.Writer) (installationLease, error) {
		return installationLease{Finish: func() error { return nil }}, nil
	}
	t.Setenv("COCONET_SKIP_AGENT_SETUP", "")
	setupAgents = func(string, io.Writer) agentplugin.SetupReport {
		return agentplugin.SetupReport{Status: "skipped", Results: []agentplugin.InstallResult{}}
	}
	verifyBundle = func(string, distribution.Manifest) error { return nil }
	t.Cleanup(func() {
		loadBundleManifest = originalLoad
		verifyBundle = originalVerify
		installBundle = originalInstall
		setupAgents = originalSetup
		guardInstallation = originalGuard
	})
}

func TestInstallConfiguresAgentsAfterRuntimeCommitAndReportsPartialFailure(t *testing.T) {
	restoreBootstrapStubs(t)
	loadBundleManifest = func(string) (distribution.Manifest, error) { return distribution.Manifest{}, nil }
	installed := false
	installBundle = func(distribution.InstallOptions) (distribution.InstallResult, error) {
		installed = true
		return distribution.InstallResult{Version: "fixture", ReleasePath: "/install/versions/fixture"}, nil
	}
	setupAgents = func(executable string, _ io.Writer) agentplugin.SetupReport {
		if !installed || executable != "/install/versions/fixture/bin/coconet" {
			t.Fatalf("Agent setup did not use the installed Runtime: %q", executable)
		}
		return agentplugin.SetupReport{Status: "partial", Results: []agentplugin.InstallResult{
			{Host: agentplugin.HostCodex, Command: "codex", Status: agentplugin.StatusFailed, Error: "fixture failure"},
			{Host: agentplugin.HostClaude, Command: "claude", Status: agentplugin.StatusConfigured, Changed: true},
		}}
	}
	for _, jsonMode := range []bool{false, true} {
		args := []string{"install", "--bundle", "/fixture"}
		if jsonMode {
			args = append(args, "--json")
		}
		var stdout, stderr bytes.Buffer
		if exit := run(args, &stdout, &stderr); exit != 0 {
			t.Fatalf("Agent failure broke Runtime installation: %d %s", exit, stderr.String())
		}
		if jsonMode {
			var result installOutput
			if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.Status != "ok" || result.Agents.Status != "partial" || len(result.Agents.Results) != 2 || stderr.Len() != 0 {
				t.Fatalf("invalid install result: %s stderr=%s err=%v", stdout.String(), stderr.String(), err)
			}
		} else if !strings.Contains(stdout.String(), "Coconet plugin configured for: claude") || !strings.Contains(stderr.String(), "Could not configure the Coconet plugin for codex") || !strings.Contains(stderr.String(), "agent list --verbose") {
			t.Fatalf("missing Agent failure explanation: stdout=%s stderr=%s", stdout.String(), stderr.String())
		}
	}
}

func TestInstallCanSkipAgentSetup(t *testing.T) {
	for _, mode := range []string{"flag", "environment"} {
		t.Run(mode, func(t *testing.T) {
			restoreBootstrapStubs(t)
			loadBundleManifest = func(string) (distribution.Manifest, error) { return distribution.Manifest{}, nil }
			installBundle = func(distribution.InstallOptions) (distribution.InstallResult, error) {
				return distribution.InstallResult{}, nil
			}
			setupAgents = func(string, io.Writer) agentplugin.SetupReport {
				t.Fatal("opt-out configured an Agent")
				return agentplugin.SetupReport{}
			}
			args := []string{"install", "--json"}
			if mode == "flag" {
				args = append(args, "--skip-agents")
			} else {
				t.Setenv("COCONET_SKIP_AGENT_SETUP", "1")
			}
			var stdout, stderr bytes.Buffer
			if run(args, &stdout, &stderr) != 0 || !strings.Contains(stdout.String(), `"status":"skipped"`) {
				t.Fatalf("skip failed: %s %s", stdout.String(), stderr.String())
			}
		})
	}
}

func TestGuardRefusalNeverActivatesStagedRelease(t *testing.T) {
	restoreBootstrapStubs(t)
	loadBundleManifest = func(string) (distribution.Manifest, error) { return distribution.Manifest{}, nil }
	staged := false
	installBundle = func(o distribution.InstallOptions) (distribution.InstallResult, error) {
		if !o.StageOnly {
			t.Fatal("activated before drain")
		}
		staged = true
		return distribution.InstallResult{ReleasePath: "/staged"}, nil
	}
	guardInstallation = func(context.Context, string, string, bool, io.Writer) (installationLease, error) {
		return installationLease{}, errors.New("unreadable active work")
	}
	var out bytes.Buffer
	if run([]string{"install", "--root", t.TempDir(), "--no-bin-link"}, &out, &out) != 1 || !staged || !strings.Contains(out.String(), "unreadable active work") {
		t.Fatal(staged, out.String())
	}
}
func TestActivationFailureReleasesGuardAndSkipsPlugins(t *testing.T) {
	restoreBootstrapStubs(t)
	loadBundleManifest = func(string) (distribution.Manifest, error) { return distribution.Manifest{}, nil }
	installBundle = func(o distribution.InstallOptions) (distribution.InstallResult, error) {
		if !o.StageOnly {
			return distribution.InstallResult{}, errors.New("activation failed")
		}
		return distribution.InstallResult{ReleasePath: "/staged"}, nil
	}
	released := false
	guardInstallation = func(context.Context, string, string, bool, io.Writer) (installationLease, error) {
		return installationLease{Finish: func() error { released = true; return nil }}, nil
	}
	setupAgents = func(string, io.Writer) agentplugin.SetupReport {
		t.Fatal("configured plugins after failed activation")
		return agentplugin.SetupReport{}
	}
	var out bytes.Buffer
	if run([]string{"install", "--root", t.TempDir(), "--no-bin-link"}, &out, &out) != 1 || !released {
		t.Fatal(released, out.String())
	}
}

func TestConcurrentInstallSkipsActivationAndPluginSetup(t *testing.T) {
	restoreBootstrapStubs(t)
	root := t.TempDir()
	loadBundleManifest = func(string) (distribution.Manifest, error) { return distribution.Manifest{Version: "1.0.0"}, nil }
	if err := os.MkdirAll(filepath.Join(root, "current"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "current", "manifest.json"), []byte(`{"product":"coconet","version":"2.0.0"}`), 0644); err != nil {
		t.Fatal(err)
	}

	installBundle = func(o distribution.InstallOptions) (distribution.InstallResult, error) {
		if !o.StageOnly {
			t.Fatal("overwrote newer installation")
		}
		return distribution.InstallResult{ReleasePath: "/staged", Version: "1.0.0"}, nil
	}
	released := false
	guardInstallation = func(_ context.Context, _, _ string, onlyIfNewer bool, _ io.Writer) (installationLease, error) {
		if !onlyIfNewer {
			t.Fatal("lost no-downgrade policy")
		}
		return installationLease{Current: true, Finish: func() error { released = true; return nil }}, nil
	}
	setupAgents = func(string, io.Writer) agentplugin.SetupReport {
		t.Fatal("repeated plugin setup")
		return agentplugin.SetupReport{}
	}
	var out, errs bytes.Buffer
	if code := run([]string{"install", "--root", root, "--if-newer", "--json"}, &out, &errs); code != 0 || !released || !strings.Contains(out.String(), `"version":"2.0.0"`) {
		t.Fatal(code, released, out.String(), errs.String())
	}
}
