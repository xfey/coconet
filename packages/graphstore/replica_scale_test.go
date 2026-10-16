package graphstore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/conversation"
	"github.com/xfey/coconet/packages/workgraph"
)

// Opt-in deterministic measurement: no model calls or user history.
func TestReplicaScaleMeasurement(t *testing.T) {
	if os.Getenv("COCONET_REPLICA_SCALE") != "1" {
		t.Skip("opt-in scale measurement")
	}
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	started := time.Now()
	entries := []conversation.Entry{}
	naive := int64(0)
	const versions = 40
	const perVersion = 50
	for version := 1; version <= versions; version++ {
		for i := 0; i < perVersion; i++ {
			id := len(entries)
			text := fmt.Sprintf("消息%d 验证临时存储与 local_retry_policy 边界。", id) + strings.Repeat("这段实际工作说明需要完整保留，检索时不要混入工具输出。", 12)
			entries = append(entries, conversation.Entry{Turn: contracts.NormalizedTurn{TurnID: fmt.Sprintf("turn-%d", id), Ordinal: id, Role: "user", Kind: "message", Text: text}})
		}
		built, err := conversation.Build(entries)
		if err != nil {
			t.Fatal(err)
		}
		for _, data := range built.Objects {
			naive += int64(len(data))
			if _, err := s.PutObject(data); err != nil {
				t.Fatal(err)
			}
		}
		data, _ := json.Marshal(built.Manifest)
		digest, err := s.PutObject(data)
		if err != nil {
			t.Fatal(err)
		}
		c := snapshotCommit()
		c.ID = fmt.Sprint("capture-", version)
		c.RunID = c.ID
		c.Base = int64(version - 1)
		c.Snapshot.Version = version
		c.Snapshot.Digest = ObjectDigest([]byte(c.ID))
		c.Snapshot.Conversation = digest
		c.Snapshot.Turns = len(entries)
		if _, err := s.Apply(c, false); err != nil {
			t.Fatal(err)
		}
	}
	captured := time.Since(started)
	started = time.Now()
	coverage, err := s.IndexProject(ctx, "project", 64)
	if err != nil || !coverage.Complete {
		t.Fatal(coverage, err)
	}
	indexed := time.Since(started)
	db, err := s.searchDB()
	if err != nil {
		t.Fatal(err)
	}
	var docs, rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM documents`).Scan(&docs); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sources`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if docs != versions*perVersion {
		t.Fatal("cumulative versions duplicated documents", docs)
	}
	state, _ := s.State("project")
	durations := []time.Duration{}
	for i := 0; i < 60; i++ {
		query := []string{"临时存储", "边界", "local_retry_policy"}[i%3]
		start := time.Now()
		result, err := s.SearchContent(ctx, state, query, 0, 20, coverage)
		if err != nil || len(result.Matches) == 0 {
			t.Fatal(err)
		}
		durations = append(durations, time.Since(start))
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	var indexBytes int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if info, err := os.Stat(filepath.Join(s.root, "session-index.sqlite"+suffix)); err == nil {
			indexBytes += info.Size()
		}
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	t.Logf("versions=%d turns=%d retained=%d documents=%d sources=%d capture=%s index=%s search_p50=%s search_p95=%s local_objects=%d naive_cumulative_bytes=%d sqlite_including_wal=%d heap_in_use=%d", versions, len(entries), len(workgraph.RetainedSources(state)), docs, rows, captured, indexed, durations[30], durations[57], s.objectBytes.Load(), naive, indexBytes, mem.HeapInuse)
}
