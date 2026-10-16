package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestMCPHealthOnlyExposesCurrentProjectSummary(t *testing.T) {
	raw := json.RawMessage(`{"status":"running","runtime_version":"test","sync_mode":"relay","inference_configured":true,
 "capture":{"pending":51,"paused":50,"last_failure":"FOREIGN_STACK"},"last_error":"GLOBAL_STACK",
 "errors":{"capture":"FOREIGN_CAPTURE","processing/other":"OTHER_PROJECT","replication/project":"PRIVATE_TRANSPORT_STACK"},
 "project":{"project_id":"project","accepted_revision":3,"last_confirmed_checkpoint_revision":2,"capture_pending":2,"capture_paused":1,"tasks":{"stage/running":1},
 "replicas":{"expected":2,"text_ready":2,"native_ready":2,"pending_uploads":0,"failed":0},"search_index":{"expected":2,"indexed":2,"complete":true,"revision":3,"instance":"PRIVATE_INDEX"},
 "issues":[{"kind":"observation","id":"PRIVATE_SESSION","state":"paused","attempts":8,"error":"PRIVATE_STACK","action":"PRIVATE_COMMAND"}],"issue_count":1,
 "recent_transfers":[{"error":"PRIVATE_TRANSFER"}],"note":"PRIVATE_NOTE"}}`)
	view, err := mcpProjectHealth(raw)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(mcpToolResult("health", view, false))
	for _, marker := range []string{"FOREIGN_", "GLOBAL_", "OTHER_PROJECT", "PRIVATE_", "last_failure", "recent_transfers"} {
		if strings.Contains(string(encoded), marker) {
			t.Fatal("diagnostics leaked to working context", marker)
		}
	}
	p := view["project"].(map[string]any)
	capture := p["background"].(map[string]any)["capture"].(map[string]int)
	if capture["queued"] != 1 || capture["paused"] != 1 || view["processing"] != "enabled" {
		t.Fatal(view)
	}
	components := p["component_errors"].(map[string]string)
	if len(components) != 1 || components["replication"] != "error" {
		t.Fatal("lost relevant transport failure", view)
	}
	if p["search_index"].(map[string]any)["complete"] != true {
		t.Fatal("background issue changed readable coverage")
	}
	if len(encoded) > 3500 {
		t.Fatal("health summary grew unexpectedly", len(encoded))
	}
}

func TestMCPHealthNeverTreatsIncompleteResponseAsHealthy(t *testing.T) {
	for _, raw := range []string{`{}`, `null`, `{"status":"running"}`, `broken`} {
		if _, err := mcpProjectHealth(json.RawMessage(raw)); err == nil {
			t.Fatal("accepted incomplete health", raw)
		}
	}
}

func TestMCPFailureKeepsRecoveryButDoesNotForwardProviderLogs(t *testing.T) {
	good := "work node source changed\nNext step: Refresh the project graph; do not silently use another version."
	if mcpRuntimeErrorText(errors.New(good)) != good {
		t.Fatal("lost controlled recovery advice")
	}
	for _, raw := range []string{"Claude helper failed\n at file:///private/profile/helper.mjs:99", strings.Repeat("wrapper logging ", 200)} {
		got := mcpRuntimeErrorText(errors.New(raw))
		if strings.Contains(got, "file://") || strings.Contains(got, "wrapper logging") || !strings.Contains(got, "diagnostics") {
			t.Fatal("unhelpful failure", got)
		}
	}
}
