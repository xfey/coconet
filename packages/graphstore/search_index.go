package graphstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xfey/coconet/packages/conversation"
	"github.com/xfey/coconet/packages/workgraph"
	_ "modernc.org/sqlite"
)

const indexSchema = `
PRAGMA journal_mode=WAL;
CREATE TABLE IF NOT EXISTS index_identity(value TEXT NOT NULL);
INSERT INTO index_identity(value) SELECT hex(randomblob(16)) WHERE NOT EXISTS(SELECT 1 FROM index_identity);
CREATE TABLE IF NOT EXISTS generations(project TEXT PRIMARY KEY, value INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS index_gc(project TEXT PRIMARY KEY);
CREATE TABLE IF NOT EXISTS sources(project TEXT NOT NULL, source TEXT NOT NULL, digest TEXT NOT NULL, PRIMARY KEY(project,source));
CREATE TABLE IF NOT EXISTS documents(id INTEGER PRIMARY KEY, project TEXT NOT NULL, session TEXT NOT NULL, identity TEXT NOT NULL, turn TEXT NOT NULL, ordinal INTEGER NOT NULL, role TEXT NOT NULL, text TEXT NOT NULL, folded TEXT NOT NULL, UNIQUE(project,session,identity));
CREATE VIRTUAL TABLE IF NOT EXISTS text_fts USING fts5(text, content='documents', content_rowid='id', tokenize='trigram');
CREATE TRIGGER IF NOT EXISTS document_insert AFTER INSERT ON documents BEGIN INSERT INTO text_fts(rowid,text) VALUES(new.id,new.text); END;
CREATE TRIGGER IF NOT EXISTS document_delete AFTER DELETE ON documents BEGIN INSERT INTO text_fts(text_fts,rowid,text) VALUES('delete',old.id,old.text); END;
CREATE TABLE IF NOT EXISTS source_ranges(project TEXT NOT NULL, source TEXT NOT NULL, lo INTEGER NOT NULL, hi INTEGER NOT NULL, PRIMARY KEY(project,source,lo));
CREATE INDEX IF NOT EXISTS source_ranges_bounds ON source_ranges(project,lo,hi);
CREATE INDEX IF NOT EXISTS document_project ON documents(project,id);
`

type IndexCoverage struct {
	Revision   int64  `json:"revision"`
	Instance   string `json:"instance"`
	Generation int64  `json:"generation"`
	Expected   int    `json:"expected"`
	Indexed    int    `json:"indexed"`
	Complete   bool   `json:"complete"`
	Scope      string `json:"scope"`
}
type ContentMatch struct {
	Source  workgraph.Source `json:"source"`
	TurnID  string           `json:"turn_id"`
	Ordinal int              `json:"ordinal"`
	Role    string           `json:"role"`
	Snippet string           `json:"snippet"`
	NodeIDs []string         `json:"work_node_ids"`
}
type ContentPage struct {
	Matches  []ContentMatch
	More     bool
	Coverage IndexCoverage
	Basis    string
}

// This file is derived from verified compact content and can be rebuilt while
// offline. It is not the graph database and is never a source for native Fork.
func (s *Store) searchDB() (*sql.DB, error) {
	s.indexMu.Lock()
	defer s.indexMu.Unlock()
	if s.indexDB != nil {
		return s.indexDB, nil
	}
	path := filepath.Join(s.root, "session-index.sqlite")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Lstat(path + suffix)
		if err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
			return nil, errors.New("session index must be a regular private file")
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: path}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "cache_size(-8192)")
	q.Add("_pragma", "max_page_count(524288)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	if _, err := db.Exec(indexSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize local Session index: %w", err)
	}
	s.indexDB = db
	return db, nil
}
func (s *Store) closeIndex() error {
	s.indexMu.Lock()
	defer s.indexMu.Unlock()
	if s.indexDB == nil {
		return nil
	}
	return s.indexDB.Close()
}

// IndexProject advances at most limit sources, committing each source only
// after validating its entire compact closure. Prefix messages are stored once;
// consecutive document IDs become source ranges rather than per-version copies.
func (s *Store) IndexProject(ctx context.Context, project string, limit int) (IndexCoverage, error) {
	if limit < 1 || limit > 64 {
		return IndexCoverage{}, errors.New("index batch must be 1–64")
	}
	ctx, cancel := indexTimeout(ctx)
	defer cancel()
	s.indexWriteMu.Lock()
	defer s.indexWriteMu.Unlock()
	release := s.PinContent()
	defer release()
	state, err := s.State(project)
	if err != nil {
		return IndexCoverage{}, err
	}
	db, err := s.searchDB()
	if err != nil {
		return IndexCoverage{}, err
	}
	coverage, err := s.IndexCoverage(ctx, state)
	if err != nil {
		return IndexCoverage{}, err
	}
	var stored, pendingGC int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sources WHERE project=?`, project).Scan(&stored); err != nil {
		return coverage, err
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM index_gc WHERE project=?`, project).Scan(&pendingGC); err != nil {
		return coverage, err
	}
	if coverage.Complete && stored == coverage.Expected && pendingGC == 0 {
		return coverage, nil
	}
	retained := workgraph.ActiveSources(state)
	keys := make([]string, 0, len(retained))
	for source := range retained {
		keys = append(keys, source)
	}
	sort.Strings(keys)
	if s.indexCursor == nil {
		s.indexCursor = map[string]string{}
	}
	start := sort.SearchStrings(keys, s.indexCursor[project])
	if start < len(keys) && keys[start] == s.indexCursor[project] {
		start++
	}
	keys = append(append([]string{}, keys[start:]...), keys[:start]...)
	// Retired sources are filtered in every query, even before this cleanup runs.
	encoded, _ := json.Marshal(keys)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return IndexCoverage{}, err
	}
	removed, err := tx.ExecContext(ctx, `DELETE FROM sources WHERE project=? AND source NOT IN (SELECT value FROM json_each(?))`, project, string(encoded))
	if err == nil {
		_, err = tx.ExecContext(ctx, `DELETE FROM source_ranges WHERE project=? AND source NOT IN (SELECT value FROM json_each(?))`, project, string(encoded))
	}
	if err == nil {
		n, _ := removed.RowsAffected()
		if n > 0 {
			err = bumpIndex(ctx, tx, project)
			if err == nil {
				_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO index_gc(project) VALUES(?)`, project)
				pendingGC = 1
			}
		}
	}
	if err != nil {
		tx.Rollback()
		return IndexCoverage{}, err
	}
	if pendingGC > 0 {
		deleted, err := tx.ExecContext(ctx, `DELETE FROM documents WHERE id IN(SELECT d.id FROM documents d WHERE d.project=? AND NOT EXISTS(SELECT 1 FROM source_ranges r WHERE r.project=d.project AND r.lo<=d.id AND r.hi>=d.id) LIMIT 256)`, project)
		if err != nil {
			tx.Rollback()
			return IndexCoverage{}, err
		}
		n, _ := deleted.RowsAffected()
		if n < 256 {
			if _, err := tx.ExecContext(ctx, `DELETE FROM index_gc WHERE project=?`, project); err != nil {
				tx.Rollback()
				return IndexCoverage{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return IndexCoverage{}, err
	}
	var failures []error
	done := 0
	for _, key := range keys {
		snapshot, ok := state.Snapshots[key]
		if !ok {
			continue
		}
		var digest string
		err := db.QueryRowContext(ctx, `SELECT digest FROM sources WHERE project=? AND source=?`, project, key).Scan(&digest)
		if err == nil && digest == snapshot.Digest {
			continue
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return IndexCoverage{}, err
		}
		if done >= limit {
			break
		}
		s.indexCursor[project] = key
		if err := s.indexSource(ctx, db, project, snapshot); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				failures = append(failures, err)
			}
		} else if err := s.RecordTransfer(Transfer{ProjectID: project, Operation: "index", Status: "completed", SHA256: snapshot.Digest, Revision: state.Revision}); err != nil {
			failures = append(failures, err)
		}
		done++
	}
	coverage, err = s.IndexCoverage(ctx, state)
	return coverage, errors.Join(append(failures, err)...)
}
func bumpIndex(ctx context.Context, tx *sql.Tx, project string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO generations(project,value) VALUES(?,1) ON CONFLICT(project) DO UPDATE SET value=value+1`, project)
	return err
}
func (s *Store) indexSource(ctx context.Context, db *sql.DB, project string, snapshot workgraph.Snapshot) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM source_ranges WHERE project=? AND source=?`, project, snapshot.Source.Key()); err != nil {
		return err
	}
	var lo, hi int64
	flush := func() error {
		if lo == 0 {
			return nil
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO source_ranges(project,source,lo,hi) VALUES(?,?,?,?)`, project, snapshot.Source.Key(), lo, hi)
		return err
	}
	err = s.WalkConversation(ctx, snapshot.Conversation, snapshot.Turns, func(entry conversation.Entry) error {
		turn := entry.Turn
		if (turn.Role != "user" && turn.Role != "assistant") || turn.Kind == "event" {
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
		identity := workgraph.Digest([]any{turn.TurnID, turn.Ordinal, turn.Role, text})
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO documents(project,session,identity,turn,ordinal,role,text,folded) VALUES(?,?,?,?,?,?,?,?)`, project, snapshot.SessionID, identity, turn.TurnID, turn.Ordinal, turn.Role, text, strings.ToLower(text)); err != nil {
			return err
		}
		var id int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM documents WHERE project=? AND session=? AND identity=?`, project, snapshot.SessionID, identity).Scan(&id); err != nil {
			return err
		}
		if lo == 0 {
			lo, hi = id, id
		} else if id == hi+1 {
			hi = id
		} else {
			if err := flush(); err != nil {
				return err
			}
			lo, hi = id, id
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := flush(); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO sources(project,source,digest) VALUES(?,?,?) ON CONFLICT(project,source) DO UPDATE SET digest=excluded.digest`, project, snapshot.Source.Key(), snapshot.Digest); err != nil {
		return err
	}
	if err := bumpIndex(ctx, tx, project); err != nil {
		return err
	}
	return tx.Commit()
}

func indexSelection(ctx context.Context, tx *sql.Tx, state workgraph.State) (IndexCoverage, []string, error) {
	retained := workgraph.ActiveSources(state)
	coverage := IndexCoverage{Revision: state.Revision, Expected: len(retained), Scope: "compact_user_assistant_text"}
	if err := tx.QueryRowContext(ctx, `SELECT value FROM index_identity`).Scan(&coverage.Instance); err != nil {
		return coverage, nil, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT value FROM generations WHERE project=?`, state.ProjectID).Scan(&coverage.Generation); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return coverage, nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT source,digest FROM sources WHERE project=?`, state.ProjectID)
	if err != nil {
		return coverage, nil, err
	}
	defer rows.Close()
	keys := []string{}
	for rows.Next() {
		var key, digest string
		if err := rows.Scan(&key, &digest); err != nil {
			return coverage, nil, err
		}
		if snapshot, ok := state.Snapshots[key]; ok && retained[key] && snapshot.Digest == digest {
			keys = append(keys, key)
		}
	}
	if err := rows.Err(); err != nil {
		return coverage, nil, err
	}
	sort.Strings(keys)
	coverage.Indexed = len(keys)
	coverage.Complete = coverage.Indexed == coverage.Expected
	return coverage, keys, nil
}
func (s *Store) IndexCoverage(ctx context.Context, state workgraph.State) (IndexCoverage, error) {
	db, err := s.searchDB()
	if err != nil {
		return IndexCoverage{}, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return IndexCoverage{}, err
	}
	defer tx.Rollback()
	coverage, _, err := indexSelection(ctx, tx, state)
	return coverage, err
}

func (s *Store) SearchContent(ctx context.Context, state workgraph.State, query string, offset, limit int, expected IndexCoverage) (ContentPage, error) {
	result := ContentPage{Matches: []ContentMatch{}, Basis: fmt.Sprintf("%x", sha256.Sum256(nil))}
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) < 1 || len(terms) > 64 || offset < 0 || limit < 0 || limit > 100 {
		return result, errors.New("search supports 1–64 terms and bounded pagination")
	}
	db, err := s.searchDB()
	if err != nil {
		return result, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	coverage, keys, err := indexSelection(ctx, tx, state)
	result.Coverage = coverage
	if err != nil {
		return result, err
	}
	if coverage.Generation != expected.Generation || coverage.Instance != expected.Instance {
		return result, errors.New("Session index changed; restart search without a cursor")
	}
	if len(keys) == 0 {
		return result, nil
	}
	encoded, _ := json.Marshal(keys)
	unions := []string{}
	args := []any{}
	long := []string{}
	for _, term := range terms {
		if utf8.RuneCountInString(term) >= 3 {
			long = append(long, `"`+strings.ReplaceAll(term, `"`, `""`)+`"`)
		} else {
			unions = append(unions, `SELECT id FROM documents WHERE project=? AND instr(folded,?)>0`)
			args = append(args, state.ProjectID, term)
		}
	}
	if len(long) > 0 {
		unions = append(unions, `SELECT rowid AS id FROM text_fts WHERE text_fts MATCH ?`)
		args = append(args, strings.Join(long, " OR "))
	}
	// Cursor validity depends on matching documents and their source mappings,
	// not unrelated project/index writes. Stream identities rather than storing
	// a result cache or loading every matching conversation into memory.
	nodeSources := map[string][]string{}
	for _, node := range state.Nodes {
		if node.State != "withdrawn" {
			nodeSources[node.Source.Key()] = append(nodeSources[node.Source.Key()], node.ID)
		}
	}
	for key := range nodeSources {
		sort.Strings(nodeSources[key])
	}
	prefix := `WITH hits AS (` + strings.Join(unions, " UNION ") + `) `
	args = append(args, state.ProjectID, string(encoded))
	basisRows, err := tx.QueryContext(ctx, prefix+`SELECT d.id,d.identity,r.source FROM hits h JOIN documents d ON d.id=h.id JOIN source_ranges r ON r.project=d.project AND r.lo<=d.id AND r.hi>=d.id WHERE d.project=? AND r.source IN(SELECT value FROM json_each(?)) ORDER BY d.id,r.source`, args...)
	if err != nil {
		return result, err
	}
	hash := sha256.New()
	encoder := json.NewEncoder(hash)
	for basisRows.Next() {
		var id int64
		var identity, source string
		if err := basisRows.Scan(&id, &identity, &source); err != nil {
			basisRows.Close()
			return result, err
		}
		if err := encoder.Encode([]any{id, identity, source, nodeSources[source]}); err != nil {
			basisRows.Close()
			return result, err
		}
	}
	err = basisRows.Err()
	basisRows.Close()
	if err != nil {
		return result, err
	}
	result.Basis = fmt.Sprintf("%x", hash.Sum(nil))
	statement := prefix + `SELECT d.id,d.turn,d.ordinal,d.role,d.text FROM hits h JOIN documents d ON d.id=h.id WHERE d.project=? AND EXISTS(SELECT 1 FROM source_ranges r WHERE r.project=d.project AND r.source IN(SELECT value FROM json_each(?)) AND r.lo<=d.id AND r.hi>=d.id) ORDER BY d.id LIMIT ? OFFSET ?`
	args = append(args, limit+1, offset)
	rows, err := tx.QueryContext(ctx, statement, args...)
	if err != nil {
		return result, err
	}
	type match struct {
		id    int64
		value ContentMatch
	}
	matches := []match{}
	for rows.Next() {
		var m match
		var text string
		if err := rows.Scan(&m.id, &m.value.TurnID, &m.value.Ordinal, &m.value.Role, &text); err != nil {
			rows.Close()
			return result, err
		}
		m.value.Snippet = contentSnippet(text, terms)
		matches = append(matches, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	if len(matches) > limit {
		result.More = true
		matches = matches[:limit]
	}
	for _, m := range matches {
		sources, err := tx.QueryContext(ctx, `SELECT source FROM source_ranges WHERE project=? AND source IN(SELECT value FROM json_each(?)) AND lo<=? AND hi>=?`, state.ProjectID, string(encoded), m.id, m.id)
		if err != nil {
			return result, err
		}
		selected := ""
		for sources.Next() {
			var key string
			if err := sources.Scan(&key); err != nil {
				sources.Close()
				return result, err
			}
			a, b := state.Snapshots[key], state.Snapshots[selected]
			preferred := len(nodeSources[key]) > 0
			previous := len(nodeSources[selected]) > 0
			if selected == "" || preferred && !previous || preferred == previous && (a.Version > b.Version || a.Version == b.Version && key < selected) {
				selected = key
			}
		}
		err = sources.Err()
		sources.Close()
		if err != nil {
			return result, err
		}
		if selected == "" {
			return result, errors.New("indexed source mapping is unavailable")
		}
		m.value.Source = state.Snapshots[selected].Source
		m.value.NodeIDs = append([]string{}, nodeSources[selected]...)
		sort.Strings(m.value.NodeIDs)
		result.Matches = append(result.Matches, m.value)
	}
	return result, nil
}
func contentSnippet(text string, terms []string) string {
	start := 0
	folded := strings.ToLower(text)
	for _, term := range terms {
		if at := strings.Index(folded, term); at >= 0 {
			start = max(0, utf8.RuneCountInString(folded[:at])-60)
			break
		}
	}
	runes := []rune(text)
	start = min(start, len(runes))
	end := min(len(runes), start+240)
	snippet := string(runes[start:end])
	if start > 0 {
		snippet = "…" + snippet
	}
	if end < len(runes) {
		snippet += "…"
	}
	return snippet
}

// Index operations are bounded independently of Session transfer time.
func indexTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 30*time.Second)
}

// Reset only derived project rows. Raw/compact content and accepted graph state
// are untouched. A generation bump invalidates all pre-rebuild search cursors.
func (s *Store) ResetSearchIndex(ctx context.Context, project string) error {
	s.indexWriteMu.Lock()
	defer s.indexWriteMu.Unlock()
	db, err := s.searchDB()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{"source_ranges", "sources", "documents", "index_gc"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE project=?", project); err != nil {
			return err
		}
	}
	if err := bumpIndex(ctx, tx, project); err != nil {
		return err
	}
	return tx.Commit()
}
