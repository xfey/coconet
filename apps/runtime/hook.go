package main

import (
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xfey/coconet/packages/contracts"
)

const maxHookInput = 1 << 20

type hookEvent struct {
	ConversationID string   `json:"conversation_id"`
	WorkspaceRoots []string `json:"workspace_roots"`
	CursorVersion  string   `json:"cursor_version"`
	HookEventName  string   `json:"hook_event_name"`
	EventName      string   `json:"event_name"`
	EventID        string   `json:"event_id"`
	SessionID      string   `json:"session_id"`
	ThreadID       string   `json:"thread_id"`
	CWD            string   `json:"cwd"`
	TranscriptPath string   `json:"transcript_path"`
	ObservedAt     string   `json:"observed_at"`
	Source         string   `json:"source"`
}

func runHook(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	// Inference threads are internal processing, never team Sessions. This also
	// protects against an older Agent invoking a hook despite its disabled flag.
	if os.Getenv("COCONET_INTERNAL_INFERENCE") == "1" {
		return 0
	}
	flags := flag.NewFlagSet("hook", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	agent := flags.String("agent", "", "codex, claude or cursor")
	skillPath := flags.String("skill-path", "", "installed Codex team-context Skill path (internal)")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return 0
	}
	if !contracts.ValidAgent(contracts.AgentType(*agent)) {
		return 0
	}
	var event hookEvent
	decoder := json.NewDecoder(io.LimitReader(stdin, maxHookInput))
	if err := decoder.Decode(&event); err != nil {
		return 0
	}
	// Cursor also discovers Claude plugins; their hooks must not misclassify
	// the same Cursor conversation as a second Claude session.
	if *agent != "cursor" && event.CursorVersion != "" {
		return 0
	}
	if *agent == "cursor" {
		if len(event.WorkspaceRoots) != 1 || !filepath.IsAbs(event.WorkspaceRoots[0]) {
			return 0
		}
		event.CWD = event.WorkspaceRoots[0]
		switch event.HookEventName {
		case "sessionStart":
			event.HookEventName, event.Source = "SessionStart", "startup"
		case "beforeSubmitPrompt":
			event.HookEventName = "UserPromptSubmit"
		case "sessionEnd":
			event.HookEventName = "SessionEnd"
		case "stop":
			event.HookEventName = "Stop"
		}
	}
	eventName := event.HookEventName
	if eventName == "" {
		eventName = event.EventName
	}
	if handled, err := enqueueManagedLocalHook(contracts.AgentType(*agent), event); handled {
		if err != nil {
			if root, rootErr := managedLocalDAGRoot(); rootErr == nil {
				_ = atomicWriteJSON(filepath.Join(root, "capture-error.json"), map[string]string{"error": err.Error(), "at": time.Now().UTC().Format(time.RFC3339Nano)}, 0600)
			}
		} else if strings.EqualFold(eventName, "SessionStart") || strings.EqualFold(eventName, "UserPromptSubmit") {
			writeManagedHookContext(contracts.AgentType(*agent), *skillPath, event, stdout)
		}
		return 0
	}
	return 0
}

func isSyncHookEvent(name string) bool {
	return strings.EqualFold(name, "Stop") || strings.EqualFold(name, "StopFailure") || strings.EqualFold(name, "SessionEnd")
}

func (event hookEvent) nativeID() string {
	if event.SessionID != "" {
		return event.SessionID
	}
	if event.ConversationID != "" {
		return event.ConversationID
	}
	return event.ThreadID
}
