package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateChecksAreCachedAndNeverEnterStructuredOutput(t *testing.T) {
	setRuntimeTestHome(t, t.TempDir())
	old := latestClientVersion
	defer func() { latestClientVersion = old }()
	calls := 0
	latestClientVersion = func(context.Context) (string, error) { calls++; return "1.2.3", nil }
	if runUpdateCheck(context.Background()) != 0 || runUpdateCheck(context.Background()) != 0 || calls != 1 {
		t.Fatal("checks were not bounded", calls)
	}
	paths, _ := resolveRuntimePaths()
	cache := readUpdateNotice(paths)
	if cache.Latest != "1.2.3" {
		t.Fatal(cache)
	}
	var out bytes.Buffer
	maybeNotifyUpdate([]string{"status", "--json"}, &out)
	maybeNotifyUpdate([]string{"mcp"}, &out)
	maybeNotifyUpdate([]string{"hook"}, &out)
	if out.Len() != 0 {
		t.Fatal("machine channel contaminated", out.String())
	}
}
func TestFailedUpdateCheckAlsoBacksOff(t *testing.T) {
	setRuntimeTestHome(t, t.TempDir())
	old := latestClientVersion
	defer func() { latestClientVersion = old }()
	calls := 0
	latestClientVersion = func(context.Context) (string, error) { calls++; return "", errors.New("offline") }
	runUpdateCheck(context.Background())
	runUpdateCheck(context.Background())
	if calls != 1 {
		t.Fatal(calls)
	}
}
func TestUpdateResolvesOwningNPMPrefixWithoutShellEvaluation(t *testing.T) {
	root := t.TempDir()
	prefix := filepath.Join(root, "a space; $(literal)")
	packageRoot := filepath.Join(prefix, "lib", "node_modules", "coconet")
	if err := os.MkdirAll(packageRoot, 0700); err != nil {
		t.Fatal(err)
	}
	prefix, _ = filepath.EvalSymlinks(prefix)
	packageRoot, _ = filepath.EvalSymlinks(packageRoot)
	p, r, err := resolveUpdateNPM(context.Background(), "must-not-run", packageRoot)
	if err != nil || p != prefix || r != packageRoot {
		t.Fatal(p, r, err)
	}
	if _, _, err := resolveUpdateNPM(context.Background(), "must-not-run", root); err == nil || !strings.Contains(err.Error(), "global npm") {
		t.Fatal(err)
	}
}

func TestUpdatePinsOneVersionAndReportsPartialInstallation(t *testing.T) {
	setRuntimeTestHome(t, t.TempDir())
	root := t.TempDir()
	prefix := filepath.Join(root, "prefix")
	pkg := filepath.Join(prefix, "lib", "node_modules", "coconet")
	if err := os.MkdirAll(pkg, 0700); err != nil {
		t.Fatal(err)
	}
	commands := filepath.Join(root, "commands")
	os.Mkdir(commands, 0700)
	trace := filepath.Join(root, "args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$COCONET_UPDATE_TEST_TRACE\"\n"
	if err := os.WriteFile(filepath.Join(commands, "npm"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", commands+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("COCONET_NPM_PACKAGE_ROOT", pkg)
	t.Setenv("COCONET_UPDATE_TEST_TRACE", trace)
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"name":"coconet","version":"1.1.0"}`), 0600)
	os.WriteFile(filepath.Join(pkg, "cli.mjs"), []byte("process.exit(9);\n"), 0600)
	oldVersion, oldLatest := version, latestClientVersion
	version = "1.0.0"
	defer func() { version = oldVersion; latestClientVersion = oldLatest }()
	calls := 0
	latestClientVersion = func(context.Context) (string, error) { calls++; return "1.1.0", nil }
	var stdout, stderr bytes.Buffer
	if code := runUpdate(context.Background(), nil, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "npm was updated, but Runtime activation did not finish") {
		t.Fatal(code, stderr.String())
	}
	data, err := os.ReadFile(trace)
	if err != nil || !strings.Contains(string(data), "--ignore-scripts\n--prefix\n") || !strings.HasSuffix(string(data), "coconet@1.1.0\n") || calls != 1 {
		t.Fatal(string(data), calls, err)
	}
	if _, err := os.Stat(filepath.Join(root, "data", "coconet", "current")); !os.IsNotExist(err) {
		t.Fatal("failed setup activated a Runtime", err)
	}
}
