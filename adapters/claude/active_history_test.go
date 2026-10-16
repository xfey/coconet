package claude

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/xfey/coconet/adapters"
)

func TestPinnedSDKActiveBranchSurvivesCompactionAndRewind(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node required")
	}
	sdk, err := filepath.Abs("../../apps/release/dependencies/claude-sdk/node_modules/@anthropic-ai/claude-agent-sdk/sdk.mjs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sdk); err != nil {
		t.Skip("install the pinned SDK fixture")
	}
	root, config := t.TempDir(), t.TempDir()
	const session = "aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa"
	directory := filepath.Join(config, "projects", "fixture")
	os.MkdirAll(directory, 0700)
	artifact := filepath.Join(directory, session+".jsonl")
	ids := []string{"aaaaaaaa-2222-4222-8222-aaaaaaaaaaaa", "bbbbbbbb-2222-4222-8222-bbbbbbbbbbbb", "cccccccc-2222-4222-8222-cccccccccccc", "dddddddd-2222-4222-8222-dddddddddddd", "eeeeeeee-2222-4222-8222-eeeeeeeeeeee"}
	appendRow := func(row map[string]any) {
		t.Helper()
		row["sessionId"], row["cwd"], row["timestamp"] = session, root, "2026-10-13T00:00:00Z"
		data, _ := json.Marshal(row)
		f, err := os.OpenFile(artifact, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.Write(append(data, '\n'))
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	message := func(id string, parent any, text string) {
		appendRow(map[string]any{"uuid": id, "parentUuid": parent, "type": "user", "message": map[string]any{"role": "user", "content": text}})
	}
	message(ids[0], nil, "A")
	message(ids[1], ids[0], "B")
	message(ids[2], ids[1], "C")
	helper, _ := filepath.Abs("helper/helper.mjs")
	a := Adapter{Command: "tclaude", NodeCommand: node, HelperPath: helper, SDKEntry: sdk, ConfigRoot: config}
	observe := func() []string {
		t.Helper()
		o, _, err := a.Observe(context.Background(), root, session, adapters.ObservationCheckpoint{})
		if err != nil {
			t.Fatal(err)
		}
		if o.ActiveHistory == nil {
			t.Fatal("missing active lineage")
		}
		return o.ActiveHistory.IDs
	}
	if got := observe(); !reflect.DeepEqual(got, ids[:3]) {
		t.Fatal(got)
	}
	appendRow(map[string]any{"uuid": "compact", "type": "system", "subtype": "compact_boundary", "parentUuid": nil, "logicalParentUuid": ids[2], "compactMetadata": map[string]any{"trigger": "manual"}})
	appendRow(map[string]any{"uuid": "summary", "type": "user", "parentUuid": "compact", "isCompactSummary": true, "message": map[string]any{"role": "user", "content": "COMPACTION_SUMMARY_NOT_NEW_WORK"}})
	message(ids[3], "summary", "D")
	if got := observe(); !reflect.DeepEqual(got, append(append([]string{}, ids[:3]...), "compact", "summary", ids[3])) {
		t.Fatal("lost compaction ancestors", got)
	}
	o, _, err := a.Observe(context.Background(), root, session, adapters.ObservationCheckpoint{})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range o.VisibleText.Turns {
		if v.TurnID == "summary" {
			t.Fatal("compaction summary became a user request")
		}
	}
	message(ids[4], ids[1], "E")
	if got := observe(); !reflect.DeepEqual(got, []string{ids[0], ids[1], ids[4]}) {
		t.Fatal("retracted branch remains", got)
	}
	// Compaction without its native logical ancestor must fail closed.
	appendRow(map[string]any{"uuid": "unanchored", "type": "system", "subtype": "compact_boundary", "parentUuid": nil, "compactMetadata": map[string]any{"trigger": "manual"}})
	message("after-unanchored", "unanchored", "G")
	if _, _, err := a.Observe(context.Background(), root, session, adapters.ObservationCheckpoint{}); err == nil {
		t.Fatal("unanchored compaction accepted as rewind")
	}
	// A corrupt ancestry cannot be mistaken for a complete replacement.
	message("invalid", "missing-parent", "F")
	if _, _, err := a.Observe(context.Background(), root, session, adapters.ObservationCheckpoint{}); err == nil {
		t.Fatal("incomplete lineage accepted")
	}
}
