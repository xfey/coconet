package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/xfey/coconet/packages/distribution"
	"github.com/xfey/coconet/packages/subprocess"
)

// One client version is selected for the whole operation. npm has no install
// scripts: replacing its launcher cannot activate or interrupt the Runtime.
func runUpdate(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "usage: coconet update")
		return 2
	}
	paths, err := resolveRuntimePaths()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	release, err := acquireLocalFileLock(ctx, filepath.Join(paths.root, "update-command.lock"))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer release()
	npm, err := exec.LookPath("npm")
	if err != nil {
		fmt.Fprintln(stderr, "coconet update requires Node.js and npm. Install them, then retry.")
		return 1
	}
	node, err := exec.LookPath("node")
	if err != nil {
		fmt.Fprintln(stderr, "coconet update requires Node.js on PATH.")
		return 1
	}
	prefix, packageRoot, err := resolveUpdateNPM(ctx, npm, os.Getenv("COCONET_NPM_PACKAGE_ROOT"))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	latest, err := latestClientVersion(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "Could not check the latest Coconet client; retry when the npm registry is reachable:", err)
		return 1
	}
	order, known := distribution.CompareVersions(version, latest)
	if !known {
		fmt.Fprintln(stderr, "This development Runtime has no release version. Install a published coconet package to use coconet update.")
		return 1
	}
	if order > 0 {
		fmt.Fprintf(stdout, "Coconet %s is newer than the latest stable client (%s); no downgrade performed.\n", version, latest)
		return 0
	}
	defaults, err := defaultUninstallPaths()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	installRoot := defaults.installRoot
	if own, e := os.Executable(); e == nil && filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(own)))) == "versions" {
		installRoot = filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(own))))
	}
	fmt.Fprintf(stdout, "Updating Coconet to %s...\n", latest)
	command := exec.Command(npm, "install", "-g", "--ignore-scripts", "--prefix", prefix, "coconet@"+latest)
	command.Stdout, command.Stderr = stderr, stderr
	if err := subprocess.Run(ctx, command); err != nil {
		fmt.Fprintln(stderr, "Could not update the npm package. Use a writable npm prefix; no sudo is required by Coconet. The Runtime is unchanged.")
		return 1
	}
	data, err := os.ReadFile(filepath.Join(packageRoot, "package.json"))
	var metadata struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err != nil || json.Unmarshal(data, &metadata) != nil || metadata.Name != "coconet" || metadata.Version != latest {
		fmt.Fprintln(stderr, "The npm package does not match the selected release; Runtime installation was not started.")
		return 1
	}
	fmt.Fprintln(stdout, "Preparing the Runtime; activation waits for ongoing processing and handoffs.")
	command = exec.Command(node, filepath.Join(packageRoot, "cli.mjs"), "--root", installRoot, "--if-newer")
	command.Stdout, command.Stderr = stdout, stderr
	if err := subprocess.Run(ctx, command); err != nil {
		fmt.Fprintln(stderr, "npm was updated, but Runtime activation did not finish. The next coconet command will retry; the existing Runtime remains usable.")
		return 1
	}
	return 0
}

func resolveUpdateNPM(ctx context.Context, npm, launcherRoot string) (string, string, error) {
	root := ""
	if launcherRoot != "" {
		var err error
		root, err = filepath.EvalSymlinks(launcherRoot)
		if err != nil {
			return "", "", err
		}
		if filepath.Base(root) != "coconet" || filepath.Base(filepath.Dir(root)) != "node_modules" || filepath.Base(filepath.Dir(filepath.Dir(root))) != "lib" {
			return "", "", errors.New("This launcher is not a global npm installation. Run npm install -g coconet, then use its coconet update command.")
		}
		return filepath.Dir(filepath.Dir(filepath.Dir(root))), root, nil
	}
	command := exec.CommandContext(ctx, npm, "root", "-g")
	var output bytes.Buffer
	command.Stdout = &output
	if err := command.Run(); err != nil {
		return "", "", fmt.Errorf("could not find the npm global installation: %w", err)
	}
	root = strings.TrimSpace(output.String())
	if !filepath.IsAbs(root) || filepath.Base(root) != "node_modules" || filepath.Base(filepath.Dir(root)) != "lib" {
		return "", "", errors.New("could not identify the npm global prefix; use npm install -g coconet with a writable prefix")
	}
	return filepath.Dir(filepath.Dir(root)), filepath.Join(root, "coconet"), nil
}

var latestClientVersion = fetchLatestClientVersion

func fetchLatestClientVersion(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", "https://registry.npmjs.org/coconet/latest", nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return "", fmt.Errorf("npm registry returned HTTP %d", response.StatusCode)
	}
	var metadata struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if json.Unmarshal(data, &metadata) != nil || metadata.Name != "coconet" {
		return "", errors.New("invalid npm client metadata")
	}
	if _, ok := distribution.CompareVersions(metadata.Version, metadata.Version); !ok || strings.ContainsAny(metadata.Version, "-+") {
		return "", errors.New("npm latest is not a stable release")
	}
	return metadata.Version, nil
}
