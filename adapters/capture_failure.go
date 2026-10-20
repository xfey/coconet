package adapters

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// CaptureFailure identifies the exact native files read by a failed capture.
// It lets the queue wait for a file change without repeatedly parsing bad bytes.
type CaptureFailure struct {
	Files    []string
	Revision string
	Cause    error
}

func (e *CaptureFailure) Error() string { return e.Cause.Error() }
func (e *CaptureFailure) Unwrap() error { return e.Cause }

// CaptureRevision is metadata only. Missing sidecars count as a change; an
// unreadable/main missing file has no stable revision and remains retryable.
func CaptureRevision(files []string) string {
	var parts []string
	for i, path := range files {
		info, err := os.Stat(path)
		if i > 0 && os.IsNotExist(err) {
			parts = append(parts, path+":missing")
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			return ""
		}
		parts = append(parts, fmt.Sprintf("%s:%d:%d:%d", path, info.Size(), info.ModTime().UnixNano(), info.Mode()))
	}
	if len(parts) == 0 {
		return ""
	}
	return Digest(strings.Join(parts, "\x00"))
}

func TrackCaptureFailure(failure *error, files []string) func() {
	revision := CaptureRevision(files)
	return func() {
		var syntax *json.SyntaxError
		// Only proven native-content errors wait for a file change. Host startup,
		// configuration and transport failures must remain retryable.
		invalidContent := errors.As(*failure, &syntax) || errors.Is(*failure, ErrNativeSessionUnreadable)
		if invalidContent && revision != "" {
			*failure = &CaptureFailure{Files: files, Revision: revision, Cause: *failure}
		}
	}
}
