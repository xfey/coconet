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

func TestAccountKeyResetRecoversLostResponse(t *testing.T) {
	remote := newFakeCoconetServer()
	current := "fixture-token-with-enough-entropy-1234"
	replacement := ""
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/account/key" {
			remote.ServeHTTP(w, r)
			return
		}
		requests++
		if r.Header.Get("Authorization") != "Bearer "+current {
			w.WriteHeader(401)
			_ = json.NewEncoder(w).Encode(contracts.ErrorEnvelope{SchemaVersion: 1, Code: "invalid_token"})
			return
		}
		var input contracts.ResetAccountKeyRequest
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			t.Error("invalid reset body")
			w.WriteHeader(400)
			return
		}
		if replacement == "" {
			replacement, current = input.NewKey, input.NewKey
			w.WriteHeader(503)
			_ = json.NewEncoder(w).Encode(contracts.ErrorEnvelope{SchemaVersion: 1, Code: "fixture_lost_response", Retryable: true})
			return
		}
		if input.NewKey != replacement {
			t.Error("retry generated a different replacement")
		}
		_ = json.NewEncoder(w).Encode(contracts.ResetAccountKeyResponse{SchemaVersion: 1, Status: contracts.StatusOK, AccountID: remote.account.AccountID})
	}))
	defer server.Close()
	_, paths := configureBoundRepository(t, remote, server.URL)
	var out, errOut bytes.Buffer
	if code := runAccount(context.Background(), []string{"key"}, &out, &errOut); code != 0 || strings.TrimSpace(out.String()) != current {
		t.Fatal("explicit key command failed")
	}
	out.Reset()
	errOut.Reset()
	if code := runAccount(context.Background(), []string{"reset-key"}, &out, &errOut); code != 1 {
		t.Fatal("lost reset response not retained as pending")
	}
	pending := filepath.Join(filepath.Dir(deploymentSyncPath(paths, fixtureDeploymentTarget(server.URL).DeploymentID)), "account-key-reset.json")
	if info, err := os.Stat(pending); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("replacement not safely persisted before request")
	}
	if strings.Contains(out.String()+errOut.String(), replacement) {
		t.Fatal("reset command exposed a credential")
	}
	out.Reset()
	errOut.Reset()
	if code := runAccount(context.Background(), []string{"key"}, &out, &errOut); code != 1 || out.Len() != 0 {
		t.Fatal("pending reset printed a stale key")
	}
	out.Reset()
	errOut.Reset()
	if code := runAccount(context.Background(), []string{"reset-key"}, &out, &errOut); code != 0 || requests != 2 {
		t.Fatal("new Runtime invocation did not recover the committed reset")
	}
	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Fatal("completed pending reset not removed")
	}
	if strings.Contains(out.String(), replacement) {
		t.Fatal("reset success printed the replacement")
	}
	out.Reset()
	if code := runAccount(context.Background(), []string{"key"}, &out, &errOut); code != 0 || strings.TrimSpace(out.String()) != replacement {
		t.Fatal("local credential was not updated")
	}
}

func TestSessionCommandPinsProjectAndVersion(t *testing.T) {
	remote := newFakeCoconetServer()
	var observed contracts.ReadSessionRequest
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/read") {
			remote.ServeHTTP(w, r)
			return
		}
		calls++
		if json.NewDecoder(r.Body).Decode(&observed) != nil {
			t.Error("invalid read request")
		}
		_ = json.NewEncoder(w).Encode(contracts.ReadSessionResponse{SchemaVersion: 1, Status: contracts.StatusOK, ServerSessionID: observed.ServerSessionID, Version: observed.Version, Turns: []contracts.ReadSessionTurn{}})
	}))
	defer server.Close()
	repository, _ := configureBoundRepository(t, remote, server.URL)
	chdirMCPTest(t, repository)
	base := []string{"read", "--project", "coco_wrong", "--node", "work_fixture", "--session", "sess_fixed", "--version", "3"}
	var output bytes.Buffer
	if code := runSessionCommand(context.Background(), base, &output, &bytes.Buffer{}); code != 1 || calls != 0 {
		t.Fatal("wrong Project reached Session API")
	}
	base[2] = remote.project.ProjectID
	if code := runSessionCommand(context.Background(), append(base, "--cursor", "fixed_cursor"), &output, &bytes.Buffer{}); code != 2 || calls != 0 {
		t.Fatal("retired remote paging was accepted")
	}

	if code := runSessionCommand(context.Background(), []string{"resume", "--project", remote.project.ProjectID, "--node", "work_fixture", "--session", "sess_fixed", "--version", "3", "--agent", "unknown"}, &output, &bytes.Buffer{}); code != 2 {
		t.Fatal("unknown Agent accepted")
	}
}
