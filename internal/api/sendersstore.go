package api

import (
	"database/sql"
	"fmt"
	"strings"
)

// ensureSendersSchema creates the sender tables. An earlier release called them tenants (one tenant per token);
// their rows are carried over, since a token then named exactly what a sender is now.
func ensureSendersSchema(db *sql.DB) error {
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS senders (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS sender_tokens (
			id TEXT PRIMARY KEY,
			sender TEXT NOT NULL,
			label TEXT NOT NULL,
			hash TEXT NOT NULL UNIQUE,
			created_at TEXT NOT NULL
		)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("creating sender tables: %w", err)
		}
	}
	var old int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'tenants'`).Scan(&old); err == nil && old > 0 {
		for _, stmt := range []string{
			`INSERT OR IGNORE INTO senders (id, name, created_at) SELECT id, name, created_at FROM tenants`,
			`INSERT OR IGNORE INTO sender_tokens (id, sender, label, hash, created_at) SELECT id, tenant, label, hash, created_at FROM tenant_tokens`,
			`DROP TABLE tenant_tokens`,
			`DROP TABLE tenants`,
		} {
			if _, err := db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "no such table") {
				return fmt.Errorf("migrating tenants to senders: %w", err)
			}
		}
	}
	return nil
}

func (s *SQLiteStore) saveSender(t Sender) error {
	_, err := s.db.Exec(`INSERT INTO senders (id, name, created_at) VALUES (?, ?, ?)`, t.ID, t.Name, t.CreatedAt)
	if err != nil {
		return fmt.Errorf("saving sender: %w", err)
	}
	return nil
}

func (s *SQLiteStore) deleteSender(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM sender_tokens WHERE sender = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM senders WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) saveSenderToken(t senderTokenRecord) error {
	_, err := s.db.Exec(`INSERT INTO sender_tokens (id, sender, label, hash, created_at) VALUES (?, ?, ?, ?, ?)`,
		t.ID, t.Sender, t.Label, t.hash, t.CreatedAt)
	if err != nil {
		return fmt.Errorf("saving sender token: %w", err)
	}
	return nil
}

func (s *SQLiteStore) deleteSenderToken(id string) error {
	_, err := s.db.Exec(`DELETE FROM sender_tokens WHERE id = ?`, id)
	return err
}

func (s *SQLiteStore) loadSenders() ([]Sender, []senderTokenRecord, error) {
	rows, err := s.db.Query(`SELECT id, name, created_at FROM senders`)
	if err != nil {
		return nil, nil, err
	}
	var senders []Sender
	for rows.Next() {
		var t Sender
		if err := rows.Scan(&t.ID, &t.Name, &t.CreatedAt); err != nil {
			rows.Close()
			return nil, nil, err
		}
		senders = append(senders, t)
	}
	rows.Close()
	trows, err := s.db.Query(`SELECT id, sender, label, hash, created_at FROM sender_tokens`)
	if err != nil {
		return nil, nil, err
	}
	defer trows.Close()
	var tokens []senderTokenRecord
	for trows.Next() {
		var t senderTokenRecord
		if err := trows.Scan(&t.ID, &t.Sender, &t.Label, &t.hash, &t.CreatedAt); err != nil {
			return nil, nil, err
		}
		tokens = append(tokens, t)
	}
	return senders, tokens, trows.Err()
}
