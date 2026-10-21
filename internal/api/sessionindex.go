package api

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/triageagent-dev/ego/internal/logx"
	"github.com/triageagent-dev/ego/internal/mcptools"
)

// The session list is paged from a small index of the archive instead of the sessions themselves: one row per
// session with what the list sorts and filters on (start time, completion, tenants, agents, a lower-cased text of
// the header fields). A page reads its rows from the index in start order, checks the caller's role on them, and
// only then loads the few sessions it shows, so the cost of a page does not grow with the archive.

// indexSep separates the values inside the tenants and agents columns. It cannot appear in a label by accident,
// and it makes "contains this exact value" a LIKE on the delimited column.
const indexSep = "\x1f"

// maxPageSize is the most sessions one page returns, whatever the caller asks for.
const maxPageSize = 100

// pageScanChunk and pageScanCap bound the index rows one request reads: a role or filter that matches few sessions
// stops at the cap with a cursor to continue from, rather than reading the whole index. Variables so a test can
// make the cap reachable.
var (
	pageScanChunk = 200
	pageScanCap   = 20000
	// countScanCap bounds the index rows read to count matches that SQL alone cannot decide.
	countScanCap = 50000
)

type sessionIndexRow struct {
	ID        string
	StartedMs int64
	Complete  bool
	LLMCalls  int
	Tenants   []string
	Agents    []string
	Search    string
	// Scored is true when online evaluation scored the session (read from online_scores, never stored here).
	Scored bool
}

// sessionKey is a position in the list: sessions are ordered by start time, newest first, and by id within one
// millisecond, so a cursor stays valid however many sessions arrive or leave while someone pages.
type sessionKey struct {
	StartedMs int64
	ID        string
}

// olderThan reports whether k comes later in the list than o.
func (k sessionKey) olderThan(o sessionKey) bool {
	return k.StartedMs < o.StartedMs || (k.StartedMs == o.StartedMs && k.ID < o.ID)
}

func (r sessionIndexRow) key() sessionKey { return sessionKey{r.StartedMs, r.ID} }

func encodeCursor(k sessionKey) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(k.StartedMs, 10) + ":" + k.ID))
}

func decodeCursor(c string) (*sessionKey, error) {
	if c == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return nil, fmt.Errorf("invalid cursor")
	}
	ms, id, ok := strings.Cut(string(raw), ":")
	n, perr := strconv.ParseInt(ms, 10, 64)
	if !ok || perr != nil {
		return nil, fmt.Errorf("invalid cursor")
	}
	return &sessionKey{n, id}, nil
}

func joinIndexList(v []string) string {
	if len(v) == 0 {
		return ""
	}
	return indexSep + strings.Join(v, indexSep) + indexSep
}

func splitIndexList(s string) []string {
	s = strings.Trim(s, indexSep)
	if s == "" {
		return nil
	}
	return strings.Split(s, indexSep)
}

// indexRowOf derives a session's index row from its spans, the same way the list summary does.
func indexRowOf(s *Session) sessionIndexRow {
	agents, models := spanIdentities(s.Spans)
	root, llm, _ := spanOutline(s.Spans)
	service, _ := spanServiceAndTokens(s.Spans)
	tenants := sessionTenants(s)
	text := []string{s.ID, s.PrimaryTraceID, service, root}
	text = append(text, agents...)
	text = append(text, models...)
	text = append(text, tenants...)
	return sessionIndexRow{
		ID:        s.ID,
		StartedMs: s.StartedAt.UnixMilli(),
		Complete:  s.IsComplete,
		LLMCalls:  llm,
		Tenants:   tenants,
		Agents:    agents,
		Search:    strings.ToLower(strings.Join(text, indexSep)),
	}
}

func ensureSessionIndexSchema(db *sql.DB) error {
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS session_index (
			session_id TEXT PRIMARY KEY,
			started_ms INTEGER NOT NULL,
			complete   INTEGER NOT NULL,
			llm_calls  INTEGER NOT NULL,
			tenants    TEXT NOT NULL,
			agents     TEXT NOT NULL,
			search     TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_session_index_started ON session_index(started_ms, session_id)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("creating session index: %w", err)
		}
	}
	return nil
}

func writeIndexRows(tx *sql.Tx, rows []sessionIndexRow) error {
	stmt, err := tx.Prepare(`INSERT INTO session_index (session_id, started_ms, complete, llm_calls, tenants, agents, search)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET started_ms = excluded.started_ms, complete = excluded.complete,
			llm_calls = excluded.llm_calls, tenants = excluded.tenants, agents = excluded.agents, search = excluded.search`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, r := range rows {
		done := 0
		if r.Complete {
			done = 1
		}
		if _, err := stmt.Exec(r.ID, r.StartedMs, done, r.LLMCalls, joinIndexList(r.Tenants), joinIndexList(r.Agents), r.Search); err != nil {
			return err
		}
	}
	return nil
}

// BackfillSessionIndex indexes archived sessions that have no index row yet (an archive written before the index
// existed), one batch at a time in id order, and returns how many it added. It only reads sessions the index lacks,
// so a start with a complete index costs one empty query.
func (s *SQLiteStore) BackfillSessionIndex(tenantAttr string) (int, error) {
	const batch = 200
	added, last := 0, ""
	for {
		rows, err := s.db.Query(`SELECT s.session_id, s.data FROM sessions s
			LEFT JOIN session_index i ON i.session_id = s.session_id
			WHERE i.session_id IS NULL AND s.session_id > ? ORDER BY s.session_id LIMIT ?`, last, batch)
		if err != nil {
			return added, err
		}
		var idx []sessionIndexRow
		n := 0
		for rows.Next() {
			var id, data string
			if err := rows.Scan(&id, &data); err != nil {
				rows.Close()
				return added, err
			}
			n++
			last = id
			var snap sessionSnapshot
			if err := json.Unmarshal([]byte(data), &snap); err != nil {
				logx.Warnf("session archive: indexing: deserializing session %s: %v", id, err)
				continue
			}
			// Not restoreSession: a completed session's invocations are not needed for its index row.
			idx = append(idx, indexRowOf(&Session{
				ID: snap.SessionID, Sender: snap.Sender, tenantAttr: tenantAttr, PrimaryTraceID: snap.TraceID,
				Spans: snap.Spans, StartedAt: snap.StartedAt, IsComplete: snap.IsComplete,
			}))
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return added, err
		}
		if len(idx) > 0 {
			tx, err := s.db.Begin()
			if err != nil {
				return added, err
			}
			if err := writeIndexRows(tx, idx); err != nil {
				tx.Rollback()
				return added, err
			}
			if err := tx.Commit(); err != nil {
				return added, err
			}
			added += len(idx)
		}
		if n < batch {
			return added, nil
		}
	}
}

// sessionPageFilter is what a page narrows the index by in SQL. The caller's role is checked afterwards, in Go.
type sessionPageFilter struct {
	since    time.Time
	tenant   string
	agent    string
	query    string // lower case
	complete *bool
	llmOnly  bool
	scored   *bool
}

// TenantCounts counts the indexed sessions of every tenant key, over the whole archive. The tenant switcher is
// built from it, so a tenant whose sessions left memory, or that is quiet since a restart, is still offered.
func (s *SQLiteStore) TenantCounts() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT tenants FROM session_index WHERE tenants != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var tenants string
		if err := rows.Scan(&tenants); err != nil {
			return nil, err
		}
		for _, k := range strings.Split(strings.Trim(tenants, indexSep), indexSep) {
			if k != "" {
				counts[k]++
			}
		}
	}
	return counts, rows.Err()
}

// tenantLike narrows the tenants column for a tenant pick; ok is false for patterns SQL cannot express, which the
// Go check still applies.
func tenantLike(want string) (string, bool) {
	switch {
	case want == "" || want == "*":
		return "", false
	case strings.HasSuffix(want, "/*"):
		return "%" + indexSep + likeEscape(strings.TrimSuffix(want, "*")) + "%", true
	case strings.HasSuffix(want, "/"):
		return "%" + indexSep + likeEscape(want) + "%", true
	case strings.Contains(want, "*"):
		return "", false
	}
	return "%" + indexSep + likeEscape(want) + indexSep + "%", true
}

// scoredExpr is decided by the scores table, not stored in the index: a session is scored after it was indexed.
const scoredExpr = `EXISTS (SELECT 1 FROM online_scores o WHERE o.session_id = session_index.session_id AND o.score IS NOT NULL)`

// where is the SQL condition (starting with " AND") and arguments that narrow the index by f, and by the position
// after when set.
func (f sessionPageFilter) where(after *sessionKey) (string, []any) {
	var q string
	var args []any
	if !f.since.IsZero() {
		q += " AND started_ms >= ?"
		args = append(args, f.since.UnixMilli())
	}
	if after != nil {
		q += " AND (started_ms < ? OR (started_ms = ? AND session_id < ?))"
		args = append(args, after.StartedMs, after.StartedMs, after.ID)
	}
	if like, ok := tenantLike(f.tenant); ok {
		q += ` AND tenants LIKE ? ESCAPE '\'`
		args = append(args, like)
	}
	if f.agent != "" {
		q += ` AND agents LIKE ? ESCAPE '\'`
		args = append(args, "%"+indexSep+likeEscape(f.agent)+indexSep+"%")
	}
	if f.query != "" {
		q += ` AND search LIKE ? ESCAPE '\'`
		args = append(args, "%"+likeEscape(f.query)+"%")
	}
	if f.complete != nil {
		done := 0
		if *f.complete {
			done = 1
		}
		q += " AND complete = ?"
		args = append(args, done)
	}
	if f.llmOnly {
		// A session still running may not have shown its LLM spans yet, so only finished ones are held to it.
		q += " AND (llm_calls > 0 OR complete = 0)"
	}
	if f.scored != nil {
		if *f.scored {
			q += " AND " + scoredExpr
		} else {
			q += " AND NOT " + scoredExpr
		}
	}
	return q, args
}

// sqlExact reports whether the SQL condition alone decides the tenant filter (a pattern SQL cannot express is left
// to the Go check).
func (f sessionPageFilter) sqlExact() bool {
	_, ok := tenantLike(f.tenant)
	return ok || f.tenant == "" || f.tenant == "*"
}

// countIndex counts the index rows that match f, leaving out the ids in skip (the sessions memory holds, which are
// counted from memory).
func (s *SQLiteStore) countIndex(f sessionPageFilter, skip []string) (int, error) {
	cond, args := f.where(nil)
	q := "SELECT COUNT(*) FROM session_index WHERE 1 = 1" + cond
	if len(skip) > 0 {
		q += " AND session_id NOT IN (?" + strings.Repeat(",?", len(skip)-1) + ")"
		for _, id := range skip {
			args = append(args, id)
		}
	}
	var n int
	err := s.db.QueryRow(q, args...).Scan(&n)
	return n, err
}

// indexPage returns up to limit index rows, newest first, older than after, matching f.
func (s *SQLiteStore) indexPage(f sessionPageFilter, after *sessionKey, limit int) ([]sessionIndexRow, error) {
	cond, args := f.where(after)
	q := `SELECT session_id, started_ms, complete, llm_calls, tenants, agents, search, ` + scoredExpr + ` FROM session_index WHERE 1 = 1` + cond
	q += " ORDER BY started_ms DESC, session_id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sessionIndexRow
	for rows.Next() {
		var r sessionIndexRow
		var done, scored int
		var tenants, agents string
		if err := rows.Scan(&r.ID, &r.StartedMs, &done, &r.LLMCalls, &tenants, &agents, &r.Search, &scored); err != nil {
			return nil, err
		}
		r.Complete, r.Scored = done == 1, scored == 1
		r.Tenants, r.Agents = splitIndexList(tenants), splitIndexList(agents)
		out = append(out, r)
	}
	return out, rows.Err()
}

// SessionPage is one page of the session list. Next is empty on the last page.
type SessionPage struct {
	Items []SessionSummary
	Next  string
	// Total is how many sessions match, counted on the first page only (TotalKnown false on later ones).
	// TotalApprox means Total is a lower bound: counting under a role that SQL cannot express stops at a cap.
	Total       int
	TotalKnown  bool
	TotalApprox bool
}

// SessionPageRequest is the filter, position and size of a page.
type SessionPageRequest struct {
	Filter  mcptools.SessionFilter
	LLMOnly bool
	Cursor  string
	Limit   int
	// Unrestricted says the caller's role allows every session, so the total can be counted in SQL.
	Unrestricted bool
}

// Page lists the sessions of one page, newest first. Sessions in memory are read from memory (they are the
// current ones, and may not be archived yet), the rest from the index; allow is the caller's role, checked on every
// row before it can count towards the page. The text query matches the header fields (session and trace id, tenant,
// agent, model, service, root span name), not the conversation.
func (s *SessionStore) Page(req SessionPageRequest, allow func(SessionSummary) bool) (SessionPage, error) {
	limit := req.Limit
	if limit <= 0 || limit > maxPageSize {
		limit = maxPageSize
	}
	after, err := decodeCursor(req.Cursor)
	if err != nil {
		return SessionPage{}, err
	}
	f := req.Filter
	want := sessionPageFilter{since: f.Since, tenant: f.Tenant, agent: f.Agent, query: strings.ToLower(f.Query), complete: f.Complete, llmOnly: req.LLMOnly, scored: f.Scored}

	keep := func(r sessionIndexRow) bool {
		if !allow(SessionSummary{Agents: r.Agents, Tenants: r.Tenants}) {
			return false
		}
		if want.complete != nil && r.Complete != *want.complete {
			return false
		}
		if want.agent != "" && !containsString(r.Agents, want.agent) {
			return false
		}
		if want.tenant != "" && !sessionInTenant(r.Tenants, want.tenant) {
			return false
		}
		if !want.since.IsZero() && r.StartedMs < want.since.UnixMilli() {
			return false
		}
		if want.llmOnly && r.Complete && r.LLMCalls == 0 {
			return false
		}
		if want.scored != nil && r.Scored != *want.scored {
			return false
		}
		return want.query == "" || strings.Contains(r.Search, want.query)
	}

	// Memory is authoritative for the sessions it holds. Which of them are scored is one lookup in the scores
	// table, taken before the walk so the lock is not held across a query.
	s.mu.Lock()
	memIDs := append([]string(nil), s.order...)
	s.mu.Unlock()
	var scoredMem map[string]bool
	if s.store != nil {
		scoredMem = s.store.OnlineScoredAmong(memIDs)
	}
	inMemory := map[string]bool{}
	var items []sessionIndexRow
	memMatches := 0
	s.mu.Lock()
	for _, id := range s.order {
		inMemory[id] = true
		sess, ok := s.sessions[id]
		if !ok {
			continue
		}
		r := indexRowOf(sess)
		r.Scored = scoredMem[id]
		if !keep(r) {
			continue
		}
		memMatches++
		if after == nil || r.key().olderThan(*after) {
			items = append(items, r)
		}
	}
	s.mu.Unlock()

	var boundary *sessionKey // set when the scan stopped at its cap before filling the page
	if s.store != nil {
		scanned, found := 0, 0
		cursor := after
		for found <= limit {
			rows, err := s.store.indexPage(want, cursor, pageScanChunk)
			if err != nil {
				return SessionPage{}, err
			}
			for _, r := range rows {
				k := r.key()
				cursor = &k
				scanned++
				if inMemory[r.ID] || !keep(r) {
					continue
				}
				items = append(items, r)
				found++
				if found > limit {
					break
				}
			}
			if len(rows) < pageScanChunk {
				break
			}
			if found <= limit && scanned >= pageScanCap {
				boundary = cursor
				break
			}
		}
	}

	sort.SliceStable(items, func(i, j int) bool { return items[j].key().olderThan(items[i].key()) })
	if boundary != nil {
		// Memory rows older than the last archived row read would come again from the next page.
		kept := items[:0]
		for _, r := range items {
			if !r.key().olderThan(*boundary) {
				kept = append(kept, r)
			}
		}
		items = kept
	}
	page := SessionPage{}
	if after == nil {
		page.Total, page.TotalKnown, page.TotalApprox = s.countMatches(req, want, keep, inMemory, memMatches)
	}
	switch {
	case len(items) > limit:
		items = items[:limit]
		page.Next = encodeCursor(items[limit-1].key())
	case boundary != nil:
		page.Next = encodeCursor(*boundary)
	}

	page.Items = make([]SessionSummary, 0, len(items))
	for _, r := range items {
		s.mu.Lock()
		sess, live := s.sessions[r.ID]
		var sum SessionSummary
		if live {
			sum = summarize(sess)
		}
		s.mu.Unlock()
		if !live {
			if s.store == nil {
				continue
			}
			snap, ok, err := s.store.Load(r.ID)
			if err != nil || !ok {
				continue
			}
			sum = summarize(restoreSession(snap, s.usageTenantAttr))
		}
		sum.Scored = r.Scored
		page.Items = append(page.Items, sum)
	}
	return page, nil
}

// countMatches is the number of sessions the request matches: the memory ones plus the archived ones. In SQL when
// the caller sees everything and the filters are expressible there; otherwise by reading index rows up to
// countScanCap, after which the number is a lower bound.
func (s *SessionStore) countMatches(req SessionPageRequest, want sessionPageFilter, keep func(sessionIndexRow) bool, inMemory map[string]bool, memMatches int) (total int, known, approx bool) {
	if s.store == nil {
		return memMatches, true, false
	}
	if req.Unrestricted && want.sqlExact() {
		skip := make([]string, 0, len(inMemory))
		for id := range inMemory {
			skip = append(skip, id)
		}
		n, err := s.store.countIndex(want, skip)
		if err != nil {
			logx.Warnf("session list: counting: %v", err)
			return 0, false, false
		}
		return n + memMatches, true, false
	}
	n, scanned := 0, 0
	var cursor *sessionKey
	for {
		rows, err := s.store.indexPage(want, cursor, pageScanChunk)
		if err != nil {
			logx.Warnf("session list: counting: %v", err)
			return 0, false, false
		}
		for _, r := range rows {
			k := r.key()
			cursor = &k
			scanned++
			if !inMemory[r.ID] && keep(r) {
				n++
			}
		}
		if len(rows) < pageScanChunk {
			return n + memMatches, true, false
		}
		if scanned >= countScanCap {
			return n + memMatches, true, true
		}
	}
}
