package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

var errTooManyRubricSets = errors.New("too many rubric sets: delete one first")

// Rubric sets are saved lists of rubric texts for the two rubric metrics, so a set is chosen on the evaluate page
// instead of typed again. They live in the session archive beside the eval sets.

// Rubric set kinds: which rubric metric a set belongs to.
const (
	RubricKindFinalResponse = "final_response" // rubric_based_final_response_quality_v1
	RubricKindToolUse       = "tool_use"       // rubric_based_tool_use_quality_v1
)

// Limits on a rubric set.
const (
	maxRubricSetNameChars = 120
	maxRubricSetDescChars = 500
	maxRubricsPerSet      = 20
	maxRubricChars        = 500
	maxRubricSets         = 200
)

func ensureRubricSetsSchema(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS rubric_sets (
		rubric_set_id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		description TEXT,
		kind TEXT NOT NULL,
		rubrics_json TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("creating rubric_sets table: %w", err)
	}
	return nil
}

// rubricSetDTO mirrors ui/src/lib/types.ts's RubricSet. A set is a few lines of text, so the list carries them.
type rubricSetDTO struct {
	RubricSetID string   `json:"rubricSetId"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Kind        string   `json:"kind"`
	Rubrics     []string `json:"rubrics"`
	CreatedAt   string   `json:"createdAt"`
	UpdatedAt   string   `json:"updatedAt"`
}

// SaveRubricSet upserts a set by its id: a re-save overwrites the content and keeps created_at.
func (s *SQLiteStore) SaveRubricSet(set rubricSetDTO) (rubricSetDTO, error) {
	b, err := json.Marshal(set.Rubrics)
	if err != nil {
		return rubricSetDTO{}, err
	}
	now := nowRFC3339()
	// The cap is part of the insert's own statement: a new id is inserted only while there is room, an existing id
	// is always updated, and two saves racing for the last place cannot both pass a separate count.
	res, err := s.db.Exec(`INSERT INTO rubric_sets (rubric_set_id, name, description, kind, rubrics_json, created_at, updated_at)
		SELECT ?, ?, ?, ?, ?, ?, ?
		WHERE EXISTS (SELECT 1 FROM rubric_sets WHERE rubric_set_id = ?) OR (SELECT COUNT(*) FROM rubric_sets) < ?
		ON CONFLICT(rubric_set_id) DO UPDATE SET
			name = excluded.name,
			description = excluded.description,
			kind = excluded.kind,
			rubrics_json = excluded.rubrics_json,
			updated_at = excluded.updated_at`,
		set.RubricSetID, set.Name, set.Description, set.Kind, string(b), now, now, set.RubricSetID, maxRubricSets)
	if err != nil {
		return rubricSetDTO{}, err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return rubricSetDTO{}, errTooManyRubricSets
	}
	created := now
	if err := s.db.QueryRow(`SELECT created_at FROM rubric_sets WHERE rubric_set_id = ?`, set.RubricSetID).Scan(&created); err != nil {
		return rubricSetDTO{}, err
	}
	set.CreatedAt, set.UpdatedAt = created, now
	return set, nil
}

func scanRubricSet(row interface{ Scan(...any) error }) (rubricSetDTO, error) {
	var set rubricSetDTO
	var desc sql.NullString
	var rubrics string
	if err := row.Scan(&set.RubricSetID, &set.Name, &desc, &set.Kind, &rubrics, &set.CreatedAt, &set.UpdatedAt); err != nil {
		return rubricSetDTO{}, err
	}
	set.Description = desc.String
	if err := json.Unmarshal([]byte(rubrics), &set.Rubrics); err != nil {
		return rubricSetDTO{}, fmt.Errorf("deserializing rubric set %s: %w", set.RubricSetID, err)
	}
	return set, nil
}

const rubricSetColumns = `rubric_set_id, name, description, kind, rubrics_json, created_at, updated_at`

// ListRubricSets returns the sets newest-updated first; a non-empty kind keeps only that kind.
func (s *SQLiteStore) ListRubricSets(kind string) ([]rubricSetDTO, error) {
	q := `SELECT ` + rubricSetColumns + ` FROM rubric_sets`
	var args []any
	if kind != "" {
		q += ` WHERE kind = ?`
		args = append(args, kind)
	}
	rows, err := s.db.Query(q+` ORDER BY updated_at DESC, rubric_set_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []rubricSetDTO{}
	for rows.Next() {
		set, err := scanRubricSet(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, set)
	}
	return out, rows.Err()
}

// GetRubricSet returns one set, or ok=false.
func (s *SQLiteStore) GetRubricSet(id string) (rubricSetDTO, bool, error) {
	set, err := scanRubricSet(s.db.QueryRow(`SELECT `+rubricSetColumns+` FROM rubric_sets WHERE rubric_set_id = ?`, id))
	if err == sql.ErrNoRows {
		return rubricSetDTO{}, false, nil
	}
	return set, err == nil, err
}

// DeleteRubricSet removes a set; ok is false when there was none.
func (s *SQLiteStore) DeleteRubricSet(id string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM rubric_sets WHERE rubric_set_id = ?`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
