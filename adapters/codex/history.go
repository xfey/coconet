package codex

// Capture native history envelopes; keep transported payloads byte-for-byte.
import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/xfey/coconet/adapters"
	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/sessionartifact"
)

const (
	historyMediaType = "application/vnd.coconet.codex-history+tar"
	historyFormat    = "coconet.codex-history"
	maxHistoryDepth  = 128
	maxMetadataSize  = 8 << 20
)

type historyPosition struct {
	ThreadID            string `json:"thread_id"`
	EndOrdinalExclusive uint64 `json:"end_ordinal_exclusive"`
	EndByteOffset       int64  `json:"end_byte_offset"`
}

type rolloutMetadata struct {
	ID          string           `json:"id"`
	Timestamp   string           `json:"timestamp"`
	HistoryMode string           `json:"history_mode"`
	HistoryBase *historyPosition `json:"history_base"`
}

type rolloutSegment struct {
	historyMember
	file     *os.File
	metadata rolloutMetadata
}

func readRolloutMetadata(reader io.Reader) (rolloutMetadata, []byte, error) {
	line, err := bufio.NewReader(io.LimitReader(reader, maxMetadataSize+1)).ReadBytes('\n')
	if err != nil || len(line) > maxMetadataSize {
		return rolloutMetadata{}, nil, errors.New("Codex rollout metadata is incomplete or too large")
	}
	var envelope struct {
		Type    string          `json:"type"`
		Payload rolloutMetadata `json:"payload"`
	}
	if json.Unmarshal(line, &envelope) != nil || envelope.Type != "session_meta" || !adapters.ValidNativeSessionID(envelope.Payload.ID) {
		return rolloutMetadata{}, nil, errors.New("Codex rollout does not start with valid Session metadata")
	}
	meta := envelope.Payload
	if meta.HistoryMode != "" && meta.HistoryMode != "legacy" && meta.HistoryMode != "paginated" {
		return rolloutMetadata{}, nil, errors.New("Codex rollout history mode is unsupported")
	}
	if base := meta.HistoryBase; base != nil {
		if meta.HistoryMode != "paginated" || !adapters.ValidNativeSessionID(base.ThreadID) || base.EndByteOffset <= 0 || base.EndOrdinalExclusive == 0 {
			return rolloutMetadata{}, nil, errors.New("Codex rollout history dependency is invalid")
		}
	}
	return meta, line, nil
}

func openRolloutSegment(path, nativeID string, size int64) (rolloutSegment, error) {
	file, err := os.Open(path)
	if err != nil {
		return rolloutSegment{}, err
	}
	success := false
	defer func() {
		if !success {
			_ = file.Close()
		}
	}()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > sessionartifact.MaxArtifactSize {
		return rolloutSegment{}, errors.New("Codex rollout is not a bounded regular file")
	}
	if size == 0 {
		size = info.Size()
	}
	if size <= 0 || size > info.Size() {
		return rolloutSegment{}, errors.New("Codex history dependency exceeds the available file")
	}
	meta, _, err := readRolloutMetadata(io.NewSectionReader(file, 0, size))
	if err != nil {
		return rolloutSegment{}, err
	}
	if meta.ID != nativeID {
		return rolloutSegment{}, errors.New("Codex rollout belongs to a different native Session")
	}
	var end [1]byte
	if _, err := file.ReadAt(end[:], size-1); err != nil || end[0] != '\n' {
		return rolloutSegment{}, errors.New("Codex history snapshot ends inside a record")
	}
	success = true
	return rolloutSegment{historyMember: historyMember{NativeID: nativeID, Size: size}, file: file, metadata: meta}, nil
}

func closeSegments(segments []rolloutSegment) {
	for _, segment := range segments {
		_ = segment.file.Close()
	}
}

// A paginated parent contributes an exact byte prefix. Check its ordinal
// boundary as well, so a replaced or truncated parent cannot silently supply
// a different history at an otherwise plausible byte offset.
func validateHistoryBoundary(segment rolloutSegment, base historyPosition) error {
	if segment.metadata.HistoryMode != "paginated" || segment.NativeID != base.ThreadID || segment.Size != base.EndByteOffset {
		return errors.New("Codex history dependency does not match its parent")
	}
	scanner := bufio.NewScanner(io.NewSectionReader(segment.file, 0, segment.Size))
	scanner.Buffer(make([]byte, 64<<10), 64<<20)
	var next uint64
	if ancestor := segment.metadata.HistoryBase; ancestor != nil {
		next = ancestor.EndOrdinalExclusive
	}
	for scanner.Scan() {
		var record struct {
			Ordinal *uint64 `json:"ordinal"`
		}
		if json.Unmarshal(scanner.Bytes(), &record) != nil || record.Ordinal == nil || *record.Ordinal != next || next == ^uint64(0) {
			return errors.New("Codex history dependency has invalid record ordinals")
		}
		next++
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read Codex history boundary: %w", err)
	}
	if next != base.EndOrdinalExclusive {
		return errors.New("Codex history dependency has a different ordinal boundary")
	}
	return nil
}

func (adapter Adapter) historyArtifact(client appServerClient, path, nativeID string) (contracts.ArtifactObservation, error) {
	root, err := openRolloutSegment(path, nativeID, 0)
	if err != nil {
		return contracts.ArtifactObservation{}, err
	}
	segments := []rolloutSegment{root}
	defer func() { closeSegments(segments) }()
	if root.metadata.HistoryBase == nil {
		hash, size, err := adapters.HashFile(path)
		return contracts.ArtifactObservation{Path: path, MediaType: "application/x-ndjson", SHA256: hash, Size: size}, err
	}
	var total = root.Size
	for base := root.metadata.HistoryBase; base != nil; base = segments[len(segments)-1].metadata.HistoryBase {
		if len(segments) >= maxHistoryDepth {
			return contracts.ArtifactObservation{}, errors.New("Codex history dependency is cyclic or too deep")
		}
		var read struct {
			Thread thread `json:"thread"`
		}
		if err := client.Request("thread/read", map[string]any{"threadId": base.ThreadID, "includeTurns": false}, &read); err != nil {
			return contracts.ArtifactObservation{}, fmt.Errorf("locate Codex history dependency: %w", err)
		}
		if read.Thread.ID != base.ThreadID || !adapter.managedRolloutPath(read.Thread.Path) {
			return contracts.ArtifactObservation{}, errors.New("Codex history dependency escaped the configured native Session roots")
		}
		parent, err := adapter.historyParent(read.Thread.Path, *base, segments)
		if err != nil {
			return contracts.ArtifactObservation{}, err
		}
		segments = append(segments, parent)
		if err := validateHistoryBoundary(parent, *base); err != nil {
			return contracts.ArtifactObservation{}, err
		}
		total += parent.Size
		if total > sessionartifact.MaxArtifactSize {
			return contracts.ArtifactObservation{}, errors.New("Codex history dependency bundle is too large")
		}
	}
	// The archive is stable across repeated observations and parent growth.
	for i, j := 0, len(segments)-1; i < j; i, j = i+1, j-1 {
		segments[i], segments[j] = segments[j], segments[i]
	}
	directory := filepath.Join(adapter.ConfigRoot, "coconet", "history-snapshots")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return contracts.ArtifactObservation{}, err
	}
	temporary, err := os.CreateTemp(directory, ".history-*.tar")
	if err != nil {
		return contracts.ArtifactObservation{}, err
	}
	defer os.Remove(temporary.Name())
	defer temporary.Close()
	if err := writeHistoryBundle(temporary, nativeID, segments); err != nil {
		return contracts.ArtifactObservation{}, err
	}
	if err := temporary.Sync(); err != nil {
		return contracts.ArtifactObservation{}, err
	}
	if err := temporary.Close(); err != nil {
		return contracts.ArtifactObservation{}, err
	}
	hash, size, err := adapters.HashFile(temporary.Name())
	if err != nil || size > sessionartifact.MaxArtifactSize {
		return contracts.ArtifactObservation{}, errors.New("Codex history bundle exceeds the artifact limit or could not be hashed")
	}
	// Runtime's SessionEnd check must use the native child's write time, not
	// the time at which its transport bundle was prepared.
	info, err := root.file.Stat()
	if err != nil {
		return contracts.ArtifactObservation{}, err
	}
	if err := os.Chtimes(temporary.Name(), info.ModTime(), info.ModTime()); err != nil {
		return contracts.ArtifactObservation{}, err
	}
	target := filepath.Join(directory, adapters.Digest(nativeID)+".tar")
	if err := os.Rename(temporary.Name(), target); err != nil {
		return contracts.ArtifactObservation{}, err
	}
	return contracts.ArtifactObservation{Path: target, MediaType: historyMediaType, SHA256: hash, Size: size}, nil
}

// Revert keeps the logical thread ID but installs a new rollout whose base
// refers to an older physical segment of that same thread. thread/read returns
// the active path, so an already visited path cannot resolve that base. Select
// an ancestor only by its identity AND exact byte/ordinal boundary; never by
// file recency. Ambiguous or unavailable history fails closed.
func (adapter Adapter) historyParent(activePath string, base historyPosition, visited []rolloutSegment) (rolloutSegment, error) {
	eligible := func(path string) (rolloutSegment, error) {
		if !adapter.managedRolloutPath(path) {
			return rolloutSegment{}, errors.New("Codex history dependency escaped its native roots")
		}
		segment, err := openRolloutSegment(path, base.ThreadID, base.EndByteOffset)
		if err != nil {
			return rolloutSegment{}, err
		}
		info, err := segment.file.Stat()
		for _, prior := range visited {
			old, statErr := prior.file.Stat()
			if err != nil || statErr != nil || os.SameFile(info, old) {
				segment.file.Close()
				return rolloutSegment{}, errors.New("Codex history dependency revisits a physical segment")
			}
		}
		if err := validateHistoryBoundary(segment, base); err != nil {
			segment.file.Close()
			return rolloutSegment{}, err
		}
		return segment, nil
	}
	selected, _ := eligible(activePath)
	var walked, candidates int
	for _, name := range []string{"sessions", "archived_sessions"} {
		root := filepath.Join(adapter.ConfigRoot, name)
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if errors.Is(err, os.ErrNotExist) && path == root {
				return nil
			}
			if err != nil {
				return err
			}
			walked++
			if walked > 100000 {
				return errors.New("Codex history lookup exceeded its file limit")
			}
			filename := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(filename, ".jsonl") || !(strings.HasSuffix(filename, "-"+base.ThreadID+".jsonl") || strings.Contains(filename, "-"+base.ThreadID+"_")) {
				return nil
			}
			candidates++
			if candidates > maxHistoryDepth {
				return errors.New("Codex history lookup has too many candidate segments")
			}
			segment, err := eligible(path)
			if err != nil {
				return nil
			}
			if selected.file != nil {
				one, _ := selected.file.Stat()
				two, _ := segment.file.Stat()
				if one != nil && two != nil && os.SameFile(one, two) {
					segment.file.Close()
					return nil
				}
				segment.file.Close()
				return errors.New("Codex history dependency has ambiguous physical segments")
			}
			selected = segment
			return nil
		})
		if err != nil {
			if selected.file != nil {
				selected.file.Close()
			}
			return rolloutSegment{}, err
		}
	}
	if selected.file == nil {
		return rolloutSegment{}, errors.New("Codex history dependency has no valid physical ancestor")
	}
	return selected, nil
}

func (adapter Adapter) managedRolloutPath(path string) bool {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	for _, name := range []string{"sessions", "archived_sessions"} {
		root, err := filepath.EvalSymlinks(filepath.Join(adapter.ConfigRoot, name))
		if err == nil && adapters.PathContains(root, resolved) {
			return true
		}
	}
	return false
}
