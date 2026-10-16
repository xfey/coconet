package cursor

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xfey/coconet/adapters"
	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/sessionartifact"
)

const DefaultCommand = "agent"

var nativeIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type Adapter struct {
	Command    string
	ConfigRoot string
	Locate     adapters.CommandLocator
}

func New(command string) Adapter {
	if command == "" {
		command = DefaultCommand
	}
	root := os.Getenv("CURSOR_CONFIG_DIR")
	if root == "" {
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			root = filepath.Join(xdg, "cursor")
		} else {
			home, _ := os.UserHomeDir()
			root = filepath.Join(home, ".cursor")
		}
	}
	return Adapter{Command: command, ConfigRoot: root}
}
func (Adapter) Type() contracts.AgentType { return contracts.AgentCursor }
func (a Adapter) Health(context.Context) contracts.ComponentHealth {
	return adapters.ExecutableHealth("adapter.cursor", a.Command, a.Locate)
}

func (a Adapter) Observe(ctx context.Context, cwd, id string, checkpoint adapters.ObservationCheckpoint) (contracts.SessionObservation, adapters.ObservationCheckpoint, error) {
	var empty contracts.SessionObservation
	if !filepath.IsAbs(cwd) || !filepath.IsAbs(a.ConfigRoot) || !nativeIDPattern.MatchString(id) {
		return empty, checkpoint, errors.New("Cursor capture requires an absolute workspace/profile and a valid conversation ID")
	}
	source := filepath.Join(chatDirectory(a.ConfigRoot, cwd, id), "store.db")
	copy, err := snapshotDatabase(ctx, source)
	if err != nil {
		return empty, checkpoint, err
	}
	ok := false
	defer func() {
		if !ok {
			os.Remove(copy)
		}
	}()
	db, err := openReadOnly(copy)
	if err != nil {
		return empty, checkpoint, err
	}
	defer db.Close()
	s, err := readState(ctx, db, id)
	if err != nil {
		return empty, checkpoint, err
	}
	turns, err := s.turns(ctx)
	if err != nil {
		return empty, checkpoint, err
	}
	if len(turns) == 0 && len(checkpoint.TurnFingerprints) > 0 {
		return empty, checkpoint, errors.New("Cursor returned empty history for a populated session; retry without advancing capture")
	}
	normalized, next := adapters.ApplyCheckpoint(turns, checkpoint)
	visible := &contracts.VisibleTextProjection{Version: 1, SourceCursor: normalized.ToCursor, Turns: []contracts.VisibleTextTurn{}}
	active := &contracts.NativeActiveHistory{IDs: []string{}}
	for _, turn := range turns {
		active.IDs = append(active.IDs, turn.TurnID)
		visible.Turns = append(visible.Turns, contracts.VisibleTextTurn{TurnID: turn.TurnID, ContentHash: turn.ContentHash, Ranges: []contracts.TextRange{{Start: 0, End: len(turn.Text)}}})
	}
	digest, size, err := adapters.HashFile(copy)
	if err != nil {
		return empty, checkpoint, err
	}
	// Native history changes in the WAL as well as in the main database.
	activity := time.Time{}
	for _, path := range []string{source, source + "-wal"} {
		if info, err := os.Stat(path); err == nil && info.ModTime().After(activity) {
			activity = info.ModTime()
		}
	}
	ok = true
	return contracts.SessionObservation{SchemaVersion: 1, ActiveHistory: active, Session: contracts.SessionEnvelope{SchemaVersion: 1, AgentType: contracts.AgentCursor, NativeSessionID: id, CWD: cwd, ActivityState: contracts.ActivityUnknown, LastActivityAt: activity.UTC().Format(time.RFC3339Nano), SourceObserved: time.Now().UTC().Format(time.RFC3339Nano), ClientVersion: "unknown", AdapterName: "cursor-cli", AdapterVersion: "1"}, RawArtifact: contracts.ArtifactObservation{Temporary: true, Path: copy, MediaType: mediaType, SHA256: digest, Size: size}, Normalized: normalized, VisibleText: visible}, next, nil
}

func (s nativeState) turns(ctx context.Context) ([]contracts.NormalizedTurn, error) {
	refs := [][]byte{}
	archives := s.fields[13]
	if len(archives) == 0 && len(s.fields[11]) > 0 {
		archives = s.fields[11]
	}
	for _, ref := range archives {
		data, err := s.blob(ctx, hex.EncodeToString(ref))
		if err != nil {
			return nil, err
		}
		fields, err := wireFields(data)
		if err != nil {
			return nil, err
		}
		refs = append(refs, fields[1]...)
	}
	refs = append(refs, s.fields[1]...)
	turns := []contracts.NormalizedTurn{}
	counts := map[string]int{}
	var totalBytes int64
	if len(refs) > 100000 {
		return nil, errors.New("Cursor active history exceeds the message limit")
	}
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		id := hex.EncodeToString(ref)
		data, err := s.blob(ctx, id)
		if err != nil {
			return nil, err
		}
		var message struct {
			Role            string          `json:"role"`
			Content         json.RawMessage `json:"content"`
			ProviderOptions struct {
				Cursor struct {
					IsSummary  bool   `json:"isSummary"`
					RequestID  string `json:"requestId"`
					TextImport string `json:"coconetTextImport"`
				} `json:"cursor"`
			} `json:"providerOptions"`
		}
		if !utf8.Valid(data) || json.Unmarshal(data, &message) != nil {
			return nil, errors.New("Cursor active message is not valid UTF-8 JSON")
		}
		if message.Role != "user" && message.Role != "assistant" || message.ProviderOptions.Cursor.IsSummary {
			continue
		}
		text, err := messageText(message.Content)
		if err != nil {
			return nil, err
		}
		if message.Role == "user" {
			// Native user messages wrap the actual prompt. Workspace instructions
			// are separate user-role records with no request identity.
			if message.ProviderOptions.Cursor.RequestID == "" {
				continue
			}
			start, end := strings.Index(text, "<user_query>"), strings.LastIndex(text, "</user_query>")
			if message.ProviderOptions.Cursor.TextImport != adapters.TextImportFormat && start >= 0 && end > start {
				text = strings.TrimSpace(text[start+len("<user_query>") : end])
			}
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		// Cursor reserializes imported prompt JSON when resuming it. Object
		// field order and non-dialogue metadata must not create a false rewind.
		identity := adapters.Digest(message.Role + "\x00" + message.ProviderOptions.Cursor.RequestID + "\x00" + text)
		counts[identity]++
		totalBytes += int64(len(text))
		if totalBytes > sessionartifact.MaxArtifactSize {
			return nil, errors.New("Cursor visible dialogue exceeds the 256 MiB capture limit")
		}
		turns = append(turns, contracts.NormalizedTurn{TurnID: fmt.Sprintf("%s/%d", identity, counts[identity]), Ordinal: len(turns), Role: message.Role, Kind: "message", Text: text, ContentHash: adapters.Digest(message.Role + "\x00message\x00" + text)})
	}
	return turns, nil
}

func messageText(raw json.RawMessage) (string, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, nil
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return "", errors.New("unsupported Cursor message content; update Coconet")
	}
	parts := []string{}
	for _, block := range blocks {
		if block.Type == "text" && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n"), nil
}

func (a Adapter) Discover(ctx context.Context, root string) ([]contracts.DiscoveredSession, error) {
	if !filepath.IsAbs(root) || !filepath.IsAbs(a.ConfigRoot) {
		return nil, errors.New("Cursor discovery requires absolute workspace and profile paths")
	}
	base := filepath.Join(a.ConfigRoot, "chats")
	buckets, err := os.ReadDir(base)
	if errors.Is(err, os.ErrNotExist) {
		return []contracts.DiscoveredSession{}, nil
	}
	if err != nil {
		return nil, err
	}
	exactBucket := filepath.Base(filepath.Dir(chatDirectory(a.ConfigRoot, root, "placeholder")))
	result := []contracts.DiscoveredSession{}
	examined := 0
	if len(buckets) > 10000 {
		return result, adapters.ErrDiscoveryLimit
	}
	for _, bucket := range buckets {
		if !bucket.IsDir() {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(base, bucket.Name()))
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if !entry.IsDir() || !nativeIDPattern.MatchString(entry.Name()) {
				continue
			}
			examined++
			if examined > 10000 {
				return result, adapters.ErrDiscoveryLimit
			}
			path := filepath.Join(base, bucket.Name(), entry.Name(), "store.db")
			db, err := openReadOnly(path)
			if err != nil {
				if bucket.Name() == exactBucket {
					return nil, err
				}
				continue
			}
			state, err := readState(ctx, db, entry.Name())
			db.Close()
			if err != nil {
				if bucket.Name() == exactBucket {
					return nil, err
				}
				continue
			}
			cwd := ""
			if bucket.Name() == exactBucket {
				cwd = root
			}
			// Native workspace URIs identify chats started below a connected root.
			// Require the URI's hash to match the actual storage bucket; never infer
			// ownership from a shell's later working directory or conversation text.
			for _, raw := range state.fields[9] {
				uri, err := url.Parse(string(raw))
				if err != nil || uri.Scheme != "file" || uri.Host != "" {
					continue
				}
				candidate := filepath.Clean(uri.Path)
				if filepath.IsAbs(candidate) && adapters.PathContains(root, candidate) && filepath.Base(filepath.Dir(chatDirectory(a.ConfigRoot, candidate, "placeholder"))) == bucket.Name() {
					cwd = candidate
					break
				}
			}
			if cwd == "" || len(state.fields[1]) == 0 && len(state.fields[13]) == 0 && len(state.fields[11]) == 0 {
				continue
			}
			if sub := state.meta["subagentInfo"]; len(sub) > 0 && string(sub) != "null" {
				continue
			}
			activity := time.Time{}
			for _, p := range []string{path, path + "-wal"} {
				if info, err := os.Stat(p); err == nil && info.ModTime().After(activity) {
					activity = info.ModTime()
				}
			}
			result = append(result, contracts.DiscoveredSession{SchemaVersion: 1, AgentType: contracts.AgentCursor, NativeSessionID: entry.Name(), CWD: cwd, ActivityState: contracts.ActivityUnknown, UpdatedAt: activity.UTC().Format(time.RFC3339Nano)})
		}
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].UpdatedAt > result[j].UpdatedAt })
	return result, nil
}

func (a Adapter) ResumeCommand(id string) contracts.ResumeCommand {
	return contracts.ResumeCommand{Program: a.Command, Args: []string{"--resume", id}, Display: a.Command + " --resume " + id}
}

// EmptySession is used only with an observed new-session lifecycle and no
// submitted prompt. Missing storage by itself is never evidence of a rewind.
func (a Adapter) EmptySession(ctx context.Context, cwd, id string) (bool, error) {
	observation, _, err := a.Observe(ctx, cwd, id, adapters.ObservationCheckpoint{})
	if errors.Is(err, adapters.ErrNativeSessionMissing) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	defer os.Remove(observation.RawArtifact.Path)
	return len(observation.Normalized.Turns) == 0, nil
}
