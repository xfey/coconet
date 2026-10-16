package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xfey/coconet/adapters"
	"github.com/xfey/coconet/packages/contracts"
)

func TestObserveKeepsCheckpointOnUnexpectedEmptyChain(t *testing.T) {
	adapter := New("tclaude")
	turns := []contracts.NormalizedTurn{{TurnID: "one", Role: "user", Kind: "message", Text: "first", ContentHash: adapters.Digest("first")}}
	adapter.run = func(_ context.Context, request helperRequest) ([]byte, error) {
		return json.Marshal(contracts.SessionObservation{SchemaVersion: 1, Session: contracts.SessionEnvelope{AgentType: contracts.AgentClaude, NativeSessionID: "source", CWD: "/fixture"}, Normalized: contracts.NormalizedObservation{Turns: turns}})
	}
	_, previous, err := adapter.Observe(context.Background(), "/fixture", "source", adapters.ObservationCheckpoint{})
	if err != nil {
		t.Fatal(err)
	}
	unchanged, next, err := adapter.Observe(context.Background(), "/fixture", "source", previous)
	if err != nil || len(unchanged.Normalized.Turns) != 0 || next.Cursor != previous.Cursor {
		t.Fatal("valid empty delta rejected", err)
	}
	turns = nil
	if _, _, err := adapter.Observe(context.Background(), "/fixture", "source", previous); err == nil || !strings.Contains(err.Error(), "empty complete message chain") {
		t.Fatal("empty source accepted", err)
	}
	if _, _, err := adapter.Observe(context.Background(), "/fixture", "source", adapters.ObservationCheckpoint{}); err != nil {
		t.Fatal("initial lifecycle-only source rejected", err)
	}
	turns = []contracts.NormalizedTurn{{TurnID: "summary", Role: "user", Kind: "message", Text: "compacted", ContentHash: adapters.Digest("compacted")}}
	compacted, next, err := adapter.Observe(context.Background(), "/fixture", "source", previous)
	if err != nil || !compacted.Normalized.Reset || next.SourceEpoch <= previous.SourceEpoch {
		t.Fatal("valid compaction rejected", err)
	}
}

func TestSDKObservationIdentityDoesNotFollowCWD(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node required")
	}
	config := t.TempDir()
	root := t.TempDir()
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(config, "projects", "origin", "source.jsonl")
	if err := os.MkdirAll(filepath.Dir(artifact), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact, []byte("{\"uuid\":\"message\",\"parentUuid\":null}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	helper, err := filepath.Abs(filepath.Join("helper", "helper.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	sdk := filepath.Join(t.TempDir(), "sdk.mjs")
	// Reproduce the real SDK: scoped metadata falls back, scoped messages return [].
	code := fmt.Sprintf(`export async function getSessionInfo(id,options) {return options ? undefined : {sessionId:id,cwd:%q};}
 export async function getSessionMessages(id,options) {return options ? [] : [{uuid:"message",type:"user",message:{content:"confirmed work"}}];}`, root)
	if err := os.WriteFile(sdk, []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
	adapter := Adapter{NodeCommand: node, HelperPath: helper, SDKEntry: sdk, ConfigRoot: config, TranscriptPath: artifact}
	for _, cwd := range []string{root, filepath.Join(root, "child"), filepath.Dir(root), filepath.Join(root, "removed-worktree")} {
		observation, _, err := adapter.Observe(context.Background(), cwd, "source", adapters.ObservationCheckpoint{})
		if err != nil || len(observation.Normalized.Turns) != 1 {
			t.Fatalf("cwd lost the source: %v", err)
		}
		if observation.Session.CWD != root {
			t.Fatal("source cwd changed")
		}
	}
	adapter.TranscriptPath = filepath.Join(t.TempDir(), "source.jsonl")
	if err := os.WriteFile(adapter.TranscriptPath, []byte("unrelated"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := adapter.Observe(context.Background(), root, "source", adapters.ObservationCheckpoint{}); err == nil {
		t.Fatal("foreign hook path accepted")
	}
	adapter.TranscriptPath = ""
	duplicate := filepath.Join(config, "projects", "duplicate", "source.jsonl")
	if err := os.MkdirAll(filepath.Dir(duplicate), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(duplicate, []byte("duplicate"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := adapter.Observe(context.Background(), root, "source", adapters.ObservationCheckpoint{}); err == nil {
		t.Fatal("ambiguous artifact accepted")
	}
	if _, _, err := adapter.Observe(context.Background(), root, "../source", adapters.ObservationCheckpoint{}); err == nil {
		t.Fatal("unsafe identity accepted")
	}
}
