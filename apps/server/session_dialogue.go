package main

import (
	"strings"

	"github.com/xfey/coconet/packages/contracts"
)

type visibleDialogue struct {
	text   string
	ranges []contracts.TextRange // nil means a legacy plain message
}

func visibleTurnDialogue(turn contracts.NormalizedTurn, visible map[visibleTextIdentity][]contracts.TextRange) (visibleDialogue, bool, bool) {
	if turn.Role != "user" && turn.Role != "assistant" || turn.Kind == "event" {
		return visibleDialogue{}, false, false
	}
	if ranges, known := visible[visibleTurnKey(turn)]; known {
		for _, span := range ranges {
			if strings.TrimSpace(turn.Text[span.Start:span.End]) != "" {
				return visibleDialogue{turn.Text, ranges}, false, false
			}
		}
		return visibleDialogue{}, false, false
	}
	if strings.TrimSpace(turn.Text) == "" {
		return visibleDialogue{}, false, turn.Kind != "message" || turn.ToolName != ""
	}
	// Old normalized messages have no reliable block boundaries. Mixed tool
	// envelopes and summaries are omitted; ordinary message text is a marked
	// fallback, never a claim of precise native visibility.
	if turn.Kind == "message" && turn.ToolName == "" {
		return visibleDialogue{text: turn.Text}, true, false
	}
	return visibleDialogue{}, false, true
}

func (dialogue visibleDialogue) size() int {
	if dialogue.ranges == nil {
		return len(dialogue.text)
	}
	n := len(dialogue.ranges) - 1
	for _, span := range dialogue.ranges {
		n += span.End - span.Start
	}
	return n
}

// Slice the virtual visible string without allocating the excluded tool text
// or joining an unbounded message. Windows are repaired at UTF-8 boundaries.
func (dialogue visibleDialogue) window(start, end int) string {
	if dialogue.ranges == nil {
		return dialogue.text[start:end]
	}
	var result strings.Builder
	result.Grow(end - start)
	offset := 0
	appendPart := func(part string) {
		lo, hi := max(0, start-offset), min(len(part), end-offset)
		if lo < hi {
			result.WriteString(part[lo:hi])
		}
		offset += len(part)
	}
	for i, span := range dialogue.ranges {
		if i > 0 {
			appendPart("\n")
		}
		appendPart(dialogue.text[span.Start:span.End])
		if offset >= end {
			break
		}
	}
	return result.String()
}
