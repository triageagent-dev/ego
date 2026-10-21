package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/triageagent-dev/ego/internal/mcptools"
	"github.com/triageagent-dev/ego/internal/online"
)

// mcpFixture is an archive holding the online evaluation of two senders' tenants ("core/acme" and "rc/acme"), one
// hallucination-scored session each with its hourly run and an open alert, plus one run that online evaluation did
// not make.
type mcpFixture struct {
	backend   *mcpBackend
	archive   *SQLiteStore
	engine    *OnlineEngine
	runOf     map[string]string // tenant -> online run id
	manualRun string
	alertOf   map[string]string // tenant -> alert id
	sessionOf map[string]string // tenant -> session id
}

func ptr[T any](v T) *T { return &v }

// hallucinationDetails is a judge's stored details for one reply: a supported sentence first, so ordering shows.
func hallucinationDetails() map[string]any {
	return map[string]any{
		"agent": "ops-agent",
		"per_invocation": []any{map[string]any{
			"invocation_id": "inv-1", "score": 0.25,
			"sentences": []any{
				map[string]any{"sentence": "The pod restarted 3 times.", "label": "supported", "rationale": "the tool output says 3", "supporting_excerpt": "restarts: 3", "contradicting_excerpt": ""},
				map[string]any{"sentence": "Memory use is at 90 percent.", "label": "unsupported", "rationale": "no tool output reports memory", "supporting_excerpt": "", "contradicting_excerpt": ""},
				map[string]any{"sentence": "The node is healthy.", "label": "contradictory", "rationale": "the node is NotReady", "supporting_excerpt": "", "contradicting_excerpt": "node: NotReady"},
				map[string]any{"sentence": "Let me know if you need more.", "label": "not_applicable", "rationale": "an offer", "supporting_excerpt": "", "contradicting_excerpt": ""},
			},
		}},
	}
}

func newMCPFixture(t *testing.T) *mcpFixture {
	t.Helper()
	e, sessions, archive, _ := newTestEngine(t, testOnlineConfig)
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	e.now = func() time.Time { return now }
	f := &mcpFixture{
		backend: newMCPBackend(sessions, archive, e, nil), archive: archive, engine: e,
		runOf: map[string]string{}, alertOf: map[string]string{}, sessionOf: map[string]string{},
	}

	for _, tenant := range []string{"core/acme", "rc/acme"} {
		run, err := archive.CreateRun(runSpecSummaryDTO{Approach: "online", Target: map[string]any{"tenant": tenant}})
		if err != nil {
			t.Fatal(err)
		}
		session := "s-" + strings.ReplaceAll(tenant, "/", "-")
		score := ptr(0.25)
		if err := archive.InsertRunResults(run.RunID, []runResultRowDTO{{
			ResultID: newRandomID("res"), RunID: run.RunID, EvalSetItemID: session, EvalSetItemName: session,
			EvaluatorName: "hallucinations_v1", EvaluatorType: "builtin", Status: "failed", Score: score,
			Details: hallucinationDetails(), CreatedAt: nowRFC3339(),
		}}); err != nil {
			t.Fatal(err)
		}
		if err := archive.FinishRun(run.RunID, "succeeded", &runSummaryDTO{TraceCount: 1, Agents: []string{"ops-agent"}}, nil); err != nil {
			t.Fatal(err)
		}
		if err := archive.InsertOnlineEval(onlineEvalRow{
			SessionID: session, TraceID: "t-" + session, AtMs: now.Add(-30 * time.Minute).UnixMilli(),
			Tenant: tenant, Kind: "rca", Service: "ops-agent", RunID: run.RunID,
		}, "v2", "gemini-test", []onlineScoreRow{{Metric: "hallucinations_v1", Score: score, Status: "failed"}}); err != nil {
			t.Fatal(err)
		}
		alert := &online.Alert{
			ID: "alert-" + session, Key: online.SeriesKey{Tenant: tenant, Kind: "rca", Metric: "hallucinations_v1"},
			Status: "open", OpenedAt: now, UpdatedAt: now,
			Last: online.TestResult{BaseMean: 0.9, CurMean: 0.25, Drop: 0.65, Z: 5, NBase: 20, NCur: 12, Regressed: true, Suspect: "version v2", Worst: []string{session, "pruned-session"}},
		}
		if err := archive.UpsertOnlineAlert(alert); err != nil {
			t.Fatal(err)
		}
		f.runOf[tenant], f.alertOf[tenant], f.sessionOf[tenant] = run.RunID, alert.ID, session
	}

	manual, err := archive.CreateRun(runSpecSummaryDTO{Approach: "traces"})
	if err != nil {
		t.Fatal(err)
	}
	f.manualRun = manual.RunID
	return f
}

func asRole(info RoleInfo) context.Context {
	return withRoleInfo(context.Background(), info)
}

func TestMCPBackend_TenantLimitsApplyToRunsAndAlerts(t *testing.T) {
	f := newMCPFixture(t)
	core := asRole(RoleInfo{Role: RoleMember, Tenants: []string{"core/*"}})
	admin := asRole(RoleInfo{Role: RoleAdmin, IsAdmin: true})

	// Runs: a limited role sees its own online runs, never another tenant's or a manual one.
	runs, err := f.backend.Runs(core, 20, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].RunID != f.runOf["core/acme"] {
		t.Fatalf("limited role's runs = %+v, want only %s", runs, f.runOf["core/acme"])
	}
	if all, _ := f.backend.Runs(admin, 20, nil); len(all) != 3 {
		t.Errorf("unlimited role sees %d runs, want 3", len(all))
	}

	// Run results: own online run readable, evidence included; a foreign or manual run refused.
	res, err := f.backend.RunResults(core, f.runOf["core/acme"])
	if err != nil || len(res.Results) != 1 || res.Results[0].EvaluatorName != "hallucinations_v1" || res.Results[0].Details == nil {
		t.Fatalf("own run results = %+v, %v", res, err)
	}
	for name, id := range map[string]string{"foreign online run": f.runOf["rc/acme"], "manual run": f.manualRun, "unknown run": "run-nope"} {
		if _, err := f.backend.RunResults(core, id); err == nil {
			t.Errorf("get_run_results on a %s was allowed", name)
		}
	}
	if _, err := f.backend.RunResults(admin, f.runOf["rc/acme"]); err != nil {
		t.Errorf("unlimited role refused a run: %v", err)
	}

	// Alerts and series: only the caller's tenants, and no shared budget.
	q, err := f.backend.Quality(core, "", 24)
	if err != nil {
		t.Fatal(err)
	}
	if len(q.OpenAlerts) != 1 || q.OpenAlerts[0].Tenant != "core/acme" || q.OpenAlerts[0].AlertID != f.alertOf["core/acme"] {
		t.Errorf("limited role's alerts = %+v", q.OpenAlerts)
	}
	if len(q.Series) != 1 || q.Series[0].Tenant != "core/acme" || q.Series[0].N != 1 || q.SessionsScoredTotal != 1 {
		t.Errorf("limited role's series = %+v, scored %d", q.Series, q.SessionsScoredTotal)
	}
	if q.Budget != nil {
		t.Errorf("a limited role got the shared budget: %+v", q.Budget)
	}
	qa, _ := f.backend.Quality(admin, "", 24)
	if len(qa.OpenAlerts) != 2 || len(qa.Series) != 2 || qa.SessionsScoredTotal != 2 || qa.Budget == nil || qa.Budget.JudgeTokensCap != 100000 {
		t.Errorf("unlimited quality = %+v", qa)
	}
	if qk, _ := f.backend.Quality(admin, "voice", 24); len(qk.OpenAlerts) != 0 || len(qk.Series) != 0 {
		t.Errorf("kind filter kept %+v", qk)
	}

	// Explain: a foreign alert or session reads like a missing one.
	if _, err := f.backend.ExplainRegression(core, f.alertOf["rc/acme"]); err == nil {
		t.Error("explain_regression on a foreign alert was allowed")
	}
	if _, err := f.backend.ExplainSession(core, f.sessionOf["rc/acme"]); err == nil {
		t.Error("explain_session on a foreign session was allowed")
	}
	if _, err := f.backend.ExplainRegression(core, f.alertOf["core/acme"]); err != nil {
		t.Errorf("own alert refused: %v", err)
	}

	// An agent-scoped role has no online views, as on the REST endpoints.
	agent := asRole(RoleInfo{Role: RoleMember, Agents: []string{"ops-agent"}})
	if _, err := f.backend.Quality(agent, "", 24); err == nil {
		t.Error("agent-scoped role got quality")
	}
}

func TestRunEndpointsApplyTenantLimits(t *testing.T) {
	f := newMCPFixture(t)
	as := withRoleInfoForTest(scopedToTenants("core/*"))

	rec := httptest.NewRecorder()
	listRunsHandler(f.archive)(rec, as(httptest.NewRequest(http.MethodGet, "/api/runs", nil)))
	var list struct{ Data []runDTO }
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Data) != 1 || list.Data[0].RunID != f.runOf["core/acme"] {
		t.Fatalf("GET /api/runs = %d %s", rec.Code, rec.Body)
	}

	get := func(h http.HandlerFunc, id string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/runs/"+id, nil)
		req.SetPathValue("id", id)
		rec := httptest.NewRecorder()
		h(rec, as(req))
		return rec.Code
	}
	for name, h := range map[string]http.HandlerFunc{"run": getRunHandler(f.archive), "results": getRunResultsHandler(f.archive)} {
		if code := get(h, f.runOf["core/acme"]); code != http.StatusOK {
			t.Errorf("own %s = %d, want 200", name, code)
		}
		for _, id := range []string{f.runOf["rc/acme"], f.manualRun} {
			if code := get(h, id); code != http.StatusNotFound {
				t.Errorf("foreign %s %s = %d, want 404", name, id, code)
			}
		}
	}
}

func TestExplainRegression_CarriesJudgeEvidence(t *testing.T) {
	f := newMCPFixture(t)
	core := asRole(RoleInfo{Role: RoleMember, Tenants: []string{"core/*"}})
	out, err := f.backend.ExplainRegression(core, f.alertOf["core/acme"])
	if err != nil {
		t.Fatal(err)
	}
	if out.Alert.AlertID != f.alertOf["core/acme"] || out.Alert.Metric != "hallucinations_v1" || out.Alert.Drop != 0.65 || out.Suspect != "version v2" {
		t.Errorf("alert = %+v suspect %q", out.Alert, out.Suspect)
	}
	if len(out.Missing) != 1 || out.Missing[0] != "pruned-session" || len(out.Sessions) != 1 {
		t.Fatalf("sessions = %d, missing %v", len(out.Sessions), out.Missing)
	}
	s := out.Sessions[0]
	if s.Tenant != "core/acme" || s.Kind != "rca" || s.Service != "ops-agent" || s.Version != "v2" || s.Model != "gemini-test" || len(s.Metrics) != 1 {
		t.Fatalf("session = %+v", s)
	}
	m := s.Metrics[0]
	if m.Metric != "hallucinations_v1" || m.Status != "failed" || m.Score == nil || *m.Score != 0.25 || m.Failing != 2 || m.Total != 4 {
		t.Errorf("metric = %+v", m)
	}
	if got := []string{m.Sentences[0].Label, m.Sentences[1].Label}; got[0] != "unsupported" || got[1] != "contradictory" {
		t.Errorf("failing sentences must lead, got %v", got)
	}

	// explain_session gives the same evidence for the session.
	one, err := f.backend.ExplainSession(core, f.sessionOf["core/acme"])
	if err != nil || len(one.Metrics) != 1 || one.Metrics[0].Failing != 2 {
		t.Errorf("explain_session = %+v, %v", one, err)
	}
}

func TestExtractEvidence_HallucinationSentences(t *testing.T) {
	long := strings.Repeat("x", 1000)
	row := runResultRowDTO{
		EvaluatorName: "hallucinations_v1", Status: "failed", Score: ptr(0.25),
		// Built from Go values, as an evaluation produces them before they are stored.
		Details: map[string]any{"per_invocation": []map[string]any{{
			"invocation_id": "inv-1",
			"sentences": []map[string]any{
				{"sentence": "A", "label": "supported", "rationale": "ok", "supporting_excerpt": "a", "contradicting_excerpt": ""},
				{"sentence": "B", "label": "disputed", "rationale": "both", "supporting_excerpt": "b1", "contradicting_excerpt": "b2"},
				{"sentence": "C", "label": "not_applicable", "rationale": "greeting", "supporting_excerpt": "", "contradicting_excerpt": ""},
				{"sentence": long, "label": "unsupported", "rationale": "not in the output", "supporting_excerpt": "", "contradicting_excerpt": ""},
			},
		}}},
	}
	ev := ExtractEvidence(row, "hallucinations_v1")
	if ev.Metric != "hallucinations_v1" || ev.Status != "failed" || *ev.Score != 0.25 || ev.Failing != 2 || ev.Total != 4 || ev.Truncated != 0 {
		t.Fatalf("evidence = %+v", ev)
	}
	var order []string
	for _, s := range ev.Sentences {
		order = append(order, s.Label)
	}
	if want := []string{"disputed", "unsupported", "supported", "not_applicable"}; !slices.Equal(order, want) {
		t.Errorf("order = %v, want failing first then the rest in order: %v", order, want)
	}
	if ev.Rationale != "both" {
		t.Errorf("rationale = %q, want the first failing sentence's", ev.Rationale)
	}
	if first := ev.Sentences[0]; first.InvocationID != "inv-1" || first.SupportingExcerpt != "b1" || first.ContradictingExcerpt != "b2" {
		t.Errorf("first sentence = %+v", first)
	}
	if got := len([]rune(ev.Sentences[1].Sentence)); got > maxEvidenceText+1 {
		t.Errorf("a long sentence kept %d runes", got)
	}

	// A row without details still reports its score and error.
	bad := ExtractEvidence(runResultRowDTO{EvaluatorName: "hallucinations_v1", Status: "errored", ErrorText: ptr("judge failed")}, "hallucinations_v1")
	if bad.Error != "judge failed" || bad.Sentences != nil {
		t.Errorf("errored row = %+v", bad)
	}
}

func TestExtractEvidence_OtherMetrics(t *testing.T) {
	rubric := ExtractEvidence(runResultRowDTO{Status: "failed", Details: map[string]any{"rubric_invocations": []any{map[string]any{
		"invocation_id": "i1", "rubrics": []any{
			map[string]any{"id": "rubric_0", "rubric": "cites a source", "verdict": "yes", "rationale": "it does"},
			map[string]any{"id": "rubric_1", "rubric": "states the cause", "verdict": "no", "rationale": "it never names one"},
			map[string]any{"id": "rubric_2", "rubric": "n/a", "verdict": nil, "rationale": ""},
		},
	}}}}, "rubric_based_final_response_quality_v1")
	if rubric.Failing != 1 || rubric.Total != 3 || rubric.Rubrics[0].ID != "rubric_1" || rubric.Rationale != "it never names one" {
		t.Errorf("rubric evidence = %+v", rubric)
	}

	traj := ExtractEvidence(runResultRowDTO{Status: "failed", Details: map[string]any{"comparisons": []any{
		map[string]any{"invocation_id": "i1", "matched": true, "expected": []any{map[string]any{"name": "a", "args": map[string]any{}}}, "actual": []any{map[string]any{"name": "a", "args": map[string]any{}}}},
		map[string]any{"invocation_id": "i2", "matched": false, "expected": []any{map[string]any{"name": "a", "args": map[string]any{}}}, "actual": []any{map[string]any{"name": "b", "args": map[string]any{"k": "v"}}}},
	}}}, "tool_trajectory_avg_score")
	if traj.Failing != 1 || traj.Comparisons[0].InvocationID != "i2" || traj.Comparisons[0].Actual[0].Tool != "b" || traj.Rationale != "expected [a], got [b]" {
		t.Errorf("trajectory evidence = %+v", traj)
	}

	jev := ExtractEvidence(runResultRowDTO{Status: "failed", Details: map[string]any{
		"question": "Is the answer grounded?", "question_type": "noul", "expect": true,
		"turns": []any{
			map[string]any{"invocation_id": "i1", "score": 1.0, "answer": map[string]any{"type": "noul", "noul": 0.9}},
			map[string]any{"invocation_id": "i2", "score": 0.0, "user_input": "why?", "final_response": "because", "answer": map[string]any{"type": "noul", "noul": 0.1}},
		},
	}}, "jev_grounded")
	if jev.Failing != 1 || jev.Decisions[0].InvocationID != "i2" || jev.Rationale != "noul 0.10" || jev.Decisions[0].Question != "Is the answer grounded?" {
		t.Errorf("jev evidence = %+v", jev)
	}
}

// callTool posts one tools/call to the MCP endpoint and returns the HTTP status and the decoded result.
func callTool(t *testing.T, h http.Handler, token, name string, args map[string]any) (int, mcpToolResult) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var rpc struct {
		Result mcpToolResult `json:"result"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &rpc)
	return rec.Code, rpc.Result
}

type mcpToolResult struct {
	IsError           bool            `json:"isError"`
	StructuredContent json.RawMessage `json:"structuredContent"`
}

func TestMCPRoute_SenderTokenAuthenticatesAsTenantLimitedMember(t *testing.T) {
	f := newMCPFixture(t)
	senders, err := NewSenderStore(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"core", "rc"} {
		if _, err := senders.Create(id, ""); err != nil {
			t.Fatal(err)
		}
	}
	coreToken, _, _ := senders.NewToken("core", "")
	roles, err := NewRoleStore(nil, RoleNone, nil)
	if err != nil {
		t.Fatal(err)
	}
	const secret = "a-session-secret-that-is-long-enough"
	h := mcpRoute(secret, senders, roles, "", nil, nil, f.backend)

	// The sender's own token: a member limited to core/*.
	code, res := callTool(t, h, coreToken, "get_quality", nil)
	if code != http.StatusOK || res.IsError {
		t.Fatalf("sender token on /mcp = %d %+v", code, res)
	}
	var q mcptools.Quality
	if err := json.Unmarshal(res.StructuredContent, &q); err != nil {
		t.Fatal(err)
	}
	if len(q.OpenAlerts) != 1 || q.OpenAlerts[0].Tenant != "core/acme" || q.Budget != nil {
		t.Errorf("sender saw %+v", q)
	}
	if _, res := callTool(t, h, coreToken, "get_run_results", map[string]any{"run_id": f.runOf["rc/acme"]}); !res.IsError {
		t.Error("sender token read another sender's run")
	}
	if _, res := callTool(t, h, coreToken, "get_run_results", map[string]any{"run_id": f.runOf["core/acme"]}); res.IsError {
		t.Error("sender token refused its own run")
	}
	// A member is not an admin: the role tools refuse it.
	if _, res := callTool(t, h, coreToken, "list_role_bindings", nil); !res.IsError {
		t.Error("sender token listed role bindings")
	}

	// The shared ingest token names no sender and opens nothing; neither does an unknown or missing token.
	for name, tok := range map[string]string{"shared otlp token": "the-shared-otlp-token", "unknown token": "egs_nope", "no token": ""} {
		if code, _ := callTool(t, h, tok, "get_quality", nil); code != http.StatusUnauthorized {
			t.Errorf("%s on /mcp = %d, want 401", name, code)
		}
	}
	// A sender token is no credential anywhere but /mcp.
	api := requireSession(secret, nil, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/runs", nil)
	req.Header.Set("Authorization", "Bearer "+coreToken)
	api.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("sender token on /api = %d, want 401", rec.Code)
	}

	// Without senders the gate is inert: the token is just an unknown bearer.
	bare := mcpRoute(secret, nil, roles, "", nil, nil, f.backend)
	if code, _ := callTool(t, bare, coreToken, "get_quality", nil); code != http.StatusUnauthorized {
		t.Errorf("sender token without a SenderStore = %d, want 401", code)
	}

	// No session secret: no login, and every caller is an admin, as on /api.
	open := mcpRoute("", senders, nil, "", nil, nil, f.backend)
	code, res = callTool(t, open, "", "get_quality", nil)
	var qa mcptools.Quality
	if err := json.Unmarshal(res.StructuredContent, &qa); err != nil || code != http.StatusOK || len(qa.OpenAlerts) != 2 || qa.Budget == nil {
		t.Errorf("no-login /mcp = %d %+v (%v)", code, qa, err)
	}
}

func TestMCPEndpoint_MethodsAndToolList(t *testing.T) {
	f := newMCPFixture(t)
	h := mcpRoute("", nil, nil, "", nil, nil, f.backend)

	// The stream for server pushes is off (nothing is ever pushed): GET is answered 405.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req.Header.Set("Accept", "text/event-stream")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /mcp = %d, want 405", rec.Code)
	}

	// The tool list is the stdio list but for the tool that reads local files.
	serve := toolNames(mcptools.Tools(f.backend, MCPToolOptions()))
	stdioOpts := MCPToolOptions()
	stdioOpts.EvaluateTraces = func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) { return nil, nil }
	stdio := toolNames(mcptools.Tools(f.backend, stdioOpts))
	if slices.Contains(serve, "evaluate_traces") {
		t.Error("serve registers evaluate_traces")
	}
	if want := append(slices.Clone(serve), "evaluate_traces"); !sameSet(stdio, want) {
		t.Errorf("stdio tools %v, serve tools %v: differ by more than evaluate_traces", stdio, serve)
	}
	for _, name := range []string{"get_quality", "explain_regression", "explain_session", "list_role_bindings"} {
		if !slices.Contains(serve, name) {
			t.Errorf("serve lacks %s", name)
		}
	}
}

func toolNames(tools []server.ServerTool) []string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Tool.Name
	}
	return names
}

func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// The stdio command reaches the same functions through REST: what its client decodes equals what the in-process
// backend returns, and the role limits hold on the endpoints too.
func TestRESTClientSeesWhatTheInProcessBackendSees(t *testing.T) {
	f := newMCPFixture(t)
	info := RoleInfo{Role: RoleMember, Tenants: []string{"core/*"}}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/online/quality", onlineQualityHandler(f.engine))
	mux.HandleFunc("/api/online/alerts/{id}/explain", onlineExplainAlertHandler(f.engine))
	mux.HandleFunc("/api/online/sessions/{id}/explain", onlineExplainSessionHandler(f.engine))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(withRoleInfo(r.Context(), info)))
	}))
	defer srv.Close()
	rest := mcptools.NewREST(srv.URL, "", srv.Client())
	ctx := asRole(info)

	same := func(name string, a, b any) {
		t.Helper()
		ja, _ := json.Marshal(a)
		jb, _ := json.Marshal(b)
		if string(ja) != string(jb) {
			t.Errorf("%s differs:\n rest       %s\n in-process %s", name, ja, jb)
		}
	}
	gotQ, err := rest.Quality(ctx, "rca", 24)
	if err != nil {
		t.Fatal(err)
	}
	wantQ, _ := f.backend.Quality(ctx, "rca", 24)
	same("quality", gotQ, wantQ)

	gotR, err := rest.ExplainRegression(ctx, f.alertOf["core/acme"])
	if err != nil {
		t.Fatal(err)
	}
	wantR, _ := f.backend.ExplainRegression(ctx, f.alertOf["core/acme"])
	same("explain_regression", gotR, wantR)

	gotS, err := rest.ExplainSession(ctx, f.sessionOf["core/acme"])
	if err != nil {
		t.Fatal(err)
	}
	wantS, _ := f.backend.ExplainSession(ctx, f.sessionOf["core/acme"])
	same("explain_session", gotS, wantS)

	if _, err := rest.ExplainRegression(ctx, f.alertOf["rc/acme"]); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("foreign alert over REST = %v, want a 404", err)
	}
}
