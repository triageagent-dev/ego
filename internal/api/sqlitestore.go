package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/triageagent-dev/ego/internal/logx"
	tracepkg "github.com/triageagent-dev/ego/internal/trace"
	_ "modernc.org/sqlite" // pure-Go driver, no CGO - registers "sqlite" with database/sql
)

// SQLiteStore is a durable archive of Sessions, backed by a single SQLite file: one row per session, the whole
// session serialized as a JSON blob, so a field added to Session never needs a migration.
type SQLiteStore struct {
	db *sql.DB
	// onChange, when set, is told which kind of data a write changed (the topic* constants), so the UI can be nudged
	// to read it again instead of polling.
	onChange func(topic string)
}

// SetOnChange registers the callback for writes; call it before the store is used.
func (s *SQLiteStore) SetOnChange(fn func(topic string)) { s.onChange = fn }

// announce reports a finished write of topic and passes err through, so a write method can end with
// `return s.announce(topic, err)`. A failed write changed nothing and is not announced.
func (s *SQLiteStore) announce(topic string, err error) error {
	if err == nil && s.onChange != nil {
		s.onChange(topic)
	}
	return err
}

// NewSQLiteStore opens (creating if needed) the archive at path, in WAL
// mode so the periodic snapshot writer and a startup load never block each
// other.
func NewSQLiteStore(path string) (*SQLiteStore, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("creating session archive directory: %w", err)
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening session archive: %w", err)
	}
	// The archive holds full prompts and responses: keep it, and the WAL files next to it, to the owner.
	defer func() {
		for _, suffix := range []string{"", "-wal", "-shm"} {
			_ = os.Chmod(path+suffix, 0o600)
		}
	}()
	for _, pragma := range []string{"PRAGMA journal_mode=WAL", "PRAGMA synchronous=NORMAL"} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("setting %q: %w", pragma, err)
		}
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS sessions (
		session_id TEXT PRIMARY KEY,
		data TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating sessions table: %w", err)
	}
	// updated_at drives the newest-first restore (LoadRecent) and the
	// retention prune (PruneSessions).
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_sessions_updated ON sessions(updated_at)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating sessions.updated_at index: %w", err)
	}
	if err := ensureSessionIndexSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := ensureRunsSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := ensureEvalSetsSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := ensureUsageSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := ensureRolesSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := ensureSendersSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := ensureRevocationsSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := ensureOnlineSchema(db); err != nil {
		db.Close()
		return nil, err
	}

	logx.Infof("session archive: %s", path)
	return &SQLiteStore{db: db}, nil
}

type sessionSnapshot struct {
	SessionID   string           `json:"session_id"`
	TraceID     string           `json:"trace_id"`
	EvalSetID   string           `json:"eval_set_id,omitempty"`
	Metadata    map[string]any   `json:"metadata,omitempty"`
	TraceIDs    []string         `json:"trace_ids,omitempty"`
	Spans       []*tracepkg.Span `json:"spans,omitempty"`
	StartedAt   time.Time        `json:"started_at"`
	Sender      string           `json:"sender,omitempty"`
	HasRootSpan bool             `json:"has_root_span,omitempty"`
	IsComplete  bool             `json:"is_complete,omitempty"`
	CompletedAt *time.Time       `json:"completed_at,omitempty"`
}

func snapshotOf(s *Session) sessionSnapshot {
	traceIDs := make([]string, 0, len(s.TraceIDs))
	for id := range s.TraceIDs {
		traceIDs = append(traceIDs, id)
	}
	// Children are rebuilt from the flat span list on every otlp.BuildTrace
	// call regardless; stripping them here keeps the persisted blob to one
	// copy of each span instead of two (flat entry + reachable via a
	// sibling's Children).
	spans := make([]*tracepkg.Span, len(s.Spans))
	for i, sp := range s.Spans {
		cp := *sp
		cp.Children = nil
		spans[i] = &cp
	}
	return sessionSnapshot{
		SessionID:   s.ID,
		Sender:      s.Sender,
		TraceID:     s.PrimaryTraceID,
		EvalSetID:   s.EvalSetID,
		Metadata:    s.Metadata,
		TraceIDs:    traceIDs,
		Spans:       spans,
		StartedAt:   s.StartedAt,
		HasRootSpan: s.HasRootSpan,
		IsComplete:  s.IsComplete,
		CompletedAt: s.CompletedAt,
	}
}

// SaveAll upserts every session's current snapshot. A session that fails
// to serialize is logged and skipped rather than aborting the whole batch.
func (s *SQLiteStore) SaveAll(sessions map[string]*Session) error {
	rows := make([]struct {
		id, data, updatedAt string
	}, 0, len(sessions))
	index := make([]sessionIndexRow, 0, len(sessions))

	now := time.Now().UTC().Format(time.RFC3339)
	for id, session := range sessions {
		data, err := json.Marshal(snapshotOf(session))
		if err != nil {
			logx.Errorf("session archive: failed to serialize session %s: %v", id, err)
			continue
		}
		rows = append(rows, struct{ id, data, updatedAt string }{id, string(data), now})
		index = append(index, indexRowOf(session))
	}
	if len(rows) == 0 {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT INTO sessions (session_id, data, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, r := range rows {
		if _, err := stmt.Exec(r.id, r.data, r.updatedAt); err != nil {
			return err
		}
	}
	if err := writeIndexRows(tx, index); err != nil {
		return err
	}
	return tx.Commit()
}

// Delete removes one session's archived row.
func (s *SQLiteStore) Delete(sessionID string) error {
	if _, err := s.db.Exec("DELETE FROM session_index WHERE session_id = ?", sessionID); err != nil {
		return err
	}
	_, err := s.db.Exec("DELETE FROM sessions WHERE session_id = ?", sessionID)
	return err
}

// LoadRecent returns the limit most recently updated sessions' snapshots (every session when limit <= 0),
// oldest first. A row that fails to deserialize is logged and skipped rather than aborting the load. maxBytes
// (0 = none) stops the read once that much archived JSON is loaded; the newest session is always kept.
func (s *SQLiteStore) LoadRecent(limit int, maxBytes int64) ([]sessionSnapshot, error) {
	q := "SELECT session_id, data FROM sessions ORDER BY updated_at DESC"
	args := []any{}
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []sessionSnapshot
	var used int64
	for rows.Next() {
		var id, data string
		if err := rows.Scan(&id, &data); err != nil {
			return nil, err
		}
		// A byte budget on top of the count: a session with many spans can
		// be megabytes, and restoring the whole count at once is what
		// OOM-killed a small pod at start. The newest session always comes
		// back; older ones past the budget stay in the archive.
		used += int64(len(data))
		if maxBytes > 0 && used > maxBytes && len(result) > 0 {
			break
		}
		var snap sessionSnapshot
		if err := json.Unmarshal([]byte(data), &snap); err != nil {
			logx.Warnf("session archive: failed to deserialize session %s: %v", id, err)
			continue
		}
		result = append(result, snap)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return result, nil
}

// Load returns one archived session's snapshot; ok is false when it is not
// archived.
func (s *SQLiteStore) Load(sessionID string) (snap sessionSnapshot, ok bool, err error) {
	var data string
	err = s.db.QueryRow("SELECT data FROM sessions WHERE session_id = ?", sessionID).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return snap, false, nil
	}
	if err != nil {
		return snap, false, err
	}
	if err := json.Unmarshal([]byte(data), &snap); err != nil {
		return snap, false, fmt.Errorf("deserializing session %s: %w", sessionID, err)
	}
	return snap, true, nil
}

// SearchSnapshots calls fn, newest first, for the archived sessions whose id or stored text mentions needle
// (ASCII case-insensitive), at most maxRows of them, until fn returns false; since, when set, skips rows last
// written before it. The archive is the only place a session evicted from memory can be found by text.
func (s *SQLiteStore) SearchSnapshots(needle string, since time.Time, maxRows int, fn func(sessionSnapshot) bool) error {
	like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(needle) + "%"
	q := `SELECT session_id, data FROM sessions WHERE (session_id LIKE ? ESCAPE '\' OR data LIKE ? ESCAPE '\')`
	args := []any{like, like}
	if !since.IsZero() {
		q += " AND updated_at >= ?"
		args = append(args, since.UTC().Format(time.RFC3339))
	}
	q += " ORDER BY updated_at DESC LIMIT ?"
	args = append(args, maxRows)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, data string
		if err := rows.Scan(&id, &data); err != nil {
			return err
		}
		var snap sessionSnapshot
		if err := json.Unmarshal([]byte(data), &snap); err != nil {
			logx.Warnf("session archive: search: deserializing session %s: %v", id, err)
			continue
		}
		if !fn(snap) {
			break
		}
	}
	return rows.Err()
}

// ForEachSnapshot calls fn for every archived session, one row at a time,
// so a full pass (the usage backfill) never holds the archive in memory.
func (s *SQLiteStore) ForEachSnapshot(fn func(sessionSnapshot)) error {
	rows, err := s.db.Query("SELECT session_id, data FROM sessions")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, data string
		if err := rows.Scan(&id, &data); err != nil {
			return err
		}
		var snap sessionSnapshot
		if err := json.Unmarshal([]byte(data), &snap); err != nil {
			logx.Warnf("session archive: failed to deserialize session %s: %v", id, err)
			continue
		}
		fn(snap)
	}
	return rows.Err()
}

// PruneSessions deletes sessions last updated before cutoff.
func (s *SQLiteStore) PruneSessions(cutoff time.Time) (int64, error) {
	at := cutoff.UTC().Format(time.RFC3339)
	if _, err := s.db.Exec("DELETE FROM session_index WHERE session_id IN (SELECT session_id FROM sessions WHERE updated_at < ?)", at); err != nil {
		return 0, err
	}
	res, err := s.db.Exec("DELETE FROM sessions WHERE updated_at < ?", at)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Compact returns freed pages to the filesystem after a prune: checkpoint
// the WAL into the main file, then VACUUM. VACUUM rewrites the whole file,
// so it runs only after rows were actually deleted.
func (s *SQLiteStore) Compact() error {
	if _, err := s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return fmt.Errorf("checkpointing WAL: %w", err)
	}
	if _, err := s.db.Exec("VACUUM"); err != nil {
		return fmt.Errorf("vacuum: %w", err)
	}
	return nil
}

// Close closes the underlying database handle.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}
