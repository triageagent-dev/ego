package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func scopedToTenants(tenants ...string) RoleInfo {
	return RoleInfo{Role: RoleViewer, Tenants: tenants}
}

// tenantTracesBody is one session of one agent; labelTenant is the tenant attribute the sender writes itself.
func tenantTracesBody(session, traceID, spanID, labelTenant string) map[string]any {
	attrs := []any{map[string]any{"key": "gen_ai.agent.name", "value": map[string]any{"stringValue": "bot"}}}
	if labelTenant != "" {
		attrs = append(attrs, map[string]any{"key": DefaultTenantAttr, "value": map[string]any{"stringValue": labelTenant}})
	}
	return map[string]any{"resourceSpans": []any{map[string]any{
		"resource": map[string]any{"attributes": []any{
			map[string]any{"key": sessionNameAttr, "value": map[string]any{"stringValue": session}},
		}},
		"scopeSpans": []any{map[string]any{"spans": []any{map[string]any{
			"traceId": traceID, "spanId": spanID, "name": "agent.run",
			"startTimeUnixNano": "1000000000", "endTimeUnixNano": "1010000000", "attributes": attrs,
		}}}},
	}}}
}

func TestIngestForStampsTenantAndIsolatesSessions(t *testing.T) {
	store := NewSessionStore(nil)

	// A tenant sender cannot claim another tenant, and its session id carries its tenant.
	store.IngestFor("acme", tenantTracesBody("chat", "aaaa", "s1", "globex"))
	store.IngestFor("globex", tenantTracesBody("chat", "bbbb", "s2", ""))
	got := map[string][]string{}
	for _, s := range store.List() {
		got[s.SessionID] = s.Tenants
	}
	if len(got) != 2 || got["acme:chat"][0] != "acme" || got["globex:chat"][0] != "globex" {
		t.Fatalf("sessions = %v, want acme:chat and globex:chat each with its own tenant", got)
	}
	if sp, _ := store.SpanRows("acme:chat"); sp[0].Tags[DefaultTenantAttr] != "acme" {
		t.Fatalf("the sender's own tenant attribute survived: %v", sp[0].Tags[DefaultTenantAttr])
	}

	// Nobody else can append to a tenant's session, by name or through an untenanted sender.
	store.IngestFor("", tenantTracesBody("acme:chat", "cccc", "s3", ""))
	store.IngestFor("globex", tenantTracesBody("acme:chat", "dddd", "s4", ""))
	if sum, _ := store.Summary("acme:chat"); sum.SpanCount != 1 {
		t.Fatalf("acme:chat has %d spans, want 1: another sender wrote into it", sum.SpanCount)
	}

	// An untenanted sender keeps the tenant attribute it writes (the shared-token sender).
	store.IngestFor("", tenantTracesBody("shared", "eeee", "s5", "newron"))
	if sum, _ := store.Summary("shared"); len(sum.Tenants) != 1 || sum.Tenants[0] != "newron" {
		t.Fatalf("shared-token session tenants = %v, want [newron]", sum.Tenants)
	}
}

func TestTenantScopedRoleSeesOnlyItsTenants(t *testing.T) {
	store := NewSessionStore(nil)
	store.IngestFor("acme", tenantTracesBody("chat", "aaaa", "s1", ""))
	store.IngestFor("globex", tenantTracesBody("chat", "bbbb", "s2", ""))
	store.IngestFor("", tenantTracesBody("plain", "cccc", "s3", ""))
	as := withRoleInfoForTest(scopedToTenants("acme"))

	list := func(mutate func(*http.Request) *http.Request) []string {
		rec := httptest.NewRecorder()
		sessionsHandler(store)(rec, mutate(httptest.NewRequest(http.MethodGet, "/api/streaming/sessions", nil)))
		var out struct{ Data []SessionSummary }
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, s := range out.Data {
			ids = append(ids, s.SessionID)
		}
		return ids
	}
	if ids := list(as); len(ids) != 1 || ids[0] != "acme:chat" {
		t.Fatalf("acme user sees %v, want only acme:chat", ids)
	}
	if ids := list(func(r *http.Request) *http.Request { return r }); len(ids) != 3 {
		t.Fatalf("an unrestricted role sees %v, want all three", ids)
	}
	if ids := list(withRoleInfoForTest(RoleInfo{Role: RoleAdmin, IsAdmin: true})); len(ids) != 3 {
		t.Fatalf("admin sees %v, want all three", ids)
	}
	if ids := list(withRoleInfoForTest(scopedToTenants("acme", "globex"))); len(ids) != 2 {
		t.Fatalf("a two-tenant user sees %v, want two", ids)
	}

	for _, id := range []string{"globex:chat", "plain"} {
		rec := httptest.NewRecorder()
		sessionSpansHandler(store)(rec, as(httptest.NewRequest(http.MethodGet, "/api/streaming/session-spans?session_id="+id, nil)))
		if rec.Code != http.StatusNotFound {
			t.Errorf("spans of %s for an acme user = %d, want 404", id, rec.Code)
		}
		rec = httptest.NewRecorder()
		getTraceHandler(store)(rec, as(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"session_id":"`+id+`"}`))))
		if rec.Code != http.StatusNotFound {
			t.Errorf("get-trace of %s for an acme user = %d, want 404", id, rec.Code)
		}
	}

	// Agent and tenant scopes both have to allow.
	both := RoleInfo{Role: RoleViewer, Agents: []string{"other"}, Tenants: []string{"acme"}}
	if both.AllowsSession([]string{"bot"}, []string{"acme"}) || !both.AllowsSession([]string{"other"}, []string{"acme"}) {
		t.Error("a role scoped to an agent and a tenant must satisfy both")
	}
}

func TestTenantScopedRoleCannotUseUntenantedViews(t *testing.T) {
	as := withRoleInfoForTest(scopedToTenants("acme"))
	for name, h := range map[string]http.HandlerFunc{
		"eval sets":    listEvalSetsHandler(nil),
		"eval set":     getEvalSetHandler(nil),
		"online state": onlineStatusHandler(nil),
	} {
		rec := httptest.NewRecorder()
		h(rec, as(httptest.NewRequest(http.MethodGet, "/", nil)))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s for a tenant-scoped role = %d, want 403", name, rec.Code)
		}
	}
	if scopedToTenants("acme").CanWrite() {
		t.Error("a tenant-scoped role must not create runs or eval sets that other tenants could see")
	}
	if !runVisibleForRole(runDTO{}, RoleInfo{Role: RoleMember}) || runVisibleForRole(runDTO{Summary: &runSummaryDTO{Agents: []string{"a"}}}, scopedToTenants("acme")) {
		t.Error("runs are hidden from tenant-scoped roles only")
	}
	items := filterByTenant([]string{"acme", "globex", ""}, scopedToTenants("acme"), func(s string) string { return s })
	if len(items) != 1 || items[0] != "acme" {
		t.Errorf("filterByTenant = %v, want [acme]", items)
	}
}

func TestUsageIsFilteredByTenant(t *testing.T) {
	archive, err := NewSQLiteStore(t.TempDir() + "/u.db")
	if err != nil {
		t.Fatal(err)
	}
	rows := []usageRow{
		{SpanID: "a", SessionID: "s1", StartMs: time.Now().UnixMilli(), Tenant: "acme", Model: "m", InputTokens: 10},
		{SpanID: "b", SessionID: "s2", StartMs: time.Now().UnixMilli(), Tenant: "globex", Model: "m", InputTokens: 20},
		{SpanID: "c", SessionID: "s3", StartMs: time.Now().UnixMilli(), Tenant: "", Model: "m", InputTokens: 30},
	}
	if err := archive.InsertUsage(rows); err != nil {
		t.Fatal(err)
	}
	from, to := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	count := func(tenants []string) int {
		calls, err := archive.TopUsageCalls(from, to, 10, tenants)
		if err != nil {
			t.Fatal(err)
		}
		sessions, _ := archive.TopUsageSessions(from, to, 10, tenants)
		series, _ := archive.UsageSeries(from, to, int64(time.Hour/time.Millisecond), tenants)
		if len(calls) != len(sessions) || len(calls) != len(series) {
			t.Fatalf("calls %d, sessions %d, series %d disagree for %v", len(calls), len(sessions), len(series), tenants)
		}
		return len(calls)
	}
	if n := count(nil); n != 3 {
		t.Errorf("unrestricted: %d rows, want 3", n)
	}
	if n := count([]string{"acme"}); n != 1 {
		t.Errorf("acme: %d rows, want 1", n)
	}
	if n := count([]string{"acme", "globex"}); n != 2 {
		t.Errorf("acme+globex: %d rows, want 2", n)
	}
	if n := count([]string{}); n != 0 {
		t.Errorf("no tenants: %d rows, want 0", n)
	}
	if n := count([]string{"x') OR 1=1 --"}); n != 0 {
		t.Errorf("an injection attempt matched %d rows", n)
	}
}

func TestHubFiltersByTenant(t *testing.T) {
	hub := newSSEHub()
	acme := hub.registerFor(scopedToTenants("acme"))
	defer hub.unregister(acme)
	hub.broadcastSession("a", "acme:chat", []string{"bot"}, []string{"acme"}, true)
	hub.broadcastSession("g", "globex:chat", []string{"bot"}, []string{"globex"}, true)
	hub.broadcastSession("n", "plain", []string{"bot"}, nil, true)
	if len(acme) != 1 || <-acme != "a" {
		t.Fatal("an acme client must receive only acme's events")
	}
}

func TestEffectiveRoleUnionsTenants(t *testing.T) {
	s, _ := NewRoleStore(nil, RoleMember, nil)
	s.Upsert(RoleBinding{SubjectType: "team", Subject: "o/a", Role: RoleViewer, Tenants: []string{"acme"}})
	s.Upsert(RoleBinding{SubjectType: "team", Subject: "o/b", Role: RoleViewer, Tenants: []string{"globex"}})
	info := s.EffectiveRole("u", []string{"o/a", "o/b"}, true)
	if len(info.Tenants) != 2 || info.Tenants[0] != "acme" || info.Tenants[1] != "globex" {
		t.Fatalf("tenants = %v, want [acme globex]", info.Tenants)
	}
	s.Upsert(RoleBinding{SubjectType: "team", Subject: "o/c", Role: RoleViewer})
	if info := s.EffectiveRole("u", []string{"o/a", "o/c"}, true); info.TenantScoped() {
		t.Fatalf("a binding without tenants is unrestricted, got %v", info.Tenants)
	}
	if info := s.EffectiveRole("x", nil, false); !info.TenantScoped() || len(info.Tenants) != 0 {
		t.Fatalf("an unresolved role must see no tenant, got %+v", info)
	}
}

func TestRoleBindingTenantsPersist(t *testing.T) {
	archive, err := NewSQLiteStore(t.TempDir() + "/r.db")
	if err != nil {
		t.Fatal(err)
	}
	s1, _ := NewRoleStore(archive, RoleMember, nil)
	if _, err := s1.Upsert(RoleBinding{SubjectType: "user", Subject: "bob", Role: RoleViewer, Tenants: []string{"acme"}}); err != nil {
		t.Fatal(err)
	}
	s2, err := NewRoleStore(archive, RoleMember, nil)
	if err != nil {
		t.Fatal(err)
	}
	if info := s2.EffectiveRole("bob", nil, true); len(info.Tenants) != 1 || info.Tenants[0] != "acme" {
		t.Fatalf("after reload bob's tenants = %v, want [acme]", info.Tenants)
	}
}

func TestTenantStoreAndAdminAPI(t *testing.T) {
	archive, err := NewSQLiteStore(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	ts, _ := NewTenantStore(archive)
	admin := withRoleInfoForTest(RoleInfo{Role: RoleAdmin, IsAdmin: true})
	member := withRoleInfoForTest(RoleInfo{Role: RoleMember})

	post := func(path string, body string, wrap func(*http.Request) *http.Request, h http.HandlerFunc, id string) *httptest.ResponseRecorder {
		req := wrap(httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
		req.SetPathValue("id", id)
		rec := httptest.NewRecorder()
		h(rec, req)
		return rec
	}
	if rec := post("/api/admin/tenants", `{"id":"acme"}`, member, adminTenantsHandler(ts), ""); rec.Code != http.StatusForbidden {
		t.Fatalf("a member creating a tenant = %d, want 403", rec.Code)
	}
	for _, bad := range []string{`{"id":"Acme"}`, `{"id":"a b"}`, `{"id":""}`, `{"id":"-x"}`} {
		if rec := post("/api/admin/tenants", bad, admin, adminTenantsHandler(ts), ""); rec.Code != http.StatusBadRequest {
			t.Errorf("tenant %s = %d, want 400", bad, rec.Code)
		}
	}
	if rec := post("/api/admin/tenants", `{"id":"acme","name":"Acme Ltd"}`, admin, adminTenantsHandler(ts), ""); rec.Code != http.StatusOK {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	if rec := post("/api/admin/tenants", `{"id":"acme"}`, admin, adminTenantsHandler(ts), ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("a duplicate tenant = %d, want 400", rec.Code)
	}

	rec := post("/api/admin/tenants/acme/tokens", `{"label":"ci"}`, admin, adminTenantTokensHandler(ts), "acme")
	var minted struct {
		Data struct {
			Token string      `json:"token"`
			Meta  TenantToken `json:"meta"`
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &minted); err != nil || !strings.HasPrefix(minted.Data.Token, "egt_") {
		t.Fatalf("mint = %d %s", rec.Code, rec.Body)
	}
	if rec := post("/api/admin/tenants/nope/tokens", `{}`, admin, adminTenantTokensHandler(ts), "nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("a token for a missing tenant = %d, want 404", rec.Code)
	}
	if strings.Contains(string(mustJSON(t, ts.Tokens("acme"))), minted.Data.Token) {
		t.Fatal("the token itself must not be listed")
	}

	// Survives a restart, and only the hash is stored.
	again, err := NewTenantStore(archive)
	if err != nil {
		t.Fatal(err)
	}
	if tenant, ok := again.Resolve(minted.Data.Token); !ok || tenant != "acme" {
		t.Fatalf("after reload the token resolves to %q, %v", tenant, ok)
	}
	var stored string
	_ = archive.db.QueryRow(`SELECT hash FROM tenant_tokens`).Scan(&stored)
	if stored == minted.Data.Token || stored == "" {
		t.Fatal("the token must be stored hashed")
	}

	// Revoking stops it; deleting the tenant removes the rest.
	req := admin(httptest.NewRequest(http.MethodDelete, "/", nil))
	req.SetPathValue("id", "acme")
	req.SetPathValue("tokenId", minted.Data.Meta.ID)
	rec = httptest.NewRecorder()
	adminTenantTokenByIDHandler(again)(rec, req)
	if _, ok := again.Resolve(minted.Data.Token); ok || rec.Code != http.StatusOK {
		t.Fatalf("a revoked token still resolves (%d)", rec.Code)
	}
	tok2, _, _ := again.NewToken("acme", "")
	req = admin(httptest.NewRequest(http.MethodDelete, "/", nil))
	req.SetPathValue("id", "acme")
	adminTenantByIDHandler(again)(httptest.NewRecorder(), req)
	if _, ok := again.Resolve(tok2); ok || again.HasTokens() {
		t.Fatal("deleting a tenant must remove its tokens")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
