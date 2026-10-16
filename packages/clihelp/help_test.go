package clihelp

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHelpSelectionAndArgumentBoundaries(t *testing.T) {
	for _, test := range []struct {
		args    []string
		handled bool
		code    int
		want    string
	}{
		{[]string{"help", "connect"}, true, 0, "--server URL"},
		{[]string{"connect", "--path", "/missing", "--help"}, true, 0, "--path DIRECTORY"},
		{[]string{"agent", "add", "claude", "-h"}, true, 0, "--config-root DIR"},
		{[]string{"agent", "--help"}, true, 0, "Remove one registered"},
		{[]string{"projection", "retry", "--help"}, true, 0, "Choose exactly one"},
		{[]string{"help", "unknown"}, true, 2, "Unknown help topic"},
		{[]string{"help", "history", "unknown"}, true, 2, "Unknown help topic"},
		{[]string{"agent", "unknown", "--help"}, true, 2, "Unknown help topic"},
		{[]string{"connect", "--path", "--help"}, false, 0, ""},
		{[]string{"connect", "--path=--help"}, false, 0, ""},
		{[]string{"library", "add", "--note", "-h"}, false, 0, ""},
		{[]string{"history", "start", "--sessions", "--help"}, false, 0, ""},
		{[]string{"connect", "--", "--help"}, false, 0, ""},
		{[]string{"connect", "--no-browser", "--help"}, true, 0, "--server URL"},
		{[]string{"connect", "--no-browser=false", "--help"}, true, 0, "--server URL"},
		{[]string{"status", "--verbose"}, false, 0, ""},
		{nil, false, 0, ""},
	} {
		t.Run(strings.Join(test.args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			handled, code := Handle(Runtime(), test.args, &stdout, &stderr)
			if handled != test.handled || code != test.code || !strings.Contains(stdout.String()+stderr.String(), test.want) {
				t.Fatalf("handled=%t code=%d stdout=%q stderr=%q", handled, code, stdout.String(), stderr.String())
			}
			if !handled && (stdout.Len() != 0 || stderr.Len() != 0) {
				t.Fatal("ordinary invocation produced help output")
			}
		})
	}
}

func TestGeneratedManualsAreCurrent(t *testing.T) {
	for file, advanced := range map[string]bool{"cli.md": false, "cli-internal.md": true} {
		content, err := os.ReadFile(filepath.Join("..", "..", "docs", file))
		if err != nil || string(content) != Manual(advanced) {
			t.Fatalf("%s is stale or missing; run go generate ./packages/clihelp (read: %v)", file, err)
		}
	}
}
