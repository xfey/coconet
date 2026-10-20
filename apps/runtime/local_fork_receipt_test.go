package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/workgraph"
)

func TestCompletedForkReceiptsOutliveArchiveCleanupWithoutRetainingInputs(t *testing.T) {
	root := t.TempDir()
	store, err := graphstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	directory, err := localForkDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := forkReceiptDirectory(directory)
	if err != nil {
		t.Fatal(err)
	}
	request := localNativeForkRequest{localForkSelection: localForkSelection{RequestID: "request-receipt", Source: workgraph.Source{SessionID: "source", Version: 1}}, ProjectID: "project"}
	result := localNativeForkResult{RequestID: request.RequestID, State: "created", NativeID: "independent-native-child", Source: request.Source}
	body, _ := json.Marshal(localForkReceipt{Schema: 1, RequestSHA256: workgraph.Digest(request), Result: result})
	for i := 0; i < 4100; i++ {
		if err := os.WriteFile(filepath.Join(completed, fmt.Sprintf("%064x.json", i)), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	key := workgraph.Digest("legacy-completed-request")
	legacy := filepath.Join(directory, key)
	if err := os.Mkdir(legacy, 0700); err != nil {
		t.Fatal(err)
	}
	// Metadata names inputs that are no longer cached; completion must not read them.
	old, _ := json.Marshal(localNativeForkAttempt{Schema: 1, Request: request, Snapshot: workgraph.Snapshot{Conversation: "retired", Artifact: "retired"}, Prepared: "retired", Result: &result})
	if err := os.WriteFile(filepath.Join(legacy, "selection.json"), old, 0600); err != nil {
		t.Fatal(err)
	}
	roots, err := localForkCacheRoots(root, store)
	if err != nil || len(roots.Objects)+len(roots.Artifacts) != 0 {
		t.Fatal("completed history blocked GC or retained inputs", roots, err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatal("completed staging not compacted", err)
	}
	replay, found, err := readForkReceipt(completed, key, request)
	if err != nil || !found || replay.NativeID != result.NativeID {
		t.Fatal("idempotent child lost", replay, found, err)
	}
	request.Source.Version++
	if _, _, err := readForkReceipt(completed, key, request); err == nil {
		t.Fatal("changed request reused completed child")
	}
	// New requests remain possible after more than 4096 completed operations.
	if err := completeForkReceipt(directory, workgraph.Digest("new"), request, result); err != nil {
		t.Fatal(err)
	}
}
