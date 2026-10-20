// Package localdag constructs minimal model inputs and drives independently
// durable stage and relation processing on a local replica.
package localdag

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/xfey/coconet/packages/conversation"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/inference"
	"github.com/xfey/coconet/packages/workgraph"
	"github.com/xfey/coconet/prompts"
)

type turn struct {
	ID           string `json:"id"`
	Role         string `json:"role"`
	Text         string `json:"text"`
	Context      bool   `json:"context,omitempty"`
	CurrentStage bool   `json:"current_stage,omitempty"`
	Inherited    bool   `json:"inherited,omitempty"`
	Truncated    bool   `json:"text_truncated,omitempty"`
}
type evidence struct {
	Scope     string `json:"scope"`
	Omissions bool   `json:"has_omissions,omitempty"`
	Turns     []turn `json:"turns"`
}
type previous struct {
	Stage *workgraph.Text  `json:"stage"`
	State string           `json:"state"`
	Items []workgraph.Item `json:"open_items"`
}
type stageInput struct {
	Previous previous `json:"previous"`
	Evidence evidence `json:"evidence"`
}
type candidate struct {
	ID string `json:"id"`
	workgraph.Text
}
type relationInput struct {
	Current            workgraph.Text `json:"current"`
	Existing           []string       `json:"existing_parents"`
	Candidates         []candidate    `json:"candidates"`
	CandidateOmissions bool           `json:"candidate_omissions,omitempty"`
	Evidence           evidence       `json:"evidence"`
}

// Audit stays outside the model input. Aliases map to the fixed private state,
// including candidate revisions/sources required when validating a decision.
type candidateAudit struct {
	ID       string           `json:"id"`
	Revision int64            `json:"revision"`
	Source   workgraph.Source `json:"source"`
}

type Audit struct {
	From             int                       `json:"from"`
	Through          int                       `json:"through"`
	Omitted          int                       `json:"omitted"`
	Truncated        int                       `json:"truncated"`
	CandidateCount   int                       `json:"candidate_count"`
	CandidateOmitted int                       `json:"candidate_omitted"`
	Items            map[string]string         `json:"items,omitempty"`
	Candidates       map[string]candidateAudit `json:"candidates,omitempty"`
}
type prepared struct {
	Execution       any               `json:"execution,omitempty"`
	Request         inference.Request `json:"request"`
	Audit           Audit             `json:"audit"`
	SessionRevision int64             `json:"session_revision"`
	NodeRevision    int64             `json:"node_revision,omitempty"`
	Source          workgraph.Source  `json:"source"`
	Anchors         map[string]bool   `json:"anchors"` // value: substantive new, non-inherited evidence
	Complete        bool              `json:"complete"`
}

func SaveConversation(store *graphstore.Store, entries []conversation.Entry) (string, error) {
	value, err := conversation.Build(entries)
	if err != nil {
		return "", err
	}
	for digest, data := range value.Objects {
		stored, err := store.PutObject(data)
		if err != nil {
			return "", err
		}
		if stored != digest {
			return "", errors.New("conversation object digest mismatch")
		}
	}
	data, err := json.Marshal(value.Manifest)
	if err != nil {
		return "", err
	}
	return store.PutObject(data)
}

func readEntries(store *graphstore.Store, snapshot workgraph.Snapshot, start, end int) ([]conversation.Entry, error) {
	data, err := store.Object(snapshot.Conversation)
	if err != nil {
		return nil, err
	}
	var manifest conversation.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}
	if manifest.Count != snapshot.Turns {
		return nil, errors.New("snapshot conversation count differs")
	}
	return conversation.Read(manifest, start, end, func(block conversation.Block) ([]byte, error) { return store.Object(block.SHA256) })
}

func makeEvidence(entries []conversation.Entry, from int, cumulative bool, stageStart int) (evidence, Audit, map[string]bool) {
	ev := evidence{Scope: "delta", Turns: []turn{}}
	if cumulative {
		ev.Scope = "cumulative"
	}
	audit := Audit{From: from}
	anchors := map[string]bool{}
	for _, entry := range entries {
		t := entry.Turn
		if (t.Role != "user" && t.Role != "assistant") || t.Kind == "tool_result" || t.Kind == "event" {
			continue
		}
		text := t.Text
		if entry.Visible != nil {
			text = *entry.Visible
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		row := turn{ID: fmt.Sprintf("e%d", t.Ordinal+1), Role: t.Role, Text: text, Context: t.Ordinal < from, Inherited: entry.Inherited}
		// Only relations distinguish earlier iterations of this node from
		// preceding stages. Stage extraction passes -1 and remains unchanged.
		if stageStart >= 0 && row.Context && !row.Inherited {
			row.CurrentStage = t.Ordinal >= stageStart
		}
		// Imported entries retain explicit inherited provenance when supplied by
		// adapters; native IDs and paths are never used as model-visible anchors.
		ev.Turns = append(ev.Turns, row)
		anchors[row.ID] = !row.Context && !row.Inherited
	}
	ev.Omissions = audit.Omitted > 0
	return ev, audit, anchors
}
func clip(text string, n int) string {
	if len(text) <= n {
		return text
	}
	for n > 0 && !utf8.RuneStart(text[n]) {
		n--
	}
	return text[:n]
}

func prepareStage(store *graphstore.Store, state workgraph.State, source workgraph.Source, expanded bool) (prepared, error) {
	session, ok := state.Sessions[source.SessionID]
	if !ok {
		return prepared{}, workgraph.ErrSource
	}
	snapshot, ok := state.Snapshots[source.Key()]
	if !ok || snapshot.Superseded || source.Version != session.ProcessedThrough+1 {
		return prepared{}, workgraph.ErrSource
	}
	from := session.ConsumedTurns
	start := max(0, from-6)
	if expanded {
		start = 0
	}
	entries, err := readEntries(store, snapshot, start, snapshot.Turns)
	if err != nil {
		return prepared{}, err
	}
	ev, audit, anchors := makeEvidence(entries, from, expanded || from == 0, -1)
	audit.Through = snapshot.Turns
	audit.Items = map[string]string{}
	prior := previous{Items: []workgraph.Item{}, State: "absent"}
	if node, ok := state.Nodes[session.Tail]; ok {
		t := node.Text
		prior.Stage = &t
		prior.State = node.State
	}
	for i, item := range session.Items {
		alias := fmt.Sprintf("i%d", i+1)
		prior.Items = append(prior.Items, workgraph.Item{ID: alias, Text: item.Text})
		audit.Items[alias] = item.ID
	}
	input, _ := json.Marshal(stageInput{Previous: prior, Evidence: ev})
	return prepared{Request: inference.Request{System: prompts.LocalStageSystem(), Input: input, Schema: stageSchema}, Audit: audit, Source: source, SessionRevision: session.Revision, Anchors: anchors, Complete: audit.Omitted == 0 && audit.Truncated == 0}, nil
}

func prepareRelations(store *graphstore.Store, state workgraph.State, id string, expanded bool) (prepared, error) {
	node, ok := state.Nodes[id]
	if !ok || node.State == "withdrawn" {
		return prepared{}, workgraph.ErrSource
	}
	snapshot, ok := state.Snapshots[node.Source.Key()]
	if !ok {
		return prepared{}, workgraph.ErrSource
	}
	// The current stage may start with "this example" or another reference to
	// the preceding stage. Keep that context even on the first relation pass,
	// but never allow it alone to prove new adoption in this node.
	from := max(node.StartTurn, node.RelationThrough)
	start := max(0, from-6)
	if expanded {
		start = max(0, node.StartTurn-6)
	}
	entries, err := readEntries(store, snapshot, start, node.EndTurn)
	if err != nil {
		return prepared{}, err
	}
	ev, audit, anchors := makeEvidence(entries, from, false, node.StartTurn)
	audit.Through = node.EndTurn
	audit.Candidates = map[string]candidateAudit{}
	input := relationInput{Current: node.Text, Existing: []string{}, Candidates: []candidate{}, Evidence: ev}
	ids := []string{}
	aliases := map[string]string{}
	for candidateID := range state.Nodes {
		if candidateID != id && state.Nodes[candidateID].State != "withdrawn" {
			ids = append(ids, candidateID)
		}
	}
	sort.Strings(ids)
	for i, candidateID := range ids {
		aliases[candidateID] = fmt.Sprintf("n%d", i+1)
	}
	for _, edge := range node.Parents {
		input.Existing = append(input.Existing, aliases[edge.Parent])
	}
	for _, candidateID := range ids {
		parent := state.Nodes[candidateID]
		// Descendants cannot become parents. Keep them out deterministically.
		if reaches(state, candidateID, id, map[string]bool{}) {
			continue
		}
		audit.CandidateCount++
		// Graph topology enforces eligibility above; it is not evidence that
		// the current conversation used a candidate's result.
		item := candidate{ID: aliases[candidateID], Text: parent.Text}
		input.Candidates = append(input.Candidates, item)
		audit.Candidates[item.ID] = candidateAudit{ID: parent.ID, Revision: parent.Revision, Source: parent.Source}
	}
	input.CandidateOmissions = audit.CandidateOmitted > 0
	data, _ := json.Marshal(input)
	return prepared{Request: inference.Request{System: prompts.LocalRelationsSystem(), Input: data, Schema: relationSchema}, Audit: audit, Source: node.Source, NodeRevision: node.Revision, Anchors: anchors, Complete: audit.Omitted == 0 && audit.Truncated == 0 && audit.CandidateOmitted == 0}, nil
}

func reaches(state workgraph.State, id, target string, seen map[string]bool) bool {
	if id == target {
		return true
	}
	if seen[id] {
		return false
	}
	seen[id] = true
	for _, edge := range state.Nodes[id].Parents {
		if reaches(state, edge.Parent, target, seen) {
			return true
		}
	}
	return false
}
