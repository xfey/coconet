package claude

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/xfey/coconet/adapters"
)

func TestArtifactLookupDistinguishesMissingAmbiguousAndUnreadable(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node required")
	}
	helper, err := filepath.Abs("helper/helper.mjs")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"missing", "ambiguous", "unreadable"} {
		t.Run(kind, func(t *testing.T) {
			profile := t.TempDir()
			sdk := filepath.Join(t.TempDir(), "sdk.mjs")
			if err := os.WriteFile(sdk, []byte("export {};"), 0600); err != nil {
				t.Fatal(err)
			}
			expected := adapters.ErrNativeSessionMissing
			projects := filepath.Join(profile, "projects")
			if kind == "ambiguous" {
				expected = adapters.ErrNativeSessionAmbiguous
				for _, dir := range []string{"a", "b"} {
					p := filepath.Join(projects, dir)
					if err := os.MkdirAll(p, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(p, "source.jsonl"), []byte("{}\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			} else if kind == "unreadable" {
				// ENOTDIR is deterministic even when tests run with elevated permissions.
				expected = adapters.ErrNativeSessionUnreadable
				if err := os.WriteFile(projects, []byte("not a directory"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			adapter := Adapter{NodeCommand: node, HelperPath: helper, SDKEntry: sdk, ConfigRoot: profile}
			_, _, err := adapter.Observe(context.Background(), profile, "source", adapters.ObservationCheckpoint{})
			if !errors.Is(err, expected) {
				t.Fatalf("lookup classification: %v, want %v", err, expected)
			}
		})
	}
}
