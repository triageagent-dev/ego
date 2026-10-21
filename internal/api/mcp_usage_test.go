package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/triageagent-dev/ego/internal/mcptools"
)

// toolSessionBody is one export of one session: a root span of service, and an LLM span of agent that reported
// tokens, labelled with tenant; failed adds an ERROR span.
func toolSessionBody(name, traceID, service, agent, tenant string, in, out int, failed bool) map[string]any {
	attr := func(k string, v map[string]any) map[string]any { return map[string]any{"key": k, "value": v} }
	str := func(s string) map[string]any { return map[string]any{"stringValue": s} }
	num := func(n int) map[string]any { return map[string]any{"intValue": strconvI(int64(n))} }
	spans := []any{
		map[string]any{
			"traceId": traceID, "spanId": traceID + "-root", "name": "agent.run",
			"startTimeUnixNano": "1000000000", "endTimeUnixNano": "9000000000",
		},
		map[string]any{
			"traceId": traceID, "spanId": traceID + "-llm", "parentSpanId": traceID + "-root", "name": "llm.chat",
			"startTimeUnixNano": "2000000000", "endTimeUnixNano": "3000000000",
			"attributes": []any{
				attr("openinference.span.kind", str("LLM")),
				attr("llm.model_name", str("model-a")),
				attr("llm.token_count.prompt", num(in)),
				attr("llm.token_count.completion", num(out)),
				attr("tenant.id", str(tenant)),
				attr("gen_ai.agent.name", str(agent)),
			},
		},
	}
	if failed {
		spans = append(spans, map[string]any{
			"traceId": traceID, "spanId": traceID + "-err", "parentSpanId": traceID + "-root", "name": "tool.call",
			"startTimeUnixNano": "4000000000", "endTimeUnixNano": "5000000000",
			"status": map[string]any{"code": float64(2)},
		})
	}
	return map[string]any{"resourceSpans": []any{map[string]any{
		"resource": map[string]any{"attributes": []any{
			attr(sessionNameAttr, str(name)), attr("service.name", str(service)),
		}},
		"scopeSpans": []any{map[string]any{"spans": spans}},
	}}}
}

// toolFixture holds three sessions and a usage ledger across two senders' tenants:
//
//	old   core/acme  agent ops     checkout  3 days ago, complete, first user message "why does the pod restart"
//	live  core/acme  agent triage  cart      1 hour ago, open
//	bad   rc/acme    agent ops     checkout  2 hours ago, open, one ERROR span
type toolFixture struct {
	backend *mcpBackend
	store   *SessionStore
	ids     map[string]string // short name -> session id
}

func newToolFixture(t *testing.T) *toolFixture {
	t.Helper()
	archive, err := NewSQLiteStore(filepath.Join(t.TempDir(), "tools.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { archive.Close() })
	store := NewSessionStoreWithArchive(nil, archive, SessionLimits{UsageTenantAttr: "tenant.id"})
	f := &toolFixture{backend: newMCPBackend(store, archive, nil, nil), store: store, ids: map[string]string{}}

	now := time.Now()
	for _, s := range []struct {
		name, sender, service, agent, tenant string
		in, out                              int
		failed, complete                     bool
		age                                  time.Duration
	}{
		{"old", "core", "checkout", "ops", "acme", 700, 50, false, true, 72 * time.Hour},
		{"live", "core", "cart", "triage", "acme", 200, 20, false, false, time.Hour},
		{"bad", "rc", "checkout", "ops", "acme", 90, 10, true, false, 2 * time.Hour},
	} {
		store.IngestFor(s.sender, toolSessionBody(s.name, "trace-"+s.name+"0000000", s.service, s.agent, s.tenant, s.in, s.out, s.failed))
		var id string
		for _, sum := range store.List() {
			if strings.HasSuffix(sum.SessionID, s.name) {
				id = sum.SessionID
			}
		}
		if id == "" {
			t.Fatalf("session %s was not stored", s.name)
		}
		f.ids[s.name] = id
		// Ingest stamps the wall clock and completes sessions on timers; the fixture pins both.
		store.mu.Lock()
		sess := store.sessions[id]
		sess.StartedAt = now.Add(-s.age)
		sess.IsComplete = s.complete
		if s.complete {
			sess.Invocations = []InvocationDTO{{UserText: "Why does the pod restart?"}, {UserText: "second question"}}
		}
		store.mu.Unlock()
	}

	// The ledger: the same activity as rows, with a session that used two models.
	ms := now.Add(-time.Hour).UnixMilli()
	row := func(span, session, tenant, kind, model, service string, in, out int64, isErr bool) usageRow {
		return usageRow{SpanID: span, TraceID: "t-" + span, SessionID: session, StartMs: ms, DurationMs: 10, Service: service,
			Tenant: tenant, Kind: kind, Model: model, Name: "llm", InputTokens: in, OutputTokens: out, IsError: isErr}
	}
	if err := archive.InsertUsage([]usageRow{
		row("u1", "sess-1", "core/acme", "ops", "model-a", "checkout", 600, 100, false),
		row("u2", "sess-1", "core/acme", "ops", "model-b", "checkout", 300, 0, false),
		row("u3", "sess-2", "core/acme", "triage", "model-a", "cart", 100, 20, false),
		row("u4", "sess-3", "rc/acme", "ops", "model-a", "checkout", 5000, 500, true),
		row("u5", "sess-4", "rc/acme", "", "model-a", "", 10, 5, false),
		// Outside the window.
		{SpanID: "u6", TraceID: "t-u6", SessionID: "sess-old", StartMs: now.Add(-200 * time.Hour).UnixMilli(), Service: "x", Tenant: "core/acme",
			Kind: "ops", Model: "model-a", Name: "llm", InputTokens: 99999},
	}); err != nil {
		t.Fatal(err)
	}
	return f
}

func sessionNames(f *toolFixture, items []mcptools.SessionListItem) []string {
	var out []string
	for _, it := range items {
		for name, id := range f.ids {
			if id == it.SessionID {
				out = append(out, name)
			}
		}
	}
	slices.Sort(out)
	return out
}

func TestListSessions_FiltersAndEnrichment(t *testing.T) {
	f := newToolFixture(t)
	admin := asRole(RoleInfo{Role: RoleAdmin, IsAdmin: true})
	yes, no := true, false
	ago := func(d time.Duration) time.Time { return time.Now().Add(-d) }

	for name, tc := range map[string]struct {
		filter mcptools.SessionFilter
		want   []string
	}{
		"none":                            {mcptools.SessionFilter{}, []string{"bad", "live", "old"}},
		"agent is exact":                  {mcptools.SessionFilter{Agent: "ops"}, []string{"bad", "old"}},
		"agent is not a substring":        {mcptools.SessionFilter{Agent: "op"}, nil},
		"tenant key":                      {mcptools.SessionFilter{Tenant: "rc/acme"}, []string{"bad"}},
		"tenant sender wildcard":          {mcptools.SessionFilter{Tenant: "core/*"}, []string{"live", "old"}},
		"tenant sender short form":        {mcptools.SessionFilter{Tenant: "core/"}, []string{"live", "old"}},
		"since":                           {mcptools.SessionFilter{Since: ago(24 * time.Hour)}, []string{"bad", "live"}},
		"since one hour":                  {mcptools.SessionFilter{Since: ago(90 * time.Minute)}, []string{"live"}},
		"query on the service":            {mcptools.SessionFilter{Query: "CART"}, []string{"live"}},
		"query on an agent":               {mcptools.SessionFilter{Query: "Triage"}, []string{"live"}},
		"query on the session id":         {mcptools.SessionFilter{Query: "old"}, []string{"old"}},
		"query on the first user message": {mcptools.SessionFilter{Query: "POD restart"}, []string{"old"}},
		"query ignores later turns":       {mcptools.SessionFilter{Query: "second question"}, nil},
		"complete":                        {mcptools.SessionFilter{Complete: &yes}, []string{"old"}},
		"open":                            {mcptools.SessionFilter{Complete: &no}, []string{"bad", "live"}},
		"filters combine":                 {mcptools.SessionFilter{Agent: "ops", Complete: &no, Since: ago(24 * time.Hour)}, []string{"bad"}},
	} {
		got, err := f.backend.Sessions(admin, tc.filter)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if names := sessionNames(f, got); !slices.Equal(names, tc.want) {
			t.Errorf("%s: sessions = %v, want %v", name, names, tc.want)
		}
	}

	all, _ := f.backend.Sessions(admin, mcptools.SessionFilter{})
	for _, it := range all {
		if it.SessionID != f.ids["bad"] {
			continue
		}
		if it.Agent != "ops" || it.Tenant != "rc/acme" || it.Service != "checkout" || it.ErrorSpans != 1 || it.TotalTokens != 100 || it.SpanCount != 3 {
			t.Errorf("enriched item = %+v", it)
		}
	}
}

func TestListSessions_FilterNeverWidensRoleLimits(t *testing.T) {
	f := newToolFixture(t)
	core := asRole(RoleInfo{Role: RoleMember, Tenants: []string{"core/*"}})

	got, err := f.backend.Sessions(core, mcptools.SessionFilter{})
	if err != nil || !slices.Equal(sessionNames(f, got), []string{"live", "old"}) {
		t.Fatalf("tenant-limited sessions = %v, %v", sessionNames(f, got), err)
	}
	// Asking for the other sender's tenant, or a wildcard, never reaches its sessions.
	for name, flt := range map[string]mcptools.SessionFilter{
		"foreign tenant":  {Tenant: "rc/acme"},
		"wildcard":        {Tenant: "*"},
		"foreign by name": {Agent: "ops", Tenant: "rc/"},
		"foreign by text": {Query: "rc"},
	} {
		got, _ := f.backend.Sessions(core, flt)
		for _, it := range got {
			if it.SessionID == f.ids["bad"] {
				t.Errorf("%s: saw the other sender's session", name)
			}
		}
	}

	scoped := asRole(scopedTo("triage"))
	got, _ = f.backend.Sessions(scoped, mcptools.SessionFilter{Query: "checkout"})
	if len(got) != 0 {
		t.Errorf("agent-scoped role found %v by another agent's service", sessionNames(f, got))
	}
}

func TestUsage_TotalsBreakdownAndTopSessions(t *testing.T) {
	f := newToolFixture(t)
	admin := asRole(RoleInfo{Role: RoleAdmin, IsAdmin: true})

	u, err := f.backend.Usage(admin, mcptools.UsageQuery{Hours: 24, Group: mcptools.UsageByKind, Top: 10})
	if err != nil {
		t.Fatal(err)
	}
	if u.InputTokens != 6010 || u.OutputTokens != 625 || u.TotalTokens != 6635 || u.Calls != 5 || u.ErrorCalls != 1 {
		t.Errorf("totals = %+v (the row outside the window must not count)", u)
	}
	want := []mcptools.UsageGroup{
		{Key: "ops", InputTokens: 5900, OutputTokens: 600, Tokens: 6500, Calls: 3},
		{Key: "triage", InputTokens: 100, OutputTokens: 20, Tokens: 120, Calls: 1},
		{Key: "(none)", InputTokens: 10, OutputTokens: 5, Tokens: 15, Calls: 1},
	}
	if !slices.Equal(u.Breakdown, want) {
		t.Errorf("by kind = %+v, want %+v", u.Breakdown, want)
	}
	// Two models of one session are one entry, summed.
	if len(u.TopSessions) != 4 || u.TopSessions[0].SessionID != "sess-3" || u.TopSessions[1].SessionID != "sess-1" ||
		u.TopSessions[1].Tokens != 1000 || u.TopSessions[1].Calls != 2 || u.TopSessions[1].Agent != "ops" || u.TopSessions[1].Tenant != "core/acme" {
		t.Errorf("top sessions = %+v", u.TopSessions)
	}

	for group, first := range map[string]string{
		mcptools.UsageByTenant: "rc/acme", mcptools.UsageByModel: "model-a", mcptools.UsageByService: "checkout",
	} {
		g, err := f.backend.Usage(admin, mcptools.UsageQuery{Hours: 24, Group: group, Top: 1})
		if err != nil || g.Breakdown[0].Key != first || len(g.TopSessions) != 1 {
			t.Errorf("group %s: %+v, %v", group, g, err)
		}
	}

	narrow, _ := f.backend.Usage(admin, mcptools.UsageQuery{Hours: 24, Group: mcptools.UsageByKind, Top: 10, Tenant: "core/*"})
	if narrow.TotalTokens != 1120 || narrow.Calls != 3 {
		t.Errorf("tenant filter totals = %+v", narrow)
	}
	if long, _ := f.backend.Usage(admin, mcptools.UsageQuery{Hours: 2160, Group: mcptools.UsageByKind, Top: 10}); long.TotalTokens != 6635+99999 {
		t.Errorf("a long window should include the old row: %d", long.TotalTokens)
	}
}

func TestUsage_TenantAndAgentLimits(t *testing.T) {
	f := newToolFixture(t)
	core := asRole(RoleInfo{Role: RoleMember, Tenants: []string{"core/*"}})
	q := mcptools.UsageQuery{Hours: 24, Group: mcptools.UsageByTenant, Top: 10}

	u, err := f.backend.Usage(core, q)
	if err != nil || u.TotalTokens != 1120 || len(u.Breakdown) != 1 || u.Breakdown[0].Key != "core/acme" {
		t.Fatalf("limited usage = %+v, %v", u, err)
	}
	for _, s := range u.TopSessions {
		if s.Tenant != "core/acme" {
			t.Errorf("limited role saw session %+v", s)
		}
	}
	// Naming the other sender's tenant does not widen the limit.
	q.Tenant = "rc/acme"
	if u, err := f.backend.Usage(core, q); err != nil || u.TotalTokens != 0 || len(u.TopSessions) != 0 {
		t.Errorf("foreign tenant usage = %+v, %v", u, err)
	}
	if _, err := f.backend.Usage(asRole(scopedTo("ops")), mcptools.UsageQuery{Hours: 24, Group: "kind", Top: 1}); err == nil {
		t.Error("an agent-scoped role read the ledger")
	}
}

// The stdio command reads the same two tools through REST: what its client builds equals what the in-process
// backend returns, for every filter and under a tenant limit.
func TestRESTClientAgreesOnUsageAndSessions(t *testing.T) {
	f := newToolFixture(t)
	for _, info := range []RoleInfo{{Role: RoleAdmin, IsAdmin: true}, {Role: RoleMember, Tenants: []string{"core/*"}}} {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/streaming/sessions", sessionsHandler(f.store))
		mux.HandleFunc("/api/usage", usageHandler(f.backend.archive))
		mux.HandleFunc("/api/usage/calls", usageCallsHandler(f.backend.archive))
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mux.ServeHTTP(w, r.WithContext(withRoleInfo(r.Context(), info)))
		}))
		rest := mcptools.NewREST(srv.URL, "", srv.Client())
		ctx := asRole(info)
		yes := true

		for _, flt := range []mcptools.SessionFilter{
			{}, {Agent: "ops"}, {Tenant: "core/*"}, {Tenant: "rc/acme"}, {Since: time.Now().Add(-24 * time.Hour)},
			{Query: "pod restart"}, {Query: "checkout"}, {Complete: &yes}, {Agent: "ops", Query: "check", Tenant: "core/acme"},
		} {
			got, err := rest.Sessions(ctx, flt)
			if err != nil {
				t.Fatal(err)
			}
			want, _ := f.backend.Sessions(ctx, flt)
			if jg, jw := mustJSON(got), mustJSON(want); jg != jw {
				t.Errorf("sessions %+v differ:\n rest       %s\n in-process %s", flt, jg, jw)
			}
		}
		for _, q := range []mcptools.UsageQuery{
			{Hours: 24, Group: "kind", Top: 10}, {Hours: 24, Group: "model", Top: 2}, {Hours: 2160, Group: "service", Top: 10},
			{Hours: 24, Group: "tenant", Top: 10, Tenant: "core/*"}, {Hours: 24, Group: "kind", Top: 10, Tenant: "rc/acme"},
		} {
			got, err := rest.Usage(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			want, _ := f.backend.Usage(ctx, q)
			if jg, jw := mustJSON(got), mustJSON(want); jg != jw {
				t.Errorf("usage %+v differs:\n rest       %s\n in-process %s", q, jg, jw)
			}
		}
		srv.Close()
	}

	// An agent-scoped role is refused by the endpoint, as it is in process.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/usage", usageHandler(f.backend.archive))
	mux.HandleFunc("/api/usage/calls", usageCallsHandler(f.backend.archive))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(withRoleInfo(r.Context(), scopedTo("ops"))))
	}))
	defer srv.Close()
	if _, err := mcptools.NewREST(srv.URL, "", srv.Client()).Usage(context.Background(), mcptools.UsageQuery{Hours: 24, Group: "kind", Top: 1}); err == nil {
		t.Error("an agent-scoped role read usage over REST")
	}
}

func TestSessionsHandler_RejectsABadFilter(t *testing.T) {
	store := NewSessionStore(nil)
	for _, qs := range []string{"since=yesterday", "complete=maybe"} {
		rec := httptest.NewRecorder()
		sessionsHandler(store)(rec, httptest.NewRequest(http.MethodGet, "/api/streaming/sessions?"+qs, nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", qs, rec.Code)
		}
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
