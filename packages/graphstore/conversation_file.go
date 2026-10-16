package graphstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/xfey/coconet/packages/conversation"
	"github.com/xfey/coconet/packages/sessionartifact"
)

// ConversationFile materializes only visible dialogue, one verified block at a
// time. Original artifacts are neither required nor opened for a compact read.
func (s *Store) ConversationFile(ctx context.Context, digest string, count int) (string, error) {
	release := s.PinContent()
	defer release()
	s.materializedMu.Lock()
	defer s.materializedMu.Unlock()
	data, err := s.Object(digest)
	if err != nil {
		return "", err
	}
	var manifest conversation.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return "", err
	}
	if manifest.Count != count {
		return "", errors.New("fixed conversation count differs")
	}
	directory, err := s.materializedDirectory()
	if err != nil {
		return "", err
	}
	file, err := os.CreateTemp(directory, ".conversation-*")
	if err != nil {
		return "", err
	}
	defer file.Close()
	defer os.Remove(file.Name())
	hash := sha256.New()
	output := &boundedMaterializedWriter{writer: io.MultiWriter(file, hash), limit: sessionartifact.MaxArtifactSize}
	encoder := json.NewEncoder(output)
	err = conversation.Walk(manifest, func(block conversation.Block) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return s.Object(block.SHA256)
	}, func(entry conversation.Entry) error {
		turn := entry.Turn
		if turn.Role != "user" && turn.Role != "assistant" || turn.Kind == "event" {
			return nil
		}
		text := turn.Text
		if entry.Visible != nil {
			text = *entry.Visible
		} else if turn.Kind == "tool_call" || turn.Kind == "tool_result" {
			return nil
		}
		if strings.TrimSpace(text) == "" {
			return nil
		}
		return encoder.Encode(struct {
			ID   string `json:"turn_id"`
			Role string `json:"role"`
			Text string `json:"text"`
		}{turn.TurnID, turn.Role, text})
	})
	if err != nil {
		return "", err
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	digest = hex.EncodeToString(hash.Sum(nil))
	path := filepath.Join(directory, digest+".jsonl")
	if _, err := s.allowMaterialized(path, output.written); err != nil {
		return "", err
	}
	if err := os.Link(file.Name(), path); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return "", err
		}
	} else {
		s.materializedBytes.Add(output.written)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("compact cache must be a regular file")
	}
	existing, err := os.Open(path)
	if err != nil {
		return "", err
	}
	hash.Reset()
	_, err = io.Copy(hash, existing)
	existing.Close()
	if err != nil {
		return "", err
	}
	if hex.EncodeToString(hash.Sum(nil)) != digest {
		return "", errors.New("compact cache integrity mismatch")
	}
	dir, err := os.Open(directory)
	if err != nil {
		return "", err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return "", err
	}
	return path, s.touchMaterialized(path)
}

// Bound incomplete output too: a failed export must not fill the disk before
// the final digest and cache quota can be checked. One writer runs at a time.
type boundedMaterializedWriter struct {
	writer         io.Writer
	written, limit int64
}

func (w *boundedMaterializedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.limit-w.written {
		return 0, errors.New("compact session exceeds the 256 MiB export limit")
	}
	n, err := w.writer.Write(p)
	w.written += int64(n)
	return n, err
}
