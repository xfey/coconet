package localdag

import (
	"context"
	"errors"
	"strings"

	"github.com/xfey/coconet/adapters"
	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/conversation"
	"github.com/xfey/coconet/packages/workgraph"
)

// Observation is a complete, fixed native observation. Its original file has
// already been copied into the local artifact manifest. Paths and tool bodies
// never enter the portable conversation or the model projection.
type Observation struct {
	ActiveHistory    *contracts.NativeActiveHistory   `json:"active_history,omitempty"`
	BaseVersion      *int                             `json:"base_version,omitempty"`
	ProjectID        string                           `json:"project_id"`
	Agent            string                           `json:"agent"`
	NativeID         string                           `json:"native_id"`
	Normalized       contracts.NormalizedObservation  `json:"normalized"`
	Visible          *contracts.VisibleTextProjection `json:"visible"`
	ArtifactManifest string                           `json:"artifact_manifest"`
	Ended            bool                             `json:"ended"`
	Inherited        bool                             `json:"inherited,omitempty"`
}

func ObservationSessionID(project, actor, agent, native string) string {
	return "session_" + workgraph.Digest([]string{project, actor, agent, native})[:32]
}

// ValidateObservationText must precede every use of visible byte ranges. A
// duplicate native ID could otherwise validate against a different message.
func ValidateObservationText(normalized contracts.NormalizedObservation, visible *contracts.VisibleTextProjection) error {
	if normalized.FromCursor != "" || visible == nil {
		return errors.New("capture requires a complete native observation and explicit visible-text projection")
	}
	ids := map[string]bool{}
	for i, turn := range normalized.Turns {
		if turn.TurnID == "" || len(turn.TurnID) > 500 || ids[turn.TurnID] || turn.Ordinal != i {
			return errors.New("native observation identities or ordering are invalid")
		}
		ids[turn.TurnID] = true
	}
	return adapters.ValidateVisibleText(visible, normalized.Turns, normalized.ToCursor)
}

func (e *Engine) CaptureObservation(ctx context.Context, observation Observation) (workgraph.Source, error) {
	return e.captureObservation(ctx, observation, false)
}

// QueueInheritedObservation durably fixes a native Fork's inherited boundary
// without contacting Relay. The existing outbox publishes it before later
// observations of this child can be accepted as new work.
func (e *Engine) QueueInheritedObservation(ctx context.Context, observation Observation) (workgraph.Source, error) {
	if !observation.Inherited {
		return workgraph.Source{}, errors.New("native Fork initialization requires an inherited observation")
	}
	return e.captureObservation(ctx, observation, true)
}

func (e *Engine) captureObservation(ctx context.Context, observation Observation, deferred bool) (workgraph.Source, error) {
	release := e.Store.PinContent()
	defer release()
	e.commitMu.Lock()
	defer e.commitMu.Unlock()
	if err := ValidateObservationText(observation.Normalized, observation.Visible); err != nil {
		return workgraph.Source{}, err
	}
	state, err := e.Store.State(observation.ProjectID)
	if err != nil {
		return workgraph.Source{}, err
	}
	sessionID := ObservationSessionID(observation.ProjectID, e.Actor, observation.Agent, observation.NativeID)
	var base []conversation.Entry
	if session := state.Sessions[sessionID]; session.Latest > 0 {
		prior := state.Snapshots[(workgraph.Source{SessionID: sessionID, Version: session.Latest}).Key()]
		base, err = readEntries(e.Store, prior, 0, prior.Turns)
		if err != nil {
			return workgraph.Source{}, err
		}
		if observation.ActiveHistory == nil && prior.Turns > 0 && len(observation.Normalized.Turns) == 0 {
			return workgraph.Source{}, errors.New("populated native Session unexpectedly returned an empty observation")
		}
	}
	entries, err := appendVisibleObservation(base, observation.Normalized.Turns, observation.Visible)
	if observation.ActiveHistory != nil {
		entries, err = activeVisibleObservation(base, observation.Normalized.Turns, observation.Visible, observation.ActiveHistory)
	}
	if err != nil {
		return workgraph.Source{}, err
	}
	if observation.BaseVersion != nil && *observation.BaseVersion != state.Sessions[sessionID].Latest {
		previous := state.Snapshots[(workgraph.Source{SessionID: sessionID, Version: state.Sessions[sessionID].Latest}).Key()]
		if workgraph.Digest(entries) != workgraph.Digest(base) || previous.Artifact != observation.ArtifactManifest || previous.Ended != observation.Ended {
			return workgraph.Source{}, ErrObservationSuperseded
		}
	}
	if observation.Inherited {
		if len(base) != 0 {
			// A repeated initialization must be identical; it may not absorb new
			// work that arrived after the native fork was created.
			if len(entries) != len(base) {
				return workgraph.Source{}, errors.New("inherited initialization cannot include later work")
			}
			for _, entry := range base {
				if !entry.Inherited {
					return workgraph.Source{}, errors.New("Session already contains its own work")
				}
			}
		}
		for i := range entries {
			entries[i].Inherited = true
		}
	}
	return e.captureMode(ctx, Capture{ProjectID: observation.ProjectID, Agent: observation.Agent, NativeID: observation.NativeID, Entries: entries, ArtifactManifest: observation.ArtifactManifest, Ended: observation.Ended, Inherited: observation.Inherited, Authoritative: observation.ActiveHistory != nil}, deferred)
}

// Stable identities include only visible content. Tool-only updates and native
// compaction cannot rewrite the cumulative prefix or make old text disappear.
// A revised native message is appended as new evidence with its native anchor.
func appendVisibleObservation(base []conversation.Entry, turns []contracts.NormalizedTurn, projection *contracts.VisibleTextProjection) ([]conversation.Entry, error) {
	result := append([]conversation.Entry(nil), base...)
	seen := map[string]bool{}
	for _, entry := range base {
		seen[entry.Turn.TurnID] = true
	}
	ranges := map[string][]contracts.TextRange{}
	for _, visible := range projection.Turns {
		ranges[visible.TurnID] = visible.Ranges
	}
	nativeIDs := map[string]bool{}
	for i, turn := range turns {
		if turn.TurnID == "" || nativeIDs[turn.TurnID] || turn.Ordinal != i {
			return nil, errors.New("native observation identities or ordering are invalid")
		}
		nativeIDs[turn.TurnID] = true
		if turn.Role != "user" && turn.Role != "assistant" || turn.Kind == "event" {
			continue
		}
		text := adapters.VisibleText(turn.Text, ranges[turn.TurnID])
		if strings.TrimSpace(text) == "" {
			continue
		}
		id := "turn_" + workgraph.Digest([]string{turn.TurnID, turn.Role, text})[:40]
		if seen[id] {
			continue
		}
		clean := contracts.NormalizedTurn{TurnID: id, SourceTurnID: turn.TurnID, Ordinal: len(result), Role: turn.Role, Kind: "message", Text: text, ContentHash: adapters.Digest(turn.Role + "\x00" + text), OccurredAt: turn.OccurredAt}
		result = append(result, conversation.Entry{Turn: clean, Visible: &text})
		seen[id] = true
	}
	return result, nil
}

// An authoritative branch selects old content by native identity, never by a
// text similarity heuristic. Compacted ancestors may use their fixed local
// text; absent native messages are withdrawn, and edited messages replace the
// previous content at that identity instead of being appended after it.
func activeVisibleObservation(base []conversation.Entry, turns []contracts.NormalizedTurn, projection *contracts.VisibleTextProjection, active *contracts.NativeActiveHistory) ([]conversation.Entry, error) {
	ids := map[string]bool{}
	for _, id := range active.IDs {
		if id == "" || len(id) > 500 || ids[id] {
			return nil, errors.New("invalid active native history")
		}
		ids[id] = true
	}
	current, err := appendVisibleObservation(nil, turns, projection)
	if err != nil {
		return nil, err
	}
	entries := map[string]conversation.Entry{}
	previous := map[string]conversation.Entry{}
	for _, entry := range base {
		entries[entry.Turn.SourceTurnID] = entry
		previous[entry.Turn.SourceTurnID] = entry
	}
	// Only omitted compacted ancestors may reuse old text. A returned native
	// message that is now empty, synthetic or tool-only withdraws its old text.
	for _, turn := range turns {
		delete(entries, turn.TurnID)
	}
	for _, entry := range current {
		id := entry.Turn.SourceTurnID
		if !ids[id] {
			return nil, errors.New("visible message is outside the active native branch")
		}
		if old, ok := previous[id]; ok && old.Turn.TurnID == entry.Turn.TurnID {
			entry = old
		}
		entries[id] = entry
	}
	result := []conversation.Entry{}
	for _, id := range active.IDs {
		if entry, ok := entries[id]; ok {
			entry.Turn.Ordinal = len(result)
			result = append(result, entry)
		}
	}
	return result, nil
}
