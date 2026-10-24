package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// Purging a tenant removes its sessions (in memory and archived) and its ledger rows, and leaves every other tenant,
// and a session shared with another tenant, alone.
func TestPurgeTenantsRemovesOnlyTheTenantsSessions(t *testing.T) {
	archive, err := NewSQLiteStore(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	sep := indexSep
	for _, r := range []struct{ id, tenants string }{
		{"old-gone", sep + "gone" + sep},
		{"old-both", sep + "gone" + sep + "keep" + sep},
		{"old-keep", sep + "keep" + sep},
	} {
		if _, err := archive.db.Exec(`INSERT INTO session_index (session_id, started_ms, complete, llm_calls, tenants, agents, search, thread, part, first_ms, ended_ms, turns)
			VALUES (?, 1, 1, 1, ?, '', '', ?, 1, 1, 1, 0)`, r.id, r.tenants, r.id); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.db.Exec(`INSERT INTO llm_calls (span_id, trace_id, session_id, start_ms, duration_ms, service, tenant, kind, model, name, input_tokens, output_tokens, is_error, thread)
			VALUES (?, 't', ?, 1, 1, '', ?, '', 'm', '', 1, 1, 0, ?)`, "sp-"+r.id, r.id, strings.Trim(strings.Split(r.tenants, sep)[1], sep), r.id); err != nil {
			t.Fatal(err)
		}
	}
	store := NewSessionStoreWithArchive(nil, archive, SessionLimits{})
	store.Ingest(labelledTracesBody("mem-gone", "aaaa", "s1", "gone"))
	store.Ingest(labelledTracesBody("mem-keep", "bbbb", "s2", "keep"))

	res, err := store.PurgeTenants([]string{"gone"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Sessions != 2 {
		t.Errorf("purged %d sessions, want the memory one and old-gone", res.Sessions)
	}
	var left []string
	for _, sum := range store.List() {
		left = append(left, sum.SessionID)
	}
	if len(left) != 1 || !strings.Contains(left[0], "mem-keep") {
		t.Errorf("sessions in memory = %v, want only mem-keep", left)
	}
	var ids []string
	rows, _ := archive.db.Query(`SELECT session_id FROM session_index ORDER BY session_id`)
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	if strings.Join(ids, ",") != "old-both,old-keep" {
		t.Errorf("archived index = %v, want the shared and the other tenant's session", ids)
	}
	var calls int
	archive.db.QueryRow(`SELECT COUNT(*) FROM llm_calls WHERE tenant = 'gone'`).Scan(&calls)
	if calls != 0 {
		t.Errorf("%d ledger rows of the purged tenant remain", calls)
	}
}

func TestPurgeTenantsEndpointIsAdminOnlyAndTakesKeysNotPatterns(t *testing.T) {
	store := NewSessionStore(nil)
	names, _ := NewTenantNames(nil)
	_ = names.Set("gone", "Gone")
	h := adminPurgeTenantsHandler(store, names)
	post := func(role RoleInfo, body string) int {
		req := withRoleInfoForTest(role)(httptest.NewRequest(http.MethodPost, "/api/admin/purge-tenants", strings.NewReader(body)))
		rec := httptest.NewRecorder()
		h(rec, req)
		return rec.Code
	}
	if code := post(RoleInfo{Role: RoleMember}, `{"keys":["gone"]}`); code != http.StatusForbidden {
		t.Errorf("a member purging = %d, want 403", code)
	}
	for _, bad := range []string{`{}`, `{"keys":["*"]}`, `{"keys":["core/*"]}`, `{"keys":[""]}`} {
		if code := post(RoleInfo{Role: RoleAdmin, IsAdmin: true}, bad); code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", bad, code)
		}
	}
	if names.Name("gone") != "Gone" {
		t.Error("a refused request cleared a name")
	}
	if code := post(RoleInfo{Role: RoleAdmin, IsAdmin: true}, `{"keys":["gone"]}`); code != http.StatusOK || names.Name("gone") != "" {
		t.Errorf("admin purge = %d, name = %q, want 200 and the name cleared", code, names.Name("gone"))
	}
}

// A purge is an erasure: the tenant's findings, corrections, alerts, labels and scores go with its sessions, a dry run
// counts them and removes nothing, and another tenant's rows stay.
func TestPurgeTenantsErasesTheOnlineRowsAndCanBeDryRun(t *testing.T) {
	archive, err := NewSQLiteStore(filepath.Join(t.TempDir(), "e.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := archive.db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for _, tenant := range []string{"gone", "keep"} {
		exec(`INSERT INTO online_findings (finding_id, tenant, kind, metric, state, alert_id, created_ms, updated_ms, data) VALUES (?, ?, 'k', 'm', 'open', 'a', 1, 1, '{}')`, "f-"+tenant, tenant)
		exec(`INSERT INTO online_corrections (correction_id, tenant, kind, metric, finding_id, version, state, created_ms, verified_ms, data) VALUES (?, ?, 'k', 'm', 'f', 'v', 'pending', 1, 0, '{}')`, "c-"+tenant, tenant)
		exec(`INSERT INTO online_labels (label_id, session_id, tenant, kind, metric, judge_score, verdict, note, "by", created_ms) VALUES (?, 's', ?, 'k', 'm', 0.5, 'agree', '', 'p', 1)`, "l-"+tenant, tenant)
		exec(`INSERT INTO online_alerts (alert_id, status, updated_ms, data) VALUES (?, 'open', 1, ?)`, "al-"+tenant, `{"key":{"tenant":"`+tenant+`"}}`)
		exec(`INSERT INTO online_scores (session_id, at_ms, tenant, kind, version, model, metric, score, status) VALUES (?, 1, ?, 'k', 'v', 'm', 'm', 0.5, 'ok')`, "s-"+tenant, tenant)
	}
	store := NewSessionStoreWithArchive(nil, archive, SessionLimits{})
	count := func(table string) (n int) {
		_ = archive.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n)
		return n
	}

	dry, err := store.PurgeTenants([]string{"gone"}, true)
	if err != nil || !dry.DryRun || dry.Findings != 1 || dry.Corrections != 1 || dry.Labels != 1 || dry.Alerts != 1 || dry.Scores != 1 {
		t.Fatalf("dry run = %+v err=%v, want one of each counted", dry, err)
	}
	for _, table := range []string{"online_findings", "online_corrections", "online_labels", "online_alerts", "online_scores"} {
		if count(table) != 2 {
			t.Errorf("%s has %d rows after a dry run, want both kept", table, count(table))
		}
	}
	if _, err := store.PurgeTenants([]string{"gone"}, false); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"online_findings", "online_corrections", "online_labels", "online_alerts", "online_scores"} {
		if count(table) != 1 {
			t.Errorf("%s has %d rows after the purge, want only the other tenant's", table, count(table))
		}
	}
}
