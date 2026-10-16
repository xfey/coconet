package inference

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xfey/coconet/packages/subprocess"
)

// Login operates solely in the chosen inference profile and empty workdir.
// It never starts a conversation or sends a model request.
func (r Local) Login(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error {
	if r.Config.Command == "" || !filepath.IsAbs(r.Config.ConfigRoot) || !filepath.IsAbs(r.Config.WorkRoot) {
		return errors.New("login requires dedicated inference directories")
	}
	for _, root := range []string{r.Config.ConfigRoot, r.Config.WorkRoot} {
		if err := os.MkdirAll(root, 0700); err != nil {
			return err
		}
	}
	work, err := os.MkdirTemp(r.Config.WorkRoot, "login-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	args := []string{"login"}
	if r.Config.Provider == "claude" && !strings.HasPrefix(filepath.Base(r.Config.Command), "tclaude") {
		args = []string{"auth", "login"}
	}
	if r.Config.Provider != "claude" && r.Config.Provider != "codex" {
		return errors.New("unsupported provider")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	command := r.command(work, args...)
	command.Stdin, command.Stdout, command.Stderr = stdin, stdout, stderr
	return subprocess.Run(ctx, command)
}
