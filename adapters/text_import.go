package adapters

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/conversation"
	"github.com/xfey/coconet/packages/sessionartifact"
)

// TextImportFormat identifies the portable, effective user/assistant history.
// Tool state, reasoning, images and provider configuration are not imported.
const TextImportFormat = "main-dialogue/1"

type TextImportRequest struct {
	AttemptDirectory string
	CompactPath      string
	CompactSHA256    string
	CWD              string
	ObservedAt       string
}

type TextImportAdapter interface {
	ForkAdapter
	ImportText(context.Context, TextImportRequest) (ForkResult, error)
}

type TextMessage struct {
	ID   string `json:"turn_id"`
	Role string `json:"role"`
	Text string `json:"text"`
}

// WalkCompact reads a bounded, hash-verified fixed source. Callers must not
// publish partial output until the entire input has passed validation.
func WalkCompact(ctx context.Context, request TextImportRequest, visit func(TextMessage) error) error {
	file, err := os.Open(request.CompactPath)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > sessionartifact.MaxArtifactSize {
		return errors.New("cross-Agent Fork requires nonempty dialogue within the 256 MiB limit")
	}
	hash := sha256.New()
	scanner := bufio.NewScanner(io.TeeReader(io.LimitReader(file, sessionartifact.MaxArtifactSize+1), hash))
	scanner.Buffer(make([]byte, 64<<10), conversation.MaxEntryBytes)
	seen := map[string]bool{}
	for scanner.Scan() {
		var message TextMessage
		if err := ctx.Err(); err != nil {
			return err
		}
		if !utf8.Valid(scanner.Bytes()) {
			return errors.New("cross-Agent dialogue is not valid UTF-8")
		}
		decoder := json.NewDecoder(strings.NewReader(scanner.Text()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&message); err != nil {
			return fmt.Errorf("decode cross-Agent dialogue: %w", err)
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return errors.New("cross-Agent dialogue has trailing data")
		}
		if message.ID == "" || seen[message.ID] || (message.Role != "user" && message.Role != "assistant") || strings.TrimSpace(message.Text) == "" {
			return errors.New("cross-Agent dialogue has invalid identity, role or empty text")
		}
		seen[message.ID] = true
		if err := visit(message); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if len(seen) == 0 || hex.EncodeToString(hash.Sum(nil)) != request.CompactSHA256 {
		return errors.New("cross-Agent dialogue failed content verification; pull the selected source again")
	}
	return nil
}

// PrepareClaudeText produces a disposable import artifact shared by both target
// adapters. IDs and bytes are stable across a retry of the same fixed operation.
func PrepareClaudeText(ctx context.Context, request TextImportRequest) (ForkRequest, func(), error) {
	empty := func() {}
	if !filepath.IsAbs(request.CWD) || !filepath.IsAbs(request.AttemptDirectory) || len(request.CompactSHA256) != 64 {
		return ForkRequest{}, empty, errors.New("cross-Agent import requires a fixed dialogue hash and absolute workspace/attempt paths")
	}
	if _, err := time.Parse(time.RFC3339Nano, request.ObservedAt); err != nil {
		return ForkRequest{}, empty, errors.New("cross-Agent import requires a valid source timestamp")
	}
	if err := os.MkdirAll(request.AttemptDirectory, 0700); err != nil {
		return ForkRequest{}, empty, err
	}
	file, err := os.CreateTemp(request.AttemptDirectory, ".text-import-*.jsonl")
	if err != nil {
		return ForkRequest{}, empty, err
	}
	cleanup := func() { _ = os.Remove(file.Name()) }
	sourceID := textUUID(TextImportFormat + "\x00" + request.AttemptDirectory + "\x00" + request.CompactSHA256)
	encoder := json.NewEncoder(&importWriter{writer: file, remaining: sessionartifact.MaxArtifactSize})
	var parent *string
	err = WalkCompact(ctx, request, func(message TextMessage) error {
		id := textUUID(sourceID + "\x00" + message.ID)
		record := map[string]any{"type": message.Role, "uuid": id, "parentUuid": parent, "sessionId": sourceID, "cwd": request.CWD, "isSidechain": false, "timestamp": request.ObservedAt, "message": map[string]any{"role": message.Role, "content": []map[string]string{{"type": "text", "text": message.Text}}}}
		if err := encoder.Encode(record); err != nil {
			return err
		}
		parent = &id
		return nil
	})
	err = errors.Join(err, file.Sync(), file.Close())
	if err != nil {
		cleanup()
		return ForkRequest{}, empty, err
	}
	hash, size, err := HashFile(file.Name())
	if err != nil || size > sessionartifact.MaxArtifactSize {
		cleanup()
		return ForkRequest{}, empty, errors.New("converted dialogue exceeds the 256 MiB artifact limit or cannot be read")
	}
	return ForkRequest{AttemptDirectory: request.AttemptDirectory, SourceArtifactPath: file.Name(), SourceArtifactSHA256: hash, SourceNativeSessionID: sourceID, CWD: request.CWD}, cleanup, nil
}

func textUUID(value string) string {
	digest := sha256.Sum256([]byte(value))
	digest[6] = (digest[6] & 15) | 0x50
	digest[8] = (digest[8] & 63) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", digest[:4], digest[4:6], digest[6:8], digest[8:10], digest[10:16])
}

// VerifyImportedText runs before the inherited baseline is registered. Extra,
// missing or changed main messages must never be silently marked as inherited.
func VerifyImportedText(ctx context.Context, request TextImportRequest, observation contracts.SessionObservation) error {
	if err := ValidateVisibleText(observation.VisibleText, observation.Normalized.Turns, observation.Normalized.ToCursor); err != nil {
		return err
	}
	visible := map[string][]contracts.TextRange{}
	if observation.VisibleText != nil {
		for _, turn := range observation.VisibleText.Turns {
			visible[turn.TurnID] = turn.Ranges
		}
	}
	var actual []TextMessage
	for _, turn := range observation.Normalized.Turns {
		if turn.Role != "user" && turn.Role != "assistant" || turn.Kind == "event" {
			continue
		}
		text := turn.Text
		if observation.VisibleText != nil {
			text = VisibleText(text, visible[turn.TurnID])
		} else if turn.Kind != "message" {
			continue
		}
		if strings.TrimSpace(text) != "" {
			actual = append(actual, TextMessage{Role: turn.Role, Text: text})
		}
	}
	index := 0
	err := WalkCompact(ctx, request, func(message TextMessage) error {
		if index >= len(actual) || actual[index].Role != message.Role || actual[index].Text != message.Text {
			return errors.New("target Agent changed or omitted imported dialogue; no inherited baseline was registered")
		}
		index++
		return nil
	})
	if err != nil {
		return err
	}
	if index != len(actual) {
		return errors.New("target Agent added unexpected dialogue; no inherited baseline was registered")
	}
	return nil
}

// Reject oversized output while writing, including JSON routing overhead.
type importWriter struct {
	writer    io.Writer
	remaining int64
}

func (w *importWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > w.remaining {
		return 0, errors.New("converted dialogue exceeds the 256 MiB artifact limit")
	}
	n, err := w.writer.Write(data)
	w.remaining -= int64(n)
	return n, err
}
