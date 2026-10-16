package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLocalDAGLaunchCooldownSurvivesConcurrentRequestsAndConfigurationChanges(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	var group sync.WaitGroup
	results := make(chan bool, 8)
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			release, err := acquireLocalFileLock(ctx, filepath.Join(root, "launch.lock"))
			if err != nil {
				failures <- err
				return
			}
			defer release()
			allowed, err := reserveLocalDAGLaunch(root, "one", now)
			if err != nil {
				failures <- err
				return
			}
			results <- allowed
		}()
	}
	group.Wait()
	close(failures)
	close(results)
	for err := range failures {
		t.Error(err)
	}
	allowed := 0
	for result := range results {
		if result {
			allowed++
		}
	}
	if allowed != 1 {
		t.Fatalf("%d simultaneous launches reserved", allowed)
	}
	for _, test := range []struct {
		configuration string
		at            time.Time
		want          bool
	}{
		{"one", now.Add(time.Second), false},
		{"one", now.Add(5 * time.Second), true},
		{"two", now.Add(6 * time.Second), true},
		{"two", now.Add(-time.Hour), true},
	} {
		got, err := reserveLocalDAGLaunch(root, test.configuration, test.at)
		if err != nil || got != test.want {
			t.Fatalf("reservation: %v %v", got, err)
		}
	}
}

func TestManagedDaemonDuplicateExitsWithoutOpeningDatabaseOrLog(t *testing.T) {
	root := t.TempDir()
	release, err := acquireLocalFileLock(context.Background(), filepath.Join(root, "managed-daemon.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	var diagnostics bytes.Buffer
	if code := runManagedLocalDAG(context.Background(), []string{"--root", root}, &diagnostics, &diagnostics); code != 0 || diagnostics.Len() != 0 {
		t.Fatalf("duplicate daemon entered startup: %d %s", code, diagnostics.String())
	}
	for _, file := range []string{"graph.db", "daemon.log", "control.json"} {
		if _, err := os.Stat(filepath.Join(root, file)); !os.IsNotExist(err) {
			t.Fatal("duplicate mutated owned state", file)
		}
	}
}

func TestLocalDAGLogBoundsExistingAndStreamingOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	if err := os.WriteFile(path, []byte("0123456789abcdefghij"), 0644); err != nil {
		t.Fatal(err)
	}
	log, err := openLocalDAGLog(path, 8)
	if err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(path)
	if string(content) != "cdefghij" {
		t.Fatal("old log tail was not retained", string(content))
	}
	if _, err := io.WriteString(log, "KLMNOPQRSTUVWXYZ"); err != nil {
		t.Fatal(err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	previous, _ := os.ReadFile(path + ".1")
	current, _ := os.ReadFile(path)
	if string(previous)+string(current) != "KLMNOPQRSTUVWXYZ" {
		t.Fatal("rotation lost the wrong tail")
	}
	for _, p := range []string{path, path + ".1"} {
		info, err := os.Stat(p)
		if err != nil || info.Size() > 8 || info.Mode().Perm() != 0600 {
			t.Fatal("log bound or privacy lost", err)
		}
	}
	log, err = openLocalDAGLog(path, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	if _, err := io.WriteString(log, "1"); err != nil {
		t.Fatal(err)
	}
	previous, _ = os.ReadFile(path + ".1")
	current, _ = os.ReadFile(path)
	if string(previous)+string(current) != "STUVWXYZ1" {
		t.Fatal("reopen lost rotation boundary")
	}
}

func TestLocalDAGLogRejectsSymlinkAndMCPResponseIsBounded(t *testing.T) {
	root := t.TempDir()
	original, log := filepath.Join(root, "original"), filepath.Join(root, "daemon.log")
	if err := os.WriteFile(original, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(original, log); err != nil {
		t.Fatal(err)
	}
	if _, err := openLocalDAGLog(log, 4); err == nil {
		t.Fatal("log symlink accepted")
	}
	content, _ := os.ReadFile(original)
	if string(content) != "preserve" {
		t.Fatal("symlink target changed")
	}
	var bounded boundedLocalResponse
	_, err := io.Copy(&bounded, strings.NewReader(strings.Repeat("x", 16<<20+1)))
	if err == nil || len(bounded.Bytes()) > 16<<20 {
		t.Fatal("stream bypassed response bound")
	}
}
