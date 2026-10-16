package codex

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/xfey/coconet/adapters"
	"github.com/xfey/coconet/packages/sessionartifact"
)

type preparedForkHistory struct {
	Path      string
	NativeID  string
	Temporary bool
	Ancestors []string
}

func (history preparedForkHistory) cleanup() {
	if history.Temporary {
		_ = os.Remove(history.Path)
	}
}

// Legacy sources use a temporary exact copy. Paginated sources retain their
// format and immutable dependencies under snapshot-specific local identities.
// Only import metadata changes; the transported raw evidence stays untouched.
func (adapter Adapter) prepareForkHistory(request adapters.ForkRequest) (preparedForkHistory, error) {
	if err := os.MkdirAll(request.AttemptDirectory, 0o700); err != nil {
		return preparedForkHistory{}, err
	}
	input, err := os.Open(request.SourceArtifactPath)
	if err != nil {
		return preparedForkHistory{}, err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > sessionartifact.MaxArtifactSize {
		return preparedForkHistory{}, errors.New("Codex Fork source is not a bounded artifact")
	}
	var prefix [1]byte
	if _, err := input.ReadAt(prefix[:], 0); err != nil {
		return preparedForkHistory{}, err
	}
	var segments []rolloutSegment
	var temporaryMembers []string
	defer func() {
		closeSegments(segments)
		for _, path := range temporaryMembers {
			_ = os.Remove(path)
		}
	}()
	if prefix[0] == '{' {
		segment, err := openRolloutSegment(request.SourceArtifactPath, request.SourceNativeSessionID, 0)
		if err != nil {
			return preparedForkHistory{}, err
		}
		segments = append(segments, segment)
		if segment.metadata.HistoryBase != nil {
			return preparedForkHistory{}, adapters.ErrIncompleteNativeHistory
		}
	} else {
		segments, temporaryMembers, err = readHistoryBundle(input, request)
		if err != nil {
			return preparedForkHistory{}, err
		}
	}
	if segments[len(segments)-1].metadata.HistoryMode == "paginated" {
		return adapter.installHistorySnapshot(request, segments)
	}
	output, err := os.CreateTemp(request.AttemptDirectory, ".codex-source-*.jsonl")
	if err != nil {
		return preparedForkHistory{}, err
	}
	history := preparedForkHistory{Path: output.Name(), NativeID: request.SourceNativeSessionID, Temporary: true}
	root := segments[len(segments)-1]
	_, copyErr := io.Copy(output, io.NewSectionReader(root.file, 0, root.Size))
	syncErr := output.Sync()
	closeErr := output.Close()
	if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
		history.cleanup()
		return preparedForkHistory{}, err
	}
	return history, nil
}

// The namespace includes the fixed artifact hash, so two versions of one
// original native ID can coexist without affecting an existing local thread.
func snapshotNativeID(artifactHash, original string) (string, error) {
	if len(original) != 36 {
		return "", errors.New("paginated Codex history requires UUID native identities")
	}
	for index, char := range original {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if char != '-' {
				return "", errors.New("invalid paginated Codex native identity")
			}
		} else if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f' || char >= 'A' && char <= 'F') {
			return "", errors.New("invalid paginated Codex native identity")
		}
	}
	digest := sha256.Sum256([]byte("coconet.codex-history.v1\x00" + artifactHash + "\x00" + original))
	originalBytes, _ := hex.DecodeString(strings.ReplaceAll(original, "-", ""))
	copy(digest[:6], originalBytes[:6]) // Keep the source UUIDv7 creation timestamp.
	digest[6] = digest[6]&0x0f | 0x70
	digest[8] = digest[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", digest[:4], digest[4:6], digest[6:8], digest[8:10], digest[10:16]), nil
}

// Rewrite known storage identity fields, including the owning thread on
// item_completed events used by native structured reads. UUID replacements
// have equal encoded lengths, preserving history_base byte offsets. Nested
// tool arguments, text, timestamps and unknown fields stay byte-for-byte intact.
func remapHistoryRecord(header []byte, identities map[string]string) ([]byte, error) {
	return remapHistoryRecordWithParent(header, identities, "")
}

func remapHistoryRecordWithParent(header []byte, identities map[string]string, parent string) ([]byte, error) {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(header, &envelope); err != nil {
		return nil, err
	}
	var fields map[string]bool
	switch envelope.Type {
	case "session_meta":
		fields = map[string]bool{"/payload/id": true, "/payload/session_id": true, "/payload/forked_from_id": true, "/payload/history_base/thread_id": true}
	case "event_msg":
		fields = map[string]bool{"/payload/thread_id": true}
	case "token_usage_record":
		fields = map[string]bool{"/payload/thread_id": true, "/payload/session_id": true}
	default:
		return header, nil
	}
	output := bytes.Clone(header)
	decoder := json.NewDecoder(bytes.NewReader(header))
	var visit func(string) error
	visit = func(path string) error {
		needed := path == ""
		for field := range fields {
			needed = needed || field == path || strings.HasPrefix(field, path+"/")
		}
		if !needed {
			// Decoder advances across opaque payloads without allocating their
			// strings, arrays and maps as a second in-memory transcript.
			return decoder.Decode(new(ignoredHistoryJSON))
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		if delimiter, ok := token.(json.Delim); ok {
			switch delimiter {
			case '{':
				for decoder.More() {
					key, err := decoder.Token()
					if err != nil {
						return err
					}
					// JSON Pointer escaping keeps unknown keys containing path
					// separators distinct from the known nested metadata fields.
					part := strings.ReplaceAll(strings.ReplaceAll(key.(string), "~", "~0"), "/", "~1")
					if err := visit(path + "/" + part); err != nil {
						return err
					}
				}
			case '[':
				for decoder.More() {
					if err := visit(path + "[]"); err != nil {
						return err
					}
				}
			default:
				return errors.New("invalid Codex metadata structure")
			}
			_, err := decoder.Token()
			return err
		}
		if !fields[path] {
			return nil
		}
		value, ok := token.(string)
		if !ok {
			return nil
		}
		replacement, found := identities[value]
		if path == "/payload/history_base/thread_id" && parent != "" {
			replacement, found = parent, true
		}
		if !found {
			return nil
		}
		encoded, replaced := strconv.Quote(value), strconv.Quote(replacement)
		end := decoder.InputOffset()
		start := end - int64(len(encoded))
		if start < 0 || len(encoded) != len(replaced) || string(header[start:end]) != encoded {
			return errors.New("Codex metadata identity cannot be remapped without moving history boundaries")
		}
		copy(output[start:end], replaced)
		return nil
	}
	if err := visit(""); err != nil {
		return nil, err
	}
	return output, nil
}

type ignoredHistoryJSON struct{}

func (*ignoredHistoryJSON) UnmarshalJSON([]byte) error { return nil }

func (adapter Adapter) installHistorySnapshot(request adapters.ForkRequest, segments []rolloutSegment) (preparedForkHistory, error) {
	identities := make(map[string]string, len(segments))
	counts := map[string]int{}
	for _, segment := range segments {
		counts[segment.NativeID]++
	}
	aliases := make([]string, len(segments))
	for i, segment := range segments {
		namespace := request.SourceArtifactSHA256
		if counts[segment.NativeID] > 1 {
			namespace += fmt.Sprintf("/segment/%d", i)
		}
		id, err := snapshotNativeID(namespace, segment.NativeID)
		if err != nil {
			return preparedForkHistory{}, err
		}
		identities[segment.NativeID] = id
		aliases[i] = id
	}
	directory := filepath.Join(adapter.ConfigRoot, "sessions", "coconet-imports")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return preparedForkHistory{}, err
	}
	stagingRoot := filepath.Join(adapter.ConfigRoot, "coconet", "history-staging")
	if err := os.MkdirAll(stagingRoot, 0o700); err != nil {
		return preparedForkHistory{}, err
	}
	// Keep unpublished paths outside native discovery and indexing roots.
	staging, err := os.MkdirTemp(stagingRoot, ".staging-")
	if err != nil {
		return preparedForkHistory{}, err
	}
	// The directory is ours and contains only these explicitly named files.
	var staged []string
	defer func() {
		for _, name := range staged {
			_ = os.Remove(filepath.Join(staging, name))
		}
		_ = os.Remove(staging)
	}()
	expected := make(map[string]string, len(segments))
	for i, segment := range segments {
		identities[segment.NativeID] = aliases[i]
		parent := ""
		if i > 0 {
			parent = aliases[i-1]
		}
		name := importedRolloutName(aliases[i], segment.metadata.Timestamp)
		path := filepath.Join(staging, name)
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return preparedForkHistory{}, err
		}
		staged = append(staged, name)
		copyErr := copyRemappedHistoryWithParent(file, segment, identities, parent)
		syncErr, closeErr := file.Sync(), file.Close()
		if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
			return preparedForkHistory{}, err
		}
		hash, _, err := adapters.HashFile(path)
		if err != nil {
			return preparedForkHistory{}, err
		}
		expected[name] = hash
	}
	target := filepath.Join(directory, request.SourceArtifactSHA256)
	if err := os.Rename(staging, target); err != nil {
		// Concurrent imports may have already published this exact snapshot.
		info, statErr := os.Lstat(target)
		if statErr != nil || !info.IsDir() {
			return preparedForkHistory{}, errors.New("Codex history snapshot directory is invalid")
		}
		entries, readErr := os.ReadDir(target)
		if readErr != nil || len(entries) != len(expected) {
			return preparedForkHistory{}, errors.New("Codex history snapshot could not be published")
		}
		for _, entry := range entries {
			hash, exists := expected[entry.Name()]
			if !exists || entry.Type() != 0 {
				return preparedForkHistory{}, errors.New("existing Codex history snapshot is inconsistent")
			}
			actual, _, err := adapters.HashFile(filepath.Join(target, entry.Name()))
			if err != nil || actual != hash {
				return preparedForkHistory{}, errors.New("existing Codex history snapshot has changed")
			}
		}
	}
	id := aliases[len(aliases)-1]
	history := preparedForkHistory{Path: filepath.Join(target, importedRolloutName(id, segments[len(segments)-1].metadata.Timestamp)), NativeID: id}
	history.Ancestors = append(history.Ancestors, aliases[:len(aliases)-1]...)
	return history, nil
}

func importedRolloutName(id, timestamp string) string {
	stamp := "2000-01-01T00-00-00"
	if parsed, err := time.Parse(time.RFC3339Nano, timestamp); err == nil {
		stamp = parsed.UTC().Format("2006-01-02T15-04-05")
	}
	return "rollout-" + stamp + "-" + id + ".jsonl"
}

func copyRemappedHistory(output io.Writer, segment rolloutSegment, identities map[string]string) error {
	return copyRemappedHistoryWithParent(output, segment, identities, "")
}

func copyRemappedHistoryWithParent(output io.Writer, segment rolloutSegment, identities map[string]string, parent string) error {
	scanner := bufio.NewScanner(io.NewSectionReader(segment.file, 0, segment.Size))
	scanner.Buffer(make([]byte, 64<<10), 64<<20)
	// ScanLines removes CR from CRLF. Native byte offsets require preserving
	// every byte except the explicitly remapped, equal-length identities.
	scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if end := bytes.IndexByte(data, '\n'); end >= 0 {
			return end + 1, data[:end], nil
		}
		if atEOF && len(data) > 0 {
			return 0, nil, errors.New("Codex history record is incomplete")
		}
		return 0, nil, nil
	})
	for scanner.Scan() {
		record, err := remapHistoryRecordWithParent(scanner.Bytes(), identities, parent)
		if err != nil {
			return err
		}
		if _, err := output.Write(record); err != nil {
			return err
		}
		if _, err := output.Write([]byte{'\n'}); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func (adapter Adapter) importedHistoryIDs() (map[string]bool, error) {
	ids := make(map[string]bool)
	root := filepath.Join(adapter.ConfigRoot, "sessions", "coconet-imports")
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) && path == root {
			return nil
		}
		if err != nil {
			return err
		}
		if entry.IsDir() && strings.HasPrefix(entry.Name(), ".staging-") {
			return filepath.SkipDir
		}
		name := entry.Name()
		if !entry.IsDir() && strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, ".jsonl") && len(name) >= 42 {
			ids[name[len(name)-42:len(name)-6]] = true
		}
		return nil
	})
	return ids, err
}
