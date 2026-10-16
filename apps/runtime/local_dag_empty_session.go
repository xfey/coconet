package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xfey/coconet/adapters"
	"github.com/xfey/coconet/adapters/cursor"
	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/localdag"
)

var errEmptyNativeSession = errors.New("session ended before any conversation; no content to capture")

func localDAGOriginPath(root, actor string, agent contracts.AgentType, profile, native string) string {
	key := adapters.Digest(actor + "\x00" + string(agent) + "\x00" + profile + "\x00" + native)
	return filepath.Join(root, "session-origins", key+".json")
}

func hookEventName(event hookEvent) string {
	if event.HookEventName != "" {
		return event.HookEventName
	}
	return event.EventName
}

func trackedEmptyStart(agent contracts.AgentType, profile string, event hookEvent) bool {
	if agent == contracts.AgentCursor && event.HookEventName == "SessionStart" && event.Source == "startup" {
		adapter := cursor.New("")
		adapter.ConfigRoot = profile
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()
		empty, err := adapter.EmptySession(ctx, event.CWD, event.nativeID())
		return err == nil && empty
	}
	if agent != contracts.AgentClaude || os.Getenv("COCONET_HOOK_LIFECYCLE") != "1" || hookEventName(event) != "SessionStart" || event.Source != "startup" {
		return false
	}
	if !filepath.IsAbs(event.TranscriptPath) || filepath.Base(event.TranscriptPath) != event.nativeID()+".jsonl" || !pathContains(filepath.Join(profile, "projects"), event.TranscriptPath) {
		return false
	}
	_, err := os.Lstat(event.TranscriptPath)
	return errors.Is(err, os.ErrNotExist)
}

func endsEmptyStart(event hookEvent) bool {
	switch hookEventName(event) {
	case "SessionStart", "UserPromptSubmit", "Stop", "StopFailure":
		return true
	}
	return false
}

// Absence alone is not evidence of an empty Session. Require a new Plugin's
// observed empty startup with no subsequent prompt/turn/resume, plus no local
// captured or pending history. Re-read the origin so late events cannot reuse
// an earlier empty state after the same Session has resumed.
func confirmedEmptySessionEnd(root string, engine *localdag.Engine, request localDAGObservationRequest) (bool, error) {
	if !request.EmptyStart || (request.Agent != contracts.AgentClaude && request.Agent != contracts.AgentCursor) || !strings.EqualFold(request.Event, "SessionEnd") {
		return false, nil
	}
	if request.Agent == contracts.AgentCursor {
		adapter := cursor.New("")
		adapter.ConfigRoot = request.ConfigRoot
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()
		empty, err := adapter.EmptySession(ctx, request.CWD, request.NativeID)
		if err != nil || !empty {
			return false, err
		}
	} else if !filepath.IsAbs(request.TranscriptPath) {
		return false, nil
	} else if _, err := os.Lstat(request.TranscriptPath); !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	data, err := readLocalPrivateFile(localDAGOriginPath(root, engine.Actor, request.Agent, request.ConfigRoot, request.NativeID), 64<<10, false)
	if err != nil {
		return false, err
	}
	var origin localDAGObservationRequest
	if err := decodeStrict(data, &origin); err != nil {
		return false, err
	}
	if !origin.EmptyStart || origin.ProjectID != request.ProjectID || origin.NativeID != request.NativeID || origin.ConfigRoot != request.ConfigRoot || origin.Agent != request.Agent {
		return false, nil
	}
	state, err := engine.Store.State(request.ProjectID)
	if err != nil {
		return false, err
	}
	if state.Sessions[localdag.ObservationSessionID(request.ProjectID, engine.Actor, string(request.Agent), request.NativeID)].Latest > 0 {
		return false, nil
	}
	pending, err := engine.Store.Pending(request.ProjectID)
	// A pending inherited baseline must never be discarded as an empty startup.
	return len(pending) == 0, err
}
