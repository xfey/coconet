package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"time"
	"unicode/utf8"

	"github.com/xfey/coconet/adapters"
	"github.com/xfey/coconet/adapters/claude"
	"github.com/xfey/coconet/adapters/codex"
	"github.com/xfey/coconet/adapters/cursor"
	"github.com/xfey/coconet/packages/agentplugin"
	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/conversation"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/localdag"
	"github.com/xfey/coconet/packages/workgraph"
)

const localNativeForkTimeout = 90 * time.Second

type localForkSelection struct {
	RequestID string           `json:"request_id"`
	NodeID    string           `json:"work_node_id"`
	Source    workgraph.Source `json:"source"`
}
type localNativeForkRequest struct {
	localForkSelection
	ProjectID  string              `json:"project_id"`
	Agent      contracts.AgentType `json:"agent"`
	CWD        string              `json:"cwd"`
	ConfigRoot string              `json:"config_root"`
}
type localNativeForkResult struct {
	SourceAgent    string                  `json:"source_agent"`
	TransferFormat string                  `json:"transfer_format,omitempty"`
	RequestID      string                  `json:"request_id"`
	RunID          string                  `json:"run_id"`
	State          string                  `json:"state"`
	NodeID         string                  `json:"work_node_id"`
	Source         workgraph.Source        `json:"source"`
	CreatedSource  workgraph.Source        `json:"created_source"`
	Agent          contracts.AgentType     `json:"agent"`
	NativeID       string                  `json:"native_session_id"`
	Resume         contracts.ResumeCommand `json:"resume"`
	CWD            string                  `json:"working_directory"`
}
type localNativeForkAttempt struct {
	TransferFormat string                 `json:"transfer_format,omitempty"`
	CompactSHA256  string                 `json:"compact_sha256,omitempty"`
	Schema         int                    `json:"schema"`
	Request        localNativeForkRequest `json:"request"`
	Snapshot       workgraph.Snapshot     `json:"snapshot"`
	NativeID       string                 `json:"native_id,omitempty"`
	Prepared       string                 `json:"prepared,omitempty"`
	Result         *localNativeForkResult `json:"result,omitempty"`
}

func localForkDirectory(root string) (string, error) {
	path := filepath.Join(root, "forks")
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("native Fork state requires a real private directory")
	}
	return path, nil
}

// This local operation reuses the tested native Adapters. Relay never runs an
// Agent, restores a workspace or writes a user's native history.
func forkLocalDAG(ctx context.Context, root string, engine *localdag.Engine, request localNativeForkRequest, configured adapters.ForkAdapter) (result localNativeForkResult, failure error) {
	if utf8.RuneCountInString(request.RequestID) < 8 || utf8.RuneCountInString(request.RequestID) > 200 || request.ProjectID == "" || request.Source.SessionID == "" || request.Source.Version < 1 || !contracts.ValidAgent(request.Agent) || !filepath.IsAbs(request.CWD) || !filepath.IsAbs(request.ConfigRoot) {
		return result, errors.New("native Fork requires a stable request_id, exact source and current Agent workspace")
	}
	config, err := loadLocalDAGService(root)
	if err != nil {
		return result, err
	}
	cwd, err := canonicalPath(request.CWD)
	if err != nil || cwd != request.CWD {
		return result, errors.New("native Fork workspace is not canonical")
	}
	profile, err := canonicalPath(request.ConfigRoot)
	if err != nil || profile != request.ConfigRoot {
		return result, errors.New("native Fork profile is not canonical")
	}
	var workspace localDAGWorkspace
	for _, candidate := range config.Workspaces {
		if pathContains(candidate.Path, cwd) && len(candidate.Path) > len(workspace.Path) {
			workspace = candidate
		}
	}
	if workspace.ProjectID != request.ProjectID {
		return result, errors.New("native Fork workspace belongs to another project or is disconnected")
	}
	adapter := configured
	if adapter == nil {
		for _, candidate := range runtimeSessionAdapters() {
			if candidate.Type() != request.Agent {
				continue
			}
			switch value := candidate.(type) {
			case codex.Adapter:
				value.ConfigRoot = profile
				adapter = value
			case cursor.Adapter:
				value.ConfigRoot = profile
				value.Command, err = cursorForkCommand(profile, value.Command)
				if err != nil {
					return result, err
				}
				adapter = value
			case claude.Adapter:
				value.ConfigRoot = profile
				adapter = value
			}
		}
	}
	if adapter == nil || adapter.Type() != request.Agent {
		return result, errors.New("this Agent does not support native Fork")
	}
	directory, err := localForkDirectory(root)
	if err != nil {
		return result, err
	}
	releaseLock, err := acquireLocalFileLock(ctx, filepath.Join(directory, "operations.lock"))
	if err != nil {
		return result, err
	}
	defer releaseLock()
	key := workgraph.Digest([]string{request.ProjectID, engine.Actor, request.RequestID})
	completed, err := forkReceiptDirectory(directory)
	if err != nil {
		return result, err
	}
	if saved, found, err := readForkReceipt(completed, key, request); err != nil || found {
		return saved, err
	}
	attemptDirectory := filepath.Join(directory, key)
	if info, err := os.Lstat(attemptDirectory); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return result, errors.New("native Fork attempt directory is invalid")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	} else {
		if err := os.Mkdir(attemptDirectory, 0700); err != nil {
			return result, err
		}
	}
	selectionPath := filepath.Join(attemptDirectory, "selection.json")
	var attempt localNativeForkAttempt
	data, readErr := readLocalPrivateFile(selectionPath, 64<<10, false)
	if readErr == nil {
		if err := decodeStrict(data, &attempt); err != nil || attempt.Schema != 1 || !reflect.DeepEqual(attempt.Request, request) {
			return result, errors.New("Fork request_id already selects different source, Agent or workspace")
		}
		if attempt.Result != nil {
			return *attempt.Result, completeForkReceipt(directory, key, request, *attempt.Result)
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return result, readErr
	}
	release := engine.Store.PinContent()
	defer release()
	stamp := func() string { return time.Now().UTC().Format(time.RFC3339Nano) }
	runID := "fork_" + newRequestID()
	input, _ := json.Marshal(request)
	if err := engine.Store.Begin(graphstore.Run{ID: runID, TaskID: runID, ProjectID: request.ProjectID, Kind: "fork", Status: "running", StartedAt: stamp(), Template: "native-fork/1", Input: input, Source: &request.Source, NodeID: request.NodeID}); err != nil {
		return result, err
	}
	defer func() {
		output, _ := json.Marshal(map[string]any{"result": result, "created_native_id": attempt.NativeID, "prepared_observation": attempt.Prepared})
		if err := engine.Store.SaveResponse(runID, string(output), nil); err != nil {
			failure = errors.Join(failure, err)
			return
		}
		status, message := "completed", ""
		if failure != nil {
			status, message = "failed", failure.Error()
			if len(message) > 4000 {
				message = message[:4000]
			}
		}
		failure = errors.Join(failure, engine.Store.Finish(runID, status, message, stamp()))
		if failure == nil {
			attempt.Result = &result
			failure = atomicWriteJSON(selectionPath, attempt, 0600)
			if failure == nil {
				failure = completeForkReceipt(directory, key, request, result)
			}
		}
	}()
	if errors.Is(readErr, os.ErrNotExist) {
		state, err := engine.Store.State(request.ProjectID)
		if err != nil {
			return result, err
		}
		_, selectErr := workgraph.SelectNode(state, request.NodeID, &request.Source)
		if selectErr != nil {
			return result, localdag.ErrSelectionChanged
		}
		snapshot, ok := state.Snapshots[request.Source.Key()]
		if !ok {
			return result, workgraph.ErrSource
		}
		crossAgent := snapshot.Agent != string(request.Agent)
		if !contracts.ValidAgent(contracts.AgentType(snapshot.Agent)) {
			return result, errors.New("Fork requires a supported Agent source: Codex, Claude or Cursor")
		}
		if crossAgent {
			if _, ok := adapter.(adapters.TextImportAdapter); !ok {
				return result, errors.New("target Agent does not support cross-Agent Fork; update Coconet")
			}
		}
		if !crossAgent && (!adapters.ValidNativeSessionID(snapshot.NativeID) || snapshot.Artifact == "") {
			return result, errors.New("selected source has no complete native history")
		}
		selected, err := engine.ReadSelectedNode(ctx, request.ProjectID, request.NodeID, request.Source, !crossAgent)
		if err != nil {
			return result, err
		}
		attempt = localNativeForkAttempt{Schema: 1, Request: request, Snapshot: snapshot}
		if crossAgent {
			attempt.TransferFormat = adapters.TextImportFormat
			attempt.CompactSHA256, _, err = adapters.HashFile(selected.CompactPath)
			if err != nil {
				return result, err
			}
		}
		if err := atomicWriteJSON(selectionPath, attempt, 0600); err != nil {
			return result, err
		}
	}
	if attempt.TransferFormat != "" && attempt.TransferFormat != adapters.TextImportFormat || attempt.Snapshot.Source != request.Source || (attempt.Snapshot.Agent != string(request.Agent)) != (attempt.TransferFormat == adapters.TextImportFormat) {
		return result, errors.New("fixed Fork source metadata differs")
	}
	var child adapters.ForkResult
	var textRequest adapters.TextImportRequest
	if attempt.TransferFormat == adapters.TextImportFormat {
		importer, ok := adapter.(adapters.TextImportAdapter)
		if !ok {
			return result, errors.New("target Agent does not support cross-Agent Fork; update Coconet")
		}
		path, err := engine.Store.ConversationFile(ctx, attempt.Snapshot.Conversation, attempt.Snapshot.Turns)
		if err != nil {
			return result, fmt.Errorf("read fixed cross-Agent source: %w", err)
		}
		textRequest = adapters.TextImportRequest{AttemptDirectory: attemptDirectory, CompactPath: path, CompactSHA256: attempt.CompactSHA256, CWD: request.CWD, ObservedAt: attempt.Snapshot.ObservedAt}
		child, err = importer.ImportText(ctx, textRequest)
	} else {
		nativePath, readErr := engine.Store.ArtifactFile(attempt.Snapshot.Artifact)
		if readErr != nil {
			return result, readErr
		}
		manifest, readErr := engine.Store.ArtifactManifest(attempt.Snapshot.Artifact)
		if readErr != nil {
			return result, readErr
		}
		child, err = adapter.Fork(ctx, adapters.ForkRequest{AttemptDirectory: attemptDirectory, SourceArtifactPath: nativePath, SourceArtifactSHA256: manifest.ArtifactSHA256, SourceNativeSessionID: attempt.Snapshot.NativeID, CWD: request.CWD})
	}
	if err != nil {
		return result, err
	}
	if !adapters.ValidNativeSessionID(child.NativeSessionID) || child.NativeSessionID == attempt.Snapshot.NativeID || attempt.NativeID != "" && attempt.NativeID != child.NativeSessionID {
		return result, errors.New("native Fork did not preserve a distinct stable child identity")
	}
	attempt.NativeID = child.NativeSessionID
	if err := atomicWriteJSON(selectionPath, attempt, 0600); err != nil {
		return result, err
	}
	var observation localdag.Observation
	if attempt.Prepared == "" {
		observation, err = prepareLocalDAGObservation(ctx, root, engine, localDAGObservationRequest{ProjectID: request.ProjectID, Agent: request.Agent, NativeID: child.NativeSessionID, Root: workspace.Path, CWD: cwd, ConfigRoot: profile}, adapter)
		if err != nil {
			return result, err
		}
		if attempt.TransferFormat == adapters.TextImportFormat {
			if err := adapters.VerifyImportedText(ctx, textRequest, contracts.SessionObservation{Normalized: observation.Normalized, VisibleText: observation.Visible}); err != nil {
				return result, err
			}
		}
		observation.Inherited = true
		data, err := json.Marshal(observation)
		if err != nil {
			return result, err
		}
		attempt.Prepared, err = engine.Store.PutObject(data)
		if err != nil {
			return result, err
		}
		if err := atomicWriteJSON(selectionPath, attempt, 0600); err != nil {
			return result, err
		}
	} else {
		data, err := engine.Store.Object(attempt.Prepared)
		if err != nil {
			return result, err
		}
		if err := decodeStrict(data, &observation); err != nil {
			return result, err
		}
		if observation.NativeID != attempt.NativeID || observation.ProjectID != request.ProjectID || observation.Agent != string(request.Agent) || !observation.Inherited {
			return result, errors.New("fixed inherited observation differs")
		}
	}
	created, err := engine.QueueInheritedObservation(ctx, observation)
	if err != nil {
		return result, err
	}
	resume := adapter.ResumeCommand(child.NativeSessionID)
	profileVariable := "CODEX_HOME"
	if request.Agent == contracts.AgentClaude {
		profileVariable = "CLAUDE_CONFIG_DIR"
	}
	if request.Agent == contracts.AgentCursor {
		profileVariable = "CURSOR_CONFIG_DIR"
	}
	resume.Args = append([]string{profileVariable + "=" + profile, resume.Program}, resume.Args...)
	resume.Program = "env"
	resume.Display = shellCommand("cd", []string{cwd}) + " && " + shellCommand(resume.Program, resume.Args)
	result = localNativeForkResult{SourceAgent: attempt.Snapshot.Agent, TransferFormat: attempt.TransferFormat, RequestID: request.RequestID, RunID: runID, State: "created", NodeID: request.NodeID, Source: request.Source, CreatedSource: created, Agent: request.Agent, NativeID: child.NativeSessionID, Resume: resume, CWD: cwd}
	return result, nil
}

// Cursor's native import does not launch the CLI, but its continuation command
// must still select the registered installation for the destination profile.
func cursorForkCommand(profile, fallback string) (string, error) {
	paths, err := resolveRuntimePaths()
	if err != nil {
		return "", err
	}
	state, _, err := loadAgentIntegrationState(paths)
	if err != nil {
		return "", err
	}
	command := ""
	for _, entry := range state.Integrations {
		if entry.Type != agentplugin.HostCursor || !samePath(entry.ConfigRoot, profile) {
			continue
		}
		if command != "" {
			return "", errors.New("multiple Cursor installations use this profile; inspect coconet agent list --verbose and retain one registration for this profile")
		}
		command = entry.Command
	}
	if command == "" {
		command = fallback
	}
	return command, nil
}

// Unfinished native operations keep their verified input copies across restarts.
// Completed results retain only lightweight request/identity metadata.
func localForkCacheRoots(root string, store *graphstore.Store) (graphstore.CacheRoots, error) {
	var roots graphstore.CacheRoots
	directory := filepath.Join(root, "forks")
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return roots, nil
	}
	if err != nil {
		return roots, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return roots, errors.New("invalid native Fork state directory")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return roots, err
	}
	for _, entry := range entries {
		if entry.Name() == "operations.lock" {
			continue
		}
		if entry.Name() == "completed" {
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				return roots, errors.New("invalid completed Fork directory")
			}
			continue
		}
		if len(entry.Name()) != 64 || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return roots, errors.New("invalid native Fork archive entry")
		}
		data, err := readLocalPrivateFile(filepath.Join(directory, entry.Name(), "selection.json"), 64<<10, false)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return roots, err
		}
		var attempt localNativeForkAttempt
		if err := decodeStrict(data, &attempt); err != nil || attempt.Schema != 1 {
			return roots, errors.New("invalid native Fork request")
		}
		if attempt.Result != nil {
			if err := completeForkReceipt(directory, entry.Name(), attempt.Request, *attempt.Result); err != nil {
				return roots, err
			}
			continue
		}
		if attempt.TransferFormat == adapters.TextImportFormat {
			data, err := store.Object(attempt.Snapshot.Conversation)
			if err != nil {
				return roots, err
			}
			var manifest conversation.Manifest
			if err := json.Unmarshal(data, &manifest); err != nil {
				return roots, err
			}
			if err := conversation.Validate(manifest); err != nil {
				return roots, err
			}
			roots.Objects = append(roots.Objects, attempt.Snapshot.Conversation)
			for _, block := range manifest.Blocks {
				roots.Objects = append(roots.Objects, block.SHA256)
			}
		} else if attempt.Snapshot.Artifact != "" {
			roots.Artifacts = append(roots.Artifacts, attempt.Snapshot.Artifact)
		}
		if attempt.Prepared != "" {
			roots.Objects = append(roots.Objects, attempt.Prepared)
			data, err := store.Object(attempt.Prepared)
			if err != nil {
				return roots, err
			}
			var observation struct {
				ArtifactManifest string `json:"artifact_manifest"`
			}
			if err := json.Unmarshal(data, &observation); err != nil {
				return roots, err
			}
			if observation.ArtifactManifest != "" {
				roots.Artifacts = append(roots.Artifacts, observation.ArtifactManifest)
			}
		}
	}
	return roots, nil
}
