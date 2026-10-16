package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
)

func TestConnectValidatesExistingProjectBeforeQueueing(t *testing.T) {
	for _, scenario := range []string{"deleted", "unauthorized", "forbidden", "generic-404", "server-error", "offline", "wrong-deployment", "wrong-account", "archived", "archived-project"} {
		t.Run(scenario, func(t *testing.T) {
			remote := newFakeCoconetServer()
			archived := strings.HasPrefix(scenario, "archived")
			if scenario == "archived" {
				remote.accessStatus = contracts.StatusProjectArchived
			}
			if scenario == "archived-project" {
				remote.project.Status = contracts.ProjectArchived
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/access") && !archived {
					status, code := 404, "project_not_found"
					switch scenario {
					case "unauthorized":
						status, code = 401, "invalid_token"
					case "forbidden":
						_ = json.NewEncoder(w).Encode(contracts.ProjectAccess{SchemaVersion: 1, Status: contracts.StatusForbidden})
						return
					case "generic-404":
						code = "not_found"
					case "server-error":
						status = 503
					}
					w.WriteHeader(status)
					_ = json.NewEncoder(w).Encode(fixtureError(code, contracts.StatusNotFound))
					return
				}
				remote.ServeHTTP(w, r)
			}))
			t.Cleanup(server.Close)
			_, paths := configureBoundRepository(t, remote, server.URL)
			if scenario == "offline" {
				server.Close()
			}
			if scenario == "wrong-deployment" || scenario == "wrong-account" {
				state, err := loadBindingState(paths)
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "wrong-deployment" {
					state.Bindings[0].ServerPublicKeySHA256 = strings.Repeat("0", 64)
				} else {
					state.Bindings[0].AccountID = "acct_someone_else"
				}
				if err := atomicWriteJSON(paths.bindingPath, state, 0600); err != nil {
					t.Fatal(err)
				}
			}
			target := fixtureDeploymentTarget(server.URL)
			queue := deploymentSyncPath(paths, target.DeploymentID)
			beforeBinding := mustReadRuntimeFile(t, paths.bindingPath)
			beforeCredential := mustReadRuntimeFile(t, paths.credentialPath)
			// A pre-existing pending upload must never be reassigned or altered.
			beforeQueue := seedRetiredQueue(t, queue)
			for _, statusOnly := range []bool{false, true} {
				var out, stderr bytes.Buffer
				var code int
				if statusOnly {
					code = runProjectStatus(context.Background(), nil, &out, &stderr)
				} else {
					code = runConnect(context.Background(), nil, strings.NewReader(""), &out, &stderr)
				}
				if (code == 0) != archived || (strings.Contains(stderr.String(), "project has been deleted") != (scenario == "deleted")) {
					t.Fatalf("wrong connection result: %d %s %s", code, out.String(), stderr.String())
				}
				if !bytes.Equal(beforeBinding, mustReadRuntimeFile(t, paths.bindingPath)) || !bytes.Equal(beforeCredential, mustReadRuntimeFile(t, paths.credentialPath)) {
					t.Fatal("status/connect changed account or binding")
				}
				if !bytes.Equal(beforeQueue, mustReadRuntimeFile(t, queue)) {
					t.Fatal("retired upload state changed")
				}
			}
		})
	}
}

func TestDeletedProjectCanReconnectWithoutChangingAccountOrOldQueue(t *testing.T) {
	remote := newFakeCoconetServer()
	oldProject := remote.project.ProjectID
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/projects/"+oldProject+"/access" {
			w.WriteHeader(404)
			_ = json.NewEncoder(w).Encode(fixtureError("project_not_found", contracts.StatusNotFound))
			return
		}
		remote.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	root, paths := configureBoundRepository(t, remote, server.URL)
	target := fixtureDeploymentTarget(server.URL)
	beforeCredential := mustReadRuntimeFile(t, paths.credentialPath)
	history := filepath.Join(root, "synthetic-session.jsonl")
	if err := os.WriteFile(history, []byte("synthetic user history\n"), 0600); err != nil {
		t.Fatal(err)
	}
	beforeHistory := mustReadRuntimeFile(t, history)
	queue := deploymentSyncPath(paths, target.DeploymentID)
	beforeQueue := seedRetiredQueue(t, queue)
	var out, stderr bytes.Buffer
	if code := runConnect(context.Background(), nil, strings.NewReader(""), &out, &stderr); code != 1 || !strings.Contains(stderr.String(), "coconet disconnect") {
		t.Fatal("missing recovery instructions", code, stderr.String())
	}
	if code := runDisconnect(context.Background(), nil, &out, &stderr); code != 0 {
		t.Fatal("cannot detach deleted project", stderr.String())
	}
	remote.mu.Lock()
	remote.project.ProjectID = "coco_replacement01"
	remote.mu.Unlock()
	if code := runConnect(context.Background(), []string{"--new"}, strings.NewReader(""), &out, &stderr); code != 0 {
		t.Fatal("cannot choose replacement project", stderr.String())
	}
	binding, err := bindingAtRoot(paths, root)
	if err != nil || binding == nil || binding.ProjectID != "coco_replacement01" || binding.AccountID != remote.account.AccountID {
		t.Fatal("replacement changed account", binding, err)
	}
	if !bytes.Equal(beforeCredential, mustReadRuntimeFile(t, paths.credentialPath)) || !bytes.Equal(beforeHistory, mustReadRuntimeFile(t, history)) {
		t.Fatal("reconnection changed credentials or native history")
	}
	if !bytes.Equal(beforeQueue, mustReadRuntimeFile(t, queue)) {
		t.Fatal("retired upload state changed")
	}
	if runSync(context.Background(), nil) != 2 {
		t.Fatal("retired worker accepted work")
	}
}

func mustReadRuntimeFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func seedRetiredQueue(t *testing.T, path string) []byte {
	t.Helper()
	data := []byte("retired upload database: preserve verbatim")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return data
}
