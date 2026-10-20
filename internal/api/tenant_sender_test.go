package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

func scopedToTenants(tenants ...string) RoleInfo {
	return RoleInfo{Role: RoleViewer, Tenants: tenants}
}

// labelledTracesBody is one session of one agent whose spans carry the tenant label (when non-empty).
func labelledTracesBody(session, traceID, spanID, label string) map[string]any {
	attrs := []any{map[string]any{"key": "gen_ai.agent.name", "value": map[string]any{"stringValue": "bot"}}}
	if label != "" {
		attrs = append(attrs, map[string]any{"key": DefaultTenantAttr, "value": map[string]any{"stringValue": label}})
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

func TestTenantKeysAndPatterns(t *testing.T) {
	if tenantKey("core", "acme") != "core/acme" || tenantKey("core", "") != "core/default" || tenantKey("", "acme") != "acme" || tenantKey("", "") != "" {
		t.Fatal("tenantKey")
	}
	for _, tc := range []struct {
		pattern, key string
		want         bool
	}{
		{"*", "core/acme", true}, {"*", "acme", true},
		{"core/acme", "core/acme", true}, {"core/acme", "rc/acme", false}, {"core/acme", "core/acme2", false},
		{"core/*", "core/acme", true}, {"core/*", "core/a/b", true}, {"core/*", "core", false}, {"core/*", "corex/acme", false},
		{"acme", "acme", true}, {"acme", "core/acme", false},
	} {
		if got := tenantPatternMatches(tc.pattern, tc.key); got != tc.want {
			t.Errorf("tenantPatternMatches(%q, %q) = %v, want %v", tc.pattern, tc.key, got, tc.want)
		}
	}
	for _, bad := range []string{"core/", "Core/acme", "a b", "x|y"} {
		if _, err := ParseTenantList(bad); err == nil {
			t.Errorf("ParseTenantList(%q) accepted", bad)
		}
	}
	if l, err := ParseTenantList("*, core/*, core/acme ,legacy"); err != nil || len(l) != 4 {
		t.Errorf("ParseTenantList = %v, %v", l, err)
	}
}

func TestIngestForKeysTenantsBySenderAndIsolatesSessions(t *testing.T) {
	store := NewSessionStore(nil)

	// One sender sends several tenants; the same tenant name from two senders is two tenants.
	store.IngestFor("core", labelledTracesBody("a", "aaaa", "s1", "acme"))
	store.IngestFor("core", labelledTracesBody("b", "bbbb", "s2", "globex"))
	store.IngestFor("rc", labelledTracesBody("a", "cccc", "s3", "acme"))
	store.IngestFor("rc", labelledTracesBody("c", "dddd", "s4", ""))
	got := map[string][]string{}
	for _, s := range store.List() {
		got[s.SessionID] = s.Tenants
	}
	want := map[string]string{"core:acme:a": "core/acme", "core:globex:b": "core/globex", "rc:acme:a": "rc/acme", "rc:c": "rc/default"}
	if len(got) != len(want) {
		t.Fatalf("sessions = %v", got)
	}
	for id, key := range want {
		if len(got[id]) != 1 || got[id][0] != key {
			t.Errorf("%s tenants = %v, want [%s]", id, got[id], key)
		}
	}

	// Nobody else can append to a sender's session, by name or through the shared token.
	store.IngestFor("", labelledTracesBody("core:acme:a", "eeee", "s5", "acme"))
	store.IngestFor("rc", labelledTracesBody("core:acme:a", "ffff", "s6", "acme"))
	if sum, _ := store.Summary("core:acme:a"); sum.SpanCount != 1 {
		t.Fatalf("core:acme:a has %d spans, want 1: another sender wrote into it", sum.SpanCount)
	}

	// The shared token has no sender: its labels are used as they are.
	store.IngestFor("", labelledTracesBody("shared", "9999", "s7", "newron"))
	if sum, _ := store.Summary("shared"); len(sum.Tenants) != 1 || sum.Tenants[0] != "newron" {
		t.Fatalf("shared-token session tenants = %v, want [newron]", sum.Tenants)
	}
}

func TestTenantGrantsFilterSessionsByKeyAndWildcard(t *testing.T) {
	store := NewSessionStore(nil)
	store.IngestFor("core", labelledTracesBody("a", "aaaa", "s1", "acme"))
	store.IngestFor("core", labelledTracesBody("b", "bbbb", "s2", "globex"))
	store.IngestFor("rc", labelledTracesBody("a", "cccc", "s3", "acme"))
	store.IngestFor("", labelledTracesBody("plain", "dddd", "s4", "acme"))

	list := func(info RoleInfo) []string {
		req := withRoleInfoForTest(info)(httptest.NewRequest(http.MethodGet, "/api/streaming/sessions", nil))
		rec := httptest.NewRecorder()
		sessionsHandler(store)(rec, req)
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
	for name, tc := range map[string]struct {
		info RoleInfo
		want int
	}{
		"unrestricted":         {RoleInfo{Role: RoleMember}, 4},
		"admin":                {RoleInfo{Role: RoleAdmin, IsAdmin: true}, 4},
		"one key":              {scopedToTenants("core/acme"), 1},
		"same name, 2 senders": {scopedToTenants("core/acme", "rc/acme"), 2},
		"whole sender":         {scopedToTenants("core/*"), 2},
		"two senders":          {scopedToTenants("core/*", "rc/*"), 3},
		"legacy bare label":    {scopedToTenants("acme"), 1},
		"nothing":              {RoleInfo{Role: RoleViewer, Tenants: []string{}}, 0},
	} {
		if ids := list(tc.info); len(ids) != tc.want {
			t.Errorf("%s: sees %v, want %d", name, ids, tc.want)
		}
	}

	as := withRoleInfoForTest(scopedToTenants("core/acme"))
	for _, id := range []string{"core:globex:b", "rc:acme:a", "plain"} {
		rec := httptest.NewRecorder()
		sessionSpansHandler(store)(rec, as(httptest.NewRequest(http.MethodGet, "/api/streaming/session-spans?session_id="+id, nil)))
		if rec.Code != http.StatusNotFound {
			t.Errorf("spans of %s for a core/acme user = %d, want 404", id, rec.Code)
		}
		rec = httptest.NewRecorder()
		getTraceHandler(store)(rec, as(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"session_id":"`+id+`"}`))))
		if rec.Code != http.StatusNotFound {
			t.Errorf("get-trace of %s for a core/acme user = %d, want 404", id, rec.Code)
		}
	}

	both := RoleInfo{Role: RoleViewer, Agents: []string{"other"}, Tenants: []string{"core/acme"}}
	if both.AllowsSession([]string{"bot"}, []string{"core/acme"}) || !both.AllowsSession([]string{"other"}, []string{"core/acme"}) {
		t.Error("a role scoped to an agent and a tenant must satisfy both")
	}
}

func TestSwitcherNarrowsButNeverWidens(t *testing.T) {
	for _, tc := range []struct {
		allowed []string
		want    string
		out     []string
		outNil  bool
	}{
		{nil, "", nil, true},
		{nil, "*", nil, true},
		{nil, "core/acme", []string{"core/acme"}, false},
		{[]string{"core/*"}, "", []string{"core/*"}, false},
		{[]string{"core/*"}, "core/acme", []string{"core/acme"}, false},
		{[]string{"core/*"}, "core/*", []string{"core/*"}, false},
		{[]string{"core/acme"}, "core/globex", []string{}, false},
		{[]string{"core/acme"}, "core/*", []string{"core/acme"}, false},
		{[]string{"rc/*"}, "core/*", []string{}, false},
		{[]string{"core/acme", "rc/*"}, "rc/x", []string{"rc/x"}, false},
	} {
		got := narrowTenants(tc.allowed, tc.want)
		if tc.outNil != (got == nil) || strings.Join(got, ",") != strings.Join(tc.out, ",") {
			t.Errorf("narrowTenants(%v, %q) = %v, want %v", tc.allowed, tc.want, got, tc.out)
		}
	}
}

func TestTenantsEndpointOffersOnlyWhatTheRoleCovers(t *testing.T) {
	store := NewSessionStore(nil)
	store.IngestFor("core", labelledTracesBody("a", "aaaa", "s1", "acme"))
	store.IngestFor("core", labelledTracesBody("b", "bbbb", "s2", "globex"))
	store.IngestFor("rc", labelledTracesBody("a", "cccc", "s3", "acme"))
	keys := func(info RoleInfo) string {
		rec := httptest.NewRecorder()
		tenantsHandler(store, nil)(rec, withRoleInfoForTest(info)(httptest.NewRequest(http.MethodGet, "/api/tenants", nil)))
		var out struct{ Data []tenantOption }
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		var ks []string
		for _, o := range out.Data {
			ks = append(ks, o.Key)
		}
		return strings.Join(ks, ",")
	}
	if got := keys(RoleInfo{Role: RoleAdmin, IsAdmin: true}); got != "core/acme,core/globex,rc/acme" {
		t.Errorf("admin is offered %q", got)
	}
	if got := keys(scopedToTenants("core/*")); got != "core/acme,core/globex" {
		t.Errorf("a core/* user is offered %q", got)
	}
	if got := keys(scopedToTenants("rc/acme")); got != "rc/acme" {
		t.Errorf("an rc/acme user is offered %q", got)
	}
	o := newTenantOption("core/acme", 2)
	if o.Sender != "core" || o.Label != "acme" {
		t.Errorf("option = %+v", o)
	}
}

func TestTenantScopedRoleCannotUseUntenantedViews(t *testing.T) {
	as := withRoleInfoForTest(scopedToTenants("core/acme"))
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
	if scopedToTenants("core/acme").CanWrite() {
		t.Error("a tenant-scoped role must not create runs or eval sets that other tenants could see")
	}
	if (RoleInfo{Role: RoleNone}).CanWrite() || (RoleInfo{Role: RoleViewer}).CanWrite() || !(RoleInfo{Role: RoleMember}).CanWrite() {
		t.Error("only admin and member may write")
	}
	if !runVisibleForRole(runDTO{}, RoleInfo{Role: RoleMember}) || runVisibleForRole(runDTO{Summary: &runSummaryDTO{Agents: []string{"a"}}}, scopedToTenants("core/acme")) {
		t.Error("runs are hidden from tenant-scoped roles only")
	}
	items := filterByTenant([]string{"core/acme", "core/globex", "rc/acme", ""}, scopedToTenants("core/acme", "rc/*"), func(s string) string { return s })
	if len(items) != 2 || items[0] != "core/acme" || items[1] != "rc/acme" {
		t.Errorf("filterByTenant = %v", items)
	}
}

func TestUsageIsFilteredByTenantKeyAndSender(t *testing.T) {
	archive, err := NewSQLiteStore(t.TempDir() + "/u.db")
	if err != nil {
		t.Fatal(err)
	}
	rows := []usageRow{
		{SpanID: "a", SessionID: "s1", StartMs: time.Now().UnixMilli(), Tenant: "core/acme", Model: "m", InputTokens: 10},
		{SpanID: "b", SessionID: "s2", StartMs: time.Now().UnixMilli(), Tenant: "rc/acme", Model: "m", InputTokens: 20},
		{SpanID: "c", SessionID: "s3", StartMs: time.Now().UnixMilli(), Tenant: "core/globex", Model: "m", InputTokens: 30},
		{SpanID: "d", SessionID: "s4", StartMs: time.Now().UnixMilli(), Tenant: "", Model: "m", InputTokens: 40},
		{SpanID: "e", SessionID: "s5", StartMs: time.Now().UnixMilli(), Tenant: "co_re/x", Model: "m", InputTokens: 50},
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
	for name, tc := range map[string]struct {
		tenants []string
		want    int
	}{
		"unrestricted":    {nil, 5},
		"star":            {[]string{"*"}, 5},
		"one key":         {[]string{"core/acme"}, 1},
		"same name":       {[]string{"core/acme", "rc/acme"}, 2},
		"whole sender":    {[]string{"core/*"}, 2},
		"underscore only": {[]string{"co_re/*"}, 1},
		"nothing":         {[]string{}, 0},
		"injection":       {[]string{"x') OR 1=1 --"}, 0},
	} {
		if n := count(tc.tenants); n != tc.want {
			t.Errorf("%s: %d rows, want %d", name, n, tc.want)
		}
	}
}

func TestHubFiltersByTenantKey(t *testing.T) {
	hub := newSSEHub()
	c := hub.registerFor(scopedToTenants("core/*"))
	defer hub.unregister(c)
	hub.broadcastSession("a", "core:acme:a", []string{"bot"}, []string{"core/acme"}, true)
	hub.broadcastSession("g", "rc:a", []string{"bot"}, []string{"rc/acme"}, true)
	hub.broadcastSession("n", "plain", []string{"bot"}, nil, true)
	if len(c) != 1 || <-c != "a" {
		t.Fatal("a core/* client must receive only core's events")
	}
}

func TestDefaultRoleNoneAndWildcardGrants(t *testing.T) {
	none, err := ParseDefaultRole("none")
	if err != nil || none != RoleNone {
		t.Fatalf("ParseDefaultRole(none) = %v, %v", none, err)
	}
	if _, err := ParseRole("none"); err == nil {
		t.Fatal("none must not be accepted in a binding")
	}
	s, _ := NewRoleStore(nil, RoleNone, nil)
	s.Upsert(RoleBinding{SubjectType: "team", Subject: "o/admins", Role: RoleMember, Tenants: []string{"*"}})
	s.Upsert(RoleBinding{SubjectType: "team", Subject: "o/core", Role: RoleViewer, Tenants: []string{"core/*"}})
	s.Upsert(RoleBinding{SubjectType: "team", Subject: "o/acme", Role: RoleViewer, Tenants: []string{"core/acme", "rc/acme"}})

	if info := s.EffectiveRole("stranger", nil, true); info.Role != RoleNone || !info.TenantScoped() || len(info.Tenants) != 0 {
		t.Errorf("a user no binding covers got %+v, want no access", info)
	}
	if info := s.EffectiveRole("a", []string{"o/admins"}, true); info.TenantScoped() || info.Role != RoleMember {
		t.Errorf("a team bound to * must be unrestricted, got %+v", info)
	}
	if info := s.EffectiveRole("c", []string{"o/core"}, true); len(info.Tenants) != 1 || info.Tenants[0] != "core/*" {
		t.Errorf("core team got %v", info.Tenants)
	}
	if info := s.EffectiveRole("x", []string{"o/core", "o/acme"}, true); len(info.Tenants) != 3 {
		t.Errorf("two teams must union their tenants, got %v", info.Tenants)
	}
	if info := s.EffectiveRole("x", []string{"o/core", "o/admins"}, true); info.TenantScoped() {
		t.Errorf("one team on * must make the union unrestricted, got %v", info.Tenants)
	}

	called := false
	h := withRole(s, "", nil, func(http.ResponseWriter, *http.Request) { called = true })
	req := httptest.NewRequest(http.MethodGet, "/api/runs", nil)
	req = req.WithContext(withIdentity(req.Context(), identity{Username: "stranger", Kind: tokenKindSession}))
	rec := httptest.NewRecorder()
	h(rec, req)
	if called || rec.Code != http.StatusForbidden {
		t.Fatalf("a user with no binding: called=%v status=%d, want 403 and no handler", called, rec.Code)
	}
}

func TestEffectiveRoleUnresolvedSeesNothing(t *testing.T) {
	s, _ := NewRoleStore(nil, RoleMember, nil)
	s.Upsert(RoleBinding{SubjectType: "team", Subject: "o/a", Role: RoleViewer, Tenants: []string{"core/*"}})
	if info := s.EffectiveRole("x", nil, false); !info.Unresolved || !info.TenantScoped() || len(info.Tenants) != 0 {
		t.Fatalf("an unresolved role must see no tenant, got %+v", info)
	}
}

func TestRoleBindingTenantsPersist(t *testing.T) {
	archive, err := NewSQLiteStore(t.TempDir() + "/r.db")
	if err != nil {
		t.Fatal(err)
	}
	s1, _ := NewRoleStore(archive, RoleMember, nil)
	if _, err := s1.Upsert(RoleBinding{SubjectType: "user", Subject: "bob", Role: RoleViewer, Tenants: []string{"core/acme"}}); err != nil {
		t.Fatal(err)
	}
	s2, err := NewRoleStore(archive, RoleMember, nil)
	if err != nil {
		t.Fatal(err)
	}
	if info := s2.EffectiveRole("bob", nil, true); len(info.Tenants) != 1 || info.Tenants[0] != "core/acme" {
		t.Fatalf("after reload bob's tenants = %v, want [core/acme]", info.Tenants)
	}
}

func TestSenderStoreAndAdminAPI(t *testing.T) {
	archive, err := NewSQLiteStore(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	ss, _ := NewSenderStore(archive)
	admin := withRoleInfoForTest(RoleInfo{Role: RoleAdmin, IsAdmin: true})
	member := withRoleInfoForTest(RoleInfo{Role: RoleMember})

	post := func(path string, body string, wrap func(*http.Request) *http.Request, h http.HandlerFunc, id string) *httptest.ResponseRecorder {
		req := wrap(httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
		req.SetPathValue("id", id)
		rec := httptest.NewRecorder()
		h(rec, req)
		return rec
	}
	if rec := post("/api/admin/senders", `{"id":"core"}`, member, adminSendersHandler(ss), ""); rec.Code != http.StatusForbidden {
		t.Fatalf("a member creating a sender = %d, want 403", rec.Code)
	}
	for _, bad := range []string{`{"id":"Core"}`, `{"id":"a b"}`, `{"id":""}`, `{"id":"-x"}`} {
		if rec := post("/api/admin/senders", bad, admin, adminSendersHandler(ss), ""); rec.Code != http.StatusBadRequest {
			t.Errorf("sender %s = %d, want 400", bad, rec.Code)
		}
	}
	if rec := post("/api/admin/senders", `{"id":"core","name":"production core"}`, admin, adminSendersHandler(ss), ""); rec.Code != http.StatusOK {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	if rec := post("/api/admin/senders", `{"id":"core"}`, admin, adminSendersHandler(ss), ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("a duplicate sender = %d, want 400", rec.Code)
	}

	rec := post("/api/admin/senders/core/tokens", `{"label":"exporter"}`, admin, adminSenderTokensHandler(ss), "core")
	var minted struct {
		Data struct {
			Token string      `json:"token"`
			Meta  SenderToken `json:"meta"`
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &minted); err != nil || !strings.HasPrefix(minted.Data.Token, "egs_") {
		t.Fatalf("mint = %d %s", rec.Code, rec.Body)
	}
	if rec := post("/api/admin/senders/nope/tokens", `{}`, admin, adminSenderTokensHandler(ss), "nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("a token for a missing sender = %d, want 404", rec.Code)
	}
	if b, _ := json.Marshal(ss.Tokens("core")); strings.Contains(string(b), minted.Data.Token) {
		t.Fatal("the token itself must not be listed")
	}

	again, err := NewSenderStore(archive)
	if err != nil {
		t.Fatal(err)
	}
	if sender, ok := again.Resolve(minted.Data.Token); !ok || sender != "core" {
		t.Fatalf("after reload the token resolves to %q, %v", sender, ok)
	}
	var stored string
	_ = archive.db.QueryRow(`SELECT hash FROM sender_tokens`).Scan(&stored)
	if stored == minted.Data.Token || stored == "" {
		t.Fatal("the token must be stored hashed")
	}

	req := admin(httptest.NewRequest(http.MethodDelete, "/", nil))
	req.SetPathValue("id", "core")
	req.SetPathValue("tokenId", minted.Data.Meta.ID)
	rec = httptest.NewRecorder()
	adminSenderTokenByIDHandler(again)(rec, req)
	if _, ok := again.Resolve(minted.Data.Token); ok || rec.Code != http.StatusOK {
		t.Fatalf("a revoked token still resolves (%d)", rec.Code)
	}
	tok2, _, _ := again.NewToken("core", "")
	req = admin(httptest.NewRequest(http.MethodDelete, "/", nil))
	req.SetPathValue("id", "core")
	adminSenderByIDHandler(again)(httptest.NewRecorder(), req)
	if _, ok := again.Resolve(tok2); ok || again.HasTokens() {
		t.Fatal("deleting a sender must remove its tokens")
	}
}

// A database written by the release that still called senders "tenants" keeps its rows.
func TestSendersMigrateFromTenantTables(t *testing.T) {
	path := t.TempDir() + "/m.db"
	first, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`DROP TABLE senders`, `DROP TABLE sender_tokens`,
		`CREATE TABLE tenants (id TEXT PRIMARY KEY, name TEXT NOT NULL, created_at TEXT NOT NULL)`,
		`CREATE TABLE tenant_tokens (id TEXT PRIMARY KEY, tenant TEXT NOT NULL, label TEXT NOT NULL, hash TEXT NOT NULL UNIQUE, created_at TEXT NOT NULL)`,
		`INSERT INTO tenants VALUES ('triage', 'core triage', '2026-10-20T00:00:00Z')`,
		`INSERT INTO tenant_tokens VALUES ('tt1', 'triage', 'exporter', '` + hashSenderToken("egs_old") + `', '2026-10-20T00:00:00Z')`,
	} {
		if _, err := first.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	first.db.Close()

	second, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ss, err := NewSenderStore(second)
	if err != nil {
		t.Fatal(err)
	}
	if sender, ok := ss.Resolve("egs_old"); !ok || sender != "triage" || !ss.Exists("triage") {
		t.Fatalf("migrated token resolves to %q, %v", sender, ok)
	}
}

// One sender can send the same session name for two tenants; they must not end up in one session, or a user of one
// tenant would read the other's spans.
func TestSameSessionNameForTwoTenantsOfOneSender(t *testing.T) {
	store := NewSessionStore(nil)
	store.IngestFor("core", labelledTracesBody("chat", "aaaa", "s1", "acme"))
	store.IngestFor("core", labelledTracesBody("chat", "bbbb", "s2", "globex"))
	list := store.List()
	if len(list) != 2 {
		t.Fatalf("got %d sessions, want 2", len(list))
	}
	for _, s := range list {
		if len(s.Tenants) != 1 {
			t.Errorf("%s mixes tenants %v", s.SessionID, s.Tenants)
		}
	}
}

func TestNarrowTenantsSenderWildcard(t *testing.T) {
	for _, tc := range []struct {
		allowed []string
		want    string
		out     string
	}{
		{nil, "core/", "core/*"},
		{nil, "core/*", "core/*"},
		{[]string{"*"}, "core/", "core/*"},
		{[]string{"core/*"}, "core/", "core/*"},
		{[]string{"core/acme", "core/globex", "rc/x"}, "core/", "core/acme,core/globex"},
		{[]string{"rc/*"}, "core/", ""},
	} {
		got := narrowTenants(tc.allowed, tc.want)
		if got == nil || strings.Join(got, ",") != tc.out {
			t.Errorf("narrowTenants(%v, %q) = %v, want %q", tc.allowed, tc.want, got, tc.out)
		}
	}
}

func TestUnlabelledSpansDoNotAddADefaultTenant(t *testing.T) {
	spans := func(labels ...string) []*tracepkg.Span {
		var out []*tracepkg.Span
		for _, l := range labels {
			sp := &tracepkg.Span{Tags: map[string]any{}}
			if l != "" {
				sp.Tags[DefaultTenantAttr] = l
			}
			out = append(out, sp)
		}
		return out
	}
	for name, tc := range map[string]struct {
		sender string
		spans  []*tracepkg.Span
		want   string
	}{
		"labelled and unlabelled": {"rc", spans("ws-1", "", ""), "rc/ws-1"},
		"two labels":              {"rc", spans("a", "", "b"), "rc/a,rc/b"},
		"no label at all":         {"rc", spans("", ""), "rc/default"},
		"shared sender, no label": {"", spans("", ""), ""},
		"no spans":                {"rc", nil, ""},
	} {
		got := strings.Join(sessionTenants(&Session{Sender: tc.sender, Spans: tc.spans}), ",")
		if got != tc.want {
			t.Errorf("%s: tenants = %q, want %q", name, got, tc.want)
		}
	}
}
