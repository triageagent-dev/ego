package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/triageagent-dev/ego/internal/online"
)

// See docs/online-evaluation.md.

func ensureOnlineSchema(db *sql.DB) error {
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS online_evals (
			session_id TEXT PRIMARY KEY,
			trace_id TEXT NOT NULL,
			at_ms INTEGER NOT NULL,
			tenant TEXT NOT NULL,
			kind TEXT NOT NULL,
			service TEXT NOT NULL,
			profile TEXT NOT NULL,
			lane TEXT NOT NULL,
			reasons TEXT NOT NULL,
			judge_tokens INTEGER NOT NULL,
			jev_calls INTEGER NOT NULL,
			run_id TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_online_evals_at ON online_evals(at_ms)`,
		`CREATE TABLE IF NOT EXISTS online_scores (
			session_id TEXT NOT NULL,
			at_ms INTEGER NOT NULL,
			tenant TEXT NOT NULL,
			kind TEXT NOT NULL,
			version TEXT NOT NULL,
			model TEXT NOT NULL,
			metric TEXT NOT NULL,
			score REAL,
			status TEXT NOT NULL,
			PRIMARY KEY (session_id, metric)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_online_scores_at ON online_scores(at_ms)`,
		`CREATE TABLE IF NOT EXISTS online_alerts (
			alert_id TEXT PRIMARY KEY,
			status TEXT NOT NULL,
			updated_ms INTEGER NOT NULL,
			data TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_online_alerts_status ON online_alerts(status, updated_ms)`,
		`CREATE TABLE IF NOT EXISTS online_findings (
			finding_id TEXT PRIMARY KEY,
			tenant TEXT NOT NULL,
			kind TEXT NOT NULL,
			metric TEXT NOT NULL,
			state TEXT NOT NULL,
			alert_id TEXT NOT NULL,
			created_ms INTEGER NOT NULL,
			updated_ms INTEGER NOT NULL,
			data TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_online_findings_state ON online_findings(state, updated_ms)`,
		`CREATE INDEX IF NOT EXISTS idx_online_findings_alert ON online_findings(alert_id)`,
		`CREATE TABLE IF NOT EXISTS online_corrections (
			correction_id TEXT PRIMARY KEY,
			tenant TEXT NOT NULL,
			kind TEXT NOT NULL,
			metric TEXT NOT NULL,
			finding_id TEXT NOT NULL,
			version TEXT NOT NULL,
			state TEXT NOT NULL,
			created_ms INTEGER NOT NULL,
			verified_ms INTEGER NOT NULL,
			data TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_online_corrections_state ON online_corrections(state, created_ms)`,
		`CREATE INDEX IF NOT EXISTS idx_online_corrections_finding ON online_corrections(finding_id)`,
		`CREATE TABLE IF NOT EXISTS online_labels (
			label_id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			tenant TEXT NOT NULL,
			kind TEXT NOT NULL,
			metric TEXT NOT NULL,
			judge_score REAL NOT NULL,
			verdict TEXT NOT NULL,
			score REAL,
			note TEXT NOT NULL,
			"by" TEXT NOT NULL,
			created_ms INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_online_labels_metric ON online_labels(metric, created_ms)`,
		`CREATE INDEX IF NOT EXISTS idx_online_labels_session ON online_labels(session_id)`,
		`CREATE TABLE IF NOT EXISTS online_state (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("creating online evaluation schema: %w", err)
		}
	}
	return nil
}

type onlineEvalRow struct {
	SessionID   string
	TraceID     string
	AtMs        int64
	Tenant      string
	Kind        string
	Service     string
	Profile     string
	Lane        string
	Reasons     string
	JudgeTokens int64
	JevCalls    int64
	RunID       string
}

type onlineScoreRow struct {
	Metric string
	Score  *float64
	Status string
}

// InsertOnlineEval writes one scored session and its scores.
func (s *SQLiteStore) InsertOnlineEval(e onlineEvalRow, version, model string, scores []onlineScoreRow) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT OR REPLACE INTO online_evals
		(session_id, trace_id, at_ms, tenant, kind, service, profile, lane, reasons, judge_tokens, jev_calls, run_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.SessionID, e.TraceID, e.AtMs, e.Tenant, e.Kind, e.Service, e.Profile, e.Lane, e.Reasons,
		e.JudgeTokens, e.JevCalls, e.RunID); err != nil {
		return err
	}
	for _, sc := range scores {
		var score sql.NullFloat64
		if sc.Score != nil {
			score = sql.NullFloat64{Float64: *sc.Score, Valid: true}
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO online_scores
			(session_id, at_ms, tenant, kind, version, model, metric, score, status)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			e.SessionID, e.AtMs, e.Tenant, e.Kind, version, model, sc.Metric, score, sc.Status); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// OnlineEvalExists reports whether a session was already scored (a session
// that reopens and completes again is scored once).
func (s *SQLiteStore) OnlineEvalExists(sessionID string) (bool, error) {
	var one int
	err := s.db.QueryRow(`SELECT 1 FROM online_evals WHERE session_id = ?`, sessionID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// OnlineSpentSince sums judge cost since sinceMs (today's spend at startup).
func (s *SQLiteStore) OnlineSpentSince(sinceMs int64) (online.Cost, error) {
	var c online.Cost
	err := s.db.QueryRow(`SELECT COALESCE(SUM(judge_tokens), 0), COALESCE(SUM(jev_calls), 0)
		FROM online_evals WHERE at_ms >= ?`, sinceMs).Scan(&c.JudgeTokens, &c.JevCalls)
	return c, err
}

type onlineSampleRow struct {
	Key    online.SeriesKey
	Sample online.Sample
}

// OnlineSamples returns every decided score (passed or failed, not errored)
// since sinceMs, oldest first.
func (s *SQLiteStore) OnlineSamples(sinceMs int64) ([]onlineSampleRow, error) {
	rows, err := s.db.Query(`SELECT session_id, at_ms, tenant, kind, version, model, metric, score
		FROM online_scores WHERE at_ms >= ? AND score IS NOT NULL AND status != 'errored'
		ORDER BY at_ms`, sinceMs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []onlineSampleRow
	for rows.Next() {
		var r onlineSampleRow
		var at int64
		if err := rows.Scan(&r.Sample.SessionID, &at, &r.Key.Tenant, &r.Key.Kind, &r.Sample.Version,
			&r.Sample.Model, &r.Key.Metric, &r.Sample.Score); err != nil {
			return nil, err
		}
		r.Sample.At = time.UnixMilli(at)
		out = append(out, r)
	}
	return out, rows.Err()
}

// OnlineSamplesFor returns the decided scores of one (tenant, kind) in [sinceMs, untilMs), oldest first. An empty
// metric pools every metric of the kind; an empty version takes every version. Verification reads its "after"
// sessions this way.
func (s *SQLiteStore) OnlineSamplesFor(tenant, kind, metric, version string, sinceMs, untilMs int64) ([]online.Sample, error) {
	q := `SELECT session_id, at_ms, version, model, score FROM online_scores
		WHERE tenant = ? AND kind = ? AND at_ms >= ? AND at_ms < ? AND score IS NOT NULL AND status != 'errored'`
	args := []any{tenant, kind, sinceMs, untilMs}
	if metric != "" {
		q += ` AND metric = ?`
		args = append(args, metric)
	}
	if version != "" {
		q += ` AND version = ?`
		args = append(args, version)
	}
	rows, err := s.db.Query(q+` ORDER BY at_ms`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []online.Sample
	for rows.Next() {
		var sm online.Sample
		var at int64
		if err := rows.Scan(&sm.SessionID, &at, &sm.Version, &sm.Model, &sm.Score); err != nil {
			return nil, err
		}
		sm.At = time.UnixMilli(at)
		out = append(out, sm)
	}
	return out, rows.Err()
}

type onlineSeriesPoint struct {
	BucketMs int64   `json:"bucketMs"`
	Tenant   string  `json:"tenant"`
	Kind     string  `json:"kind"`
	Metric   string  `json:"metric"`
	Mean     float64 `json:"mean"`
	N        int     `json:"n"`
}

// OnlineSeries returns hourly mean scores per (tenant, kind, metric).
func (s *SQLiteStore) OnlineSeries(sinceMs int64) ([]onlineSeriesPoint, error) {
	rows, err := s.db.Query(`SELECT (at_ms / 3600000) * 3600000 AS bucket, tenant, kind, metric, AVG(score), COUNT(*)
		FROM online_scores WHERE at_ms >= ? AND score IS NOT NULL AND status != 'errored'
		GROUP BY bucket, tenant, kind, metric ORDER BY bucket`, sinceMs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []onlineSeriesPoint{}
	for rows.Next() {
		var p onlineSeriesPoint
		if err := rows.Scan(&p.BucketMs, &p.Tenant, &p.Kind, &p.Metric, &p.Mean, &p.N); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpsertOnlineAlert writes an alert's state.
func (s *SQLiteStore) UpsertOnlineAlert(a *online.Alert) error {
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT OR REPLACE INTO online_alerts (alert_id, status, updated_ms, data) VALUES (?, ?, ?, ?)`,
		a.ID, a.Status, a.UpdatedAt.UnixMilli(), string(b))
	return err
}

// OnlineAlerts lists alerts newest first; status "" lists all.
func (s *SQLiteStore) OnlineAlerts(status string, limit int) ([]*online.Alert, error) {
	q := `SELECT data FROM online_alerts`
	var args []any
	if status != "" {
		q += ` WHERE status = ?`
		args = append(args, status)
	}
	q += ` ORDER BY updated_ms DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*online.Alert{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var a online.Alert
		if err := json.Unmarshal([]byte(data), &a); err != nil {
			return nil, err
		}
		out = append(out, &a)
	}
	return out, rows.Err()
}

// OnlineState reads a small key ("" when unset).
func (s *SQLiteStore) OnlineState(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM online_state WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetOnlineState writes a small key.
func (s *SQLiteStore) SetOnlineState(key, value string) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO online_state (key, value) VALUES (?, ?)`, key, value)
	return err
}

// OnlineStatePrefix returns every state key that starts with prefix, with its value.
func (s *SQLiteStore) OnlineStatePrefix(prefix string) (map[string]string, error) {
	// substr rather than LIKE: metric names carry underscores, which LIKE reads as wildcards.
	rows, err := s.db.Query(`SELECT key, value FROM online_state WHERE substr(key, 1, ?) = ?`, len(prefix), prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// PruneOnline deletes online rows older than cutoff; resolved alerts too.
func (s *SQLiteStore) PruneOnline(cutoff time.Time) error {
	ms := cutoff.UnixMilli()
	for _, q := range []string{
		`DELETE FROM online_evals WHERE at_ms < ?`,
		`DELETE FROM online_scores WHERE at_ms < ?`,
		`DELETE FROM online_alerts WHERE status = 'resolved' AND updated_ms < ?`,
		`DELETE FROM online_findings WHERE state IN ('resolved', 'stale') AND updated_ms < ?`,
		`DELETE FROM online_corrections WHERE state != 'pending' AND created_ms < ?`,
		`DELETE FROM online_labels WHERE created_ms < ?`,
	} {
		if _, err := s.db.Exec(q, ms); err != nil {
			return err
		}
	}
	return nil
}

// OnlineEval returns the stored scoring record of one session.
func (s *SQLiteStore) OnlineEval(sessionID string) (onlineEvalRow, bool, error) {
	var e onlineEvalRow
	err := s.db.QueryRow(`SELECT session_id, trace_id, at_ms, tenant, kind, service, profile, lane, reasons,
		judge_tokens, jev_calls, run_id FROM online_evals WHERE session_id = ?`, sessionID).Scan(
		&e.SessionID, &e.TraceID, &e.AtMs, &e.Tenant, &e.Kind, &e.Service, &e.Profile, &e.Lane, &e.Reasons,
		&e.JudgeTokens, &e.JevCalls, &e.RunID)
	if errors.Is(err, sql.ErrNoRows) {
		return onlineEvalRow{}, false, nil
	}
	return e, err == nil, err
}

// onlineSessionScore is one stored score of a session with the version and model it was produced at.
type onlineSessionScore struct {
	Metric  string
	Score   *float64
	Status  string
	Version string
	Model   string
}

// OnlineScoresOf returns every score of one session, ordered by metric name.
func (s *SQLiteStore) OnlineScoresOf(sessionID string) ([]onlineSessionScore, error) {
	rows, err := s.db.Query(`SELECT metric, score, status, version, model FROM online_scores
		WHERE session_id = ? ORDER BY metric`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []onlineSessionScore
	for rows.Next() {
		var sc onlineSessionScore
		var score sql.NullFloat64
		if err := rows.Scan(&sc.Metric, &score, &sc.Status, &sc.Version, &sc.Model); err != nil {
			return nil, err
		}
		if score.Valid {
			sc.Score = &score.Float64
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// OnlineAlert returns one alert by id.
func (s *SQLiteStore) OnlineAlert(id string) (*online.Alert, bool, error) {
	var data string
	err := s.db.QueryRow(`SELECT data FROM online_alerts WHERE alert_id = ?`, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var a online.Alert
	if err := json.Unmarshal([]byte(data), &a); err != nil {
		return nil, false, err
	}
	return &a, true, nil
}

type onlineScoredCount struct {
	Tenant   string
	Kind     string
	Sessions int
}

// OnlineScoredCounts counts the sessions scored since sinceMs per tenant and kind.
func (s *SQLiteStore) OnlineScoredCounts(sinceMs int64) ([]onlineScoredCount, error) {
	rows, err := s.db.Query(`SELECT tenant, kind, COUNT(*) FROM online_evals WHERE at_ms >= ?
		GROUP BY tenant, kind ORDER BY tenant, kind`, sinceMs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []onlineScoredCount
	for rows.Next() {
		var c onlineScoredCount
		if err := rows.Scan(&c.Tenant, &c.Kind, &c.Sessions); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// OnlineRunTenants maps the id of every online run to its tenant. An hourly run holds the sessions of one
// (tenant, kind), so a run has one tenant; this is what makes a run attributable to a tenant-limited role.
func (s *SQLiteStore) OnlineRunTenants() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT run_id, tenant FROM online_evals WHERE run_id != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, tenant string
		if err := rows.Scan(&id, &tenant); err != nil {
			return nil, err
		}
		out[id] = tenant
	}
	return out, rows.Err()
}

// OnlineRunTenant returns the tenant of one online run; ok is false for a run that online evaluation did not make.
func (s *SQLiteStore) OnlineRunTenant(runID string) (tenant string, ok bool, err error) {
	if runID == "" {
		return "", false, nil
	}
	err = s.db.QueryRow(`SELECT tenant FROM online_evals WHERE run_id = ? LIMIT 1`, runID).Scan(&tenant)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return tenant, err == nil, err
}
