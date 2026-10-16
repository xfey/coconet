// Package inference runs a fixed, tool-free inference request through a local
// Agent runtime. It does not read the project workspace or resume user threads.
package inference

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const MaxResponse = 4 << 20

type Request struct {
	System string          `json:"system"`
	Input  json.RawMessage `json:"input"`
	Schema json.RawMessage `json:"schema"`
}

// Visible contains the provider's visible final response envelope, including
// unsuccessful responses. Callers must archive it before interpreting Text.
type Response struct {
	Text    string          `json:"text"`
	Visible string          `json:"visible"`
	Usage   json.RawMessage `json:"usage,omitempty"`
	Model   string          `json:"model,omitempty"`
}

type Runner interface {
	Infer(context.Context, Request) (Response, error)
}

// Deferred means no provider call was made. Scheduling must wait without
// counting this as a failed model attempt.
type Deferred struct{ Until time.Time }

func (e *Deferred) Error() string {
	return "local inference budget exhausted; waiting for the next quota window"
}

// ConfigRoot is the source Agent profile: reuse its authentication and default
// model. Calls use a fresh work directory and non-persistent, tool-free contexts.
// Command/Args are explicit executable argv, never shell commands.
type Config struct {
	Provider   string        `json:"provider"`
	Command    string        `json:"command"`
	Args       []string      `json:"args,omitempty"`
	ConfigRoot string        `json:"config_root"`
	WorkRoot   string        `json:"work_root"`
	Model      string        `json:"model,omitempty"`
	Timeout    time.Duration `json:"timeout"` // zero waits for completion or caller cancellation
	Env        []string      `json:"-"`
}

type Local struct{ Config Config }

func (r Local) Configuration() any { return r.Config }

func (r Local) Infer(ctx context.Context, request Request) (Response, error) {
	c := r.Config
	if c.Command == "" || !filepath.IsAbs(c.ConfigRoot) || !filepath.IsAbs(c.WorkRoot) || c.ConfigRoot == c.WorkRoot {
		return Response{}, errors.New("inference requires an executable and separate absolute profile/work roots")
	}
	if c.Timeout < 0 || c.Timeout > 15*time.Minute {
		return Response{}, errors.New("inference timeout must be zero or within 15 minutes")
	}
	if request.System == "" || !json.Valid(request.Input) || !json.Valid(request.Schema) || len(request.System)+len(request.Input)+len(request.Schema) > 1<<20 {
		return Response{}, errors.New("invalid or oversized inference request")
	}
	for _, root := range []string{c.ConfigRoot, c.WorkRoot} {
		if err := os.MkdirAll(root, 0700); err != nil {
			return Response{}, err
		}
		info, err := os.Lstat(root)
		if err != nil {
			return Response{}, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return Response{}, errors.New("inference roots must be real directories")
		}
	}
	work, err := os.MkdirTemp(c.WorkRoot, "attempt-")
	if err != nil {
		return Response{}, err
	}
	defer os.RemoveAll(work) // Only the fresh attempt directory owned by this call.
	// Native runtimes hash their canonical cwd (not macOS /var aliases).
	work, err = filepath.EvalSymlinks(work)
	if err != nil {
		return Response{}, err
	}
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	switch c.Provider {
	case "codex":
		return r.codex(ctx, work, request)
	case "claude":
		return r.claude(ctx, work, request)
	case "cursor":
		return r.cursor(ctx, work, request)
	default:
		return Response{}, fmt.Errorf("unsupported inference provider %q", c.Provider)
	}
}

func (r Local) command(work string, args ...string) *exec.Cmd {
	cmd := exec.Command(r.Config.Command, append(append([]string{}, r.Config.Args...), args...)...)
	cmd.Dir = work
	// Keep user authentication/proxy variables, but replace all inherited capture
	// and profile settings. Inference never belongs to a collaboration workspace.
	env := append(append([]string{}, os.Environ()...), r.Config.Env...)
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "COCONET_") || key == "CODEX_HOME" || key == "CLAUDE_CONFIG_DIR" || key == "CLAUDE_CODE_SIMPLE" || key == "CLAUDE_CODE_SAFE_MODE" || key == "CURSOR_CONFIG_DIR" || key == "CURSOR_DATA_DIR" || key == "CURSOR_AGENT_PERSIST_SESSION" || key == "CURSOR_CONVERSATION_ID" || key == "CURSOR_AGENT" {
			continue
		}
		if r.Config.Provider == "claude" && (key == "MAX_THINKING_TOKENS" || key == "CLAUDE_CODE_DISABLE_THINKING" || key == "CLAUDE_CODE_DISABLE_ADAPTIVE_THINKING") {
			continue
		}
		filtered = append(filtered, entry)
	}
	cmd.Env = append(filtered, "COCONET_INTERNAL_INFERENCE=1", "CODEX_HOME="+r.Config.ConfigRoot, "CLAUDE_CONFIG_DIR="+r.Config.ConfigRoot, "CLAUDE_CODE_SIMPLE=1")
	if r.Config.Provider == "cursor" {
		cmd.Env = append(cmd.Env, "CURSOR_CONFIG_DIR="+r.Config.ConfigRoot, "CURSOR_DATA_DIR="+filepath.Join(work, "data"))
	}
	if r.Config.Provider == "claude" {
		cmd.Env = append(cmd.Env, "MAX_THINKING_TOKENS=0", "CLAUDE_CODE_DISABLE_THINKING=1")
	}
	return cmd
}
