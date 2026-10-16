package relay

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/xfey/coconet/packages/storageguard"
)

func TestDiskReserveRejectsAppendWithoutChangingAcceptedData(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.Append("p", "a", proposal("one", 0)); err != nil {
		t.Fatal(err)
	}
	s.guard, err = storageguard.New([]string{filepath.Join(root, "relay.db")}, 4<<30, 1<<60)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Append("p", "a", proposal("two", 1)); !errors.Is(err, storageguard.ErrSpace) {
		t.Fatal(err)
	}
	page, err := s.Since("p", 0, 100)
	if err != nil || page.Head != 1 || len(page.Entries) != 1 {
		t.Fatal(page, err)
	}
}
