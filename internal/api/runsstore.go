package api

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

// This file backs GET /api/runs, GET /api/runs/{id} and GET /api/runs/{id}/results with real persistence,
// sharing SQLiteStore's connection/file (one row per Run, one row per RunResultRow - see ui/src/lib/types.ts's
// Run/RunResultRow). Evaluation is synchronous: evaluateHandler/ evaluateStreamHandler
// write a Run + its RunResultRows directly when an evaluation finishes, with no separate submit/worker/queue
// step - enough to make the Runs dashboard (ui/src/components/runs) show real data instead
// of always being empty.
func ensureRunsSchema(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS runs (
		run_id TEXT PRIMARY KEY,
		status TEXT NOT NULL,
		spec_json TEXT NOT NULL,
		summary_json TEXT,
		error TEXT,
		created_at TEXT NOT NULL,
		started_at TEXT,
		finished_at TEXT
	)`); err != nil {
		return fmt.Errorf("creating runs table: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_runs_created_at ON runs(created_at)`); err != nil {
		return fmt.Errorf("creating runs.created_at index: %w", err)
	}
	// Covers ListRuns' most selective query shape (status IN (...) ORDER BY
	// created_at DESC) - idx_runs_created_at alone lets SQLite use the
	// index for the ORDER BY but still requires a full table scan to apply
	// the status filter, or vice versa; a composite index lets it satisfy
	// both from one index walk instead.
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_runs_status_created_at ON runs(status, created_at)`); err != nil {
		return fmt.Errorf("creating runs.status,created_at index: %w", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS run_results (
		result_id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		eval_set_item_id TEXT NOT NULL,
		eval_set_item_name TEXT NOT NULL,
		evaluator_name TEXT NOT NULL,
		evaluator_type TEXT NOT NULL,
		status TEXT NOT NULL,
		score REAL,
		per_invocation_scores_json TEXT,
		trace_id TEXT,
		details_json TEXT,
		error_text TEXT,
		created_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("creating run_results table: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_run_results_run_id ON run_results(run_id)`); err != nil {
		return fmt.Errorf("creating run_results.run_id index: %w", err)
	}
	return nil
}

// newRandomID returns "<prefix>-<16 random bytes, base64url>", matching
// oauth.go's makeStateToken pattern.
func newRandomID(prefix string) string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failing is effectively unrecoverable, but a
		// timestamp-based fallback keeps this from panicking a live
		// evaluation over an ID collision risk that's astronomically
		// unlikely in practice.
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + base64.RawURLEncoding.EncodeToString(buf)
}

func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// CreateRun inserts a new run row with status "running" and returns its
// generated ID. There is no "queued" state here since evaluation starts
// synchronously in the same request that creates the run.
func (s *SQLiteStore) CreateRun(spec runSpecSummaryDTO) (runDTO, error) {
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return runDTO{}, fmt.Errorf("serializing run spec: %w", err)
	}
	now := nowRFC3339()
	run := runDTO{
		RunID:     newRandomID("run"),
		Status:    "running",
		Spec:      spec,
		CreatedAt: now,
		StartedAt: &now,
	}
	_, err = s.db.Exec(`INSERT INTO runs (run_id, status, spec_json, created_at, started_at)
		VALUES (?, ?, ?, ?, ?)`, run.RunID, run.Status, string(specJSON), run.CreatedAt, *run.StartedAt)
	if err != nil {
		return runDTO{}, fmt.Errorf("inserting run: %w", err)
	}
	s.announce(topicRuns, nil)
	return run, nil
}

// FinishRun marks a run terminal (succeeded/failed) with its computed
// summary, or records the failure reason in errMsg.
func (s *SQLiteStore) FinishRun(runID, status string, summary *runSummaryDTO, errMsg *string) error {
	var summaryJSON sql.NullString
	if summary != nil {
		b, err := json.Marshal(summary)
		if err != nil {
			return fmt.Errorf("serializing run summary: %w", err)
		}
		summaryJSON = sql.NullString{String: string(b), Valid: true}
	}
	var errVal sql.NullString
	if errMsg != nil {
		errVal = sql.NullString{String: *errMsg, Valid: true}
	}
	_, err := s.db.Exec(`UPDATE runs SET status = ?, summary_json = ?, error = ?, finished_at = ? WHERE run_id = ?`,
		status, summaryJSON, errVal, nowRFC3339(), runID)
	return s.announce(topicRuns, err)
}

// InsertRunResults bulk-inserts one row per (trace, evaluator) result.
func (s *SQLiteStore) InsertRunResults(runID string, rows []runResultRowDTO) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT INTO run_results (
		result_id, run_id, eval_set_item_id, eval_set_item_name, evaluator_name, evaluator_type,
		status, score, per_invocation_scores_json, trace_id, details_json, error_text, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, row := range rows {
		perInvJSON, err := json.Marshal(row.PerInvocationScores)
		if err != nil {
			return fmt.Errorf("serializing perInvocationScores: %w", err)
		}
		var detailsJSON sql.NullString
		if row.Details != nil {
			b, err := json.Marshal(row.Details)
			if err != nil {
				return fmt.Errorf("serializing details: %w", err)
			}
			detailsJSON = sql.NullString{String: string(b), Valid: true}
		}
		var traceID, errText sql.NullString
		if row.TraceID != nil {
			traceID = sql.NullString{String: *row.TraceID, Valid: true}
		}
		if row.ErrorText != nil {
			errText = sql.NullString{String: *row.ErrorText, Valid: true}
		}
		var score sql.NullFloat64
		if row.Score != nil {
			score = sql.NullFloat64{Float64: *row.Score, Valid: true}
		}
		if _, err := stmt.Exec(
			row.ResultID, runID, row.EvalSetItemID, row.EvalSetItemName, row.EvaluatorName, row.EvaluatorType,
			row.Status, score, string(perInvJSON), traceID, detailsJSON, errText, row.CreatedAt,
		); err != nil {
			return err
		}
	}
	return s.announce(topicRuns, tx.Commit())
}

// ListRuns returns runs newest-first, optionally filtered by status and
// paginated via before (an RFC3339 createdAt cursor, exclusive).
func (s *SQLiteStore) ListRuns(statuses []string, limit int, before string) ([]runDTO, error) {
	query := `SELECT run_id, status, spec_json, summary_json, error, created_at, started_at, finished_at FROM runs`
	var args []any
	var conds []string
	if len(statuses) > 0 {
		placeholders := ""
		for i, st := range statuses {
			if i > 0 {
				placeholders += ", "
			}
			placeholders += "?"
			args = append(args, st)
		}
		conds = append(conds, "status IN ("+placeholders+")")
	}
	if before != "" {
		conds = append(conds, "created_at < ?")
		args = append(args, before)
	}
	for i, c := range conds {
		if i == 0 {
			query += " WHERE " + c
		} else {
			query += " AND " + c
		}
	}
	query += " ORDER BY created_at DESC"
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []runDTO
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

// GetRun returns a single run by ID, or ok=false if it doesn't exist.
func (s *SQLiteStore) GetRun(runID string) (runDTO, bool, error) {
	row := s.db.QueryRow(`SELECT run_id, status, spec_json, summary_json, error, created_at, started_at, finished_at
		FROM runs WHERE run_id = ?`, runID)
	run, err := scanRun(row)
	if err == sql.ErrNoRows {
		return runDTO{}, false, nil
	}
	if err != nil {
		return runDTO{}, false, err
	}
	return run, true, nil
}

// rowScanner is the subset of *sql.Row/*sql.Rows scanRun needs, so it works
// against both ListRuns (multi-row) and GetRun (single-row) results.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanRun(row rowScanner) (runDTO, error) {
	var (
		run                               runDTO
		specJSON                          string
		summaryJSON, errVal, started, fin sql.NullString
	)
	if err := row.Scan(&run.RunID, &run.Status, &specJSON, &summaryJSON, &errVal, &run.CreatedAt, &started, &fin); err != nil {
		return runDTO{}, err
	}
	if err := json.Unmarshal([]byte(specJSON), &run.Spec); err != nil {
		return runDTO{}, fmt.Errorf("deserializing run %s spec: %w", run.RunID, err)
	}
	if summaryJSON.Valid {
		var summary runSummaryDTO
		if err := json.Unmarshal([]byte(summaryJSON.String), &summary); err != nil {
			return runDTO{}, fmt.Errorf("deserializing run %s summary: %w", run.RunID, err)
		}
		run.Summary = &summary
	}
	if errVal.Valid {
		run.Error = &errVal.String
	}
	if started.Valid {
		run.StartedAt = &started.String
	}
	if fin.Valid {
		run.FinishedAt = &fin.String
	}
	return run, nil
}

// GetRunResults returns every result row for one run, oldest first.
func (s *SQLiteStore) GetRunResults(runID string) ([]runResultRowDTO, error) {
	return s.queryRunResults(`run_id = ?`, runID)
}

// GetRunResultsOfItem returns the result rows of one eval-set item of a run, oldest first. For an online run the
// item is a session, and a run holds a whole hour of them.
func (s *SQLiteStore) GetRunResultsOfItem(runID, itemID string) ([]runResultRowDTO, error) {
	return s.queryRunResults(`run_id = ? AND eval_set_item_id = ?`, runID, itemID)
}

func (s *SQLiteStore) queryRunResults(where string, args ...any) ([]runResultRowDTO, error) {
	rows, err := s.db.Query(`SELECT result_id, run_id, eval_set_item_id, eval_set_item_name, evaluator_name,
		evaluator_type, status, score, per_invocation_scores_json, trace_id, details_json, error_text, created_at
		FROM run_results WHERE `+where+` ORDER BY created_at ASC, result_id ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]runResultRowDTO, 0)
	for rows.Next() {
		var (
			row                          runResultRowDTO
			score                        sql.NullFloat64
			perInvJSON                   string
			traceID, detailsJSON, errTxt sql.NullString
		)
		if err := rows.Scan(&row.ResultID, &row.RunID, &row.EvalSetItemID, &row.EvalSetItemName, &row.EvaluatorName,
			&row.EvaluatorType, &row.Status, &score, &perInvJSON, &traceID, &detailsJSON, &errTxt, &row.CreatedAt); err != nil {
			return nil, err
		}
		if score.Valid {
			row.Score = &score.Float64
		}
		if traceID.Valid {
			row.TraceID = &traceID.String
		}
		if errTxt.Valid {
			row.ErrorText = &errTxt.String
		}
		if perInvJSON != "" {
			if err := json.Unmarshal([]byte(perInvJSON), &row.PerInvocationScores); err != nil {
				return nil, fmt.Errorf("deserializing result %s perInvocationScores: %w", row.ResultID, err)
			}
		}
		if detailsJSON.Valid {
			if err := json.Unmarshal([]byte(detailsJSON.String), &row.Details); err != nil {
				return nil, fmt.Errorf("deserializing result %s details: %w", row.ResultID, err)
			}
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
