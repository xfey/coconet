package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xfey/coconet/packages/clihelp"
)

func TestAllCommandHelpIsOfflineAndReadOnly(t *testing.T) {
	root := filepath.Join(t.TempDir(), "uncreated")
	setRuntimeTestHome(t, root)
	var visit func(clihelp.Command, []string)
	visit = func(c clihelp.Command, path []string) {
		for _, args := range [][]string{
			append([]string{"help"}, path...),
			append(append([]string{}, path...), "--help"),
			append(append([]string{}, path...), "-h"),
		} {
			t.Run(strings.Join(args, " "), func(t *testing.T) {
				var stdout, stderr bytes.Buffer
				code := run(context.Background(), args, strings.NewReader(""), &stdout, &stderr)
				if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "Usage: coconet") || !strings.Contains(stdout.String(), c.Summary) {
					t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
				}
				if strings.Contains(stdout.String(), "\x1b") {
					t.Fatal("redirected help contains terminal controls")
				}
			})
		}
		for _, child := range c.Children {
			visit(child, append(append([]string{}, path...), child.Name))
		}
	}
	visit(clihelp.Runtime(), nil)
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("help created Runtime state: %v", err)
	}
}

func TestConnectHelpDoesNotUpgradeRegisteredIntegrations(t *testing.T) {
	restoreAgentIntegrationStubs(t)
	setRuntimeTestHome(t, t.TempDir())
	paths, err := resolveRuntimePaths()
	if err != nil {
		t.Fatal(err)
	}
	entry, err := newAgentIntegration("claude", "fixture-agent", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	entry.RuntimeVersion = "outdated"
	if err := saveAgentIntegrationState(paths, agentIntegrationState{SchemaVersion: 1, Integrations: []agentIntegration{entry}}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(paths.integrationPath)
	for _, args := range [][]string{{"connect", "--help"}, {"init", "--help"}, {"connect", "coco_example", "--no-browser", "--help"}} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); code != 0 || stderr.Len() != 0 {
			t.Fatalf("%v: %d %q", args, code, stderr.String())
		}
	}
	after, _ := os.ReadFile(paths.integrationPath)
	if !bytes.Equal(before, after) {
		t.Fatal("help reconfigured an outdated Agent integration")
	}
}
