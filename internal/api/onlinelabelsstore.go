package api

import (
	"database/sql"
	"time"

	"github.com/triageagent-dev/ego/internal/online"
)

// Storage of labels (docs/rsi.md, section 4). A label is never edited: a changed mind is a newer row, and
// online.Latest picks the one that counts.

// InsertOnlineLabel writes a label.
func (s *SQLiteStore) InsertOnlineLabel(l *online.Label) error {
	var score sql.NullFloat64
	if l.Score != nil {
		score = sql.NullFloat64{Float64: *l.Score, Valid: true}
	}
	_, err := s.db.Exec(`INSERT INTO online_labels
		(label_id, session_id, tenant, kind, metric, judge_score, verdict, score, note, "by", created_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		l.ID, l.SessionID, l.Tenant, l.Kind, l.Metric, l.JudgeScore, l.Verdict, score, l.Note, l.By, l.CreatedAt.UnixMilli())
	return err
}

const labelColumns = `label_id, session_id, tenant, kind, metric, judge_score, verdict, score, note, "by", created_ms`

func scanLabel(row interface{ Scan(...any) error }) (*online.Label, error) {
	var l online.Label
	var score sql.NullFloat64
	var created int64
	if err := row.Scan(&l.ID, &l.SessionID, &l.Tenant, &l.Kind, &l.Metric, &l.JudgeScore, &l.Verdict, &score, &l.Note, &l.By, &created); err != nil {
		return nil, err
	}
	if score.Valid {
		l.Score = &score.Float64
	}
	l.CreatedAt = time.UnixMilli(created).UTC()
	return &l, nil
}

// OnlineLabels lists labels newest first. Empty metric or sessionID match all; limit <= 0 is no limit.
func (s *SQLiteStore) OnlineLabels(metric, sessionID string, sinceMs int64, limit int) ([]*online.Label, error) {
	q := `SELECT ` + labelColumns + ` FROM online_labels WHERE created_ms >= ?`
	args := []any{sinceMs}
	if metric != "" {
		q += ` AND metric = ?`
		args = append(args, metric)
	}
	if sessionID != "" {
		q += ` AND session_id = ?`
		args = append(args, sessionID)
	}
	q += ` ORDER BY created_ms DESC, label_id`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*online.Label{}
	for rows.Next() {
		l, err := scanLabel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
