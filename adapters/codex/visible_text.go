package codex

import (
	"encoding/json"

	"github.com/xfey/coconet/packages/contracts"
)

// Walk in collectText's rendering order, but admit only the message's own
// text and top-level typed text blocks. Nested tools and unknown blocks still
// advance the byte offset in the unchanged normalized transcript.
func visibleItemRanges(raw json.RawMessage) []contracts.TextRange {
	var item map[string]any
	if json.Unmarshal(raw, &item) != nil {
		return nil
	}
	switch item["type"] {
	case "userMessage", "agentMessage", "plan":
	default:
		return nil
	}
	for _, key := range []string{"phase", "channel"} {
		if value, ok := item[key].(string); ok && value != "" && value != "commentary" && value != "final_answer" && value != "final" {
			return nil
		}
	}
	if recipient, ok := item["recipient"].(string); ok && recipient != "" && recipient != "all" {
		return nil
	}
	var ranges []contracts.TextRange
	offset, populated := 0, false
	appendText := func(text string, visible bool) {
		if text == "" {
			return
		}
		if populated {
			offset++
		}
		if visible {
			ranges = append(ranges, contracts.TextRange{Start: offset, End: offset + len(text)})
		}
		offset += len(text)
		populated = true
	}
	var walk func(any, int)
	walk = func(value any, depth int) {
		switch value := value.(type) {
		case string:
			appendText(value, depth == 1)
		case []any:
			for _, child := range value {
				walk(child, depth)
			}
		case map[string]any:
			for _, key := range []string{"text", "output", "aggregatedOutput"} {
				if text, ok := value[key].(string); ok {
					visible := key == "text" && (depth == 0 || depth == 1 && (value["type"] == "text" || value["type"] == "input_text" || value["type"] == "output_text"))
					appendText(text, visible)
				}
			}
			for _, key := range []string{"content", "fragments", "items"} {
				// Only content is a user-facing message body. The other fields
				// are retained by normalization but have no visibility contract.
				next := depth + 1
				if key != "content" {
					next = 2
				}
				walk(value[key], next)
			}
		}
	}
	walk(item, 0)
	return ranges
}
