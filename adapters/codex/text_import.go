package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/xfey/coconet/adapters"
	"github.com/xfey/coconet/packages/contracts"
)

// ImportText uses Codex's official explicit session import; no configuration,
// plugin or account migration is requested and no model turn is started.
func (adapter Adapter) ImportText(ctx context.Context, request adapters.TextImportRequest) (adapters.ForkResult, error) {
	// The official importer currently discards leading assistant messages and
	// trims text edges. Reject those inputs before creating native state rather
	// than silently losing content or inventing a user turn.
	first := true
	if err := adapters.WalkCompact(ctx, request, func(message adapters.TextMessage) error {
		if first && message.Role != "user" {
			return errors.New("Codex session import requires history beginning with a user message; this source starts with an assistant. Use pull_session to read it in the current conversation instead")
		}
		first = false
		if strings.TrimSpace(message.Text) != message.Text {
			return errors.New("Codex's session importer would trim this source's text; use pull_session to preserve it in the current conversation instead")
		}
		return nil
	}); err != nil {
		return adapters.ForkResult{}, err
	}
	native, cleanup, err := adapters.PrepareClaudeText(ctx, request)
	if err != nil {
		return adapters.ForkResult{}, err
	}
	defer cleanup()
	verify := func(result adapters.ForkResult) (adapters.ForkResult, error) {
		client, err := adapter.openClient(ctx, request.CWD)
		if err != nil {
			return result, err
		}
		defer client.Close()
		var response struct {
			Thread thread `json:"thread"`
		}
		if err := client.Request("thread/read", map[string]any{"threadId": result.NativeSessionID, "includeTurns": false}, &response); err != nil {
			return result, fmt.Errorf("verify imported Codex session: %w", err)
		}
		if response.Thread.ID != result.NativeSessionID || response.Thread.CWD != request.CWD || response.Thread.Ephemeral {
			return result, errors.New("imported Codex session identity or workspace differs")
		}
		return result, nil
	}
	if marked, exists, err := adapters.LoadForkMarker(native, contracts.AgentCodex); err != nil {
		return adapters.ForkResult{}, err
	} else if exists {
		return verify(marked)
	}
	source, remove, err := stageImportSource(native)
	if err != nil {
		return adapters.ForkResult{}, err
	}
	defer remove()
	recoverID := func() (adapters.ForkResult, bool, error) {
		return adapter.findTextImport(source, native.SourceArtifactSHA256)
	}
	if result, found, err := recoverID(); err != nil {
		return result, err
	} else if found {
		if err := adapters.SaveForkMarker(native, contracts.AgentCodex, result); err != nil {
			return result, err
		}
		return verify(result)
	}
	intent := filepath.Join(request.AttemptDirectory, "codex-import-started")
	if _, err := os.Lstat(intent); err == nil {
		return adapters.ForkResult{}, errors.New("a previous Codex import stopped before its identity was recorded; no duplicate was created. Retry this same request after Codex finishes; if it persists, inspect local Fork logs")
	} else if !errors.Is(err, os.ErrNotExist) {
		return adapters.ForkResult{}, err
	}
	client, err := adapter.openClient(ctx, request.CWD)
	if err != nil {
		return adapters.ForkResult{}, err
	}
	defer client.Close()
	notifications, ok := client.(notificationClient)
	if !ok {
		return adapters.ForkResult{}, errors.New("Codex adapter lacks import completion support")
	}
	details := map[string]any{"sessions": []map[string]any{{"path": source, "cwd": request.CWD, "title": "Coconet continuation"}}, "plugins": []any{}, "skills": []any{}, "mcpServers": []any{}, "hooks": []any{}, "subagents": []any{}, "commands": []any{}}
	marker, err := os.OpenFile(intent, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return adapters.ForkResult{}, err
	}
	_, writeErr := marker.WriteString(native.SourceArtifactSHA256 + "\n")
	err = errors.Join(writeErr, marker.Sync(), marker.Close())
	if err != nil {
		return adapters.ForkResult{}, err
	}
	var response struct {
		ImportID string `json:"importId"`
	}
	err = client.Request("externalAgentConfig/import", map[string]any{"migrationItems": []map[string]any{{"itemType": "SESSIONS", "description": "Selected Coconet dialogue", "cwd": nil, "details": details}}}, &response)
	if err != nil {
		var rejected *rpcError
		if errors.As(err, &rejected) {
			_ = os.Remove(intent)
		}
		return adapters.ForkResult{}, fmt.Errorf("Codex session import failed (requires session-import support; verified with Codex 0.162.1). Retry the same request; inspect the local import log before creating another: %w", err)
	}
	if response.ImportID == "" {
		return adapters.ForkResult{}, errors.New("Codex did not return an import operation ID; upgrade Codex and retry")
	}
	var completed struct {
		ItemTypeResults []struct {
			ItemType  string                             `json:"itemType"`
			Successes []struct{ Source, Target *string } `json:"successes"`
			Failures  []struct {
				Message string `json:"message"`
				Stage   string `json:"failureStage"`
			} `json:"failures"`
		} `json:"itemTypeResults"`
	}
	if err := notifications.WaitNotification("externalAgentConfig/import/completed", response.ImportID, &completed); err != nil {
		return adapters.ForkResult{}, err
	}
	var result adapters.ForkResult
	failures := 0
	failureDetail := ""
	for _, item := range completed.ItemTypeResults {
		if item.ItemType != "SESSIONS" {
			continue
		}
		failures += len(item.Failures)
		if len(item.Failures) > 0 {
			failureDetail = item.Failures[0].Stage + ": " + item.Failures[0].Message
			if len(failureDetail) > 1200 {
				failureDetail = failureDetail[:1200]
			}
		}
		for _, success := range item.Successes {
			if success.Source != nil && *success.Source == source && success.Target != nil {
				if result.NativeSessionID != "" {
					return result, errors.New("Codex import returned multiple target identities")
				}
				result.NativeSessionID = *success.Target
			}
		}
	}
	// The native ledger is written before the completion notification. It also
	// recovers a lost response without importing the same source a second time.
	if result.NativeSessionID == "" {
		recovered, found, err := recoverID()
		if err != nil {
			return result, err
		}
		if found {
			result = recovered
		}
	}
	if !adapters.ValidNativeSessionID(result.NativeSessionID) || result.NativeSessionID == native.SourceNativeSessionID {
		if failures > 0 {
			_ = os.Remove(intent)
		}
		return adapters.ForkResult{}, fmt.Errorf("Codex did not create a readable imported session (%d import failures: %s); check Codex import support and workspace access, then retry the same request", failures, failureDetail)
	}
	if err := adapters.SaveForkMarker(native, contracts.AgentCodex, result); err != nil {
		return result, err
	}
	_ = os.Remove(intent)
	return verify(result)
}

// Codex accepts Claude imports only below ~/.claude/projects. Use an exclusive
// operation-specific directory there, never a live Claude project bucket. This
// does not require Claude installation or change either Agent's configuration.
func stageImportSource(request adapters.ForkRequest) (string, func(), error) {
	empty := func() {}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", empty, err
	}
	parent := filepath.Join(home, ".claude", "projects")
	if err := os.MkdirAll(parent, 0700); err != nil {
		return "", empty, err
	}
	parent, err = filepath.EvalSymlinks(parent)
	if err != nil {
		return "", empty, err
	}
	directory := filepath.Join(parent, ".coconet-import-"+request.SourceNativeSessionID)
	if err := os.Mkdir(directory, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", empty, err
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", empty, errors.New("Codex import staging directory is not private")
	}
	path := filepath.Join(directory, "source.jsonl")
	cleanup := func() {
		if hash, _, err := adapters.HashFile(path); err == nil && hash == request.SourceArtifactSHA256 {
			_ = os.Remove(path)
		}
		_ = os.Remove(directory)
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return "", empty, errors.New("Codex staging source must be a regular file")
		}
		hash, _, err := adapters.HashFile(path)
		if err != nil || hash != request.SourceArtifactSHA256 {
			return "", empty, errors.New("Codex staging source conflicts with this fixed operation")
		}
		return path, cleanup, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", empty, err
	}
	input, err := os.Open(request.SourceArtifactPath)
	if err != nil {
		return "", empty, err
	}
	defer input.Close()
	// The complete private file is atomically linked into the import namespace.
	tmp, err := os.CreateTemp(directory, ".staging-")
	if err != nil {
		return "", empty, err
	}
	defer os.Remove(tmp.Name())
	_, copyErr := io.Copy(tmp, input)
	err = errors.Join(copyErr, tmp.Sync(), tmp.Close())
	if err != nil {
		return "", empty, err
	}
	if err := os.Link(tmp.Name(), path); err != nil {
		return "", empty, err
	}
	return path, cleanup, nil
}

func (adapter Adapter) findTextImport(source, hash string) (adapters.ForkResult, bool, error) {
	path := filepath.Join(adapter.ConfigRoot, "external_agent_session_imports.json")
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return adapters.ForkResult{}, false, nil
	}
	if err != nil {
		return adapters.ForkResult{}, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return adapters.ForkResult{}, false, err
	}
	if !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return adapters.ForkResult{}, false, errors.New("Codex import ledger is not a bounded regular file")
	}
	var ledger struct {
		Records []struct {
			SourcePath       string `json:"source_path"`
			ContentSHA256    string `json:"content_sha256"`
			ImportedThreadID string `json:"imported_thread_id"`
		} `json:"records"`
	}
	if err := json.NewDecoder(io.LimitReader(file, 16<<20)).Decode(&ledger); err != nil {
		return adapters.ForkResult{}, false, fmt.Errorf("read Codex import recovery ledger: %w", err)
	}
	var id string
	for _, record := range ledger.Records {
		if record.SourcePath == source {
			if record.ContentSHA256 != hash || id != "" || !adapters.ValidNativeSessionID(record.ImportedThreadID) {
				return adapters.ForkResult{}, false, errors.New("Codex import ledger conflicts with the fixed source")
			}
			id = record.ImportedThreadID
		}
	}
	return adapters.ForkResult{NativeSessionID: id}, id != "", nil
}
