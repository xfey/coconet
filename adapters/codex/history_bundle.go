package codex

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/xfey/coconet/adapters"
	"github.com/xfey/coconet/packages/sessionartifact"
)

type historyMember struct {
	NativeID string `json:"native_session_id"`
	Size     int64  `json:"size"`
}

type historyManifest struct {
	Format        string          `json:"format"`
	SchemaVersion int             `json:"schema_version"`
	RootNativeID  string          `json:"root_native_session_id"`
	Members       []historyMember `json:"members"`
}

func historyMemberName(index int) string { return fmt.Sprintf("rollouts/%03d.jsonl", index) }

func writeHistoryBundle(output io.Writer, nativeID string, segments []rolloutSegment) error {
	manifest := historyManifest{Format: historyFormat, SchemaVersion: 1, RootNativeID: nativeID}
	seen := map[string]bool{}
	for _, segment := range segments {
		if seen[segment.NativeID] {
			manifest.SchemaVersion = 2
		}
		seen[segment.NativeID] = true
		manifest.Members = append(manifest.Members, segment.historyMember)
	}
	content, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	writer := tar.NewWriter(output)
	if err := writer.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	if _, err := writer.Write(content); err != nil {
		return err
	}
	for index, segment := range segments {
		if err := writer.WriteHeader(&tar.Header{Name: historyMemberName(index), Mode: 0o600, Size: segment.Size, Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		if _, err := io.Copy(writer, io.NewSectionReader(segment.file, 0, segment.Size)); err != nil {
			return err
		}
	}
	return writer.Close()
}

func readHistoryBundle(input io.Reader, request adapters.ForkRequest) ([]rolloutSegment, []string, error) {
	reader := tar.NewReader(input)
	header, err := reader.Next()
	if err != nil || header.Name != "manifest.json" || header.Typeflag != tar.TypeReg || header.Size <= 0 || header.Size > 64<<10 {
		return nil, nil, errors.New("Codex history bundle manifest is invalid")
	}
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var manifest historyManifest
	if err := decoder.Decode(&manifest); err != nil {
		return nil, nil, fmt.Errorf("decode Codex history manifest: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF || manifest.Format != historyFormat || (manifest.SchemaVersion != 1 && manifest.SchemaVersion != 2) || manifest.RootNativeID != request.SourceNativeSessionID || len(manifest.Members) < 2 || len(manifest.Members) > maxHistoryDepth {
		return nil, nil, errors.New("Codex history bundle identity or format is invalid")
	}
	var segments []rolloutSegment
	var paths []string
	seen := make(map[string]bool)
	var total int64
	for index, member := range manifest.Members {
		total += member.Size
		if !adapters.ValidNativeSessionID(member.NativeID) || (seen[member.NativeID] && manifest.SchemaVersion == 1) || member.Size <= 0 || member.Size > sessionartifact.MaxArtifactSize || total > sessionartifact.MaxArtifactSize {
			return segments, paths, errors.New("Codex history bundle member is invalid or too large")
		}
		seen[member.NativeID] = true
		header, err := reader.Next()
		if err != nil || header.Name != historyMemberName(index) || header.Typeflag != tar.TypeReg || header.Size != member.Size {
			return segments, paths, errors.New("Codex history bundle member does not match its manifest")
		}
		file, err := os.CreateTemp(request.AttemptDirectory, ".codex-history-*.jsonl")
		if err != nil {
			return segments, paths, err
		}
		paths = append(paths, file.Name())
		_, copyErr := io.Copy(file, reader)
		closeErr := file.Close()
		if copyErr != nil {
			return segments, paths, copyErr
		}
		if closeErr != nil {
			return segments, paths, closeErr
		}
		segment, err := openRolloutSegment(file.Name(), member.NativeID, member.Size)
		if err != nil {
			return segments, paths, err
		}
		segments = append(segments, segment)
		base := segment.metadata.HistoryBase
		if index == 0 {
			if base != nil {
				return segments, paths, errors.New("Codex history bundle is missing an ancestor")
			}
		} else if base == nil {
			return segments, paths, errors.New("Codex history bundle contains unrelated history")
		} else if err := validateHistoryBoundary(segments[index-1], *base); err != nil {
			return segments, paths, err
		}
	}
	if segments[len(segments)-1].NativeID != manifest.RootNativeID {
		return segments, paths, errors.New("Codex history bundle root is inconsistent")
	}
	if _, err := reader.Next(); err != io.EOF {
		return segments, paths, errors.New("Codex history bundle contains unexpected trailing members")
	}
	return segments, paths, nil
}
