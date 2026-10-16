package adapters

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/xfey/coconet/packages/contracts"
)

type Adapter interface {
	Type() contracts.AgentType
	Health(context.Context) contracts.ComponentHealth
	Discover(context.Context, string) ([]contracts.DiscoveredSession, error)
	Observe(context.Context, string, string, ObservationCheckpoint) (contracts.SessionObservation, ObservationCheckpoint, error)
}

// ErrIncompleteNativeHistory identifies a fixed artifact that cannot supply
// all native history dependencies. Retrying the same incomplete bytes cannot
// recover it; a compatible source Adapter must capture a new fixed version.
var ErrIncompleteNativeHistory = errors.New("native history dependencies are missing")

// Native lookup failures are distinct from an empty conversation and from
// unreadable storage. Callers must not discard history on a generic lookup error.
var ErrNativeSessionMissing = errors.New("native session file was not found in this Agent profile; check the original session and profile before retrying")
var ErrNativeSessionAmbiguous = errors.New("multiple native session files have the same ID; resolve the duplicate source before retrying")
var ErrNativeSessionUnreadable = errors.New("native session storage could not be read; check file permissions and the Agent profile")

// Discover may return a bounded partial inventory with this sentinel.
var ErrDiscoveryLimit = errors.New("native session discovery reached its limit")

type ForkRequest struct {
	AttemptDirectory      string
	SourceArtifactPath    string
	SourceArtifactSHA256  string
	SourceNativeSessionID string
	CWD                   string
}

type ForkResult struct {
	NativeSessionID string `json:"native_session_id"`
}

type ForkAdapter interface {
	Adapter
	Fork(context.Context, ForkRequest) (ForkResult, error)
	ResumeCommand(string) contracts.ResumeCommand
}

type forkMarker struct {
	SchemaVersion         int                 `json:"schema_version"`
	AgentType             contracts.AgentType `json:"agent_type"`
	SourceNativeSessionID string              `json:"source_native_session_id"`
	SourceArtifactSHA256  string              `json:"source_artifact_sha256"`
	NativeSessionID       string              `json:"native_session_id"`
}

type ObservationCheckpoint struct {
	Cursor           string   `json:"cursor,omitempty"`
	TurnFingerprints []string `json:"turn_fingerprints,omitempty"`
	SourceEpoch      int      `json:"source_epoch,omitempty"`
}

type CommandLocator func(string) (string, error)

func ExecutableHealth(component, command string, locate CommandLocator) contracts.ComponentHealth {
	if locate == nil {
		locate = exec.LookPath
	}
	if command == "" {
		return contracts.ComponentHealth{
			Name:    component,
			Status:  contracts.StatusUnavailable,
			Message: "command is not configured",
		}
	}
	if _, err := locate(command); err != nil {
		return contracts.ComponentHealth{
			Name:    component,
			Status:  contracts.StatusUnavailable,
			Message: "command not found",
		}
	}
	return contracts.ComponentHealth{Name: component, Status: contracts.StatusOK}
}

func HashFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hasher := sha256.New()
	size, err := io.Copy(hasher, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hasher.Sum(nil)), size, nil
}

func LoadForkMarker(request ForkRequest, agent contracts.AgentType) (ForkResult, bool, error) {
	content, err := os.ReadFile(filepath.Join(request.AttemptDirectory, "native-fork.json"))
	if errors.Is(err, os.ErrNotExist) {
		return ForkResult{}, false, nil
	}
	if err != nil {
		return ForkResult{}, false, fmt.Errorf("read native Fork marker: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	var marker forkMarker
	if err := decoder.Decode(&marker); err != nil {
		return ForkResult{}, false, fmt.Errorf("decode native Fork marker: %w", err)
	}
	if marker.SchemaVersion != contracts.SchemaVersion || marker.AgentType != agent || marker.SourceNativeSessionID != request.SourceNativeSessionID || marker.SourceArtifactSHA256 != request.SourceArtifactSHA256 || marker.NativeSessionID == "" {
		return ForkResult{}, false, errors.New("native Fork marker does not match this Fork attempt")
	}
	return ForkResult{NativeSessionID: marker.NativeSessionID}, true, nil
}

func SaveForkMarker(request ForkRequest, agent contracts.AgentType, result ForkResult) error {
	if result.NativeSessionID == "" {
		return errors.New("native Fork result is empty")
	}
	if err := os.MkdirAll(request.AttemptDirectory, 0o700); err != nil {
		return fmt.Errorf("create Fork attempt directory: %w", err)
	}
	content, err := json.Marshal(forkMarker{
		SchemaVersion: contracts.SchemaVersion, AgentType: agent,
		SourceNativeSessionID: request.SourceNativeSessionID,
		SourceArtifactSHA256:  request.SourceArtifactSHA256,
		NativeSessionID:       result.NativeSessionID,
	})
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(request.AttemptDirectory, ".native-fork-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(content, '\n')); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, filepath.Join(request.AttemptDirectory, "native-fork.json"))
}

func ApplyCheckpoint(turns []contracts.NormalizedTurn, checkpoint ObservationCheckpoint) (contracts.NormalizedObservation, ObservationCheckpoint) {
	fingerprints := make([]string, len(turns))
	for index, turn := range turns {
		fingerprints[index] = Digest(strings.Join([]string{
			turn.TurnID,
			fmt.Sprintf("%d", turn.Ordinal),
			turn.Role,
			turn.Kind,
			turn.ContentHash,
		}, "\x00"))
	}
	cursor := Digest(strings.Join(fingerprints, "\n"))
	start := 0
	reset := false
	epoch := checkpoint.SourceEpoch
	if epoch < 1 {
		epoch = 1
	}
	if len(checkpoint.TurnFingerprints) > 0 {
		if fingerprintPrefix(checkpoint.TurnFingerprints, fingerprints) {
			start = len(checkpoint.TurnFingerprints)
		} else {
			reset = true
			epoch++
		}
	}
	delta := append([]contracts.NormalizedTurn(nil), turns[start:]...)
	if reset {
		delta = append([]contracts.NormalizedTurn(nil), turns...)
	}
	return contracts.NormalizedObservation{
		FromCursor:  checkpoint.Cursor,
		ToCursor:    cursor,
		Reset:       reset,
		SourceEpoch: epoch,
		Turns:       delta,
	}, ObservationCheckpoint{Cursor: cursor, TurnFingerprints: fingerprints, SourceEpoch: epoch}
}

func Digest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func ValidNativeSessionID(value string) bool {
	return value != "" && len(value) <= 300 && !strings.ContainsAny(value, `/\\`) && strings.IndexFunc(value, unicode.IsControl) < 0
}

func PathContains(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)))
}

func fingerprintPrefix(previous, current []string) bool {
	if len(previous) > len(current) {
		return false
	}
	for index := range previous {
		if previous[index] != current[index] {
			return false
		}
	}
	return true
}
