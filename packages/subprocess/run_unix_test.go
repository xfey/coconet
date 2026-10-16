//go:build unix

package subprocess

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunBoundsInheritedPipesAndStopsDescendant(t *testing.T) {
	for _, parentExits := range []bool{false, true} {
		t.Run(strconv.FormatBool(parentExits), func(t *testing.T) {
			pidfile := filepath.Join(t.TempDir(), "child.pid")
			script := `sleep 30 & printf '%s' "$!" > "$1"; wait`
			if parentExits {
				script = `sleep 30 & printf '%s' "$!" > "$1"; exit 0`
			}
			command := exec.Command("sh", "-c", script, "fixture", pidfile)
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			command.WaitDelay = 50 * time.Millisecond
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- Run(ctx, command) }()
			child := 0
			deadline := time.Now().Add(2 * time.Second)
			for child == 0 && time.Now().Before(deadline) {
				content, _ := os.ReadFile(pidfile)
				child, _ = strconv.Atoi(string(content))
				if child == 0 {
					time.Sleep(5 * time.Millisecond)
				}
			}
			if child == 0 {
				cancel()
				t.Fatal("fixture did not start its child")
			}
			t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL) })
			if !parentExits {
				cancel()
			}
			select {
			case err := <-done:
				expected := error(context.Canceled)
				if parentExits {
					expected = exec.ErrWaitDelay
				}
				if !errors.Is(err, expected) {
					t.Fatalf("unexpected result: %v; want %v", err, expected)
				}
			case <-time.After(2 * time.Second):
				cancel()
				_ = syscall.Kill(child, syscall.SIGKILL)
				t.Fatal("descendant kept Run blocked")
			}
			// Linux PID 1 may retain a killed orphan as a zombie briefly. It must
			// not remain a live process capable of modifying host configuration.
			deadline = time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				if errors.Is(syscall.Kill(child, 0), syscall.ESRCH) {
					return
				}
				state, _ := exec.Command("ps", "-p", strconv.Itoa(child), "-o", "stat=").Output()
				if strings.HasPrefix(strings.TrimSpace(string(state)), "Z") {
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatal("the command returned but its descendant is still running")
		})
	}
}

func TestRunPreservesOutputAndExitStatus(t *testing.T) {
	var stdout, stderr bytes.Buffer
	command := exec.Command("sh", "-c", "printf result; printf diagnostic >&2; exit 7")
	command.Stdout, command.Stderr = &stdout, &stderr
	err := Run(context.Background(), command)
	var exited *exec.ExitError
	if !errors.As(err, &exited) || exited.ExitCode() != 7 || stdout.String() != "result" || stderr.String() != "diagnostic" {
		t.Fatalf("output or status changed: stdout=%q stderr=%q err=%v", stdout.String(), stderr.String(), err)
	}
}

func TestRunCancelledBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	command := exec.Command("sh", "-c", "exit 0")
	if err := Run(ctx, command); !errors.Is(err, context.Canceled) || command.Process != nil {
		t.Fatalf("cancelled command started: %v", err)
	}
}

func TestRunAllowsNestedCommandCleanup(t *testing.T) {
	if root := os.Getenv("COCONET_SUBPROCESS_NESTED_FIXTURE"); root != "" {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM)
		defer cancel()
		command := exec.Command("sh", "-c", `trap '' TERM; printf '%s' "$$" > "$1"; exec sleep 30`, "fixture", filepath.Join(root, "child.pid"))
		command.WaitDelay = 50 * time.Millisecond
		if err := Run(ctx, command); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "cleaned"), []byte("ok"), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	root := t.TempDir()
	command := exec.Command(os.Args[0], "-test.run=^TestRunAllowsNestedCommandCleanup$")
	command.Env = append(os.Environ(), "COCONET_SUBPROCESS_NESTED_FIXTURE="+root)
	command.WaitDelay = 500 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, command) }()
	child := 0
	deadline := time.Now().Add(2 * time.Second)
	for child == 0 && time.Now().Before(deadline) {
		content, _ := os.ReadFile(filepath.Join(root, "child.pid"))
		child, _ = strconv.Atoi(string(content))
		if child == 0 {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if child == 0 {
		t.Fatal("nested fixture did not start")
	}
	t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL) })
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("nested fixture did not stop")
	}
	if content, err := os.ReadFile(filepath.Join(root, "cleaned")); err != nil || string(content) != "ok" {
		t.Fatal("outer cancellation prevented nested cleanup")
	}
	if !errors.Is(syscall.Kill(child, 0), syscall.ESRCH) {
		t.Fatal("nested child is still present after cleanup")
	}
}
