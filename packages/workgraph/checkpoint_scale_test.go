package workgraph

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Synthetic structural baseline for capacity measurements; this is not a
// model-quality fixture or a claim that all prior commits were replayed.
func scaleState(nodes, versions int) State {
	s := New("scale_project")
	s.Revision = int64(nodes * (versions*2 + 2))
	for i := 0; i < nodes; i++ {
		id, nodeID := fmt.Sprintf("session_%06d", i), fmt.Sprintf("node_%06d", i)
		s.Sessions[id] = Session{ID: id, Owner: "account.device", Agent: "fixture", NativeID: id, Latest: versions, ProcessedThrough: versions, ConsumedTurns: versions * 2, Tail: nodeID, Revision: int64(versions + 1), Items: []Item{}}
		for version := 1; version <= versions; version++ {
			source := Source{id, version}
			s.Snapshots[source.Key()] = Snapshot{Source: source, Owner: "account.device", Agent: "fixture", NativeID: id, Digest: Digest(source), Conversation: Digest("conversation"), Artifact: Digest("artifact"), Turns: version * 2, ObservedAt: testTime}
		}
		node := Node{ID: nodeID, Text: Text{Objective: "Implement bounded retries", Summary: strings.Repeat("A concise result with scope and verification. ", 8), Approach: "Separate transient failures from explicit stop conditions."}, Source: Source{id, versions}, State: "sealed", Revision: int64(versions + 1), EndTurn: versions * 2, Parents: []Edge{}, Relations: "ready", RelationThrough: versions * 2, CreatedAt: testTime, UpdatedAt: testTime, RunID: "run_" + nodeID}
		if i > 0 {
			node.Parents = []Edge{{Parent: fmt.Sprintf("node_%06d", i-1), Source: Source{fmt.Sprintf("session_%06d", i-1), versions}, Reason: "Uses the verified earlier result", Evidence: []string{"turn_1"}, RunID: node.RunID}}
		}
		if i == nodes-1 {
			node.State = "current"
			node.Revision--
		}
		s.Nodes[nodeID] = node
	}
	return s
}

var scaleCases = []struct{ nodes, versions int }{{100, 10}, {1000, 10}, {5000, 1}, {1000, 50}}

func TestCheckpointScaleFixtures(t *testing.T) {
	root := os.Getenv("COCONET_CHECKPOINT_SCALE_DIR")
	if root == "" {
		t.Skip("opt-in capacity fixture export")
	}
	if !filepath.IsAbs(root) {
		t.Fatal("fixture directory must be absolute")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	for _, size := range scaleCases {
		state := scaleState(size.nodes, size.versions)
		if err := ValidateState(state); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		name := fmt.Sprintf("nodes_%d_versions_%d.json", size.nodes, size.versions)
		if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
		t.Logf("nodes=%d snapshots=%d bytes=%d exceeds_16MiB=%t", size.nodes, len(state.Snapshots), len(data), len(data) > 16<<20)
	}
}

func BenchmarkCheckpointScale(b *testing.B) {
	for _, size := range scaleCases {
		b.Run(fmt.Sprintf("nodes_%d_versions_%d", size.nodes, size.versions), func(b *testing.B) {
			state := scaleState(size.nodes, size.versions)
			if err := ValidateState(state); err != nil {
				b.Fatal(err)
			}
			data, err := json.Marshal(state)
			if err != nil {
				b.Fatal(err)
			}
			expectedDigest, expectedBytes := Digest(state), sha256.Sum256(data)
			b.Run("validate", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if err := ValidateState(state); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run("encode", func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(data)))
				for i := 0; i < b.N; i++ {
					if _, err := json.Marshal(state); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run("decode_validate", func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(data)))
				for i := 0; i < b.N; i++ {
					if sha256.Sum256(data) != expectedBytes {
						b.Fatal("input digest differs")
					}
					var s State
					if err := json.Unmarshal(data, &s); err != nil {
						b.Fatal(err)
					}
					if err := ValidateState(s); err != nil {
						b.Fatal(err)
					}
					if Digest(s) != expectedDigest {
						b.Fatal("roundtrip differs")
					}
				}
			})
			last := fmt.Sprintf("session_%06d", size.nodes-1)
			prior := state.Snapshots[(Source{last, size.versions}).Key()]
			prior.Version++
			prior.Turns += 2
			prior.Digest = Digest(prior.Source)
			commit := Commit{Schema: 1, ID: "next", ProjectID: state.ProjectID, Actor: prior.Owner, Base: state.Revision, RunID: "run_next", At: testTime, Snapshot: &prior}
			b.Run("apply_one_capture", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if _, _, err := Apply(state, commit); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}
