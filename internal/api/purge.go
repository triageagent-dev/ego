package api

import (
	"database/sql"
	"net/http"
	"strings"
)

// Purging tenants that no longer exist. Retention removes old sessions on its own, but a tenant a sender stopped
// using (or the bare label of a core that has since got a sender token) stays in the switcher until its last session
// ages out; an admin can take it out at once.

// PurgeResult counts what a purge removed.
type PurgeResult struct {
	Sessions int `json:"sessions"`
	Calls    int `json:"calls"`
}

// allIn reports whether list is not empty and every entry of it is in set.
func allIn(list []string, set map[string]bool) bool {
	if len(list) == 0 {
		return false
	}
	for _, v := range list {
		if !set[v] {
			return false
		}
	}
	return true
}

// PurgeTenants removes every session whose tenants are all among keys: the session, its index row, its ledger rows
// and its online scores, from memory and from the archive. A session that also belongs to another tenant stays.
// Human labels are kept: they are ground truth a person gave.
func (s *SessionStore) PurgeTenants(keys []string) (PurgeResult, error) {
	set := map[string]bool{}
	for _, k := range keys {
		set[k] = true
	}
	doomed := map[string]bool{}

	s.mu.Lock()
	order := s.order[:0]
	for _, id := range s.order {
		sess := s.sessions[id]
		if sess != nil && allIn(sessionTenants(sess), set) {
			doomed[id] = true
			delete(s.sessions, id)
			delete(s.persistedSeq, id)
			if t, ok := s.idleTimers[id]; ok {
				t.Stop()
				delete(s.idleTimers, id)
			}
			if t, ok := s.completionTimers[id]; ok {
				t.Stop()
				delete(s.completionTimers, id)
			}
			continue
		}
		order = append(order, id)
	}
	s.order = order
	kept := s.pendingUsage[:0]
	for _, row := range s.pendingUsage {
		if !doomed[row.SessionID] {
			kept = append(kept, row)
		}
	}
	s.pendingUsage = kept
	s.mu.Unlock()

	var res PurgeResult
	if s.store != nil {
		archived, err := s.store.purgeTenantRows(set, doomed)
		if err != nil {
			return res, err
		}
		res.Calls = archived
	}
	res.Sessions = len(doomed)
	return res, nil
}

// purgeTenantRows deletes the archived sessions whose tenants are all in set, plus doomed (sessions that were in
// memory), and every ledger row of the tenants. It returns the ledger rows deleted. doomed gains the archived ids.
func (s *SQLiteStore) purgeTenantRows(set map[string]bool, doomed map[string]bool) (int, error) {
	rows, err := s.db.Query(`SELECT session_id, tenants FROM session_index WHERE tenants != ''`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var id, tenants string
		if err := rows.Scan(&id, &tenants); err != nil {
			rows.Close()
			return 0, err
		}
		if allIn(splitIndexList(tenants), set) {
			doomed[id] = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for id := range doomed {
		for _, q := range []string{
			`DELETE FROM sessions WHERE session_id = ?`,
			`DELETE FROM session_index WHERE session_id = ?`,
			`DELETE FROM online_evals WHERE session_id = ?`,
			`DELETE FROM online_scores WHERE session_id = ?`,
			`DELETE FROM llm_calls WHERE session_id = ?`,
		} {
			if _, err := tx.Exec(q, id); err != nil {
				return 0, err
			}
		}
	}
	calls, err := deleteTenantCalls(tx, set)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	s.announce(topicUsage, nil)
	return calls, nil
}

// deleteTenantCalls removes the ledger rows labelled with one of the tenants, whichever session they belong to.
func deleteTenantCalls(tx *sql.Tx, set map[string]bool) (int, error) {
	total := 0
	for k := range set {
		res, err := tx.Exec(`DELETE FROM llm_calls WHERE tenant = ?`, k)
		if err != nil {
			return 0, err
		}
		n, _ := res.RowsAffected()
		total += int(n)
	}
	return total, nil
}

type purgeTenantsRequest struct {
	Keys []string `json:"keys"`
}

// adminPurgeTenantsHandler implements POST /api/admin/purge-tenants {"keys": [...]}. Admin only. It removes the
// sessions, usage rows and names of the tenant keys it is given, and nothing else.
func adminPurgeTenantsHandler(store *SessionStore, names *TenantNames) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !roleInfoOrOpen(r.Context()).IsAdmin {
			writeRoleForbidden(w)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req purgeTenantsRequest
		if err := decodeJSON(w, r, &req); err != nil {
			writeEvaluateError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
		var keys []string
		for _, k := range req.Keys {
			k = strings.TrimSpace(k)
			if k == "" || k == "*" || strings.HasSuffix(k, "/*") {
				writeEvaluateError(w, http.StatusBadRequest, "keys must be tenant keys, not patterns")
				return
			}
			keys = append(keys, k)
		}
		if len(keys) == 0 {
			writeEvaluateError(w, http.StatusBadRequest, "keys is required")
			return
		}
		res, err := store.PurgeTenants(keys)
		if err != nil {
			writeEvaluateError(w, http.StatusInternalServerError, "failed to purge tenants: "+err.Error())
			return
		}
		for _, k := range keys {
			if err := names.Set(k, ""); err != nil {
				writeEvaluateError(w, http.StatusInternalServerError, "failed to clear tenant name: "+err.Error())
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": res, "error": nil})
	}
}
