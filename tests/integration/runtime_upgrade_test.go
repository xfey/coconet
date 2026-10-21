package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/conversation"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/localdag"
	"golang.org/x/sys/unix"
)

// CI exercises the current protocol. Release acceptance can additionally use
// an actual published pre-drain binary, without downloading or running Agents.
func TestRuntimeUpgradeRestartsSelectedVersionAndPreservesQueue(t *testing.T) {
	base := t.TempDir()
	candidate := filepath.Join(base, "candidate", "bin", "coconet")
	if supplied := os.Getenv("COCONET_TEST_UPGRADE_RUNTIME"); supplied != "" {
		data, err := os.ReadFile(supplied)
		if err != nil {
			t.Fatal(err)
		}
		mustMkdirUpgrade(t, filepath.Dir(candidate))
		if err := os.WriteFile(candidate, data, 0755); err != nil {
			t.Fatal(err)
		}
	} else {
		buildVersionedRuntime(t, candidate, "99.1.0")
	}

	previous := os.Getenv("COCONET_TEST_LEGACY_RUNTIME")
	if previous == "" {
		previous = filepath.Join(base, "previous", "bin", "coconet")
		buildVersionedRuntime(t, previous, "99.0.0")
	}
	for _, mode := range []string{"rollback", "cancel", "activate"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			dag := filepath.Join(root, "config", "coconet", "local-dag")
			install := filepath.Join(root, "install")
			mustMkdirUpgrade(t, install)
			env := []string{"PATH=/usr/bin:/bin", "HOME=" + root, "XDG_CONFIG_HOME=" + filepath.Join(root, "config"), "XDG_DATA_HOME=" + filepath.Join(root, "data"), "CODEX_HOME=" + filepath.Join(root, "codex"), "CLAUDE_CONFIG_DIR=" + filepath.Join(root, "claude"), "CURSOR_CONFIG_DIR=" + filepath.Join(root, "cursor"), "COCONET_LOCAL_DAG_HOME=" + dag, "COCONET_SKIP_AGENT_SETUP=1"}
			store, err := graphstore.Open(dag)
			if err != nil {
				t.Fatal(err)
			}
			engine := &localdag.Engine{Store: store, Actor: "fixture"}
			_, err = engine.Capture(context.Background(), localdag.Capture{ProjectID: "project", Agent: "fixture", NativeID: "native", Artifact: []byte("retained native history"), Entries: []conversation.Entry{{Turn: contracts.NormalizedTurn{TurnID: "u1", Ordinal: 0, Role: "user", Kind: "message", Text: "Preserve this work through the upgrade."}}}})
			if err != nil {
				t.Fatal(err)
			}
			tasks, err := store.Tasks("project")
			if err != nil || len(tasks) != 1 {
				t.Fatal(tasks, err)
			}
			tasks[0].Status = "paused"
			tasks[0].Error = "fixture: user paused"
			if err := store.PutTask(tasks[0]); err != nil {
				t.Fatal(err)
			}
			before, err := store.State("project")
			if err != nil {
				t.Fatal(err)
			}
			store.Close()
			writeUpgradeJSON(t, filepath.Join(dag, "runtime.json"), map[string]any{"schema": 1, "actor": "fixture", "provider": "codex", "command": "/usr/bin/false", "profile": filepath.Join(root, "codex"), "model": "gpt-5.6-sol", "max_calls": 1, "timeout_seconds": 120, "workspaces": []any{}}, 0600)
			oldRoot := filepath.Dir(filepath.Dir(previous))
			if err := os.Symlink(oldRoot, filepath.Join(install, "current")); err != nil {
				t.Fatal(err)
			}
			worker := exec.Command(previous, "_local-daemon", "--root", dag)
			worker.Env = env
			if err := worker.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = worker.Process.Kill(); _ = worker.Wait() })
			old := waitUpgradeEndpoint(t, dag, "")
			// Stop only this isolated worker even if an assertion fails after restart.
			t.Cleanup(func() {
				stop := exec.Command(candidate, "local-dag", "stop", "--root", dag)
				stop.Env = env
				_ = stop.Run()
			})
			var fork *os.File
			if old.Protocol == 0 {
				mustMkdirUpgrade(t, filepath.Join(dag, "forks"))
				fork, err = os.OpenFile(filepath.Join(dag, "forks", "operations.lock"), os.O_CREATE|os.O_RDWR, 0600)
				if err != nil {
					t.Fatal(err)
				}
				defer fork.Close()
				if err := unix.Flock(int(fork.Fd()), unix.LOCK_EX); err != nil {
					t.Fatal(err)
				}
			}
			guard := exec.Command(candidate, "_install-guard", install)
			guard.Env = env
			input, err := guard.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			output, err := guard.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var diagnostics bytes.Buffer
			guard.Stderr = &diagnostics
			if err := guard.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { input.Close(); _ = guard.Process.Kill() })
			ready := make(chan error, 1)
			go func() {
				var v struct {
					Ready bool `json:"ready"`
				}
				err := json.NewDecoder(output).Decode(&v)
				if err == nil && !v.Ready {
					err = fmt.Errorf("missing readiness")
				}
				ready <- err
			}()
			if fork != nil {
				select {
				case err := <-ready:
					t.Fatalf("migration passed unfinished handoff: %v", err)
				case <-time.After(150 * time.Millisecond):
				}
				// The published process remains reachable until its native receipt lock releases.
				if got := waitUpgradeEndpoint(t, dag, ""); got.Token != old.Token {
					t.Fatal("old worker changed during handoff")
				}
				unix.Flock(int(fork.Fd()), unix.LOCK_UN)
			}
			select {
			case err := <-ready:
				if err != nil {
					t.Fatal(err, diagnostics.String())
				}
			case <-time.After(10 * time.Second):
				t.Fatal("guard failed to drain")
			}
			if err := worker.Wait(); err != nil {
				t.Fatal("old worker did not stop cleanly", err)
			}
			// A failed activation leaves current untouched. The staged helper must
			// restart that old executable, even though it lacks the new helper command.
			expected := old.Version
			if mode == "activate" {
				next := filepath.Join(install, "next")
				if err := os.Symlink(filepath.Dir(filepath.Dir(candidate)), next); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(next, filepath.Join(install, "current")); err != nil {
					t.Fatal(err)
				}
				expected = "99.1.0"
			}
			if mode == "cancel" {
				if err := guard.Process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
				if err := guard.Wait(); err == nil {
					t.Fatal("cancelled guard reported success")
				}
			} else {
				input.Close()
				if err := guard.Wait(); err != nil {
					t.Fatal(err, diagnostics.String())
				}
			}
			next := waitUpgradeEndpoint(t, dag, old.Token)
			if next.Version != expected {
				t.Fatal("wrong version resumed", next.Version, expected)
			}
			stop := exec.Command(candidate, "local-dag", "stop", "--root", dag)
			stop.Env = env
			if out, err := stop.CombinedOutput(); err != nil {
				t.Fatal(err, string(out))
			}
			// Wait for the detached worker's lifetime lock before reopening its DB.
			lock, err := os.OpenFile(filepath.Join(dag, "managed-daemon.lock"), os.O_RDWR, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
				t.Fatal(err)
			}
			defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
			restored, err := graphstore.Open(dag)
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			after, err := restored.State("project")
			if err != nil {
				t.Fatal(err)
			}
			pending, err := restored.Tasks("project")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(tasks, pending) {
				t.Fatal("upgrade altered accepted graph or paused queue", pending)
			}
			t.Logf("%s -> %s, protocol %d: graph, paused queue and restart verified", old.Version, next.Version, old.Protocol)
		})
	}
}

func buildVersionedRuntime(t *testing.T, path, version string) {
	t.Helper()
	mustMkdirUpgrade(t, filepath.Dir(path))
	cmd := exec.Command("go", "build", "-ldflags", "-X main.version="+version, "-o", path, "./apps/runtime")
	cmd.Dir = projectRoot(t)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
}
func mustMkdirUpgrade(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
}
func writeUpgradeJSON(t *testing.T, path string, value any, mode os.FileMode) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

type upgradeEndpoint struct {
	URL      string `json:"url"`
	Token    string `json:"token"`
	Version  string `json:"version"`
	Protocol int    `json:"update_protocol"`
}

func waitUpgradeEndpoint(t *testing.T, root, previous string) upgradeEndpoint {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(filepath.Join(root, "control.json"))
		var v upgradeEndpoint
		if err == nil && json.Unmarshal(data, &v) == nil && v.Token != "" && v.Token != previous {
			req, _ := http.NewRequest("GET", v.URL+"/status", nil)
			req.Header.Set("Authorization", "Bearer "+v.Token)
			client := http.Client{Timeout: time.Second}
			resp, err := client.Do(req)
			if err == nil {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if resp.StatusCode == 200 {
					return v
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	logs, _ := os.ReadFile(filepath.Join(root, "daemon.log"))
	t.Fatal("worker failed to start", string(logs))
	return upgradeEndpoint{}
}
