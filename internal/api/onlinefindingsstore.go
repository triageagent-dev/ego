package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/triageagent-dev/ego/internal/online"
)

// Storage of findings and corrections (docs/rsi.md, sections 2 and 3). The columns are what is queried; data holds
// the rest as JSON, so a new field needs no migration.

// UpsertOnlineFinding writes a finding.
func (s *SQLiteStore) UpsertOnlineFinding(f *online.Finding) error {
	b, err := json.Marshal(f.FindingData)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT OR REPLACE INTO online_findings
		(finding_id, tenant, kind, metric, state, alert_id, created_ms, updated_ms, data)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.ID, f.Tenant, f.Kind, f.Metric, f.State, f.AlertID, f.CreatedAt.UnixMilli(), f.UpdatedAt.UnixMilli(), string(b))
	return s.announce(topicQuality, err)
}

const findingColumns = `finding_id, tenant, kind, metric, state, alert_id, created_ms, updated_ms, data`

func scanFinding(row interface{ Scan(...any) error }) (*online.Finding, error) {
	var f online.Finding
	var created, updated int64
	var data string
	if err := row.Scan(&f.ID, &f.Tenant, &f.Kind, &f.Metric, &f.State, &f.AlertID, &created, &updated, &data); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(data), &f.FindingData); err != nil {
		return nil, err
	}
	f.CreatedAt, f.UpdatedAt = time.UnixMilli(created).UTC(), time.UnixMilli(updated).UTC()
	if f.Sessions == nil {
		f.Sessions = []string{}
	}
	if f.History == nil {
		f.History = []online.CorrectionNote{}
	}
	if f.Diagnosis.Evidence == nil {
		f.Diagnosis.Evidence = []online.DiagnosisEvidence{}
	}
	return &f, nil
}

// OnlineFinding returns one finding by id.
func (s *SQLiteStore) OnlineFinding(id string) (*online.Finding, bool, error) {
	f, err := scanFinding(s.db.QueryRow(`SELECT `+findingColumns+` FROM online_findings WHERE finding_id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	return f, err == nil, err
}

// OnlineFindings lists findings last updated first. Empty state or kind match all; sinceMs filters on the last
// update, so a refreshed finding shows again.
func (s *SQLiteStore) OnlineFindings(state, kind string, sinceMs int64, limit int) ([]*online.Finding, error) {
	q := `SELECT ` + findingColumns + ` FROM online_findings WHERE updated_ms >= ?`
	args := []any{sinceMs}
	if state != "" {
		q += ` AND state = ?`
		args = append(args, state)
	}
	if kind != "" {
		q += ` AND kind = ?`
		args = append(args, kind)
	}
	q += ` ORDER BY updated_ms DESC, finding_id LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*online.Finding{}
	for rows.Next() {
		f, err := scanFinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// OnlineFindingByAlert returns the finding written for an alert.
func (s *SQLiteStore) OnlineFindingByAlert(alertID string) (*online.Finding, bool, error) {
	f, err := scanFinding(s.db.QueryRow(`SELECT `+findingColumns+` FROM online_findings
		WHERE alert_id = ? ORDER BY created_ms DESC LIMIT 1`, alertID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	return f, err == nil, err
}

// OnlineActiveFindings returns every open or corrected finding.
func (s *SQLiteStore) OnlineActiveFindings() ([]*online.Finding, error) {
	rows, err := s.db.Query(`SELECT ` + findingColumns + ` FROM online_findings
		WHERE state IN ('open', 'corrected') ORDER BY created_ms, finding_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*online.Finding
	for rows.Next() {
		f, err := scanFinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// UpsertOnlineCorrection writes a correction.
func (s *SQLiteStore) UpsertOnlineCorrection(c *online.Correction) error {
	b, err := json.Marshal(c.CorrectionData)
	if err != nil {
		return err
	}
	var verified int64
	if c.VerifiedAt != nil {
		verified = c.VerifiedAt.UnixMilli()
	}
	_, err = s.db.Exec(`INSERT OR REPLACE INTO online_corrections
		(correction_id, tenant, kind, metric, finding_id, version, state, created_ms, verified_ms, data)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Tenant, c.Kind, c.Metric, c.FindingID, c.Version, c.State, c.CreatedAt.UnixMilli(), verified, string(b))
	return s.announce(topicQuality, err)
}

const correctionColumns = `correction_id, tenant, kind, metric, finding_id, version, state, created_ms, verified_ms, data`

func scanCorrection(row interface{ Scan(...any) error }) (*online.Correction, error) {
	var c online.Correction
	var created, verified int64
	var data string
	if err := row.Scan(&c.ID, &c.Tenant, &c.Kind, &c.Metric, &c.FindingID, &c.Version, &c.State, &created, &verified, &data); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(data), &c.CorrectionData); err != nil {
		return nil, err
	}
	c.CreatedAt = time.UnixMilli(created).UTC()
	if verified > 0 {
		t := time.UnixMilli(verified).UTC()
		c.VerifiedAt = &t
	}
	return &c, nil
}

// OnlineCorrection returns one correction by id.
func (s *SQLiteStore) OnlineCorrection(id string) (*online.Correction, bool, error) {
	c, err := scanCorrection(s.db.QueryRow(`SELECT `+correctionColumns+` FROM online_corrections WHERE correction_id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	return c, err == nil, err
}

// CountOnlineCorrections is how many corrections a (tenant, kind) holds in a state.
func (s *SQLiteStore) CountOnlineCorrections(tenant, kind, state string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM online_corrections WHERE tenant = ? AND kind = ? AND state = ?`, tenant, kind, state).Scan(&n)
	return n, err
}

// OnlineCorrections lists corrections newest first; empty state or kind match all; limit <= 0 is no limit.
func (s *SQLiteStore) OnlineCorrections(state, kind string, limit int) ([]*online.Correction, error) {
	q := `SELECT ` + correctionColumns + ` FROM online_corrections WHERE 1 = 1`
	var args []any
	if state != "" {
		q += ` AND state = ?`
		args = append(args, state)
	}
	if kind != "" {
		q += ` AND kind = ?`
		args = append(args, kind)
	}
	q += ` ORDER BY created_ms DESC, correction_id`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*online.Correction{}
	for rows.Next() {
		c, err := scanCorrection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
