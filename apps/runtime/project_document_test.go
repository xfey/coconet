package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/replication"
)

type documentRemote struct {
	doc contracts.ProjectDocument
	err error
}

func (r documentRemote) ProjectDocument(context.Context, string) (contracts.ProjectDocument, error) {
	return r.doc, r.err
}

func TestProjectDocumentOfflineCacheAndNotificationScope(t *testing.T) {
	root := t.TempDir()
	remote := documentRemote{doc: contracts.ProjectDocument{SchemaVersion: 1, ProjectID: "project", Version: 1, Markdown: "# Team priorities"}}
	if err := syncProjectDocument(context.Background(), root, "project", remote); err != nil {
		t.Fatal(err)
	}
	var hints []string
	emit := func(text string) error { hints = append(hints, text); return nil }
	writeProjectDocumentHint(root, "project", "claude:session-one", false, emit)
	writeProjectDocumentHint(root, "project", "claude:session-one", false, emit)
	writeProjectDocumentHint(root, "project", "codex:session-one", false, emit)
	if len(hints) != 2 || !strings.Contains(hints[0], "read_project_notes") || strings.Contains(hints[0], "Team priorities") {
		t.Fatal(hints)
	}
	writeProjectDocumentHint(t.TempDir(), "project", "claude:session-one", false, emit)
	writeProjectDocumentHint(root, "other-project", "claude:session-one", false, emit)
	if len(hints) != 2 {
		t.Fatal("scope leaked")
	}
	remote.doc.Version = 2
	remote.doc.Markdown = "Updated priorities"
	if err := syncProjectDocument(context.Background(), root, "project", remote); err != nil {
		t.Fatal(err)
	}
	writeProjectDocumentHint(root, "project", "claude:session-one", false, func(string) error { return io.ErrClosedPipe })
	writeProjectDocumentHint(root, "project", "claude:session-one", false, emit)
	writeProjectDocumentHint(root, "project", "claude:session-one", true, emit)
	if len(hints) != 4 || !strings.Contains(hints[2], "v2") {
		t.Fatal(hints)
	}
	remote.err = errors.New("offline")
	if err := syncProjectDocument(context.Background(), root, "project", remote); err == nil {
		t.Fatal("offline not reported")
	}
	read, err := readProjectDocument(root, "project")
	if err != nil || read["document"].(contracts.ProjectDocument).Markdown != remote.doc.Markdown {
		t.Fatal(read, err)
	}
	remote.err = &replication.RequestError{Status: 403, Code: "forbidden"}
	_ = syncProjectDocument(context.Background(), root, "project", remote)
	read, err = readProjectDocument(root, "project")
	if err != nil || read["status"] != "sync_pending" {
		t.Fatal("revocation retained content", read, err)
	}
}

func TestProjectDocumentHookSingleEnvelope(t *testing.T) {
	root := t.TempDir()
	workspace, err := canonicalPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("COCONET_LOCAL_DAG_HOME", root)
	setRuntimeTestHome(t, t.TempDir())
	config := localDAGServiceConfig{Schema: 1, Actor: "actor", Provider: "codex", Command: "unused-test-command", Profile: workspace, Model: "gpt-5.6-sol", MaxCalls: 1, TimeoutSeconds: 120, Workspaces: []localDAGWorkspace{{Path: workspace, ProjectID: "project"}}}
	if err := atomicWriteJSON(filepath.Join(root, "runtime.json"), config, 0600); err != nil {
		t.Fatal(err)
	}
	// Use the same service configuration filename as the managed loader.
	if _, err := loadLocalDAGService(root); err != nil {
		t.Fatal(err)
	}
	remote := documentRemote{doc: contracts.ProjectDocument{SchemaVersion: 1, ProjectID: "project", Version: 1, Markdown: "Shared note"}}
	if err := syncProjectDocument(context.Background(), root, "project", remote); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []contracts.AgentType{contracts.AgentCodex, contracts.AgentClaude, contracts.AgentCursor} {
		event := hookEvent{HookEventName: "SessionStart", SessionID: "session", CWD: workspace}
		var out bytes.Buffer
		writeManagedHookContext(agent, "", event, &out)
		var value map[string]any
		decoder := json.NewDecoder(&out)
		if err := decoder.Decode(&value); err != nil {
			t.Fatal(err)
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			t.Fatal("multiple hook envelopes", err)
		}
		data, _ := json.Marshal(value)
		if !bytes.Contains(data, []byte("read_project_notes")) || !bytes.Contains(data, []byte("read_project_work_graph")) {
			t.Fatal(string(data))
		}
		event.HookEventName = "UserPromptSubmit"
		writeManagedHookContext(agent, "", event, &out)
		if out.Len() != 0 {
			t.Fatal("unchanged next turn repeated context", out.String())
		}
	}
	if info, err := os.Stat(projectDocumentPath(root, "project")); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("cache permissions", info, err)
	}
}
