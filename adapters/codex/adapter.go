package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xfey/coconet/adapters"
	"github.com/xfey/coconet/packages/contracts"
)

const (
	DefaultCommand = "codex"
	adapterName    = "codex-app-server"
	adapterVersion = "6"
)

type Adapter struct {
	Command    string
	ConfigRoot string
	Locate     adapters.CommandLocator
	start      clientStarter
}

func New(command string) Adapter {
	if command == "" {
		command = DefaultCommand
	}
	return Adapter{Command: command, ConfigRoot: defaultConfigRoot()}
}

func (Adapter) Type() contracts.AgentType { return contracts.AgentCodex }

func (adapter Adapter) Health(context.Context) contracts.ComponentHealth {
	return adapters.ExecutableHealth("adapter.codex", adapter.Command, adapter.Locate)
}

type thread struct {
	ID           string          `json:"id"`
	SessionID    string          `json:"sessionId"`
	ForkedFromID string          `json:"forkedFromId"`
	Ephemeral    bool            `json:"ephemeral"`
	CWD          string          `json:"cwd"`
	Path         string          `json:"path"`
	CLIVersion   string          `json:"cliVersion"`
	CreatedAt    int64           `json:"createdAt"`
	UpdatedAt    int64           `json:"updatedAt"`
	Status       json.RawMessage `json:"status"`
	GitInfo      map[string]any  `json:"gitInfo"`
	Turns        []struct {
		ID          string            `json:"id"`
		StartedAt   *int64            `json:"startedAt"`
		CompletedAt *int64            `json:"completedAt"`
		Items       []json.RawMessage `json:"items"`
	} `json:"turns"`
}

func (adapter Adapter) Discover(ctx context.Context, root string) ([]contracts.DiscoveredSession, error) {
	client, err := adapter.openClient(ctx, root)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	imported, err := adapter.importedHistoryIDs()
	if err != nil {
		return nil, fmt.Errorf("read Codex imported history inventory: %w", err)
	}
	result := make([]contracts.DiscoveredSession, 0)
	var cursor string
	seenCursors := map[string]bool{}
	truncated := false
	for pages := 0; ; pages++ {
		var page struct {
			Data       []thread `json:"data"`
			NextCursor *string  `json:"nextCursor"`
		}
		params := map[string]any{"limit": 100, "sortKey": "updated_at", "sortDirection": "desc", "sourceKinds": []string{"cli", "exec"}, "cwd": root}
		if cursor != "" {
			params["cursor"] = cursor
		}
		if err := client.Request("thread/list", params, &page); err != nil {
			return nil, err
		}
		for _, candidate := range page.Data {
			if candidate.ID == "" || imported[candidate.ID] || candidate.CWD == "" || !adapters.PathContains(root, candidate.CWD) {
				continue
			}
			result = append(result, contracts.DiscoveredSession{
				SchemaVersion:   contracts.SchemaVersion,
				AgentType:       contracts.AgentCodex,
				NativeSessionID: candidate.ID,
				CWD:             candidate.CWD,
				ActivityState:   activityState(candidate.Status),
				CreatedAt:       unixTimestamp(candidate.CreatedAt),
				UpdatedAt:       unixTimestamp(candidate.UpdatedAt),
			})
		}
		if page.NextCursor == nil || *page.NextCursor == "" {
			break
		}
		cursor = *page.NextCursor
		if seenCursors[cursor] {
			return nil, errors.New("Codex discovery returned a repeated cursor")
		}
		seenCursors[cursor] = true
		if pages >= 99 {
			truncated = true
			break
		}
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].UpdatedAt > result[j].UpdatedAt })
	if truncated {
		return result, adapters.ErrDiscoveryLimit
	}
	return result, nil
}

func (adapter Adapter) Observe(ctx context.Context, cwd, nativeSessionID string, checkpoint adapters.ObservationCheckpoint) (result contracts.SessionObservation, next adapters.ObservationCheckpoint, failure error) {
	client, err := adapter.openClient(ctx, cwd)
	if err != nil {
		return contracts.SessionObservation{}, adapters.ObservationCheckpoint{}, err
	}
	defer client.Close()
	var read struct {
		Thread thread `json:"thread"`
	}
	if err := client.Request("thread/read", map[string]any{"threadId": nativeSessionID, "includeTurns": false}, &read); err != nil {
		return contracts.SessionObservation{}, adapters.ObservationCheckpoint{}, err
	}
	if read.Thread.ID != nativeSessionID || read.Thread.CWD == "" {
		return contracts.SessionObservation{}, adapters.ObservationCheckpoint{}, errors.New("thread/read returned an unexpected thread identity or cwd")
	}
	artifactPath := read.Thread.Path
	if artifactPath == "" {
		artifactPath, err = findArtifact(filepath.Join(adapter.ConfigRoot, "sessions"), nativeSessionID+".jsonl")
		if err != nil {
			return contracts.SessionObservation{}, adapters.ObservationCheckpoint{}, err
		}
	}
	before, err := os.Stat(artifactPath)
	if err != nil {
		return contracts.SessionObservation{}, adapters.ObservationCheckpoint{}, err
	}
	defer adapters.TrackCaptureFailure(&failure, []string{artifactPath})()
	// Locate first, then read the effective branch while the native file is
	// stable. Otherwise a concurrent rollback could pair old text with a new
	// raw artifact and make that mismatched snapshot available for Fork.
	read.Thread = thread{}
	if err := client.Request("thread/read", map[string]any{"threadId": nativeSessionID, "includeTurns": true}, &read); err != nil {
		return contracts.SessionObservation{}, adapters.ObservationCheckpoint{}, err
	}
	if read.Thread.ID != nativeSessionID || read.Thread.CWD == "" || read.Thread.Path != "" && read.Thread.Path != artifactPath {
		return contracts.SessionObservation{}, adapters.ObservationCheckpoint{}, errors.New("thread changed during structured observation")
	}
	artifact, err := adapter.historyArtifact(client, artifactPath, nativeSessionID)
	if err != nil {
		return contracts.SessionObservation{}, adapters.ObservationCheckpoint{}, fmt.Errorf("capture Codex history: %w", err)
	}
	after, err := os.Stat(artifactPath)
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return contracts.SessionObservation{}, adapters.ObservationCheckpoint{}, errors.New("native history changed during structured observation; retry after the host finishes writing")
	}
	var latest struct {
		Thread thread `json:"thread"`
	}
	if err := client.Request("thread/read", map[string]any{"threadId": nativeSessionID, "includeTurns": false}, &latest); err != nil {
		return contracts.SessionObservation{}, adapters.ObservationCheckpoint{}, err
	}
	if latest.Thread.ID != nativeSessionID || latest.Thread.Path != "" && latest.Thread.Path != artifactPath {
		return contracts.SessionObservation{}, adapters.ObservationCheckpoint{}, errors.New("native history branch changed during artifact capture; retry observation")
	}
	turns := make([]contracts.NormalizedTurn, 0, len(read.Thread.Turns))
	visible := &contracts.VisibleTextProjection{Version: 1, Turns: []contracts.VisibleTextTurn{}}
	for _, source := range read.Thread.Turns {
		occurredAt := ""
		if source.CompletedAt != nil {
			occurredAt = unixTimestamp(*source.CompletedAt)
		} else if source.StartedAt != nil {
			occurredAt = unixTimestamp(*source.StartedAt)
		}
		for index, raw := range source.Items {
			role, kind, text, toolName := normalizeItem(raw)
			// Official session import appends a UI-only AgentMessage event,
			// without a model-visible ResponseItem. Scope this lifecycle marker
			// to the final item of an official external-import turn; an ordinary
			// conversation quoting the same text remains visible.
			importedMarker := strings.HasPrefix(source.ID, "external-import-turn-") && index == len(source.Items)-1 && role == "assistant" && text == "<EXTERNAL SESSION IMPORTED>"
			if importedMarker {
				role, kind = "other", "event"
			}
			turns = append(turns, contracts.NormalizedTurn{
				TurnID: source.ID + "/item/" + strconv.Itoa(index), Ordinal: len(turns),
				Role: role, Kind: kind, Text: text, ToolName: toolName, OccurredAt: occurredAt,
				ContentHash: adapters.Digest(strings.Join([]string{role, kind, text}, "\x00")),
			})
			turn := turns[len(turns)-1]
			if ranges := visibleItemRanges(raw); !importedMarker && len(ranges) > 0 {
				visible.Turns = append(visible.Turns, contracts.VisibleTextTurn{TurnID: turn.TurnID, ContentHash: turn.ContentHash, Ranges: ranges})
			}
		}
	}
	active := &contracts.NativeActiveHistory{IDs: []string{}}
	for _, turn := range turns {
		active.IDs = append(active.IDs, turn.TurnID)
	}
	normalized, nextCheckpoint := adapters.ApplyCheckpoint(turns, checkpoint)
	visible.SourceCursor = normalized.ToCursor
	clientVersion := read.Thread.CLIVersion
	if clientVersion == "" {
		clientVersion = "unknown"
	}
	lineage := &contracts.NativeLineageHints{RootID: read.Thread.SessionID, ParentID: read.Thread.ForkedFromID}
	if lineage.RootID == "" && lineage.ParentID == "" {
		lineage = nil
	}
	return contracts.SessionObservation{
		ActiveHistory: active,
		SchemaVersion: contracts.SchemaVersion,
		Session:       contracts.SessionEnvelope{SchemaVersion: contracts.SchemaVersion, AgentType: contracts.AgentCodex, NativeSessionID: nativeSessionID, CWD: read.Thread.CWD, ActivityState: activityState(read.Thread.Status), SourceObserved: time.Now().UTC().Format(time.RFC3339Nano), ClientVersion: clientVersion, AdapterName: adapterName, AdapterVersion: adapterVersion},
		RawArtifact:   artifact,
		Normalized:    normalized, VisibleText: visible, Git: normalizeGit(read.Thread.GitInfo), NativeLineage: lineage,
	}, nextCheckpoint, nil
}

func (adapter Adapter) Fork(ctx context.Context, request adapters.ForkRequest) (adapters.ForkResult, error) {
	if !adapters.ValidNativeSessionID(request.SourceNativeSessionID) || request.SourceArtifactPath == "" || request.SourceArtifactSHA256 == "" || request.CWD == "" || request.AttemptDirectory == "" {
		return adapters.ForkResult{}, errors.New("Codex Fork request is incomplete")
	}
	hash, _, err := adapters.HashFile(request.SourceArtifactPath)
	if err != nil || hash != request.SourceArtifactSHA256 {
		return adapters.ForkResult{}, errors.New("Codex Fork source artifact failed hash validation")
	}
	history, err := adapter.prepareForkHistory(request)
	if err != nil {
		return adapters.ForkResult{}, fmt.Errorf("prepare Codex Fork history: %w", err)
	}
	defer history.cleanup()
	if err := adapter.prepareImportedHistory(ctx, request.CWD, history); err != nil {
		return adapters.ForkResult{}, err
	}
	if marked, exists, err := adapters.LoadForkMarker(request, contracts.AgentCodex); err != nil {
		return adapters.ForkResult{}, err
	} else if exists {
		history.cleanup()
		if err := adapter.verifyFork(ctx, request.CWD, marked.NativeSessionID, history.NativeID); err != nil {
			return adapters.ForkResult{}, fmt.Errorf("verify recorded Codex Fork: %w", err)
		}
		return marked, nil
	}

	client, err := adapter.openClient(ctx, request.CWD)
	if err != nil {
		return adapters.ForkResult{}, err
	}
	var forked struct {
		Thread thread `json:"thread"`
	}
	err = client.Request("thread/fork", map[string]any{
		"threadId": history.NativeID, "path": history.Path,
		"cwd": request.CWD, "ephemeral": false,
		// This internal context closes before the Runtime registers the child.
		// A lifecycle Hook here could sync it first as an unrelated Session.
		"config": map[string]any{"features.hooks": false},
	}, &forked)
	// Persist the identity before closing the native process. A failed later
	// verification must retry this child rather than create another one.
	if err == nil {
		if !adapters.ValidNativeSessionID(forked.Thread.ID) || forked.Thread.ID == request.SourceNativeSessionID || forked.Thread.ID == history.NativeID || forked.Thread.ForkedFromID != history.NativeID {
			err = errors.New("Codex App Server did not return a distinct child with direct parent lineage")
		} else {
			err = adapters.SaveForkMarker(request, contracts.AgentCodex, adapters.ForkResult{NativeSessionID: forked.Thread.ID})
		}
	}
	_ = client.Close()
	if err != nil {
		return adapters.ForkResult{}, fmt.Errorf("fork imported Codex source: %w", err)
	}
	history.cleanup()
	result := adapters.ForkResult{NativeSessionID: forked.Thread.ID}
	// Reopen with a fresh process to verify durable native history. Paginated
	// dependencies remain managed; only legacy temporary imports are removed.
	if err := adapter.verifyFork(ctx, request.CWD, result.NativeSessionID, history.NativeID); err != nil {
		return adapters.ForkResult{}, fmt.Errorf("verify independent Codex Fork: %w", err)
	}
	return result, nil
}

func (adapter Adapter) ResumeCommand(nativeSessionID string) contracts.ResumeCommand {
	args := []string{"resume", nativeSessionID}
	return contracts.ResumeCommand{Program: adapter.Command, Args: args, Display: strings.Join(append([]string{adapter.Command}, args...), " ")}
}

func (adapter Adapter) verifyFork(ctx context.Context, cwd, nativeSessionID, parentID string) error {
	client, err := adapter.openClient(ctx, cwd)
	if err != nil {
		return err
	}
	defer client.Close()
	return adapter.verifyForkWithClient(client, nativeSessionID, parentID)
}

func (Adapter) verifyForkWithClient(client appServerClient, nativeSessionID, parentID string) error {
	var read struct {
		Thread thread `json:"thread"`
	}
	if err := client.Request("thread/read", map[string]any{"threadId": nativeSessionID, "includeTurns": true}, &read); err != nil {
		return err
	}
	if read.Thread.ID != nativeSessionID || read.Thread.ForkedFromID != parentID {
		return errors.New("Codex App Server could not read the new Fork")
	}
	return nil
}

// Current Codex materializes only the immediate source on a latest Fork.
// Metadata reads and full-history reads do not populate cold ancestor item
// indexes. Prepare each imported ancestor with a non-persistent native Fork,
// with Hooks disabled, before creating the one durable user child. Closing
// each client releases its ephemeral context before preparing the next one.
func (adapter Adapter) prepareImportedHistory(ctx context.Context, cwd string, history preparedForkHistory) error {
	for _, id := range history.Ancestors {
		client, err := adapter.openClient(ctx, cwd)
		if err != nil {
			return err
		}
		var prepared struct {
			Thread thread `json:"thread"`
		}
		err = client.Request("thread/fork", map[string]any{
			"threadId": id, "ephemeral": true, "excludeTurns": true, "cwd": cwd,
			"config": map[string]any{"features.hooks": false},
		}, &prepared)
		_ = client.Close()
		if err != nil {
			return fmt.Errorf("prepare Codex ancestor index: %w", err)
		}
		if !prepared.Thread.Ephemeral || prepared.Thread.Path != "" || prepared.Thread.ForkedFromID != id {
			return errors.New("Codex ancestor preparation did not return a temporary native context")
		}
	}
	return nil
}

func (adapter Adapter) openClient(ctx context.Context, cwd string) (appServerClient, error) {
	starter := adapter.start
	if starter == nil {
		starter = startProcessClient
	}
	return starter(ctx, adapter.Command, adapter.ConfigRoot, cwd)
}

// App Server turns contain multiple messages and tool executions. Each item is
// independent evidence; assigning the enclosing turn's user role to all of it
// loses provenance and lets tool output consume the user-message text budget.
func normalizeItem(raw json.RawMessage) (string, string, string, string) {
	role, kind := "other", "event"
	texts := make([]string, 0)
	var item map[string]any
	if json.Unmarshal(raw, &item) != nil {
		return role, kind, "", ""
	}
	itemType, _ := item["type"].(string)
	switch itemType {
	case "userMessage":
		role, kind = "user", "message"
	case "agentMessage", "plan":
		role, kind = "assistant", "message"
	case "commandExecution", "mcpToolCall", "fileChange", "dynamicToolCall":
		role, kind = "tool", "tool_result"
		name := itemType
		if tool, ok := item["tool"].(string); ok && tool != "" {
			name = tool
		}
		metadata := map[string]any{"type": itemType}
		for _, key := range []string{"id", "tool", "server", "command", "arguments", "input", "status", "exitCode", "success"} {
			if value, ok := item[key]; ok {
				metadata[key] = value
			}
		}
		encoded, _ := json.Marshal(metadata)
		texts = append(texts, "tool: "+string(encoded))
		for _, key := range []string{"aggregatedOutput", "output", "result", "error", "contentItems", "changes"} {
			if value, ok := item[key]; ok && value != nil {
				if valueText, ok := value.(string); ok {
					texts = append(texts, key+": "+valueText)
				} else {
					encoded, _ := json.Marshal(value)
					texts = append(texts, key+": "+string(encoded))
				}
			}
		}
		return role, kind, strings.Join(texts, "\n"), name
	}
	collectText(item, &texts)
	return role, kind, strings.Join(texts, "\n"), ""
}

func collectText(value any, destination *[]string) {
	switch typed := value.(type) {
	case string:
		if typed != "" {
			*destination = append(*destination, typed)
		}
	case []any:
		for _, item := range typed {
			collectText(item, destination)
		}
	case map[string]any:
		for _, key := range []string{"text", "output", "aggregatedOutput"} {
			if text, ok := typed[key].(string); ok && text != "" {
				*destination = append(*destination, text)
			}
		}
		for _, key := range []string{"content", "fragments", "items"} {
			collectText(typed[key], destination)
		}
	}
}

func activityState(raw json.RawMessage) contracts.ActivityState {
	var status struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &status) == nil && status.Type == "active" {
		return contracts.ActivityActive
	}
	return contracts.ActivityUnknown
}

func normalizeGit(input map[string]any) *contracts.GitObservation {
	if len(input) == 0 {
		return nil
	}
	result := &contracts.GitObservation{}
	for _, key := range []string{"sha", "head"} {
		if value, ok := input[key].(string); ok && value != "" {
			result.Head = value
			break
		}
	}
	if value, ok := input["branch"].(string); ok {
		result.Branch = value
	}
	if result.Head == "" && result.Branch == "" {
		return nil
	}
	return result
}

func findArtifact(root, suffix string) (string, error) {
	matches, err := findArtifacts(root, suffix)
	if err != nil {
		return "", err
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("expected exactly one Codex artifact, found %d", len(matches))
	}
	return matches[0], nil
}

func findArtifacts(root, suffix string) ([]string, error) {
	matches := make([]string, 0, 1)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), suffix) {
			matches = append(matches, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return matches, nil
}

func defaultConfigRoot() string {
	if root := os.Getenv("CODEX_HOME"); root != "" {
		return root
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".codex"
	}
	return filepath.Join(home, ".codex")
}

func unixTimestamp(value int64) string {
	if value <= 0 {
		return ""
	}
	return time.Unix(value, 0).UTC().Format(time.RFC3339Nano)
}
