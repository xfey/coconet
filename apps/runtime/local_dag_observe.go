package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xfey/coconet/adapters"
	"github.com/xfey/coconet/adapters/claude"
	"github.com/xfey/coconet/adapters/codex"
	"github.com/xfey/coconet/adapters/cursor"
	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/localdag"
	"github.com/xfey/coconet/packages/sessionartifact"
	"github.com/xfey/coconet/packages/workgraph"
)

type localDAGObservationRequest struct {
	ProjectID      string              `json:"project_id"`
	Agent          contracts.AgentType `json:"agent"`
	NativeID       string              `json:"native_id"`
	Root           string              `json:"root"`
	CWD            string              `json:"cwd"`
	ConfigRoot     string              `json:"config_root"`
	TranscriptPath string              `json:"transcript_path,omitempty"`
	Event          string              `json:"event,omitempty"`
	EmptyStart     bool                `json:"empty_start,omitempty"`
	ObservedAt     string              `json:"observed_at,omitempty"`
}

func prepareLocalDAGObservation(ctx context.Context, root string, engine *localdag.Engine, request localDAGObservationRequest, configured adapters.Adapter) (localdag.Observation, error) {
	if request.ProjectID == "" || !adapters.ValidNativeSessionID(request.NativeID) || !filepath.IsAbs(request.Root) || !filepath.IsAbs(request.CWD) || !filepath.IsAbs(request.ConfigRoot) || !pathContains(request.Root, request.CWD) {
		return localdag.Observation{}, errors.New("native observation requires a fixed Session, source configuration and workspace boundary")
	}
	adapter := configured
	if adapter == nil {
		for _, candidate := range runtimeSessionAdapters() {
			if candidate.Type() == request.Agent {
				adapter = candidate
				break
			}
		}
	}
	switch source := adapter.(type) {
	case codex.Adapter:
		source.ConfigRoot = request.ConfigRoot
		adapter = source
	case cursor.Adapter:
		source.ConfigRoot = request.ConfigRoot
		adapter = source
	case claude.Adapter:
		source.ConfigRoot = request.ConfigRoot
		source.TranscriptPath = request.TranscriptPath
		adapter = source
	}
	if adapter == nil || adapter.Type() != request.Agent {
		return localdag.Observation{}, errors.New("unsupported observation Agent")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	baseVersion := 0
	if engine != nil && engine.Store != nil {
		baseline, err := engine.Store.State(request.ProjectID)
		if err != nil {
			return localdag.Observation{}, err
		}
		baseVersion = baseline.Sessions[localdag.ObservationSessionID(request.ProjectID, engine.Actor, string(request.Agent), request.NativeID)].Latest
	}
	observation, _, err := adapter.Observe(ctx, request.CWD, request.NativeID, adapters.ObservationCheckpoint{})
	if err != nil {
		if errors.Is(err, adapters.ErrNativeSessionMissing) {
			empty, checkErr := confirmedEmptySessionEnd(root, engine, request)
			if checkErr != nil {
				return localdag.Observation{}, errors.Join(err, checkErr)
			}
			if empty {
				return localdag.Observation{}, errEmptyNativeSession
			}
		}
		return localdag.Observation{}, err
	}
	if observation.RawArtifact.Temporary {
		defer os.Remove(observation.RawArtifact.Path)
	}
	if len(observation.Normalized.Turns) == 0 && request.EmptyStart && strings.EqualFold(request.Event, "SessionEnd") {
		empty, err := confirmedEmptySessionEnd(root, engine, request)
		if err != nil {
			return localdag.Observation{}, err
		}
		if empty {
			return localdag.Observation{}, errEmptyNativeSession
		}
	}
	if observation.Session.NativeSessionID != request.NativeID || observation.Session.AgentType != request.Agent || !pathContains(request.Root, observation.Session.CWD) {
		return localdag.Observation{}, errors.New("native observation identity or workspace escaped its connected source")
	}
	if observation.VisibleText == nil {
		return localdag.Observation{}, errors.New("native adapter lacks the required visible-text projection")
	}
	if err := localdag.ValidateObservationText(observation.Normalized, observation.VisibleText); err != nil {
		return localdag.Observation{}, err
	}
	if engine == nil || engine.Store == nil {
		return localdag.Observation{}, errors.New("native capture requires a local store")
	}
	release := engine.Store.PinContent()
	defer release()
	// A late SessionEnd must never close a Session that already resumed.
	ended := observation.Session.ActivityState == contracts.ActivityEnded
	if strings.EqualFold(request.Event, "SessionEnd") {
		at, err := time.Parse(time.RFC3339Nano, request.ObservedAt)
		if err != nil {
			return localdag.Observation{}, errors.New("SessionEnd observation time is invalid")
		}
		// Codex does not supply LastActivityAt. Its fixed artifact (including
		// inherited history bundles) preserves the native child's write time.
		// Prefer the adapter's stable activity evidence when available, since
		// some hosts append bookkeeping after the SessionEnd hook.
		activity, err := time.Parse(time.RFC3339Nano, observation.Session.LastActivityAt)
		if err != nil {
			info, err := os.Stat(observation.RawArtifact.Path)
			if err != nil {
				return localdag.Observation{}, err
			}
			activity = info.ModTime()
		}
		if !activity.After(at) {
			ended = true
		}
	}
	copy, err := copyLocalObservationArtifact(root, observation.RawArtifact)
	if err != nil {
		return localdag.Observation{}, err
	}
	defer os.Remove(copy)
	artifact, err := engine.Store.SaveArtifact(copy, observation.RawArtifact.MediaType)
	if err != nil {
		return localdag.Observation{}, err
	}
	// Queue only actual dialogue. Native tools and nested opaque payloads stay
	// in the compressed artifact, never in the portable text sidecar.
	byID := map[string][]contracts.TextRange{}
	for _, turn := range observation.VisibleText.Turns {
		byID[turn.TurnID] = turn.Ranges
	}
	turns := []contracts.NormalizedTurn{}
	for _, turn := range observation.Normalized.Turns {
		if turn.Role != "user" && turn.Role != "assistant" || turn.Kind == "event" {
			continue
		}
		text := adapters.VisibleText(turn.Text, byID[turn.TurnID])
		if strings.TrimSpace(text) == "" {
			continue
		}
		turns = append(turns, contracts.NormalizedTurn{TurnID: turn.TurnID, Ordinal: len(turns), Role: turn.Role, Kind: "message", Text: text, ContentHash: adapters.Digest(turn.Role + "\x00" + text), OccurredAt: turn.OccurredAt})
	}
	normalized, _ := adapters.ApplyCheckpoint(turns, adapters.ObservationCheckpoint{})
	visible := &contracts.VisibleTextProjection{Version: 1, SourceCursor: normalized.ToCursor, Turns: []contracts.VisibleTextTurn{}}
	for _, turn := range turns {
		visible.Turns = append(visible.Turns, contracts.VisibleTextTurn{TurnID: turn.TurnID, ContentHash: turn.ContentHash, Ranges: []contracts.TextRange{{Start: 0, End: len(turn.Text)}}})
	}
	if err := rememberLocalInferenceSource(root, engine, request); err != nil {
		return localdag.Observation{}, err
	}
	return localdag.Observation{ActiveHistory: observation.ActiveHistory, BaseVersion: &baseVersion, ProjectID: request.ProjectID, Agent: string(request.Agent), NativeID: request.NativeID, Normalized: normalized, Visible: visible, ArtifactManifest: artifact, Ended: ended}, nil
}

func copyLocalObservationArtifact(root string, observation contracts.ArtifactObservation) (string, error) {
	if observation.Path == "" || observation.Size <= 0 || observation.Size > sessionartifact.MaxArtifactSize {
		return "", errors.New("invalid native artifact size")
	}
	source, err := os.Open(observation.Path)
	if err != nil {
		return "", err
	}
	defer source.Close()
	file, err := os.CreateTemp(root, ".native-copy-*")
	if err != nil {
		return "", err
	}
	path := file.Name()
	ok := false
	defer func() {
		file.Close()
		if !ok {
			os.Remove(path)
		}
	}()
	if _, err := io.Copy(file, io.LimitReader(source, observation.Size+1)); err != nil {
		return "", err
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	digest, size, err := adapters.HashFile(path)
	if err != nil {
		return "", err
	}
	if digest != observation.SHA256 || size != observation.Size {
		return "", errors.New("native artifact changed during fixed capture")
	}
	ok = true
	return path, nil
}

func observeLocalDAG(ctx context.Context, root string, engine *localdag.Engine, request localDAGObservationRequest) (workgraph.Source, error) {
	unlock, err := acquireLocalFileLock(ctx, filepath.Join(root, "inbox-processing.lock"))
	if err != nil {
		return workgraph.Source{}, err
	}
	defer unlock()
	blocked, err := localHistoryBlocksCapture(ctx, root, engine, request)
	if err != nil {
		return workgraph.Source{}, err
	}
	if blocked {
		return workgraph.Source{}, errors.New("this Session has an unfinished history import; resume or retry that plan first")
	}
	observation, err := prepareLocalDAGObservation(ctx, root, engine, request, nil)
	if err != nil {
		return workgraph.Source{}, err
	}
	return engine.CaptureObservation(ctx, observation)
}

func captureLocalDAG(ctx context.Context, root string, engine *localdag.Engine, capture localdag.Capture) (workgraph.Source, error) {
	unlock, err := acquireLocalFileLock(ctx, filepath.Join(root, "inbox-processing.lock"))
	if err != nil {
		return workgraph.Source{}, err
	}
	defer unlock()
	blocked, err := localHistoryBlocksCapture(ctx, root, engine, localDAGObservationRequest{ProjectID: capture.ProjectID, Agent: contracts.AgentType(capture.Agent), NativeID: capture.NativeID})
	if err != nil {
		return workgraph.Source{}, err
	}
	if blocked {
		return workgraph.Source{}, errors.New("this Session has an unfinished history import; resume or retry that plan first")
	}
	return engine.Capture(ctx, capture)
}
