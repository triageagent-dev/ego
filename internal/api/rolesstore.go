package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// This file backs RoleStore's optional durability (see roles.go), sharing
// SQLiteStore's connection/file the same way runsstore.go/evalsetsstore.go
// do: one row per RoleBinding, its Agents scope stored as a JSON array so
// it needs no join table.
func ensureRolesSchema(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS role_bindings (
		id TEXT PRIMARY KEY,
		subject_type TEXT NOT NULL,
		subject TEXT NOT NULL,
		role TEXT NOT NULL,
		agents_json TEXT,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("creating role_bindings table: %w", err)
	}
	return nil
}

// UpsertRoleBinding inserts or replaces one binding row by ID.
func (s *SQLiteStore) UpsertRoleBinding(b RoleBinding) error {
	agentsJSON, err := json.Marshal(b.Agents)
	if err != nil {
		return fmt.Errorf("serializing role binding agents: %w", err)
	}
	_, err = s.db.Exec(`INSERT INTO role_bindings (id, subject_type, subject, role, agents_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			subject_type = excluded.subject_type,
			subject = excluded.subject,
			role = excluded.role,
			agents_json = excluded.agents_json,
			updated_at = excluded.updated_at`,
		b.ID, b.SubjectType, b.Subject, string(b.Role), string(agentsJSON), b.CreatedAt, b.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upserting role binding %s: %w", b.ID, err)
	}
	return nil
}

// DeleteRoleBinding removes one binding row by ID; deleting a nonexistent
// ID is a no-op, not an error (RoleStore.Delete already checked existence
// in its in-memory map before calling this).
func (s *SQLiteStore) DeleteRoleBinding(id string) error {
	_, err := s.db.Exec(`DELETE FROM role_bindings WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("deleting role binding %s: %w", id, err)
	}
	return nil
}

// ListRoleBindings returns every persisted binding, in no particular
// order (RoleStore.List sorts by CreatedAt itself).
func (s *SQLiteStore) ListRoleBindings() ([]RoleBinding, error) {
	rows, err := s.db.Query(`SELECT id, subject_type, subject, role, agents_json, created_at, updated_at FROM role_bindings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]RoleBinding, 0)
	for rows.Next() {
		var (
			b          RoleBinding
			role       string
			agentsJSON sql.NullString
		)
		if err := rows.Scan(&b.ID, &b.SubjectType, &b.Subject, &role, &agentsJSON, &b.CreatedAt, &b.UpdatedAt); err != nil {
			return nil, err
		}
		b.Role = Role(role)
		if agentsJSON.Valid && agentsJSON.String != "" && agentsJSON.String != "null" {
			if err := json.Unmarshal([]byte(agentsJSON.String), &b.Agents); err != nil {
				return nil, fmt.Errorf("deserializing role binding %s agents: %w", b.ID, err)
			}
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
