package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/localdag"
	"github.com/xfey/coconet/packages/workgraph"
)

const maxLocalInboxEvents = 4096

type localDAGInboxEvent struct {
	Schema   int                        `json:"schema"`
	ID       string                     `json:"id"`
	Request  localDAGObservationRequest `json:"request"`
	Prepared string                     `json:"prepared,omitempty"`
	Attempts int                        `json:"attempts"`
	NextAt   string                     `json:"next_at,omitempty"`
	Paused   bool                       `json:"paused"`
	Error    string                     `json:"error,omitempty"`
	Source   *workgraph.Source          `json:"source,omitempty"`
}

// Hook delivery never opens the daemon-owned database or waits for a model.
// Every event gets its own durable file, so consecutive turns cannot overwrite
// one another while the daemon is offline or waiting for authentication.
func enqueueLocalDAGObservation(root string, request localDAGObservationRequest) error {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	release, err := acquireLocalFileLock(ctx, filepath.Join(root, "inbox.lock"))
	if err != nil {
		return err
	}
	defer release()
	directory := filepath.Join(root, "inbox")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	files, err := localDAGInboxFiles(root)
	if err != nil {
		return err
	}
	if len(files) >= maxLocalInboxEvents {
		return errors.New("local capture inbox is full; resume processing or inspect capture failures")
	}
	id := fmt.Sprintf("%020d-%s", time.Now().UnixNano(), newRequestID())
	event := localDAGInboxEvent{Schema: 1, ID: id, Request: request}
	if err := atomicWriteJSON(filepath.Join(directory, id+".json"), event, 0600); err != nil {
		return err
	}
	return syncLocalDirectory(directory)
}

func syncLocalDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func localDAGInboxFiles(root string) ([]string, error) {
	directory := filepath.Join(root, "inbox")
	file, err := os.Open(directory)
	if errors.Is(err, os.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	entries, err := file.ReadDir(maxLocalInboxEvents + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	paths := []string{}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			paths = append(paths, filepath.Join(directory, entry.Name()))
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func readLocalDAGInbox(path string) (localDAGInboxEvent, error) {
	var event localDAGInboxEvent
	data, err := readLocalPrivateFile(path, 64<<10, false)
	if err != nil {
		return event, err
	}
	if err := decodeStrict(data, &event); err != nil {
		return event, err
	}
	if event.Schema != 1 || event.ID+".json" != filepath.Base(path) || event.Attempts < 0 {
		return event, errors.New("invalid capture inbox identity")
	}
	return event, nil
}

func localDAGInbox(root string) ([]localDAGInboxEvent, error) {
	paths, err := localDAGInboxFiles(root)
	if err != nil {
		return nil, err
	}
	result := []localDAGInboxEvent{}
	for _, path := range paths {
		event, err := readLocalDAGInbox(path)
		if err != nil {
			return nil, err
		}
		result = append(result, event)
	}
	return result, nil
}

func localDAGInboxHealth(root string) (map[string]any, error) {
	queue, err := localDAGInbox(root)
	if err != nil {
		return nil, err
	}
	paused := 0
	lastFailure := ""
	for _, event := range queue {
		if event.Paused {
			paused++
		}
		if event.Error != "" {
			lastFailure = event.Error
		}
	}
	result := map[string]any{"pending": len(queue), "paused": paused, "last_failure": lastFailure}
	data, err := readLocalPrivateFile(filepath.Join(root, "capture-error.json"), 64<<10, false)
	if err == nil {
		var receipt any
		if err := json.Unmarshal(data, &receipt); err != nil {
			return nil, err
		}
		result["last_hook_failure"] = receipt
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return result, nil
}

func drainLocalDAGInbox(ctx context.Context, root string, engine *localdag.Engine) error {
	release, err := acquireLocalFileLock(ctx, filepath.Join(root, "inbox-processing.lock"))
	if err != nil {
		return err
	}
	defer release()
	paths, err := localDAGInboxFiles(root)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	var failures []error
	for _, path := range paths {
		if ctx.Err() != nil {
			return errors.Join(append(failures, ctx.Err())...)
		}
		event, err := readLocalDAGInbox(path)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if localDAGIsConnectedRoot(root) {
			allowed, err := localDAGObservationAllowed(root, event.Request)
			if err != nil {
				failures = append(failures, err)
				continue
			}
			if !allowed {
				continue
			} // disconnected work stays on disk, never silently reassigned
		}
		key := event.Request.ProjectID + "/" + string(event.Request.Agent) + "/" + event.Request.NativeID
		if seen[key] {
			continue
		}
		seen[key] = true
		blocked, err := localHistoryBlocksCapture(ctx, root, engine, event.Request)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if blocked {
			continue
		}
		next, parseErr := time.Parse(time.RFC3339Nano, event.NextAt)
		if event.NextAt != "" && parseErr != nil {
			failures = append(failures, errors.New("invalid capture retry time"))
			continue
		}
		if event.Paused || !next.IsZero() && time.Now().Before(next) {
			continue
		}
		err = traceLocalDAGInbox(ctx, root, engine, path, &event)
		if err == nil || errors.Is(err, localdag.ErrObservationSuperseded) || errors.Is(err, errEmptyNativeSession) {
			if err := os.Remove(path); err != nil {
				failures = append(failures, err)
			} else if err := syncLocalDirectory(filepath.Dir(path)); err != nil {
				failures = append(failures, err)
			}
			continue
		}
		// A predecessor in the durable outbox is a dependency, not a failed
		// native capture. In particular an offline Fork must not exhaust the
		// first resumed turn's retry budget while its baseline is unpublished.
		// Only a pure dependency wait is exempt; a joined archive/write error
		// must still use the ordinary failure path.
		waiting := err == localdag.ErrCapturePending
		if !waiting {
			event.Attempts++
			event.Paused = event.Attempts >= 8
		}
		event.Error = err.Error()
		if len(event.Error) > 4000 {
			event.Error = event.Error[:4000]
		}
		event.NextAt = time.Now().UTC().Add(time.Duration(1<<min(event.Attempts, 8)) * time.Second).Format(time.RFC3339Nano)
		if waiting {
			event.NextAt = time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano)
		}
		failures = append(failures, err, atomicWriteJSON(path, event, 0600))
	}
	return errors.Join(failures...)
}

func traceLocalDAGInbox(ctx context.Context, root string, engine *localdag.Engine, path string, event *localDAGInboxEvent) error {
	input, err := json.Marshal(event)
	if err != nil {
		return err
	}
	runID := "observation_" + newRequestID()
	stamp := func() string { return time.Now().UTC().Format(time.RFC3339Nano) }
	if err := engine.Store.Begin(graphstore.Run{ID: runID, TaskID: "inbox/" + event.ID, ProjectID: event.Request.ProjectID, Kind: "observation", Status: "running", StartedAt: stamp(), Template: "observation/1", Input: input}); err != nil {
		return err
	}
	err = processLocalDAGInbox(ctx, root, engine, path, event)
	output, _ := json.Marshal(map[string]any{"event": event.ID, "prepared": event.Prepared, "captured": err == nil})
	if archiveErr := engine.Store.SaveResponse(runID, string(output), nil); archiveErr != nil {
		return errors.Join(err, archiveErr)
	}
	status, message := "captured", ""
	if err != nil {
		status, message = "failed", err.Error()
		if errors.Is(err, localdag.ErrObservationSuperseded) {
			status = "superseded"
		}
		if errors.Is(err, errEmptyNativeSession) {
			status = "skipped"
		}
		if errors.Is(err, localdag.ErrCapturePending) {
			status = "deferred"
		}
		if len(message) > 4000 {
			message = message[:4000]
		}
	}
	if finishErr := engine.Store.Finish(runID, status, message, stamp()); finishErr != nil {
		return errors.Join(err, finishErr)
	}
	return err
}

func processLocalDAGInbox(ctx context.Context, root string, engine *localdag.Engine, path string, event *localDAGInboxEvent) error {
	release := engine.Store.PinContent()
	defer release()
	var observation localdag.Observation
	if event.Prepared == "" {
		value, err := prepareLocalDAGObservation(ctx, root, engine, event.Request, nil)
		if err != nil {
			return err
		}
		observation = value
		data, err := json.Marshal(observation)
		if err != nil {
			return err
		}
		event.Prepared, err = engine.Store.PutObject(data)
		if err != nil {
			return err
		}
		// Fix the compact observation and artifact closure before publication;
		// retries never reread a live, potentially longer native conversation.
		if err := atomicWriteJSON(path, event, 0600); err != nil {
			return err
		}
		if err := syncLocalDirectory(filepath.Dir(path)); err != nil {
			return err
		}
	} else {
		data, err := engine.Store.Object(event.Prepared)
		if err != nil {
			return err
		}
		if err := decodeStrict(data, &observation); err != nil {
			return err
		}
	}
	source, err := engine.CaptureObservation(ctx, observation)
	if err != nil {
		return err
	}
	event.Source = &source
	if err := atomicWriteJSON(path, event, 0600); err != nil {
		return err
	}
	return syncLocalDirectory(filepath.Dir(path))
}

func retryLocalDAGInbox(root, id string) error {
	return retryLocalDAGInboxProject(root, id, "")
}

func retryLocalDAGInboxProject(root, id, project string) error {
	if id == "" || filepath.Base(id) != id || strings.ContainsAny(id, "/\\") {
		return errors.New("invalid capture event identity")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	release, err := acquireLocalFileLock(ctx, filepath.Join(root, "inbox-processing.lock"))
	if err != nil {
		return errors.New("capture processing is busy; retry shortly")
	}
	defer release()
	path := filepath.Join(root, "inbox", id+".json")
	event, err := readLocalDAGInbox(path)
	if err != nil {
		return err
	}
	if project != "" && event.Request.ProjectID != project {
		return errors.New("capture event belongs to another project")
	}
	if !event.Paused {
		return errors.New("capture event is not paused")
	}
	event.Paused, event.Attempts, event.NextAt, event.Error = false, 0, "", ""
	if err := atomicWriteJSON(path, event, 0600); err != nil {
		return err
	}
	return syncLocalDirectory(filepath.Dir(path))
}
