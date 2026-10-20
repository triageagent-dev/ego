package api

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/triageagent-dev/ego/internal/adk"
)

// This file backs POST/GET /api/evalsets and GET /api/evalsets/{id}, sharing SQLiteStore's connection/file with
// the runs/session archive (see runsstore.go's doc comment for the analogous /api/runs design). This adds real
// server-side persistence so a saved EvalSet shows up in a list instead of only living in a browser-downloaded
// file.
func ensureEvalSetsSchema(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS eval_sets (
		eval_set_id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		description TEXT,
		num_cases INTEGER NOT NULL,
		eval_set_json TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("creating eval_sets table: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_eval_sets_updated_at ON eval_sets(updated_at)`); err != nil {
		return fmt.Errorf("creating eval_sets.updated_at index: %w", err)
	}
	return nil
}

// evalSetSummaryDTO mirrors ui/src/lib/types.ts's EvalSetSummary: enough to
// render a list without shipping every eval case's full conversation over
// the wire for GET /api/evalsets.
type evalSetSummaryDTO struct {
	EvalSetID   string `json:"evalSetId"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	NumCases    int    `json:"numCases"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

// SaveEvalSet upserts an eval set by its eval_set_id: a re-save with the
// same ID overwrites the previous content and bumps updated_at, but keeps
// the original created_at.
func (s *SQLiteStore) SaveEvalSet(evalSet adk.EvalSet) (evalSetSummaryDTO, error) {
	evalSetJSON, err := json.Marshal(evalSet)
	if err != nil {
		return evalSetSummaryDTO{}, fmt.Errorf("serializing eval set: %w", err)
	}
	now := nowRFC3339()

	var createdAt string
	err = s.db.QueryRow(`SELECT created_at FROM eval_sets WHERE eval_set_id = ?`, evalSet.EvalSetID).Scan(&createdAt)
	switch {
	case err == sql.ErrNoRows:
		createdAt = now
	case err != nil:
		return evalSetSummaryDTO{}, fmt.Errorf("checking existing eval set: %w", err)
	}

	_, err = s.db.Exec(`INSERT INTO eval_sets (eval_set_id, name, description, num_cases, eval_set_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(eval_set_id) DO UPDATE SET
			name = excluded.name,
			description = excluded.description,
			num_cases = excluded.num_cases,
			eval_set_json = excluded.eval_set_json,
			updated_at = excluded.updated_at`,
		evalSet.EvalSetID, evalSet.Name, evalSet.Description, len(evalSet.EvalCases), string(evalSetJSON), createdAt, now)
	if err != nil {
		return evalSetSummaryDTO{}, fmt.Errorf("upserting eval set: %w", err)
	}

	return evalSetSummaryDTO{
		EvalSetID:   evalSet.EvalSetID,
		Name:        evalSet.Name,
		Description: evalSet.Description,
		NumCases:    len(evalSet.EvalCases),
		CreatedAt:   createdAt,
		UpdatedAt:   now,
	}, nil
}

// ListEvalSets returns eval set summaries newest-updated-first.
func (s *SQLiteStore) ListEvalSets() ([]evalSetSummaryDTO, error) {
	rows, err := s.db.Query(`SELECT eval_set_id, name, description, num_cases, created_at, updated_at
		FROM eval_sets ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]evalSetSummaryDTO, 0)
	for rows.Next() {
		var (
			summary     evalSetSummaryDTO
			description sql.NullString
		)
		if err := rows.Scan(&summary.EvalSetID, &summary.Name, &description, &summary.NumCases, &summary.CreatedAt, &summary.UpdatedAt); err != nil {
			return nil, err
		}
		if description.Valid {
			summary.Description = description.String
		}
		out = append(out, summary)
	}
	return out, rows.Err()
}

// GetEvalSet returns one full eval set by ID, or ok=false if it doesn't
// exist.
func (s *SQLiteStore) GetEvalSet(evalSetID string) (adk.EvalSet, bool, error) {
	var evalSetJSON string
	err := s.db.QueryRow(`SELECT eval_set_json FROM eval_sets WHERE eval_set_id = ?`, evalSetID).Scan(&evalSetJSON)
	if err == sql.ErrNoRows {
		return adk.EvalSet{}, false, nil
	}
	if err != nil {
		return adk.EvalSet{}, false, err
	}
	var evalSet adk.EvalSet
	if err := json.Unmarshal([]byte(evalSetJSON), &evalSet); err != nil {
		return adk.EvalSet{}, false, fmt.Errorf("deserializing eval set %s: %w", evalSetID, err)
	}
	return evalSet, true, nil
}
