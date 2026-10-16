package projectlink

import (
	"strings"
	"testing"
)

func TestProjectIdentifiersAreCanonical(t *testing.T) {
	for _, raw := range [][16]byte{{}, {0, 1, 2, 3}, {255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255}} {
		id := NewProjectID(raw)
		decoded, ok := ProjectUID(id)
		if !ok || decoded != raw || !strings.HasPrefix(id, "coco_") {
			t.Fatal("roundtrip", id)
		}
	}
	for _, id := range []string{"coco_", "coco_111111111111111", "coco_11111111111111111", "coco_0OIl", " coco_1111111111111111", "prj_1111111111111111", "coco_zzzzzzzzzzzzzzzzzzzzzz", "coco_YcVfxkQb6JRzqk5kF2tNLw"} {
		if _, ok := ProjectUID(id); ok {
			t.Fatal("invalid ID accepted", id)
		}
	}
}
