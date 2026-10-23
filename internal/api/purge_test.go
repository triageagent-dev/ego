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

	res, err := store.PurgeTenants([]string{"gone"})
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
