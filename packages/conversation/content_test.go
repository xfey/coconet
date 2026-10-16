package conversation

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
)

func TestIndependentManifestReusesBlocksAndReadsRange(t *testing.T) {
	entries := make([]Entry, 80)
	for i := range entries {
		entries[i].Turn = contracts.NormalizedTurn{TurnID: fmt.Sprintf("turn_%d", i), Ordinal: i, Role: "user", Kind: "message", Text: strings.Repeat("内容", 1000)}
	}
	old, err := Build(entries[:60])
	if err != nil {
		t.Fatal(err)
	}
	latest, err := Build(entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(old.Manifest.Blocks) < 2 || old.Manifest.Blocks[0] != latest.Manifest.Blocks[0] {
		t.Fatal("stable prefix was not shared")
	}
	// Only the latest manifest and its objects exist: the old snapshot and
	// manifest are deliberately not part of the reconstruction interface.
	reads := 0
	got, err := Read(latest.Manifest, 19, 24, func(b Block) ([]byte, error) { reads++; return latest.Objects[b.SHA256], nil })
	if err != nil || !reflect.DeepEqual(got, entries[19:24]) {
		t.Fatalf("range reconstruction: %v", err)
	}
	if reads >= len(latest.Manifest.Blocks) {
		t.Fatal("range read decoded unrelated blocks")
	}
	got, err = Read(latest.Manifest, 0, len(entries), func(b Block) ([]byte, error) { return latest.Objects[b.SHA256], nil })
	if err != nil || !reflect.DeepEqual(got, entries) {
		t.Fatalf("independent reconstruction: %v", err)
	}
}

func TestConversationRejectsCorruption(t *testing.T) {
	prepared, err := Build([]Entry{{Turn: contracts.NormalizedTurn{TurnID: "one", Ordinal: 0, Text: "visible"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Read(prepared.Manifest, 0, 1, func(b Block) ([]byte, error) {
		data := append([]byte(nil), prepared.Objects[b.SHA256]...)
		data[1] ^= 1
		return data, nil
	}); err == nil {
		t.Fatal("corrupt block accepted")
	}
	m := prepared.Manifest
	m.Count++
	if Validate(m) == nil {
		t.Fatal("changed manifest accepted")
	}
	if _, err := Read(prepared.Manifest, 0, 2, nil); err == nil {
		t.Fatal("invalid range accepted")
	}
	if _, err := Build([]Entry{{Turn: contracts.NormalizedTurn{TurnID: "one", Ordinal: 3}}}); err == nil {
		t.Fatal("invalid anchor accepted")
	}
}

func TestConversationPreservesExplicitHiddenText(t *testing.T) {
	hidden := ""
	prepared, err := Build([]Entry{{Turn: contracts.NormalizedTurn{TurnID: "one", Ordinal: 0}, Visible: &hidden}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Read(prepared.Manifest, 0, 1, func(b Block) ([]byte, error) { return prepared.Objects[b.SHA256], nil })
	if err != nil || got[0].Visible == nil || *got[0].Visible != "" {
		t.Fatal("explicit hidden text lost")
	}
	empty, err := Build(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(empty.Manifest); err != nil {
		t.Fatal(err)
	}
}
