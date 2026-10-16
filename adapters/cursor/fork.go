package cursor

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/xfey/coconet/adapters"
	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/sessionartifact"
)

var _ adapters.TextImportAdapter = Adapter{}

func nativeUUID(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	sum[6] = sum[6]&15 | 0x50
	sum[8] = sum[8]&63 | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

// Fork copies only the selected, verified SQLite snapshot. It never mutates the
// source chat or inherits its permission overrides. A private ownership record
// makes publication retryable after a crash before the attempt marker is saved.
func (a Adapter) Fork(ctx context.Context, req adapters.ForkRequest) (adapters.ForkResult, error) {
	var zero adapters.ForkResult
	if !filepath.IsAbs(a.ConfigRoot) || !filepath.IsAbs(req.CWD) || !filepath.IsAbs(req.AttemptDirectory) || !nativeIDPattern.MatchString(req.SourceNativeSessionID) {
		return zero, errors.New("Cursor Fork requires absolute profile/workspace/attempt paths and a source conversation ID")
	}
	hash, size, err := adapters.HashFile(req.SourceArtifactPath)
	if err != nil || hash != req.SourceArtifactSHA256 || size <= 0 || size > sessionartifact.MaxArtifactSize {
		return zero, errors.New("Cursor Fork source artifact failed content verification; pull the selected version again")
	}
	id := nativeUUID("cursor-fork/1\x00" + req.AttemptDirectory + "\x00" + req.SourceNativeSessionID + "\x00" + hash)
	result := adapters.ForkResult{NativeSessionID: id}
	if marked, exists, err := adapters.LoadForkMarker(req, contracts.AgentCursor); err != nil {
		return zero, err
	} else if exists && marked.NativeSessionID != id {
		return zero, errors.New("Cursor Fork attempt identity changed")
	}
	dir := chatDirectory(a.ConfigRoot, req.CWD, id)
	owner := adapters.Digest(req.SourceNativeSessionID + "\x00" + hash + "\x00" + req.CWD)
	if _, err := os.Lstat(dir); err == nil {
		data, err := os.ReadFile(filepath.Join(dir, ".coconet-source"))
		if err != nil || string(data) != owner {
			return zero, errors.New("Cursor Fork target already exists and is not owned by this attempt")
		}
		if err := a.verifyFork(ctx, req.CWD, id); err != nil {
			return zero, err
		}
		return result, adapters.SaveForkMarker(req, contracts.AgentCursor, result)
	} else if !errors.Is(err, os.ErrNotExist) {
		return zero, err
	}
	source, err := openReadOnly(req.SourceArtifactPath)
	if err != nil {
		return zero, err
	}
	defer source.Close()
	s, err := readState(ctx, source, req.SourceNativeSessionID)
	if err != nil {
		return zero, err
	}
	if _, err = s.turns(ctx); err != nil {
		return zero, err
	}
	if err = os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
		return zero, err
	}
	staging, err := os.MkdirTemp(filepath.Dir(dir), ".coconet-fork-")
	if err != nil {
		return zero, err
	}
	defer os.RemoveAll(staging)
	target := filepath.Join(staging, "store.db")
	if _, err = source.ExecContext(ctx, "VACUUM INTO ?", target); err != nil {
		return zero, err
	}
	if err = os.Chmod(target, 0600); err != nil {
		return zero, err
	}
	db, err := sql.Open("sqlite", target)
	if err != nil {
		return zero, err
	}
	// Keep unknown protobuf fields, but replace the active workspace URI.
	root, err := replaceBytesField(s.root, 9, []byte((&url.URL{Scheme: "file", Path: req.CWD}).String()))
	if err == nil {
		var rootID []byte
		rootID, err = storeBlob(ctx, db, root)
		if err == nil {
			meta := map[string]any{"agentId": id, "latestRootBlobId": hex.EncodeToString(rootID), "createdAt": time.Now().UnixMilli(), "mode": "default", "isRunEverything": false}
			for _, key := range []string{"name", "blobEncryptionKey"} {
				if raw, ok := s.meta[key]; ok {
					meta[key] = raw
				}
			}
			err = storeMeta(ctx, db, meta)
		}
	}
	err = errors.Join(err, db.Close())
	if err != nil {
		return zero, err
	}
	if err = os.WriteFile(filepath.Join(staging, ".coconet-source"), []byte(owner), 0600); err != nil {
		return zero, err
	}
	if err = os.Rename(staging, dir); err != nil {
		return zero, err
	}
	if err = a.verifyFork(ctx, req.CWD, id); err != nil {
		return zero, err
	}
	return result, adapters.SaveForkMarker(req, contracts.AgentCursor, result)
}

func (a Adapter) verifyFork(ctx context.Context, cwd, id string) error {
	db, err := openReadOnly(filepath.Join(chatDirectory(a.ConfigRoot, cwd, id), "store.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	s, err := readState(ctx, db, id)
	if err != nil {
		return err
	}
	_, err = s.turns(ctx)
	return err
}

func storeBlob(ctx context.Context, db *sql.DB, data []byte) ([]byte, error) {
	sum := sha256.Sum256(data)
	_, err := db.ExecContext(ctx, "INSERT OR IGNORE INTO blobs(id,data) VALUES(?,?)", hex.EncodeToString(sum[:]), data)
	return sum[:], err
}
func storeMeta(ctx context.Context, db *sql.DB, meta any) error {
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, "INSERT OR REPLACE INTO meta(key,value) VALUES('0',?)", hex.EncodeToString(data))
	return err
}

// ImportText writes only the portable main dialogue. Native prompt references
// carry model context; UI turns independently carry the visible transcript.
func (a Adapter) ImportText(ctx context.Context, req adapters.TextImportRequest) (adapters.ForkResult, error) {
	var zero adapters.ForkResult
	if !filepath.IsAbs(req.AttemptDirectory) || !filepath.IsAbs(req.CWD) {
		return zero, errors.New("Cursor import requires absolute workspace and attempt paths")
	}
	observed, err := time.Parse(time.RFC3339Nano, req.ObservedAt)
	if err != nil {
		return zero, errors.New("Cursor import requires a valid source timestamp")
	}
	if err = os.MkdirAll(req.AttemptDirectory, 0700); err != nil {
		return zero, err
	}
	staging, err := os.MkdirTemp(req.AttemptDirectory, ".cursor-import-")
	if err != nil {
		return zero, err
	}
	defer os.RemoveAll(staging)
	path := filepath.Join(staging, "store.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return zero, err
	}
	defer db.Close()
	if _, err = db.ExecContext(ctx, "PRAGMA user_version=1; CREATE TABLE blobs(id TEXT PRIMARY KEY,data BLOB); CREATE TABLE meta(key TEXT PRIMARY KEY,value TEXT)"); err != nil {
		return zero, err
	}
	id := nativeUUID("cursor-text/1\x00" + req.CompactSHA256)
	var root, uiUser []byte
	var steps [][]byte
	var turnID string
	flush := func() error {
		if len(uiUser) == 0 && len(steps) == 0 {
			return nil
		}
		turn := appendField(nil, 1, uiUser)
		for _, step := range steps {
			turn = appendField(turn, 2, step)
		}
		turn = appendField(turn, 3, []byte(turnID))
		turn = appendField(turn, 10, []byte(turnID))
		ref, e := storeBlob(ctx, db, appendField(nil, 1, turn))
		if e != nil {
			return e
		}
		root = appendField(root, 8, ref)
		uiUser = nil
		steps = nil
		return nil
	}
	err = adapters.WalkCompact(ctx, req, func(msg adapters.TextMessage) error {
		requestID := nativeUUID(id + "\x00" + msg.ID)
		prompt := map[string]any{"role": msg.Role, "content": []any{map[string]string{"type": "text", "text": msg.Text}}, "providerOptions": map[string]any{"cursor": map[string]string{"requestId": requestID, "coconetTextImport": adapters.TextImportFormat}}}
		data, e := json.Marshal(prompt)
		if e != nil {
			return e
		}
		ref, e := storeBlob(ctx, db, data)
		if e != nil {
			return e
		}
		root = appendField(root, 1, ref)
		if msg.Role == "user" {
			if e = flush(); e != nil {
				return e
			}
			turnID = requestID
			uiUser, e = storeBlob(ctx, db, appendField(appendField(nil, 1, []byte(msg.Text)), 2, []byte(requestID)))
			return e
		}
		if turnID == "" {
			turnID = requestID
		}
		step, e := storeBlob(ctx, db, appendField(nil, 1, appendField(nil, 1, []byte(msg.Text))))
		if e != nil {
			return e
		}
		steps = append(steps, step)
		return nil
	})
	if err != nil {
		return zero, err
	}
	if err = flush(); err != nil {
		return zero, err
	}
	root = appendField(root, 9, []byte((&url.URL{Scheme: "file", Path: req.CWD}).String()))
	ref, err := storeBlob(ctx, db, root)
	if err != nil {
		return zero, err
	}
	if err = storeMeta(ctx, db, map[string]any{"agentId": id, "latestRootBlobId": hex.EncodeToString(ref), "createdAt": observed.UnixMilli(), "name": "Imported conversation", "mode": "default", "isRunEverything": false}); err != nil {
		return zero, err
	}
	if err = db.Close(); err != nil {
		return zero, err
	}
	hash, _, err := adapters.HashFile(path)
	if err != nil {
		return zero, err
	}
	return a.Fork(ctx, adapters.ForkRequest{AttemptDirectory: req.AttemptDirectory, SourceArtifactPath: path, SourceArtifactSHA256: hash, SourceNativeSessionID: id, CWD: req.CWD})
}

func replaceBytesField(data []byte, field int, value []byte) ([]byte, error) {
	var out []byte
	for len(data) > 0 {
		tag, n := binary.Uvarint(data)
		if n <= 0 {
			return nil, errors.New("invalid Cursor protobuf tag")
		}
		end := n
		switch tag & 7 {
		case 0:
			_, n = binary.Uvarint(data[end:])
			if n <= 0 {
				return nil, errors.New("invalid Cursor protobuf value")
			}
			end += n
		case 1:
			end += 8
		case 5:
			end += 4
		case 2:
			l, k := binary.Uvarint(data[end:])
			if k <= 0 || l > uint64(len(data)-end-k) {
				return nil, errors.New("invalid Cursor protobuf bytes")
			}
			end += k + int(l)
		default:
			return nil, errors.New("unsupported Cursor protobuf field")
		}
		if end > len(data) {
			return nil, errors.New("truncated Cursor protobuf")
		}
		if int(tag>>3) != field {
			out = append(out, data[:end]...)
		}
		data = data[end:]
	}
	return appendField(out, field, value), nil
}
