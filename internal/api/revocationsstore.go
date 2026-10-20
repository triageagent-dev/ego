package api

import (
	"database/sql"
	"fmt"
)

const (
	revokeID      = "token"
	revokeSubject = "subject"
)

type revocationRow struct {
	kind, key string
	value     int64
}

func ensureRevocationsSchema(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS revocations (
		kind TEXT NOT NULL,
		key TEXT NOT NULL,
		value INTEGER NOT NULL,
		PRIMARY KEY (kind, key)
	)`); err != nil {
		return fmt.Errorf("creating revocations table: %w", err)
	}
	return nil
}

func (s *SQLiteStore) saveRevocation(kind, key string, value int64) error {
	_, err := s.db.Exec(`INSERT INTO revocations (kind, key, value) VALUES (?, ?, ?)
		ON CONFLICT(kind, key) DO UPDATE SET value = excluded.value`, kind, key, value)
	if err != nil {
		return fmt.Errorf("saving revocation: %w", err)
	}
	return nil
}

func (s *SQLiteStore) listRevocations() ([]revocationRow, error) {
	rows, err := s.db.Query(`SELECT kind, key, value FROM revocations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []revocationRow
	for rows.Next() {
		var r revocationRow
		if err := rows.Scan(&r.kind, &r.key, &r.value); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
