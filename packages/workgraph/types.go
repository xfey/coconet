// Package workgraph applies versioned local DAG decisions deterministically.
package workgraph

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/xfey/coconet/packages/contracts"
)

const SchemaVersion = 1

var (
	ErrConflict   = errors.New("work graph baseline changed")
	ErrSource     = errors.New("work graph source is missing or changed")
	ErrPermission = errors.New("work graph writer does not own this Session")
)

type Source struct {
	SessionID string `json:"session_id"`
	Version   int    `json:"version"`
}

func (s Source) Key() string { return fmt.Sprintf("%s/%d", s.SessionID, s.Version) }

type Snapshot struct {
	Source
	InheritedTurns int                  `json:"inherited_turns,omitempty"`
	Rewrite        *ConversationRewrite `json:"rewrite,omitempty"`
	Superseded     bool                 `json:"superseded,omitempty"`
	Owner          string               `json:"owner"`
	Agent          string               `json:"agent"`
	NativeID       string               `json:"native_id"`
	Digest         string               `json:"digest"`
	Conversation   string               `json:"conversation"`
	Artifact       string               `json:"artifact,omitempty"` // compressed native manifest object
	Turns          int                  `json:"turns"`
	Ended          bool                 `json:"ended"`
	Inherited      bool                 `json:"inherited,omitempty"` // first native Fork snapshot, already consumed
	ObservedAt     string               `json:"observed_at"`
}

// ConversationRewrite replaces a native branch without reusing version numbers.
// CommonTurns is the verified visible prefix shared with PreviousVersion.
type ConversationRewrite struct {
	PreviousVersion int `json:"previous_version"`
	CommonTurns     int `json:"common_turns"`
	// Explicit opt-in preserves the exact state digest of pre-fix commits.
	RestoreEditableTails bool `json:"restore_editable_tails,omitempty"`
}

// StageCheckpoint is small derived state on a retained node, not a history of
// every iteration or a second copy of the native Session.
type StageCheckpoint struct {
	Items []Item `json:"items"`
}

type Text struct {
	Objective string `json:"objective"`
	Summary   string `json:"summary,omitempty"`
	Approach  string `json:"approach,omitempty"`
}

type Item struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type ItemChanges struct {
	Add      []Item   `json:"add,omitempty"`
	Revise   []Item   `json:"revise,omitempty"`
	Complete []string `json:"complete,omitempty"`
	Withdraw []string `json:"withdraw,omitempty"`
}

type Session struct {
	ID               string `json:"id"`
	Owner            string `json:"owner"`
	Agent            string `json:"agent"`
	NativeID         string `json:"native_id"`
	Latest           int    `json:"latest"`
	ProcessedThrough int    `json:"processed_through"`
	ConsumedTurns    int    `json:"consumed_turns"`
	Tail             string `json:"tail,omitempty"`
	Revision         int64  `json:"revision"`
	Items            []Item `json:"items"`
}

type Edge struct {
	Parent   string   `json:"parent"`
	Source   Source   `json:"source"`
	Reason   string   `json:"reason"`
	Evidence []string `json:"evidence"`
	RunID    string   `json:"run_id"`
}

type Node struct {
	Checkpoint *StageCheckpoint `json:"checkpoint,omitempty"`
	ID         string           `json:"id"`
	Text
	Source          Source `json:"source"`
	State           string `json:"state"`
	Revision        int64  `json:"revision"`
	StartTurn       int    `json:"start_turn"`
	EndTurn         int    `json:"end_turn"`
	Parents         []Edge `json:"parents"`
	Relations       string `json:"relations"`
	RelationThrough int    `json:"relation_through"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
	RunID           string `json:"run_id"`
}

type State struct {
	// Fixed display/source selections are independent of mutable author nodes.
	// Omitted on older states so their accepted digests remain replayable.
	LibrarySources map[string]Node     `json:"library_sources,omitempty"`
	Schema         int                 `json:"schema"`
	ProjectID      string              `json:"project_id"`
	Revision       int64               `json:"revision"`
	Sessions       map[string]Session  `json:"sessions"`
	Snapshots      map[string]Snapshot `json:"snapshots"`
	Nodes          map[string]Node     `json:"nodes"`
	Library        map[string]string   `json:"library"`
}

func New(project string) State {
	return State{Schema: SchemaVersion, ProjectID: project, Sessions: map[string]Session{}, Snapshots: map[string]Snapshot{}, Nodes: map[string]Node{}, Library: map[string]string{}}
}

// StageDecision is model semantics plus identities assigned by the caller.
// Continue with no Text explicitly confirms continuity without rewriting text.
type StageDecision struct {
	Checkpoint      bool        `json:"checkpoint,omitempty"`
	Source          Source      `json:"source"`
	SessionRevision int64       `json:"session_revision"`
	Action          string      `json:"action"` // continue, start, none; needs_context is never committed
	Text            *Text       `json:"text,omitempty"`
	NewNodeID       string      `json:"new_node_id,omitempty"`
	Items           ItemChanges `json:"items"`
	Reason          string      `json:"reason"`
	Evidence        []string    `json:"evidence,omitempty"`
}

type Parent struct {
	NodeID   string   `json:"node_id"`
	Revision int64    `json:"revision"`
	Source   Source   `json:"source"`
	Reason   string   `json:"reason"`
	Evidence []string `json:"evidence"`
}

type RelationDecision struct {
	NodeID       string   `json:"node_id"`
	NodeRevision int64    `json:"node_revision"`
	Through      int      `json:"through"`
	Parents      []Parent `json:"parents"`
	Complete     bool     `json:"complete"`
	Reason       string   `json:"reason"`
}

type LibraryChange struct {
	Pin    bool    `json:"pin,omitempty"`
	NodeID string  `json:"node_id"`
	Source *Source `json:"source,omitempty"`
	Note   string  `json:"note"`
	Remove bool    `json:"remove"`
}

// Retirement removes only unreferenced, consumed source descriptors. Session
// version counters remain monotonic, so an absent old source cannot reappear.
type Retirement struct {
	Sources []Source `json:"sources"`
}

// Commit is the saved, replayable unit. Exactly one payload must be present.
type Commit struct {
	Processing *contracts.ProcessingAuthorization `json:"processing,omitempty"`
	Schema     int                                `json:"schema"`
	ID         string                             `json:"id"`
	ProjectID  string                             `json:"project_id"`
	Actor      string                             `json:"actor"`
	Base       int64                              `json:"base"`
	RunID      string                             `json:"run_id"`
	At         string                             `json:"at"`
	Snapshot   *Snapshot                          `json:"snapshot,omitempty"`
	Stage      *StageDecision                     `json:"stage,omitempty"`
	Relations  *RelationDecision                  `json:"relations,omitempty"`
	Library    *LibraryChange                     `json:"library,omitempty"`
	Retire     *Retirement                        `json:"retire,omitempty"`
}

type Change struct {
	CommitID    string   `json:"commit_id"`
	RunID       string   `json:"run_id"`
	Before      int64    `json:"before"`
	After       int64    `json:"after"`
	Nodes       []string `json:"nodes"`
	Sessions    []string `json:"sessions"`
	StateDigest string   `json:"state_digest"`
}

func Digest(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	} // all graph values are JSON-only; callers cannot supply arbitrary objects
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
