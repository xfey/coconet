package adapters

import (
	"github.com/xfey/coconet/packages/contracts"
	"testing"
)

func TestVisibleTextValidatesSourceAndUTF8Boundaries(t *testing.T) {
	turn := contracts.NormalizedTurn{TurnID: "one", Role: "assistant", Kind: "tool_call", Text: "中文\ntool\n结论", ContentHash: Digest("fixture")}
	turns := []contracts.NormalizedTurn{turn}
	makeProjection := func() *contracts.VisibleTextProjection {
		return &contracts.VisibleTextProjection{Version: 1, SourceCursor: "cursor", Turns: []contracts.VisibleTextTurn{{TurnID: turn.TurnID, ContentHash: turn.ContentHash, Ranges: []contracts.TextRange{{Start: 0, End: 6}, {Start: 12, End: 18}}}}}
	}
	p := makeProjection()
	if err := ValidateVisibleText(p, turns, "cursor"); err != nil {
		t.Fatal(err)
	}
	if got := VisibleText(turn.Text, p.Turns[0].Ranges); got != "中文\n结论" {
		t.Fatal(got)
	}
	for name, mutate := range map[string]func(*contracts.VisibleTextProjection){
		"version":   func(p *contracts.VisibleTextProjection) { p.Version = 2 },
		"cursor":    func(p *contracts.VisibleTextProjection) { p.SourceCursor = "other" },
		"null":      func(p *contracts.VisibleTextProjection) { p.Turns = nil },
		"hash":      func(p *contracts.VisibleTextProjection) { p.Turns[0].ContentHash = "different" },
		"identity":  func(p *contracts.VisibleTextProjection) { p.Turns[0].TurnID = "missing" },
		"unicode":   func(p *contracts.VisibleTextProjection) { p.Turns[0].Ranges[0].Start = 1 },
		"overflow":  func(p *contracts.VisibleTextProjection) { p.Turns[0].Ranges[1].End = 19 },
		"overlap":   func(p *contracts.VisibleTextProjection) { p.Turns[0].Ranges[1].Start = 3 },
		"empty":     func(p *contracts.VisibleTextProjection) { p.Turns[0].Ranges = nil },
		"duplicate": func(p *contracts.VisibleTextProjection) { p.Turns = append(p.Turns, p.Turns[0]) },
	} {
		t.Run(name, func(t *testing.T) {
			p := makeProjection()
			mutate(p)
			if ValidateVisibleText(p, turns, "cursor") == nil {
				t.Fatal("invalid projection accepted")
			}
		})
	}
	if ValidateVisibleText(nil, turns, "cursor") != nil {
		t.Fatal("legacy projection rejected")
	}
	if ValidateVisibleText(&contracts.VisibleTextProjection{Version: 1, SourceCursor: "cursor", Turns: []contracts.VisibleTextTurn{}}, turns, "cursor") != nil {
		t.Fatal("known-empty projection rejected")
	}
}
