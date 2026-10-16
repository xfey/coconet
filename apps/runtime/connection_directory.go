package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"

	"github.com/xfey/coconet/packages/contracts"
)

type connectionOptions struct {
	path, project, server          string
	newProject, noBrowser, verbose bool
}

func parseConnectionOptions(args []string) (connectionOptions, error) {
	var options connectionOptions
	var positional string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		positional, args = args[0], args[1:]
	}
	flags := flag.NewFlagSet("connect", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&options.path, "path", "", "Folder to connect (defaults to the working directory)")
	flags.StringVar(&options.project, "project", "", "Project ID to connect")
	flags.StringVar(&options.server, "server", "", "Coconet server URL")
	flags.BoolVar(&options.newProject, "new", false, "Create and connect a project without browser selection")
	flags.BoolVar(&options.noBrowser, "no-browser", false, "Print the connection link without opening a browser")
	flags.BoolVar(&options.verbose, "verbose", false, "Show connection details")
	if err := flags.Parse(args); err != nil {
		return options, err
	}
	if flags.NArg() == 1 && positional == "" {
		positional = flags.Arg(0)
	} else if flags.NArg() != 0 {
		return options, errors.New("unexpected connection arguments")
	}
	if positional != "" {
		if options.project != "" && options.project != positional {
			return options, errors.New("conflicting Project IDs")
		}
		options.project = positional
	}
	if options.newProject && options.project != "" || strings.TrimSpace(options.server) != options.server {
		return options, errors.New("choose one project connection method")
	}
	return options, nil
}

func remoteTerminal() bool { return os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != "" }

func connectionDirectory(paths runtimePaths, selected string, noBrowser bool) (contracts.ConnectionDirectory, repository, *contracts.LocalBinding, error) {
	var directory contracts.ConnectionDirectory
	if selected == "" {
		selected = currentDirectory()
	} else if !filepath.IsAbs(selected) {
		selected = filepath.Join(currentDirectory(), selected)
	}
	cwd, err := canonicalPath(selected)
	if err != nil {
		return directory, repository{}, nil, err
	}
	info, err := os.Stat(cwd)
	if err != nil || !info.IsDir() {
		return directory, repository{}, nil, fmt.Errorf("choose an existing folder: %s", selected)
	}
	root, binding, err := collaborationRootAt(paths, cwd)
	if err != nil {
		return directory, repository{}, nil, err
	}
	for _, value := range []string{cwd, root.root} {
		if len(value) > 4096 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return directory, repository{}, nil, errors.New("folder path is too long or contains control characters")
		}
	}
	directory = contracts.ConnectionDirectory{CWD: cwd, Path: root.root, Remote: remoteTerminal(), CanBrowse: !noBrowser && !remoteTerminal() && folderChooserProgram() != "", Revision: 1}
	return directory, root, binding, nil
}

func folderChooserProgram() string {
	if runtime.GOOS == "darwin" {
		if program, err := exec.LookPath("osascript"); err == nil {
			return program
		}
		return ""
	}
	if runtime.GOOS == "linux" && (os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != "") {
		for _, name := range []string{"zenity", "kdialog"} {
			if program, err := exec.LookPath(name); err == nil {
				return program
			}
		}
	}
	return ""
}

var errFolderSelectionCancelled = errors.New("folder selection cancelled")

func chooseConnectionFolder(ctx context.Context, current string) (string, error) {
	program := folderChooserProgram()
	if program == "" {
		return "", errors.New("folder chooser unavailable")
	}
	var args []string
	switch filepath.Base(program) {
	case "osascript":
		// The path is an argument, never interpolated into executable script text.
		args = []string{"-e", "on run argv\nreturn POSIX path of (choose folder with prompt \"Choose a folder for Coconet\" default location (POSIX file (item 1 of argv)))\nend run", current}
	case "zenity":
		args = []string{"--file-selection", "--directory", "--title=Coconet", "--filename=" + current + string(filepath.Separator)}
	case "kdialog":
		args = []string{"--getexistingdirectory", current, "--title", "Coconet"}
	}
	output, err := exec.CommandContext(ctx, program, args...).Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && (filepath.Base(program) == "osascript" && strings.Contains(string(exit.Stderr), "(-128)") || filepath.Base(program) != "osascript" && exit.ExitCode() == 1) {
			return "", errFolderSelectionCancelled
		}
		return "", err
	}
	return strings.TrimSuffix(string(output), "\n"), nil
}
