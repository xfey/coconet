package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"net/http/httptest"
)

func TestConnectPathUsesSelectedFolderWithoutChangingCWD(t *testing.T) {
	for _, invite := range []bool{false, true} {
		t.Run(map[bool]string{false: "create", true: "invitation"}[invite], func(t *testing.T) {
			remote := newFakeCoconetServer()
			server := httptest.NewServer(remote)
			t.Cleanup(server.Close)
			remote.advertisedEndpoint = server.URL
			cwd, selected := t.TempDir(), filepath.Join(t.TempDir(), "工作 project")
			if err := os.Mkdir(selected, 0o700); err != nil {
				t.Fatal(err)
			}
			configureRuntimeTest(t, t.TempDir(), cwd, server.URL)
			before, _ := os.Getwd()
			args := []string{"--path", selected, "--new", "--verbose"}
			if invite {
				args = []string{remote.project.ProjectID, "--path", selected, "--verbose", "--no-browser"}
			}
			var output, stderr bytes.Buffer
			if code := runConnect(context.Background(), args, strings.NewReader(""), &output, &stderr); code != 0 {
				t.Fatal(code, stderr.String())
			}
			paths, _ := resolveRuntimePaths()
			binding, err := bindingAtRoot(paths, selected)
			if err != nil || binding == nil || binding.ProjectID != remote.project.ProjectID {
				t.Fatal("selected folder not connected", err)
			}
			if other, _ := bindingAtRoot(paths, cwd); other != nil {
				t.Fatal("original cwd was connected instead")
			}
			after, _ := os.Getwd()
			if after != before || currentDirectory() != cwd {
				t.Fatal("connection changed process working directory")
			}
			if !strings.Contains(output.String(), selected) || strings.Contains(output.String(), "not connected") {
				t.Fatal("status read the original cwd", output.String())
			}
		})
	}
}

func TestConnectionDirectoryResolvesGitRootRelativePathsAndSSH(t *testing.T) {
	root := initTestRepository(t, "path-root")
	root, _ = canonicalPath(root)
	child := filepath.Join(root, "nested")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(child, link); err != nil {
		t.Fatal(err)
	}
	configureRuntimeTest(t, t.TempDir(), root, "http://127.0.0.1:1")
	t.Setenv("SSH_CONNECTION", "fixture remote terminal")
	paths, _ := resolveRuntimePaths()
	for _, path := range []string{child, "nested", link} {
		directory, _, _, err := connectionDirectory(paths, path, false)
		if err != nil || directory.CWD != child || directory.Path != root || !directory.Remote || directory.CanBrowse {
			t.Fatalf("wrong directory: %#v %v", directory, err)
		}
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{file, filepath.Join(root, "missing")} {
		if _, _, _, err := connectionDirectory(paths, path, false); err == nil {
			t.Fatal("non-directory accepted")
		}
	}
}

func TestConnectOptionsAndBindingConflict(t *testing.T) {
	for _, args := range [][]string{{"--project", "p", "--new"}, {"code", "--project", "p"}, {"--path"}, {"--path", "p", "a", "b"}} {
		if _, err := parseConnectionOptions(args); err == nil {
			t.Fatalf("invalid options accepted: %v", args)
		}
	}
	remote := newFakeCoconetServer()
	server := httptest.NewServer(remote)
	t.Cleanup(server.Close)
	_, paths := configureBoundRepository(t, remote, server.URL)
	before, _ := loadBindingState(paths)
	var output, stderr bytes.Buffer
	if code := runConnect(context.Background(), []string{"--project", "coco_1111111111111111"}, strings.NewReader(""), &output, &stderr); code != 1 || !strings.Contains(stderr.String(), "already connected") {
		t.Fatal("binding conflict accepted", code, stderr.String())
	}
	after, _ := loadBindingState(paths)
	if len(after.Bindings) != len(before.Bindings) || after.Bindings[0] != before.Bindings[0] {
		t.Fatal("conflict changed binding")
	}
}
