package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Merging a bare tenant key into the sender's key re-keys its completed sessions, in memory and archived, and its
// ledger and online rows. A session still open, one shared with another tenant, and every other tenant stay.
func TestMergeTenantsRekeysSessionsAndRows(t *testing.T) {
	archive, err := NewSQLiteStore(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()

	// The first process holds bare sessions; one is archived only (it is evicted below by a fresh store).
	first := NewSessionStoreWithArchive(nil, archive, SessionLimits{})
	first.Ingest(labelledTracesBody("arch-1", "a1", "s1", "ws1"))
	first.Ingest(labelledTracesBody("other-1", "a2", "s2", "ws2"))
	first.completeSession("arch-1")
	first.completeSession("other-1")
	if err := first.flushPersist(); err != nil {
		t.Fatal(err)
	}

	store := NewSessionStoreWithArchive(nil, archive, SessionLimits{})
	store.Ingest(labelledTracesBody("mem-1", "b1", "s3", "ws1"))
	store.Ingest(labelledTracesBody("open-1", "b2", "s4", "ws1"))
	store.completeSession("mem-1")
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := archive.db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for _, tenant := range []string{"ws1", "ws2"} {
		exec(`INSERT INTO runs (run_id, status, spec_json, created_at) VALUES (?, 'completed', ?, '2026-01-01T00:00:00Z')`,
			"run-"+tenant, `{"approach":"traces","tenants":["`+tenant+`"],"keep":"me"}`)
		exec(`INSERT INTO llm_calls (span_id, trace_id, session_id, start_ms, duration_ms, service, tenant, kind, model, name, input_tokens, output_tokens, is_error, thread)
			VALUES (?, 't', 's', 1, 1, '', ?, '', 'm', '', 1, 1, 0, 's')`, "sp-"+tenant, tenant)
		exec(`INSERT INTO online_findings (finding_id, tenant, kind, metric, state, alert_id, created_ms, updated_ms, data) VALUES (?, ?, 'k', 'm', 'open', 'a', 1, 1, ?)`,
			"f-"+tenant, tenant, `{"tenant":"`+tenant+`"}`)
		exec(`INSERT INTO online_alerts (alert_id, status, updated_ms, data) VALUES (?, 'open', 1, ?)`, "al-"+tenant, `{"key":{"tenant":"`+tenant+`"}}`)
	}

	dry, err := store.MergeTenants("ws1", "core/ws1", true)
	if err != nil {
		t.Fatal(err)
	}
	if !dry.DryRun || dry.Sessions != 2 || dry.Skipped != 1 || dry.Calls != 1 || dry.Findings != 1 || dry.Alerts != 1 || dry.Runs != 1 {
		t.Fatalf("dry run = %+v, want 2 sessions (arch-1, mem-1), 1 skipped (open-1) and one ledger, finding and alert row", dry)
	}
	if keys := tenantKeysOf(store); !slices.Contains(keys, "ws1") || slices.Contains(keys, "core/ws1") {
		t.Fatalf("a dry run changed the tenants: %v", keys)
	}

	// A real merge refuses while a session is open, and moves nothing: the rows move by tenant, so the open
	// session would be split from its own ledger rows and scores.
	if _, err := store.MergeTenants("ws1", "core/ws1", false); !errors.Is(err, errMergeSkips) {
		t.Fatalf("merge with an open session: err = %v, want errMergeSkips", err)
	}
	var left int
	archive.db.QueryRow(`SELECT COUNT(*) FROM llm_calls WHERE tenant = 'ws1'`).Scan(&left)
	if keys := tenantKeysOf(store); left != 1 || slices.Contains(keys, "core/ws1") {
		t.Fatalf("a refused merge moved something: ledger rows left under ws1 = %d, tenants = %v", left, keys)
	}
	store.completeSession("open-1")
	res, err := store.MergeTenants("ws1", "core/ws1", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Sessions != 3 || res.Skipped != 0 {
		t.Fatalf("merge = %+v, want 3 sessions moved and none skipped", res)
	}

	// Memory: both completed sessions moved, the other tenant did not.
	got := map[string][]string{}
	for _, id := range []string{"mem-1", "open-1"} {
		sess := store.sessions[id]
		if sess == nil {
			t.Fatalf("session %s missing", id)
		}
		got[id] = sessionTenants(sess)
	}
	if !slices.Equal(got["mem-1"], []string{"core/ws1"}) || !slices.Equal(got["open-1"], []string{"core/ws1"}) {
		t.Errorf("in memory: mem-1 = %v, open-1 = %v, want core/ws1 for both", got["mem-1"], got["open-1"])
	}

	// The archive: the index row and the stored blob both say core/ws1, so the session comes back under it.
	var tenants string
	archive.db.QueryRow(`SELECT tenants FROM session_index WHERE session_id = 'arch-1'`).Scan(&tenants)
	if strings.Trim(tenants, indexSep) != "core/ws1" {
		t.Errorf("archived index tenants = %q, want core/ws1", tenants)
	}
	snap, ok, err := archive.Load("arch-1")
	if err != nil || !ok {
		t.Fatalf("load arch-1: %v %v", ok, err)
	}
	if k := sessionTenants(sessionHeader(snap, DefaultTenantAttr)); !slices.Equal(k, []string{"core/ws1"}) {
		t.Errorf("archived blob reads as %v, want core/ws1", k)
	}
	archive.db.QueryRow(`SELECT tenants FROM session_index WHERE session_id = 'other-1'`).Scan(&tenants)
	if strings.Trim(tenants, indexSep) != "ws2" {
		t.Errorf("another tenant's session was touched: %q", tenants)
	}

	// The rows: moved for ws1, left for ws2, and the finding's own record follows its column.
	var n int
	archive.db.QueryRow(`SELECT COUNT(*) FROM llm_calls WHERE tenant = 'core/ws1'`).Scan(&n)
	if n != 1 {
		t.Errorf("ledger rows under core/ws1 = %d, want 1", n)
	}
	var data string
	archive.db.QueryRow(`SELECT data FROM online_findings WHERE finding_id = 'f-ws1'`).Scan(&data)
	if !strings.Contains(data, `"core/ws1"`) {
		t.Errorf("finding data = %s, want the tenant core/ws1", data)
	}
	archive.db.QueryRow(`SELECT COUNT(*) FROM online_alerts WHERE json_extract(data, '$.key.tenant') = 'core/ws1'`).Scan(&n)
	if n != 1 {
		t.Errorf("alerts under core/ws1 = %d, want 1", n)
	}
	archive.db.QueryRow(`SELECT COUNT(*) FROM llm_calls WHERE tenant = 'ws2'`).Scan(&n)
	if n != 1 {
		t.Errorf("the other tenant's ledger row moved")
	}

	var spec string
	archive.db.QueryRow(`SELECT spec_json FROM runs WHERE run_id = 'run-ws1'`).Scan(&spec)
	if !strings.Contains(spec, `"core/ws1"`) || !strings.Contains(spec, `"keep":"me"`) {
		t.Errorf("run spec = %s, want the tenant moved and the other fields kept", spec)
	}
	archive.db.QueryRow(`SELECT spec_json FROM runs WHERE run_id = 'run-ws2'`).Scan(&spec)
	if !strings.Contains(spec, `"ws2"`) {
		t.Errorf("another tenant's run was touched: %s", spec)
	}

	// Run again: nothing is moved twice.
	again, err := store.MergeTenants("ws1", "core/ws1", false)
	if err != nil {
		t.Fatal(err)
	}
	if again.Sessions != 0 || again.Skipped != 0 || again.Calls != 0 {
		t.Errorf("second merge = %+v, want nothing left to move", again)
	}
}

func tenantKeysOf(s *SessionStore) []string {
	var out []string
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sess := range s.sessions {
		out = append(out, sessionTenants(sess)...)
	}
	return out
}

func TestMergeTenantsEndpointIsAdminOnlyAndTakesKeys(t *testing.T) {
	store := NewSessionStore(nil)
	names, _ := NewTenantNames(nil)
	_ = names.Set("ws1", "Workspace")
	h := adminMergeTenantsHandler(store, names, nil, nil)
	post := func(role RoleInfo, body string) int {
		req := withRoleInfoForTest(role)(httptest.NewRequest(http.MethodPost, "/api/admin/merge-tenants", strings.NewReader(body)))
		rec := httptest.NewRecorder()
		h(rec, req)
		return rec.Code
	}
	admin := RoleInfo{Role: RoleAdmin, IsAdmin: true}
	if code := post(RoleInfo{Role: RoleMember}, `{"from":"ws1","to":"core/ws1"}`); code != http.StatusForbidden {
		t.Errorf("a member merging = %d, want 403", code)
	}
	for _, bad := range []string{`{}`, `{"from":"ws1"}`, `{"from":"*","to":"core/ws1"}`, `{"from":"ws1","to":"core/*"}`, `{"from":"ws1","to":"ws1"}`, `{"from":"ws1","to":"core/a b"}`} {
		if code := post(admin, bad); code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", bad, code)
		}
	}
	if names.Name("ws1") != "Workspace" {
		t.Error("a refused request moved a name")
	}
	if code := post(admin, `{"from":"ws1","to":"core/ws1"}`); code != http.StatusOK {
		t.Errorf("admin merge = %d, want 200", code)
	}
	if names.Name("core/ws1") != "Workspace" || names.Name("ws1") != "" {
		t.Errorf("names after merge: core/ws1 = %q, ws1 = %q, want the name moved to the target", names.Name("core/ws1"), names.Name("ws1"))
	}
}
