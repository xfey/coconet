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

func TestServerHelpDoesNotOpenDeploymentOrLogOperations(t *testing.T) {
	var visit func(clihelp.Command, []string)
	visit = func(c clihelp.Command, path []string) {
		for _, args := range [][]string{append([]string{"help"}, path...), append(append([]string{}, path...), "--help"), append(append([]string{}, path...), "-h")} {
			var stdout, stderr bytes.Buffer
			code := runServerCLI(context.Background(), args, &stdout, &stderr)
			if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), c.Summary) {
				t.Fatalf("%v: code=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
			}
		}
		for _, child := range c.Children {
			visit(child, append(append([]string{}, path...), child.Name))
		}
	}
	visit(clihelp.Server(), nil)
	dest := filepath.Join(t.TempDir(), "uncreated")
	for _, args := range [][]string{
		{"init", "--data-dir", dest, "--advertised-endpoint", "http://127.0.0.1:8080", "--help"},
		{"restore", "--data-dir", dest, "--input", "/nonexistent/archive", "--help"},
		{"serve", "--config", "/nonexistent/config", "--help"},
	} {
		var stdout, stderr bytes.Buffer
		if code := runServerCLI(context.Background(), args, &stdout, &stderr); code != 0 || stderr.Len() != 0 {
			t.Fatalf("%v: code=%d stderr=%q", args, code, stderr.String())
		}
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("help created deployment state: %v", err)
	}
}
