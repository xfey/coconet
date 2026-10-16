package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xfey/coconet/adapters"
	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/localdag"
)

func TestLocalObservationSessionEndPreservesLaterWork(t *testing.T) {
	end := time.Date(2026, 10, 10, 1, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name, event, activity string
		modified              time.Time
		want                  bool
	}{
		{"codex-end", "SessionEnd", "", end.Add(-time.Second), true},
		{"codex-later-work", "SessionEnd", "", end.Add(time.Second), false},
		{"stop-is-not-end", "Stop", "", end.Add(-time.Second), false},
		{"bookkeeping", "SessionEnd", end.Add(-time.Second).Format(time.RFC3339Nano), end.Add(time.Second), true},
		{"later-activity", "SessionEnd", end.Add(time.Second).Format(time.RFC3339Nano), end.Add(-time.Second), false},
		{"invalid-activity", "sessionend", "invalid", end.Add(-time.Second), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, workspace := t.TempDir(), t.TempDir()
			store, err := graphstore.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			artifact := filepath.Join(workspace, "native.jsonl")
			data := []byte("{\"text\":\"fixture\"}\n")
			if err := os.WriteFile(artifact, data, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(artifact, test.modified, test.modified); err != nil {
				t.Fatal(err)
			}
			normalized, _ := adapters.ApplyCheckpoint([]contracts.NormalizedTurn{}, adapters.ObservationCheckpoint{})
			adapter := malformedObservationAdapter{observation: contracts.SessionObservation{
				Session:     contracts.SessionEnvelope{NativeSessionID: "native", AgentType: contracts.AgentCodex, CWD: workspace, LastActivityAt: test.activity},
				RawArtifact: contracts.ArtifactObservation{Path: artifact, Size: int64(len(data)), SHA256: adapters.Digest(string(data)), MediaType: "application/x-ndjson"},
				Normalized:  normalized, VisibleText: &contracts.VisibleTextProjection{Version: 1, SourceCursor: normalized.ToCursor, Turns: []contracts.VisibleTextTurn{}},
			}}
			request := localDAGObservationRequest{ProjectID: "project", Agent: contracts.AgentCodex, NativeID: "native", Root: workspace, CWD: workspace, ConfigRoot: t.TempDir(), Event: test.event, ObservedAt: end.Format(time.RFC3339Nano)}
			engine := &localdag.Engine{Store: store, Actor: "device"}
			observation, err := prepareLocalDAGObservation(context.Background(), root, engine, request, adapter)
			if err != nil || observation.Ended != test.want {
				t.Fatalf("ended=%v want=%v error=%v", observation.Ended, test.want, err)
			}
			request.Event, request.ObservedAt = "SessionEnd", "invalid"
			if _, err := prepareLocalDAGObservation(context.Background(), root, engine, request, adapter); err == nil {
				t.Fatal("invalid SessionEnd timestamp accepted")
			}
		})
	}
}
