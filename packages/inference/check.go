package inference

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xfey/coconet/packages/subprocess"
)

// Check verifies the installed protocol without submitting any model request.
// Login/model access is checked on actual bounded attempts, not claimed here.
func (r Local) Check(ctx context.Context) error {
	if r.Config.Command == "" || !filepath.IsAbs(r.Config.ConfigRoot) || !filepath.IsAbs(r.Config.WorkRoot) {
		return errors.New("provider check requires an executable and absolute inference directories")
	}
	if err := os.MkdirAll(r.Config.WorkRoot, 0700); err != nil {
		return err
	}
	work, err := os.MkdirTemp(r.Config.WorkRoot, "check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	switch r.Config.Provider {
	case "codex":
		if err := r.codexCapabilities(ctx, work); err != nil {
			return err
		}
		p, err := r.startCodexRPC(ctx, work)
		if err != nil {
			return err
		}
		p.close()
		return nil
	case "cursor":
		output := &boundedBuffer{limit: 128 << 10}
		// Hidden options are checked by parsing them alongside --help; no model
		// request or workspace trust is involved in this capability check.
		cmd := r.command(work, "--allowed-tools", "", "--disable-project-configs", "--data-dir", filepath.Join(work, "data"), "--help")
		cmd.Stdout, cmd.Stderr = output, &boundedBuffer{limit: 32 << 10}
		if err := subprocess.Run(ctx, cmd); err != nil || output.overflow {
			return errors.New("installed Cursor CLI lacks required inference options; update it before retrying")
		}
		if !strings.Contains(output.String(), "--output-format") {
			return errors.New("Cursor CLI does not advertise JSON output")
		}
		return nil
	case "claude":
		help := func(args ...string) (string, error) {
			output := &boundedBuffer{limit: 128 << 10}
			cmd := r.command(work, args...)
			cmd.Stdout, cmd.Stderr = output, &boundedBuffer{limit: 32 << 10}
			if err := subprocess.Run(ctx, cmd); err != nil {
				return "", errors.New("Claude capability check failed; check the executable and login")
			}
			if output.overflow {
				return "", errors.New("Claude help exceeds capability check limit")
			}
			return output.String(), nil
		}
		output, err := help("--help")
		if err != nil {
			return err
		}
		// TClaude owns --help itself and documents this exact forwarding form.
		// Do not try it on native Claude: '-- -h' could become a user prompt.
		if filepath.Base(r.Config.Command) == "tclaude" && strings.Contains(output, "tclaude wrapper for Claude Code.") && strings.Contains(output, "tclaude -- -h") {
			output, err = help("--", "-h")
			if err != nil {
				return err
			}
		}
		for _, option := range []string{"--json-schema", "--tools", "--strict-mcp-config", "--setting-sources", "--no-session-persistence", "--disable-slash-commands", "--permission-mode"} {
			if !strings.Contains(output, option) {
				return errors.New("installed Claude does not support required inference option " + option)
			}
		}
		return nil
	default:
		return errors.New("provider must be codex, claude or cursor")
	}
}
