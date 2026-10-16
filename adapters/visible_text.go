package adapters

import (
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/xfey/coconet/packages/contracts"
)

// ValidateVisibleText validates provenance and byte boundaries without changing
// the canonical transcript, content hashes, or normalization checkpoint.
func ValidateVisibleText(projection *contracts.VisibleTextProjection, turns []contracts.NormalizedTurn, cursor string) error {
	if projection == nil {
		return nil
	}
	if projection.Version != 1 || projection.SourceCursor != cursor || projection.Turns == nil || len(projection.Turns) > len(turns) {
		return errors.New("invalid visible text projection boundary")
	}
	byID := make(map[string]contracts.NormalizedTurn, len(turns))
	for _, turn := range turns {
		byID[turn.TurnID] = turn
	}
	previous := -1
	for _, visible := range projection.Turns {
		turn, exists := byID[visible.TurnID]
		if !exists || turn.Ordinal <= previous || visible.ContentHash != turn.ContentHash || (turn.Role != "user" && turn.Role != "assistant") || turn.Kind == "event" || len(visible.Ranges) == 0 {
			return errors.New("visible text does not identify an ordered conversation turn")
		}
		previous = turn.Ordinal
		end := -1
		for _, span := range visible.Ranges {
			if span.Start < 0 || span.Start < end || span.End <= span.Start || span.End > len(turn.Text) || !utf8.ValidString(turn.Text[span.Start:span.End]) {
				return errors.New("invalid visible text byte range")
			}
			end = span.End
		}
	}
	return nil
}

func VisibleText(text string, ranges []contracts.TextRange) string {
	if len(ranges) == 1 {
		return text[ranges[0].Start:ranges[0].End]
	}
	var result strings.Builder
	for i, span := range ranges {
		if i > 0 {
			result.WriteByte('\n')
		}
		result.WriteString(text[span.Start:span.End])
	}
	return result.String()
}
