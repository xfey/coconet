package cursor

import (
	"context"
	"crypto/md5"
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

	"github.com/xfey/coconet/adapters"
	"github.com/xfey/coconet/packages/sessionartifact"
	_ "modernc.org/sqlite"
)

const mediaType = "application/vnd.coconet.cursor-session+sqlite3"

func chatDirectory(profile, cwd, id string) string {
	// Cursor uses MD5 only as a workspace directory name, not for integrity.
	digest := md5.Sum([]byte(filepath.Clean(cwd)))
	return filepath.Join(profile, "chats", hex.EncodeToString(digest[:]), id)
}

func openReadOnly(path string) (*sql.DB, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, adapters.ErrNativeSessionMissing
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %v", adapters.ErrNativeSessionUnreadable, err)
	}
	if !info.Mode().IsRegular() || info.Size() > sessionartifact.MaxArtifactSize {
		return nil, errors.New("Cursor session database is not a regular file within the 256 MiB capture limit")
	}
	u := &url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Set("mode", "ro")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "trusted_schema(OFF)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err == nil {
		db.SetMaxOpenConns(1)
	}
	return db, err
}

func snapshotDatabase(ctx context.Context, path string) (string, error) {
	db, err := openReadOnly(path)
	if err != nil {
		return "", err
	}
	defer db.Close()
	var pages, pageSize int64
	if err := db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
		return "", err
	}
	if err := db.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
		return "", err
	}
	if pages < 1 || pageSize < 1 || pages > sessionartifact.MaxArtifactSize/pageSize {
		return "", errors.New("Cursor session exceeds the 256 MiB capture limit")
	}
	f, err := os.CreateTemp("", "coconet-cursor-snapshot-*.db")
	if err != nil {
		return "", err
	}
	pathCopy := f.Name()
	if err := f.Close(); err != nil {
		os.Remove(pathCopy)
		return "", err
	}
	// VACUUM INTO reads one committed SQLite snapshot, including the WAL. The
	// source is opened read-only; no checkpoint or write touches native history.
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", pathCopy); err != nil {
		os.Remove(pathCopy)
		return "", fmt.Errorf("snapshot Cursor session: %w", err)
	}
	return pathCopy, nil
}

type nativeState struct {
	meta   map[string]json.RawMessage
	root   []byte
	fields map[int][][]byte
	db     *sql.DB
}

func readState(ctx context.Context, db *sql.DB, id string) (nativeState, error) {
	s := nativeState{db: db}
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return s, err
	}
	if version != 1 {
		return s, errors.New("unsupported Cursor session format; update Coconet before importing this history")
	}
	var raw string
	if err := db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = '0'").Scan(&raw); err != nil {
		return s, fmt.Errorf("read Cursor session metadata: %w", err)
	}
	if len(raw) > 2<<20 {
		return s, errors.New("Cursor session metadata is too large")
	}
	data, err := hex.DecodeString(raw)
	if err != nil || json.Unmarshal(data, &s.meta) != nil {
		return s, errors.New("invalid Cursor session metadata")
	}
	var agentID, rootID string
	if json.Unmarshal(s.meta["agentId"], &agentID) != nil || agentID != id || json.Unmarshal(s.meta["latestRootBlobId"], &rootID) != nil {
		return s, errors.New("Cursor session identity or active history is invalid")
	}
	s.root, err = s.blob(ctx, rootID)
	if err != nil {
		return s, err
	}
	s.fields, err = wireFields(s.root)
	return s, err
}

func (s nativeState) blob(ctx context.Context, id string) ([]byte, error) {
	if decoded, err := hex.DecodeString(id); err != nil || len(decoded) != sha256.Size {
		return nil, errors.New("invalid Cursor history reference")
	}
	var data []byte
	if err := s.db.QueryRowContext(ctx, "SELECT data FROM blobs WHERE id = ? AND length(data) <= ?", id, sessionartifact.MaxArtifactSize).Scan(&data); err != nil {
		return nil, fmt.Errorf("%w: Cursor history blob is missing or too large", adapters.ErrIncompleteNativeHistory)
	}
	hash := sha256.Sum256(data)
	if hex.EncodeToString(hash[:]) != id {
		return nil, errors.New("Cursor history blob failed content verification")
	}
	return data, nil
}

// wireFields reads protobuf's standard wire format. Only native field numbers
// needed for active history are interpreted; unknown fields are skipped.
func wireFields(data []byte) (map[int][][]byte, error) {
	result := map[int][][]byte{}
	for len(data) > 0 {
		tag, n := binary.Uvarint(data)
		if n <= 0 || tag>>3 == 0 || tag>>3 > 1<<29-1 {
			return nil, errors.New("invalid Cursor history protobuf")
		}
		data = data[n:]
		field := int(tag >> 3)
		switch tag & 7 {
		case 0:
			_, n = binary.Uvarint(data)
			if n <= 0 {
				return nil, errors.New("truncated Cursor history value")
			}
			data = data[n:]
		case 1:
			if len(data) < 8 {
				return nil, errors.New("truncated Cursor history value")
			}
			data = data[8:]
		case 5:
			if len(data) < 4 {
				return nil, errors.New("truncated Cursor history value")
			}
			data = data[4:]
		case 2:
			length, n := binary.Uvarint(data)
			if n <= 0 || length > uint64(len(data)-n) {
				return nil, errors.New("truncated Cursor history reference")
			}
			data = data[n:]
			result[field] = append(result[field], data[:int(length)])
			data = data[int(length):]
		default:
			return nil, errors.New("unsupported Cursor history wire type")
		}
	}
	return result, nil
}

func appendField(data []byte, field int, value []byte) []byte {
	data = binary.AppendUvarint(data, uint64(field<<3|2))
	data = binary.AppendUvarint(data, uint64(len(value)))
	return append(data, value...)
}
