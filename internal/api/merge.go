package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/triageagent-dev/ego/internal/logx"
)

// Merging one tenant into another. A core that started without a sender token and got one later leaves its first
// sessions under the bare label ("<workspace id>") and the later ones under "<sender>/<workspace id>": one workspace
// as two tenants. Retention would remove the old rows in 30 days; an admin can instead move them under the right key.

// MergeResult counts what a merge moved, or with a dry run what it would move.
type MergeResult struct {
	DryRun bool `json:"dryRun,omitempty"`
	// Sessions are the sessions re-keyed; Skipped are the sessions of the tenant that stay where they are: one still
	// open, or one that also belongs to another tenant. Run the merge again once the open ones are complete.
	Sessions int `json:"sessions"`
	Skipped  int `json:"skipped"`
	// Calls are the usage ledger rows; Scores, Evals, Findings, Corrections, Alerts and Labels the online evaluation
	// rows of the tenant.
	Calls       int `json:"calls"`
	Scores      int `json:"scores"`
	Evals       int `json:"evals"`
	Findings    int `json:"findings"`
	Corrections int `json:"corrections"`
	Alerts      int `json:"alerts"`
	Labels      int `json:"labels"`
	// Runs are the evaluation runs started on the tenant's sessions: their record names the new tenant afterwards.
	Runs int `json:"runs"`
}

// splitTenantKey is the inverse of tenantKey: a key with no "/" is a bare label of the shared token. A label never
// holds a "/" (cleanLabel), so the first one is the separator.
func splitTenantKey(key string) (sender, label string) {
	if i := strings.Index(key, "/"); i >= 0 {
		return key[:i], key[i+1:]
	}
	return "", key
}

// sessionIsOnly reports whether the session belongs to the tenant key and to no other.
func sessionIsOnly(s *Session, key string) bool {
	t := sessionTenants(s)
	return len(t) == 1 && t[0] == key
}

// rekeySession moves a session to the tenant key to: its owner becomes the sender of to, and the label on its spans
// becomes the label of to, so that sessionTenants reads the new key from the session alone.
func rekeySession(s *Session, from, to string) {
	_, fromLabel := splitTenantKey(from)
	toSender, toLabel := splitTenantKey(to)
	attr := s.tenantAttr
	if attr == "" {
		attr = DefaultTenantAttr
	}
	if fromLabel != toLabel {
		for _, sp := range s.Spans {
			if cleanLabel(sp.TagString(attr)) == fromLabel {
				sp.Tags[attr] = toLabel
			}
		}
	}
	s.Sender = toSender
	if s.tenantKey != "" {
		s.tenantKey = to
	}
}

// MergeTenants moves every completed session that belongs to the tenant from alone under the tenant key to, with
// its usage ledger rows and its online evaluation rows (scores, evaluations, findings, corrections, alerts and
// labels). The session ids stay as they are: only the owner and the label change, so nothing that links to a session
// breaks. A session still open, or shared with another tenant, is left and counted in Skipped. With dryRun it only
// counts. The ledger and online rows move first in one transaction, then the sessions, so a failure between the two
// leaves a merge that the same request finishes.
func (s *SessionStore) MergeTenants(from, to string, dryRun bool) (MergeResult, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	res := MergeResult{DryRun: dryRun}
	attr := s.usageTenantAttr

	var inMemory []*Session
	s.mu.Lock()
	memIDs := map[string]bool{}
	for _, id := range s.order {
		sess := s.sessions[id]
		if sess == nil {
			continue
		}
		memIDs[id] = true
		if !sessionIsOnly(sess, from) {
			continue
		}
		if !sess.IsComplete {
			res.Skipped++
			continue
		}
		inMemory = append(inMemory, sess)
	}
	s.mu.Unlock()
	res.Sessions = len(inMemory)

	var archived []sessionSnapshot
	if s.store != nil {
		ids, skipped, err := s.store.mergeCandidates(from, memIDs)
		if err != nil {
			return res, err
		}
		res.Skipped += skipped
		// The ledger and online rows move by tenant, so a session left behind would be split from its own rows.
		if !dryRun && res.Skipped > 0 {
			return res, errMergeSkips
		}
		for _, id := range ids {
			snap, ok, err := s.store.Load(id)
			if err != nil || !ok {
				continue
			}
			archived = append(archived, snap)
		}
		res.Sessions += len(archived)
		if !dryRun {
			s.rekeyPendingUsage(from, to)
		}
		if err := s.store.mergeTenantRows(from, to, dryRun, &res); err != nil {
			if !dryRun {
				s.rekeyPendingUsage(to, from)
			}
			return res, err
		}
	}
	if dryRun {
		return res, nil
	}

	moved := map[string]*Session{}
	for _, snap := range archived {
		sess := sessionHeader(snap, attr)
		rekeySession(sess, from, to)
		moved[snap.SessionID] = sess
	}
	s.mu.Lock()
	for _, sess := range inMemory {
		rekeySession(sess, from, to)
		s.markChangedLocked(sess)
		moved[sess.ID] = sess
	}
	s.mu.Unlock()
	if s.store != nil && len(moved) > 0 {
		// Written now, not left to the next flush: the index rows decide what the switcher counts.
		if err := s.store.SaveAll(moved); err != nil {
			return res, err
		}
	}
	return res, nil
}

// errMergeSkips is returned by a real merge that would leave a session under the old key: one still open, or shared
// with another tenant. The dry run reports the count; the merge is retried when the open sessions have completed.
var errMergeSkips = errors.New("some sessions are still open or shared with another tenant; merge when they have completed")

// rekeyPendingUsage points the ledger rows still waiting for the next flush at the new key, so they do not land
// under the old one after the stored rows have moved.
func (s *SessionStore) rekeyPendingUsage(from, to string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.pendingUsage {
		if s.pendingUsage[i].Tenant == from {
			s.pendingUsage[i].Tenant = to
		}
	}
}

// mergeCandidates lists the archived sessions, not in memory, that belong to the tenant key alone and are complete,
// and counts the ones it leaves (open, or shared with another tenant).
func (s *SQLiteStore) mergeCandidates(key string, inMemory map[string]bool) (ids []string, skipped int, err error) {
	rows, err := s.db.Query(`SELECT session_id, complete, tenants FROM session_index WHERE tenants LIKE ? ESCAPE '\'`,
		"%"+strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(key)+"%")
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, tenants string
		var complete int
		if err := rows.Scan(&id, &complete, &tenants); err != nil {
			return nil, 0, err
		}
		if inMemory[id] {
			continue
		}
		list := splitIndexList(tenants)
		if !containsString(list, key) {
			continue
		}
		if len(list) != 1 || complete == 0 {
			skipped++
			continue
		}
		ids = append(ids, id)
	}
	return ids, skipped, rows.Err()
}

// mergeTenantRows re-keys the ledger and online rows of from to to in one transaction and counts them into res.
func (s *SQLiteStore) mergeTenantRows(from, to string, dryRun bool, res *MergeResult) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if !dryRun {
		if err := takeWriteLock(tx); err != nil {
			return err
		}
	}
	if res.Runs, err = rekeyRuns(tx, from, to, dryRun); err != nil {
		return err
	}
	for _, q := range []struct {
		count, move string
		margs       func() []any
		n           *int
	}{
		{`SELECT COUNT(*) FROM llm_calls WHERE tenant = ?`, `UPDATE llm_calls SET tenant = ? WHERE tenant = ?`, nil, &res.Calls},
		{`SELECT COUNT(*) FROM online_scores WHERE tenant = ?`, `UPDATE online_scores SET tenant = ? WHERE tenant = ?`, nil, &res.Scores},
		{`SELECT COUNT(*) FROM online_evals WHERE tenant = ?`, `UPDATE online_evals SET tenant = ? WHERE tenant = ?`, nil, &res.Evals},
		{`SELECT COUNT(*) FROM online_labels WHERE tenant = ?`, `UPDATE online_labels SET tenant = ? WHERE tenant = ?`, nil, &res.Labels},
		// The finding and correction rows keep their whole record in data, which names the tenant as well.
		{`SELECT COUNT(*) FROM online_findings WHERE tenant = ?`,
			`UPDATE online_findings SET data = json_set(data, '$.tenant', ?1), tenant = ?1 WHERE tenant = ?2`, nil, &res.Findings},
		{`SELECT COUNT(*) FROM online_corrections WHERE tenant = ?`,
			`UPDATE online_corrections SET data = json_set(data, '$.tenant', ?1), tenant = ?1 WHERE tenant = ?2`, nil, &res.Corrections},
		{`SELECT COUNT(*) FROM online_alerts WHERE json_extract(data, '$.key.tenant') = ?`,
			`UPDATE online_alerts SET data = json_set(data, '$.key.tenant', ?1) WHERE json_extract(data, '$.key.tenant') = ?2`, nil, &res.Alerts},
	} {
		if err := tx.QueryRow(q.count, from).Scan(q.n); err != nil {
			return err
		}
		if dryRun {
			continue
		}
		if _, err := tx.Exec(q.move, to, from); err != nil {
			return err
		}
	}
	if dryRun {
		return nil
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.announce(topicUsage, nil)
	return nil
}

// takeWriteLock makes a transaction that is about to read and then write hold the write lock from its first
// statement. A deferred transaction that reads first cannot upgrade once another connection has committed, and
// fails at once with SQLITE_BUSY, which the busy timeout does not retry; a write statement, even one that matches no
// row, takes the lock up front and waits for it like any other writer.
func takeWriteLock(tx *sql.Tx) error {
	_, err := tx.Exec(`UPDATE llm_calls SET tenant = tenant WHERE 0`)
	return err
}

// rekeyRuns names the tenant to instead of from in the spec of every evaluation run that has from, and returns how
// many that is. The spec is edited as generic JSON, so a field this code does not know is kept as it was.
func rekeyRuns(tx *sql.Tx, from, to string, dryRun bool) (int, error) {
	rows, err := tx.Query(`SELECT run_id, spec_json FROM runs`)
	if err != nil {
		return 0, err
	}
	specs := map[string]string{}
	for rows.Next() {
		var id, spec string
		if err := rows.Scan(&id, &spec); err != nil {
			rows.Close()
			return 0, err
		}
		specs[id] = spec
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	moved := 0
	for id, raw := range specs {
		var spec map[string]any
		if json.Unmarshal([]byte(raw), &spec) != nil {
			continue
		}
		changed := false
		if list, ok := spec["tenants"].([]any); ok {
			var kept []any
			seen := map[any]bool{}
			for _, v := range list {
				if v == from {
					v, changed = to, true
				}
				if !seen[v] {
					seen[v] = true
					kept = append(kept, v)
				}
			}
			spec["tenants"] = kept
		}
		if target, ok := spec["target"].(map[string]any); ok && target["tenant"] == from {
			target["tenant"], changed = to, true
		}
		if !changed {
			continue
		}
		moved++
		if dryRun {
			continue
		}
		out, err := json.Marshal(spec)
		if err != nil {
			return moved, err
		}
		if _, err := tx.Exec(`UPDATE runs SET spec_json = ? WHERE run_id = ?`, string(out), id); err != nil {
			return moved, err
		}
	}
	return moved, nil
}

type mergeTenantsRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
	// DryRun counts what the merge would move and moves nothing.
	DryRun bool `json:"dryRun,omitempty"`
}

// adminMergeTenantsHandler implements POST /api/admin/merge-tenants {"from": key, "to": key}. Admin only. It moves
// the sessions, usage and online evaluation rows of one tenant key under another, and its display name when the
// target has none. Both are tenant keys, never patterns.
func adminMergeTenantsHandler(store *SessionStore, names *TenantNames, senders *SenderStore, guard mergeGuard) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !roleInfoOrOpen(r.Context()).IsAdmin {
			writeRoleForbidden(w)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req mergeTenantsRequest
		if err := decodeJSON(w, r, &req); err != nil {
			writeEvaluateError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
		from, to := strings.TrimSpace(req.From), strings.TrimSpace(req.To)
		for _, k := range []string{from, to} {
			if k == "" || k == "*" || strings.HasSuffix(k, "/*") {
				writeEvaluateError(w, http.StatusBadRequest, "from and to must be tenant keys, not patterns")
				return
			}
		}
		if from == to {
			writeEvaluateError(w, http.StatusBadRequest, "from and to are the same tenant")
			return
		}
		if err := validTenantPattern(from); err != nil {
			writeEvaluateError(w, http.StatusBadRequest, "from: "+err.Error())
			return
		}
		toSender, toLabel := splitTenantKey(to)
		if ValidTenantLabel(toLabel) != nil || validTenantPattern(to) != nil {
			writeEvaluateError(w, http.StatusBadRequest, "to is not a valid tenant key")
			return
		}
		// The owner of the moved sessions becomes this sender, and later spans are matched against it, so it must
		// be one that can send.
		if toSender != "" && senders != nil && !senders.Exists(toSender) {
			writeEvaluateError(w, http.StatusBadRequest, "to names a sender that does not exist")
			return
		}
		who := "an admin"
		if id, ok := identityFromContext(r.Context()); ok && id.Username != "" {
			who = id.Username
		}
		merge := func() (MergeResult, error) { return store.MergeTenants(from, to, req.DryRun) }
		var res MergeResult
		var err error
		if guard != nil {
			res, err = guard(from, req.DryRun, merge)
		} else {
			res, err = merge()
		}
		if errors.Is(err, errMergeSkips) {
			writeEvaluateError(w, http.StatusConflict, err.Error()+" (dry run counts them as skipped)")
			return
		}
		if err != nil {
			logx.Errorf("merge tenants %s into %s by %s failed: %v", from, to, who, err)
			writeEvaluateError(w, http.StatusInternalServerError, "failed to merge tenants: "+err.Error())
			return
		}
		// The name moves after the data: a name left on a key with no sessions is harmless, a name lost is not.
		if !req.DryRun {
			if n := names.Name(from); n != "" {
				if names.Name(to) == "" {
					_ = names.Set(to, n)
				}
				_ = names.Set(from, "")
			}
		}
		logx.Warnf("merge tenants %s into %s by %s: dry run %v, %+v", from, to, who, req.DryRun, res)
		writeJSON(w, http.StatusOK, map[string]any{"data": res, "error": nil})
	}
}

// mergeGuard runs a merge while the online engine is held still, so that the alerts and runs it keeps in memory for
// the old key are not written back over the merge. It is nil when online evaluation is off.
type mergeGuard func(from string, dryRun bool, merge func() (MergeResult, error)) (MergeResult, error)
