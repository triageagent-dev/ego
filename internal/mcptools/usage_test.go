package mcptools

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestBuildUsage_SumsGroupsAndMergesSessionModels(t *testing.T) {
	cells := []UsageCell{
		{Kind: "ops", Model: "m1", Service: "s1", Tenant: "a/x", Calls: 2, Errors: 1, InputTokens: 100, OutputTokens: 10},
		{Kind: "ops", Model: "m2", Service: "s1", Tenant: "a/x", Calls: 1, InputTokens: 50},
		{Kind: "", Model: "m1", Service: "s2", Tenant: "a/y", Calls: 1, InputTokens: 500},
	}
	rows := []UsageSessionRow{
		{SessionID: "s-b", Model: "m1", Calls: 1, InputTokens: 10, Kinds: "ops", Tenants: "a/x"},
		{SessionID: "s-a", Model: "m1", Calls: 1, InputTokens: 100, Kinds: "ops,triage", Tenants: "a/x"},
		{SessionID: "s-a", Model: "m2", Calls: 2, InputTokens: 20, OutputTokens: 5, Kinds: "triage", Tenants: "a/x"},
	}
	u := BuildUsage(UsageQuery{Hours: 6, Group: UsageByKind, Top: 1}, cells, rows)
	if u.InputTokens != 650 || u.OutputTokens != 10 || u.TotalTokens != 660 || u.Calls != 4 || u.ErrorCalls != 1 || u.Hours != 6 {
		t.Errorf("totals = %+v", u)
	}
	if want := []UsageGroup{
		{Key: "(none)", InputTokens: 500, Tokens: 500, Calls: 1},
		{Key: "ops", InputTokens: 150, OutputTokens: 10, Tokens: 160, Calls: 3},
	}; !slices.Equal(u.Breakdown, want) {
		t.Errorf("breakdown = %+v, want %+v", u.Breakdown, want)
	}
	if len(u.TopSessions) != 1 || u.TopSessions[0] != (UsageSession{
		SessionID: "s-a", Agent: "ops, triage", Tenant: "a/x", InputTokens: 120, OutputTokens: 5, Tokens: 125, Calls: 3,
	}) {
		t.Errorf("top sessions = %+v", u.TopSessions)
	}
	if byModel := BuildUsage(UsageQuery{Group: UsageByModel}, cells, nil); byModel.Breakdown[0].Key != "m1" || len(byModel.TopSessions) != 0 {
		t.Errorf("by model = %+v", byModel)
	}
}

func TestListSessionsHandler_PassesFiltersToTheServer(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		_, _ = w.Write([]byte(`{"data":[{"sessionId":"s1","isComplete":true,"spanCount":4,"startedAt":"2026-01-01T00:00:00Z",
			"agents":["ops","triage"],"tenants":["a/x"],"service":"checkout","errors":2,"totalTokens":900}],"error":null}`))
	}))
	defer srv.Close()

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"agent": "ops", "tenant": "a/*", "since": "2026-01-01T00:00:00Z", "query": " Pod ", "complete": false,
	}
	result, err := listSessionsHandler(NewREST(srv.URL, "", srv.Client()))(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	// "a/*" goes as "a/": gateways refuse "/*" in a query string.
	for k, want := range map[string]string{"agent": "ops", "tenant": "a/", "since": "2026-01-01T00:00:00Z", "q": "Pod", "complete": "false"} {
		if got.Get(k) != want {
			t.Errorf("query %s = %q, want %q", k, got.Get(k), want)
		}
	}
	var out []SessionListItem
	structuredContent(t, result, &out)
	if len(out) != 1 || out[0] != (SessionListItem{
		SessionID: "s1", IsComplete: true, SpanCount: 4, StartedAt: "2026-01-01T00:00:00Z",
		Agent: "ops, triage", Tenant: "a/x", Service: "checkout", ErrorSpans: 2, TotalTokens: 900,
	}) {
		t.Errorf("item = %+v", out)
	}

	// No arguments: no filter reaches the server, and complete is not sent as false.
	got = nil
	if _, err := listSessionsHandler(NewREST(srv.URL, "", srv.Client()))(t.Context(), mcp.CallToolRequest{}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("unfiltered call sent %v", got)
	}

	bad := mcp.CallToolRequest{}
	bad.Params.Arguments = map[string]any{"since": "yesterday"}
	if res, _ := listSessionsHandler(NewREST(srv.URL, "", srv.Client()))(t.Context(), bad); !res.IsError {
		t.Error("a bad since was accepted")
	}
}

func TestGetUsageHandler_ReadsTheViewsEndpointsAndChecksArguments(t *testing.T) {
	var cellsQ, callsQ url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/usage":
			cellsQ = r.URL.Query()
			_, _ = w.Write([]byte(`{"data":{"buckets":[{"kind":"ops","model":"m","calls":2,"inputTokens":30,"outputTokens":12}]},"error":null}`))
		case "/api/usage/calls":
			callsQ = r.URL.Query()
			_, _ = w.Write([]byte(`{"data":[{"sessionId":"s1","kinds":"ops","tenants":"a/x","calls":2,"inputTokens":30,"outputTokens":12}],"error":null}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	h := getUsageHandler(NewREST(srv.URL, "", srv.Client()))

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"hours": float64(6), "tenant": "a/*"}
	result, err := h(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	var u Usage
	structuredContent(t, result, &u)
	if u.Hours != 6 || u.Group != "kind" || u.TotalTokens != 42 || u.Calls != 2 || len(u.Breakdown) != 1 || len(u.TopSessions) != 1 || u.TopSessions[0].Tokens != 42 {
		t.Errorf("usage = %+v", u)
	}
	if cellsQ.Get("hours") != "6" || cellsQ.Get("tenant") != "a/" || callsQ.Get("group") != "session" || callsQ.Get("limit") != "500" {
		t.Errorf("queries = %v, %v", cellsQ, callsQ)
	}

	for name, args := range map[string]map[string]any{
		"zero hours": {"hours": float64(0)}, "too many hours": {"hours": float64(5000)},
		"bad group": {"group": "colour"}, "top too big": {"top": float64(101)},
	} {
		bad := mcp.CallToolRequest{}
		bad.Params.Arguments = args
		if res, _ := h(t.Context(), bad); !res.IsError {
			t.Errorf("%s was accepted", name)
		}
	}
}
