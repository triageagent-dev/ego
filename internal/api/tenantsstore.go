package api

import (
	"database/sql"
	"fmt"
)

func ensureTenantsSchema(db *sql.DB) error {
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS tenants (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS tenant_tokens (
			id TEXT PRIMARY KEY,
			tenant TEXT NOT NULL,
			label TEXT NOT NULL,
			hash TEXT NOT NULL UNIQUE,
			created_at TEXT NOT NULL
		)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("creating tenant tables: %w", err)
		}
	}
	return nil
}

func (s *SQLiteStore) saveTenant(t Tenant) error {
	_, err := s.db.Exec(`INSERT INTO tenants (id, name, created_at) VALUES (?, ?, ?)`, t.ID, t.Name, t.CreatedAt)
	if err != nil {
		return fmt.Errorf("saving tenant: %w", err)
	}
	return nil
}

func (s *SQLiteStore) deleteTenant(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM tenant_tokens WHERE tenant = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM tenants WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) saveTenantToken(t tenantTokenRecord) error {
	_, err := s.db.Exec(`INSERT INTO tenant_tokens (id, tenant, label, hash, created_at) VALUES (?, ?, ?, ?, ?)`,
		t.ID, t.Tenant, t.Label, t.hash, t.CreatedAt)
	if err != nil {
		return fmt.Errorf("saving tenant token: %w", err)
	}
	return nil
}

func (s *SQLiteStore) deleteTenantToken(id string) error {
	_, err := s.db.Exec(`DELETE FROM tenant_tokens WHERE id = ?`, id)
	return err
}

func (s *SQLiteStore) loadTenants() ([]Tenant, []tenantTokenRecord, error) {
	rows, err := s.db.Query(`SELECT id, name, created_at FROM tenants`)
	if err != nil {
		return nil, nil, err
	}
	var tenants []Tenant
	for rows.Next() {
		var t Tenant
		if err := rows.Scan(&t.ID, &t.Name, &t.CreatedAt); err != nil {
			rows.Close()
			return nil, nil, err
		}
		tenants = append(tenants, t)
	}
	rows.Close()
	trows, err := s.db.Query(`SELECT id, tenant, label, hash, created_at FROM tenant_tokens`)
	if err != nil {
		return nil, nil, err
	}
	defer trows.Close()
	var tokens []tenantTokenRecord
	for trows.Next() {
		var t tenantTokenRecord
		if err := trows.Scan(&t.ID, &t.Tenant, &t.Label, &t.hash, &t.CreatedAt); err != nil {
			return nil, nil, err
		}
		tokens = append(tokens, t)
	}
	return tenants, tokens, trows.Err()
}
