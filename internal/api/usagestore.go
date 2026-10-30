package api

import (
	"cmp"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/triageagent-dev/ego/internal/adk"
	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

// Token usage ledger.
// One row per LLM span that reported tokens, kept apart from the session
// blobs so GET /api/usage aggregates with one indexed SQL query instead of
// unmarshalling every archived session. Rows are keyed by span ID and
// written with INSERT OR IGNORE, so re-ingesting a span (a reopened
// session, the startup backfill) never counts it twice.

// DefaultUsageRetention is how long usage rows are kept. Rows are a few
// hundred bytes, so this is far longer than a session is worth keeping.
const DefaultUsageRetention = 90 * 24 * time.Hour

type usageRow struct {
	SpanID    string
	TraceID   string
	SessionID string
	// Thread is the thread the session is a segment of (segments.go).
	Thread       string
	StartMs      int64
	DurationMs   int64
	Service      string
	Tenant       string
	Kind         string
	Model        string
	Name         string
	InputTokens  int64
	OutputTokens int64
	// CacheReadTokens and CacheWriteTokens are the parts of InputTokens the model read from its prompt cache and
	// wrote to it, which are priced differently from the rest of the input.
	CacheReadTokens  int64
	CacheWriteTokens int64
	IsError          bool
}

func ensureUsageSchema(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS llm_calls (
		span_id TEXT PRIMARY KEY,
		trace_id TEXT NOT NULL,
		session_id TEXT NOT NULL,
		start_ms INTEGER NOT NULL,
		duration_ms INTEGER NOT NULL,
		service TEXT NOT NULL,
		tenant TEXT NOT NULL,
		kind TEXT NOT NULL,
		model TEXT NOT NULL,
		name TEXT NOT NULL,
		input_tokens INTEGER NOT NULL,
		output_tokens INTEGER NOT NULL,
		is_error INTEGER NOT NULL,
		thread TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		return fmt.Errorf("creating llm_calls table: %w", err)
	}
	// A ledger written before segments has no thread column: add it, and read the session id as the thread, which is
	// what every such session was.
	if err := addColumn(db, "llm_calls", "thread", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	// NULL on a row written before the cache split; FillUsageCache reads those spans again at start.
	for _, c := range []string{"cache_read_tokens", "cache_write_tokens"} {
		if err := addColumn(db, "llm_calls", c, "INTEGER"); err != nil {
			return err
		}
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_llm_calls_thread ON llm_calls(thread)`); err != nil {
		return fmt.Errorf("creating llm_calls.thread index: %w", err)
	}
	if _, err := db.Exec(`UPDATE llm_calls SET thread = session_id WHERE thread = ''`); err != nil {
		return fmt.Errorf("filling llm_calls.thread: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_llm_calls_start ON llm_calls(start_ms)`); err != nil {
		return fmt.Errorf("creating llm_calls.start_ms index: %w", err)
	}
	return nil
}

// usageRowFromSpan returns the ledger row for an LLM span that reported
// tokens. A span that is not an LLM call, or reported no tokens, has no row:
// an AGENT/CHAIN parent repeating its child's counts would double them.
//
// The call kind is the OTel GenAI gen_ai.agent.name on the LLM span, so any
// producer that follows the semantic conventions is grouped without
// ego knowing about it. A span without it takes the name of its
// trace's root span (the operation that made the call, e.g. voice.session)
// once that root is seen: FillUsageKinds. The tenant has no convention, so its
// attribute key is deployment config (--usage-tenant-attr); empty leaves
// the tenant blank.
//
// A span with no tenant label takes fallbackLabel, the label of the trace it belongs to, so a call made inside a
// labelled operation is ledgered under that tenant, the same one the session reports.
func usageRowFromSpan(span *tracepkg.Span, sessionID, tenantAttr, sender, fallbackLabel string) (usageRow, bool) {
	if !adk.IsLLMSpan(span) {
		return usageRow{}, false
	}
	in, out, model := adk.ExtractTokenUsageFromAttrs(span.Tags)
	// A sender's token counts are summed into totals: a negative or absurd value is held to what one call can be.
	in, out = clampTokens(in), clampTokens(out)
	if in == 0 && out == 0 {
		return usageRow{}, false
	}
	return usageRow{
		SpanID:           span.SpanID,
		TraceID:          span.TraceID,
		SessionID:        sessionID,
		StartMs:          span.StartTime / 1000,
		DurationMs:       span.Duration / 1000,
		Service:          span.TagString(adk.OtelServiceName),
		Tenant:           tenantKey(sender, cmp.Or(tenantOf(span, tenantAttr), fallbackLabel)),
		Kind:             span.TagString(adk.GenAIAgentName),
		Model:            model,
		Name:             span.OperationName,
		InputTokens:      in,
		OutputTokens:     out,
		CacheReadTokens:  clampTokens(tagInt(span, "gen_ai.usage.cache_read.input_tokens")),
		CacheWriteTokens: cacheWriteTokens(span),
		IsError:          span.TagString("otel.status_code") == "ERROR",
	}, true
}

// maxCallTokens is the most tokens one model call is taken to have used, far above any context window.
const maxCallTokens = 1 << 30

func clampTokens(n int64) int64 { return max(0, min(n, maxCallTokens)) }

// cacheWriteTokens reads the tokens written to the prompt cache; the convention's name for them changed between versions.
func cacheWriteTokens(span *tracepkg.Span) int64 {
	if n := tagInt(span, "gen_ai.usage.cache_creation.input_tokens"); n > 0 {
		return clampTokens(n)
	}
	return clampTokens(tagInt(span, "gen_ai.usage.cache_write.input_tokens"))
}

func tenantOf(span *tracepkg.Span, attr string) string {
	if attr == "" {
		return ""
	}
	return cleanLabel(span.TagString(attr))
}

// FillUsageKinds names the kind of every kindless row in each trace after
// that trace's root span (traceID -> root span name).
func (s *SQLiteStore) FillUsageKinds(roots map[string]string) error {
	if len(roots) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`UPDATE llm_calls SET kind = ? WHERE trace_id = ? AND kind = ''`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()
	for traceID, name := range roots {
		if _, err := stmt.Exec(name, traceID); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// UsageKindlessSessions lists the sessions of the rows that still have no kind. A row whose trace never had a root
// span stays kindless for good, so the startup pass must read these sessions only, never the whole archive.
func (s *SQLiteStore) UsageKindlessSessions() ([]string, error) {
	rows, err := s.db.Query("SELECT DISTINCT session_id FROM llm_calls WHERE kind = ''")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// UsageCacheUnknown reports whether any row was written before the cache split.
func (s *SQLiteStore) UsageCacheUnknown() (bool, error) {
	var one int
	err := s.db.QueryRow("SELECT 1 FROM llm_calls WHERE cache_read_tokens IS NULL LIMIT 1").Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// cacheSplit is the cache part of one LLM call's input tokens.
type cacheSplit struct{ read, write int64 }

// FillUsageCache sets the cache split of the rows that predate it (span id -> split), in one transaction. A span with
// no cache attribute gets zero, so a row is filled once. Call FinishUsageCache after the last batch.
func (s *SQLiteStore) FillUsageCache(splits map[string]cacheSplit) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`UPDATE llm_calls SET cache_read_tokens = ?, cache_write_tokens = ? WHERE span_id = ? AND cache_read_tokens IS NULL`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()
	for id, c := range splits {
		if _, err := stmt.Exec(c.read, c.write, id); err != nil {
			tx.Rollback()
			return err
		}
	}
	return s.announce(topicUsage, tx.Commit())
}

// FinishUsageCache marks the rows no archived span matched as having no cache part, so they are not looked for again.
func (s *SQLiteStore) FinishUsageCache() error {
	_, err := s.db.Exec(`UPDATE llm_calls SET cache_read_tokens = 0, cache_write_tokens = 0 WHERE cache_read_tokens IS NULL`)
	return err
}

// InsertUsage writes rows in one transaction, ignoring spans already
// recorded.
func (s *SQLiteStore) InsertUsage(rows []usageRow) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO llm_calls
		(span_id, trace_id, session_id, start_ms, duration_ms, service, tenant, kind, model, name, input_tokens, output_tokens, is_error, thread,
			cache_read_tokens, cache_write_tokens)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, r := range rows {
		if _, err := stmt.Exec(r.SpanID, r.TraceID, r.SessionID, r.StartMs, r.DurationMs, r.Service, r.Tenant,
			r.Kind, r.Model, r.Name, r.InputTokens, r.OutputTokens, r.IsError, cmp.Or(r.Thread, r.SessionID), r.CacheReadTokens, r.CacheWriteTokens); err != nil {
			tx.Rollback()
			return err
		}
	}
	return s.announce(topicUsage, tx.Commit())
}

// PruneUsage deletes rows that started before cutoff.
func (s *SQLiteStore) PruneUsage(cutoff time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM llm_calls WHERE start_ms < ?`, cutoff.UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// usageBucketDTO is one (time bucket, service, tenant, kind, model) cell of
// GET /api/usage. The UI regroups cells by whichever dimension is selected,
// so the server never needs one query per grouping.
type usageBucketDTO struct {
	Start        int64  `json:"start"`
	Service      string `json:"service"`
	Tenant       string `json:"tenant"`
	Kind         string `json:"kind"`
	Model        string `json:"model"`
	Calls        int64  `json:"calls"`
	Errors       int64  `json:"errors"`
	InputTokens  int64  `json:"inputTokens"`
	OutputTokens int64  `json:"outputTokens"`
	// CacheReadTokens and CacheWriteTokens are the parts of InputTokens served from and written to the prompt cache.
	CacheReadTokens  int64 `json:"cacheReadTokens"`
	CacheWriteTokens int64 `json:"cacheWriteTokens"`
	DurationMs       int64 `json:"durationMs"`
}

// tenantClause limits a query on llm_calls to the given tenant grants (keys, "<sender>/*" or "*"); nil means no limit
// and an empty list matches nothing (a role scoped to tenants that have no data).
func tenantClause(tenants []string) (string, []any) {
	if tenants == nil {
		return "", nil
	}
	if len(tenants) == 0 {
		return " AND 0", nil
	}
	var parts []string
	var args []any
	for _, p := range tenants {
		switch {
		case p == "*":
			return "", nil
		case strings.HasSuffix(p, "/*"):
			// Every tenant of one sender: its keys start with "<sender>/".
			parts = append(parts, `tenant LIKE ? ESCAPE '\'`)
			args = append(args, likeEscape(strings.TrimSuffix(p, "*"))+"%")
		default:
			parts = append(parts, "tenant = ?")
			args = append(args, p)
		}
	}
	return " AND (" + strings.Join(parts, " OR ") + ")", args
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(s)
}

// UsageSeries aggregates rows in [from, to) into bucketMs-wide cells.
func (s *SQLiteStore) UsageSeries(from, to time.Time, bucketMs int64, tenants []string) ([]usageBucketDTO, error) {
	tc, targs := tenantClause(tenants)
	rows, err := s.db.Query(`SELECT (start_ms / ?) * ? AS bucket, service, tenant, kind, model,
			COUNT(*), SUM(is_error), SUM(input_tokens), SUM(output_tokens), SUM(duration_ms),
			SUM(COALESCE(cache_read_tokens, 0)), SUM(COALESCE(cache_write_tokens, 0))
		FROM llm_calls WHERE start_ms >= ? AND start_ms < ?`+tc+`
		GROUP BY bucket, service, tenant, kind, model ORDER BY bucket`,
		append([]any{bucketMs, bucketMs, from.UnixMilli(), to.UnixMilli()}, targs...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []usageBucketDTO{}
	for rows.Next() {
		var b usageBucketDTO
		if err := rows.Scan(&b.Start, &b.Service, &b.Tenant, &b.Kind, &b.Model,
			&b.Calls, &b.Errors, &b.InputTokens, &b.OutputTokens, &b.DurationMs, &b.CacheReadTokens, &b.CacheWriteTokens); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// usageCallDTO is one LLM call in GET /api/usage/calls.
type usageCallDTO struct {
	SpanID       string `json:"spanId"`
	TraceID      string `json:"traceId"`
	SessionID    string `json:"sessionId"`
	Start        int64  `json:"start"`
	DurationMs   int64  `json:"durationMs"`
	Service      string `json:"service"`
	Tenant       string `json:"tenant"`
	Kind         string `json:"kind"`
	Model        string `json:"model"`
	Name         string `json:"name"`
	InputTokens  int64  `json:"inputTokens"`
	OutputTokens int64  `json:"outputTokens"`
	IsError      bool   `json:"isError"`
}

// usageSessionDTO is one row of GET /api/usage/calls?group=session: every
// call of one session on one model, summed. A long multi-turn session
// re-reads its context each turn, so its calls are many and alike; ranking
// sessions keeps one of them from filling the whole list. Rows are per
// model, not per session, so the UI can still price each row.
type usageSessionDTO struct {
	SessionID    string `json:"sessionId"`
	Start        int64  `json:"start"`
	End          int64  `json:"end"`
	Calls        int64  `json:"calls"`
	Errors       int64  `json:"errors"`
	Kinds        string `json:"kinds"`
	Tenants      string `json:"tenants"`
	Model        string `json:"model"`
	InputTokens  int64  `json:"inputTokens"`
	OutputTokens int64  `json:"outputTokens"`
	PeakTokens   int64  `json:"peakTokens"`
	DurationMs   int64  `json:"durationMs"`
	// CacheReadTokens and CacheWriteTokens are the parts of InputTokens served from and written to the prompt cache.
	CacheReadTokens  int64 `json:"cacheReadTokens"`
	CacheWriteTokens int64 `json:"cacheWriteTokens"`
}

// TopUsageSessions returns the sessions (per model) that used the most
// tokens in [from, to).
func (s *SQLiteStore) TopUsageSessions(from, to time.Time, limit int, tenants []string) ([]usageSessionDTO, error) {
	tc, targs := tenantClause(tenants)
	rows, err := s.db.Query(`SELECT session_id, MIN(start_ms), MAX(start_ms), COUNT(*), SUM(is_error),
			COALESCE(GROUP_CONCAT(DISTINCT NULLIF(kind, '')), ''), COALESCE(GROUP_CONCAT(DISTINCT NULLIF(tenant, '')), ''),
			model, SUM(input_tokens), SUM(output_tokens), MAX(input_tokens + output_tokens), SUM(duration_ms),
			SUM(COALESCE(cache_read_tokens, 0)), SUM(COALESCE(cache_write_tokens, 0))
		FROM llm_calls WHERE start_ms >= ? AND start_ms < ?`+tc+`
		GROUP BY session_id, model
		ORDER BY SUM(input_tokens + output_tokens) DESC LIMIT ?`,
		append(append([]any{from.UnixMilli(), to.UnixMilli()}, targs...), limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []usageSessionDTO{}
	for rows.Next() {
		var r usageSessionDTO
		if err := rows.Scan(&r.SessionID, &r.Start, &r.End, &r.Calls, &r.Errors, &r.Kinds, &r.Tenants,
			&r.Model, &r.InputTokens, &r.OutputTokens, &r.PeakTokens, &r.DurationMs, &r.CacheReadTokens, &r.CacheWriteTokens); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// usageFailedDTO is a session with failed LLM calls: how many, and when the last one was.
type usageFailedDTO struct {
	SessionID string `json:"sessionId"`
	LastAt    int64  `json:"lastAt"`
	Failed    int64  `json:"failed"`
	Calls     int64  `json:"calls"`
	Kinds     string `json:"kinds"`
	Tenants   string `json:"tenants"`
}

// FailedUsageSessions returns the sessions with failed LLM calls in [from, to), the one that failed last first.
func (s *SQLiteStore) FailedUsageSessions(from, to time.Time, limit int, tenants []string) ([]usageFailedDTO, error) {
	tc, targs := tenantClause(tenants)
	rows, err := s.db.Query(`SELECT session_id, MAX(CASE WHEN is_error THEN start_ms END), SUM(is_error), COUNT(*),
			COALESCE(GROUP_CONCAT(DISTINCT NULLIF(kind, '')), ''), COALESCE(GROUP_CONCAT(DISTINCT NULLIF(tenant, '')), '')
		FROM llm_calls WHERE start_ms >= ? AND start_ms < ?`+tc+`
		GROUP BY session_id HAVING SUM(is_error) > 0
		ORDER BY MAX(CASE WHEN is_error THEN start_ms END) DESC LIMIT ?`,
		append(append([]any{from.UnixMilli(), to.UnixMilli()}, targs...), limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []usageFailedDTO{}
	for rows.Next() {
		var r usageFailedDTO
		if err := rows.Scan(&r.SessionID, &r.LastAt, &r.Failed, &r.Calls, &r.Kinds, &r.Tenants); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// TopUsageCalls returns the calls in [from, to) with the most total tokens.
func (s *SQLiteStore) TopUsageCalls(from, to time.Time, limit int, tenants []string) ([]usageCallDTO, error) {
	tc, targs := tenantClause(tenants)
	rows, err := s.db.Query(`SELECT span_id, trace_id, session_id, start_ms, duration_ms, service, tenant, kind, model,
			name, input_tokens, output_tokens, is_error
		FROM llm_calls WHERE start_ms >= ? AND start_ms < ?`+tc+`
		ORDER BY input_tokens + output_tokens DESC LIMIT ?`,
		append(append([]any{from.UnixMilli(), to.UnixMilli()}, targs...), limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []usageCallDTO{}
	for rows.Next() {
		var c usageCallDTO
		if err := rows.Scan(&c.SpanID, &c.TraceID, &c.SessionID, &c.Start, &c.DurationMs, &c.Service, &c.Tenant,
			&c.Kind, &c.Model, &c.Name, &c.InputTokens, &c.OutputTokens, &c.IsError); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UsageEmpty reports whether the ledger has no rows yet - the one time the
// startup backfill from the archive is worth its full pass.
func (s *SQLiteStore) UsageEmpty() (bool, error) {
	var one int
	err := s.db.QueryRow("SELECT 1 FROM llm_calls LIMIT 1").Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	return false, err
}

// addColumn adds a column to a table that may already have it.
func addColumn(db *sql.DB, table, column, definition string) error {
	if _, err := db.Exec("ALTER TABLE " + table + " ADD COLUMN " + column + " " + definition); err != nil && !strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("adding %s.%s: %w", table, column, err)
	}
	return nil
}
