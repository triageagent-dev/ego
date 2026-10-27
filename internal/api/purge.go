package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/triageagent-dev/ego/internal/logx"
)

// Purging tenants that no longer exist. Retention removes old sessions on its own, but a tenant a sender stopped
// using (or the bare label of a core that has since got a sender token) stays in the switcher until its last session
// ages out; an admin can take it out at once.

// PurgeResult counts what a purge removed, or with a dry run what it would remove.
type PurgeResult struct {
	DryRun   bool `json:"dryRun,omitempty"`
	Sessions int  `json:"sessions"`
	Calls    int  `json:"calls"`
	// Scores, Findings, Corrections, Alerts and Labels are the online evaluation rows of the tenants.
	Scores      int `json:"scores"`
	Findings    int `json:"findings"`
	Corrections int `json:"corrections"`
	Alerts      int `json:"alerts"`
	Labels      int `json:"labels"`
	// Runs are the evaluation runs of the tenants, with their results: the judge's verdicts and rationales per
	// session. A run shared with another tenant stays.
	Runs int `json:"runs"`
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

// PurgeTenants removes every session whose tenants are all among keys, with its index row, ledger rows and online
// scores, every finding, correction, alert, score and label of the tenants, and their evaluation runs with the
// results of those runs: all that ego holds of them but the golden eval sets and rubric sets, which belong to no
// tenant. A session or run that also belongs to another tenant stays, with the other tenants' rows. With dryRun it only counts. The archive
// is deleted first and memory second, under the lock the persistence flush takes, so a flush cannot write a purged
// session back.
func (s *SessionStore) PurgeTenants(keys []string, dryRun bool) (PurgeResult, error) {
	set := map[string]bool{}
	for _, k := range keys {
		set[k] = true
	}
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	doomed := map[string]bool{}
	s.mu.Lock()
	for _, id := range s.order {
		if sess := s.sessions[id]; sess != nil && allIn(sessionTenants(sess), set) {
			doomed[id] = true
		}
	}
	s.mu.Unlock()

	res := PurgeResult{DryRun: dryRun}
	if s.store != nil {
		var err error
		if res, err = s.store.purgeTenantRows(set, doomed, dryRun); err != nil {
			return res, err
		}
	}
	res.Sessions = len(doomed)
	if dryRun {
		return res, nil
	}

	s.mu.Lock()
	order := s.order[:0]
	for _, id := range s.order {
		if !doomed[id] {
			order = append(order, id)
			continue
		}
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
	return res, nil
}

// purgeTenantRows finds the archived sessions whose tenants are all in set (doomed gains them) and deletes them, with
// the online rows and ledger rows of the tenants, in one transaction. With dryRun it counts and deletes nothing.
func (s *SQLiteStore) purgeTenantRows(set map[string]bool, doomed map[string]bool, dryRun bool) (PurgeResult, error) {
	res := PurgeResult{DryRun: dryRun}
	rows, err := s.db.Query(`SELECT session_id, tenants FROM session_index WHERE tenants != ''`)
	if err != nil {
		return res, err
	}
	for rows.Next() {
		var id, tenants string
		if err := rows.Scan(&id, &tenants); err != nil {
			rows.Close()
			return res, err
		}
		if allIn(splitIndexList(tenants), set) {
			doomed[id] = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return res, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return res, err
	}
	defer tx.Rollback()
	// The runs are found before the online rows go: an online run is tied to its tenant by them as well as by its spec.
	runs, err := doomedRunIDs(tx, set)
	if err != nil {
		return res, err
	}
	res.Runs = len(runs)
	// Per tenant: the rows that carry the tenant key itself.
	byTenant := []struct {
		count, del string
		n          *int
	}{
		{`SELECT COUNT(*) FROM llm_calls WHERE tenant = ?`, `DELETE FROM llm_calls WHERE tenant = ?`, &res.Calls},
		{`SELECT COUNT(*) FROM online_scores WHERE tenant = ?`, `DELETE FROM online_scores WHERE tenant = ?`, &res.Scores},
		{`SELECT COUNT(*) FROM online_findings WHERE tenant = ?`, `DELETE FROM online_findings WHERE tenant = ?`, &res.Findings},
		{`SELECT COUNT(*) FROM online_corrections WHERE tenant = ?`, `DELETE FROM online_corrections WHERE tenant = ?`, &res.Corrections},
		{`SELECT COUNT(*) FROM online_labels WHERE tenant = ?`, `DELETE FROM online_labels WHERE tenant = ?`, &res.Labels},
		{`SELECT COUNT(*) FROM online_alerts WHERE json_extract(data, '$.key.tenant') = ?`, `DELETE FROM online_alerts WHERE json_extract(data, '$.key.tenant') = ?`, &res.Alerts},
		{`SELECT COUNT(*) FROM online_evals WHERE tenant = ?`, `DELETE FROM online_evals WHERE tenant = ?`, new(int)},
	}
	for k := range set {
		for _, q := range byTenant {
			var n int
			if err := tx.QueryRow(q.count, k).Scan(&n); err != nil {
				return res, err
			}
			*q.n += n
			if !dryRun {
				if _, err := tx.Exec(q.del, k); err != nil {
					return res, err
				}
			}
		}
	}
	if dryRun {
		return res, nil
	}
	for _, id := range runs {
		for _, q := range []string{`DELETE FROM run_results WHERE run_id = ?`, `DELETE FROM runs WHERE run_id = ?`} {
			if _, err := tx.Exec(q, id); err != nil {
				return res, err
			}
		}
	}
	for id := range doomed {
		for _, q := range []string{
			`DELETE FROM sessions WHERE session_id = ?`,
			`DELETE FROM session_index WHERE session_id = ?`,
			`DELETE FROM online_evals WHERE session_id = ?`,
			`DELETE FROM online_scores WHERE session_id = ?`,
			`DELETE FROM llm_calls WHERE session_id = ?`,
		} {
			if _, err := tx.Exec(q, id); err != nil {
				return res, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return res, err
	}
	s.announce(topicUsage, nil)
	return res, nil
}

// doomedRunIDs are the runs whose tenants are all among set: an evaluation started on sessions of those tenants, or
// an online run made for one of them. A run that cannot be attributed to a tenant (uploaded files) is not theirs.
func doomedRunIDs(tx *sql.Tx, set map[string]bool) ([]string, error) {
	online := map[string]string{}
	rows, err := tx.Query(`SELECT DISTINCT run_id, tenant FROM online_evals WHERE run_id != ''`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, tenant string
		if err := rows.Scan(&id, &tenant); err != nil {
			rows.Close()
			return nil, err
		}
		online[id] = tenant
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}

	rows, err = tx.Query(`SELECT run_id, spec_json FROM runs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id, specJSON string
		if err := rows.Scan(&id, &specJSON); err != nil {
			return nil, err
		}
		var spec runSpecSummaryDTO
		if json.Unmarshal([]byte(specJSON), &spec) != nil {
			continue
		}
		if allIn(runTenants(runDTO{RunID: id, Spec: spec}, online[id]), set) {
			out = append(out, id)
		}
	}
	return out, rows.Err()
}

// purgeGuard runs a purge while the online engine is held still, so that nothing it keeps in memory is written back
// over the purge. It is nil when online evaluation is off.
type purgeGuard func(keys []string, dryRun bool, purge func() (PurgeResult, error)) (PurgeResult, error)

type purgeTenantsRequest struct {
	Keys []string `json:"keys"`
	// DryRun counts what the purge would remove and removes nothing.
	DryRun bool `json:"dryRun,omitempty"`
}

// adminPurgeTenantsHandler implements POST /api/admin/purge-tenants {"keys": [...]}. Admin only. It removes the
// sessions, usage rows and names of the tenant keys it is given, and nothing else.
func adminPurgeTenantsHandler(store *SessionStore, names *TenantNames, guard purgeGuard) http.HandlerFunc {
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
		who := "an admin"
		if id, ok := identityFromContext(r.Context()); ok && id.Username != "" {
			who = id.Username
		}
		// The names go first: clearing one cannot fail after the data is gone, and a name left over for a tenant that
		// no longer exists is harmless.
		if !req.DryRun {
			for _, k := range keys {
				if err := names.Set(k, ""); err != nil {
					writeEvaluateError(w, http.StatusInternalServerError, "failed to clear tenant name: "+err.Error())
					return
				}
			}
		}
		purge := func() (PurgeResult, error) { return store.PurgeTenants(keys, req.DryRun) }
		var res PurgeResult
		var err error
		if guard != nil {
			res, err = guard(keys, req.DryRun, purge)
		} else {
			res, err = purge()
		}
		if err != nil {
			logx.Errorf("purge tenants %v by %s failed: %v", keys, who, err)
			writeEvaluateError(w, http.StatusInternalServerError, "failed to purge tenants: "+err.Error())
			return
		}
		logx.Warnf("purge tenants %v by %s: dry run %v, %+v", keys, who, req.DryRun, res)
		writeJSON(w, http.StatusOK, map[string]any{"data": res, "error": nil})
	}
}
