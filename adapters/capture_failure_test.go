package adapters

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCaptureFailureWaitsOnlyForInvalidContent(t *testing.T) {
	file := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(file, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	var malformed any
	syntax := json.Unmarshal([]byte("{"), &malformed)
	for _, cause := range []error{syntax, ErrNativeSessionUnreadable, errors.New("host could not start"), os.ErrPermission} {
		failure := cause
		TrackCaptureFailure(&failure, []string{file, file + "-wal"})()
		var native *CaptureFailure
		fixed := errors.As(failure, &native)
		if fixed != (cause == syntax || cause == ErrNativeSessionUnreadable) {
			t.Fatal("incorrect source classification", cause)
		}
		if fixed {
			before := native.Revision
			if CaptureRevision(native.Files) != before {
				t.Fatal("unstable metadata")
			}
			if err := os.WriteFile(file+"-wal", []byte("new content"), 0600); err != nil {
				t.Fatal(err)
			}
			if CaptureRevision(native.Files) == before {
				t.Fatal("WAL change did not wake source")
			}
			if err := os.Remove(file + "-wal"); err != nil {
				t.Fatal(err)
			}
		}
	}
}
