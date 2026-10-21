package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/triageagent-dev/ego/internal/mcptools"
	"github.com/triageagent-dev/ego/internal/online"
	"github.com/triageagent-dev/ego/skills"
)

var adminInfo = RoleInfo{Role: RoleAdmin, IsAdmin: true}

func labelIn(session, verdict string) mcptools.LabelInput {
	return mcptools.LabelInput{SessionID: session, Metric: "hallucinations_v1", Verdict: verdict}
}

func TestOnlineLabelsHandler(t *testing.T) {
	f := newFindingsFixture(t, findingsConfig)
	acme := f.seed("acme", 0.4, f.now.Add(-time.Hour), "v1", "q one", "get_pods")
	globex := f.seed("globex", 0.8, f.now.Add(-time.Hour), "v1", "q two", "get_pods")

	mux := http.NewServeMux()
	mux.HandleFunc("/api/online/labels", onlineLabelsHandler(f.e))
	do := func(ctx context.Context, method, path, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var env map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &env)
		return rec.Code, env
	}
	withRole := func(info RoleInfo) context.Context { return withRoleInfo(context.Background(), info) }
	post := func(session, metric, verdict string) string {
		return fmt.Sprintf(`{"sessionId": %q, "metric": %q, "verdict": %q}`, session, metric, verdict)
	}

	// 201 with the judge's score at label time and the caller as author (taken from the identity, not the body).
	ctx := withIdentity(withRole(adminInfo), identity{Username: "ops-user", Kind: tokenKindAPI})
	code, env := do(ctx, http.MethodPost, "/api/online/labels",
		fmt.Sprintf(`{"sessionId": %q, "metric": "hallucinations_v1", "verdict": "disagree", "score": 0.9, "note": "the figure is in the tool output", "by": "someone else"}`, acme))
	l, _ := env["data"].(map[string]any)
	if code != 201 || l["judge_score"] != 0.4 || l["score"] != 0.9 || l["verdict"] != "disagree" || l["tenant"] != "acme" ||
		l["kind"] != "rca" || l["by"] != "ops-user" || l["note"] != "the figure is in the tool output" || l["label_id"] == "" {
		t.Errorf("POST = %d %v", code, env)
	}

	// No online score for the metric, or no such session: 404. Bad input: 400.
	if code, _ := do(ctx, http.MethodPost, "/api/online/labels", post(acme, "rouge", "agree")); code != 404 {
		t.Errorf("a metric the session has no score for = %d, want 404", code)
	}
	if code, _ := do(ctx, http.MethodPost, "/api/online/labels", post("otlp-nope", "hallucinations_v1", "agree")); code != 404 {
		t.Errorf("unknown session = %d, want 404", code)
	}
	if code, _ := do(ctx, http.MethodPost, "/api/online/labels", post(acme, "hallucinations_v1", "maybe")); code != 400 {
		t.Errorf("bad verdict = %d, want 400", code)
	}
	if code, _ := do(ctx, http.MethodPost, "/api/online/labels",
		fmt.Sprintf(`{"sessionId": %q, "metric": "hallucinations_v1", "verdict": "agree", "score": 1.5}`, acme)); code != 400 {
		t.Errorf("score out of range = %d, want 400", code)
	}
	if code, _ := do(ctx, http.MethodPost, "/api/online/labels", `{not json`); code != 400 {
		t.Errorf("bad JSON = %d, want 400", code)
	}
	if code, _ := do(ctx, http.MethodDelete, "/api/online/labels", ``); code != 405 {
		t.Errorf("DELETE = %d, want 405", code)
	}

	// A viewer reads but never labels; a tenant-limited member labels only its own sessions.
	if code, _ := do(withRole(RoleInfo{Role: RoleViewer}), http.MethodPost, "/api/online/labels", post(acme, "hallucinations_v1", "agree")); code != 403 {
		t.Errorf("viewer POST = %d, want 403", code)
	}
	if code, _ := do(withRole(member("acme")), http.MethodPost, "/api/online/labels", post(globex, "hallucinations_v1", "agree")); code != 404 {
		t.Errorf("a foreign tenant's session = %d, want 404", code)
	}
	if code, _ := do(withRole(member("acme")), http.MethodPost, "/api/online/labels", post(acme, "hallucinations_v1", "agree")); code != 201 {
		t.Errorf("own session = %d, want 201", code)
	}
	if code, _ := do(withRole(RoleInfo{Role: RoleMember, Agents: []string{"x"}}), http.MethodPost, "/api/online/labels", post(acme, "hallucinations_v1", "agree")); code != 403 {
		t.Errorf("agent-scoped = %d, want 403", code)
	}
	if code, _ := do(withRole(adminInfo), http.MethodPost, "/api/online/labels", post(globex, "hallucinations_v1", "agree")); code != 201 {
		t.Errorf("admin on globex = %d, want 201", code)
	}

	// GET: newest first, filters, and the tenant limit.
	code, env = do(withRole(adminInfo), http.MethodGet, "/api/online/labels", "")
	if rows, _ := env["data"].([]any); code != 200 || len(rows) != 3 {
		t.Errorf("all labels = %d %v", code, env)
	}
	code, env = do(withRole(adminInfo), http.MethodGet, "/api/online/labels?session="+acme+"&metric=hallucinations_v1", "")
	if rows, _ := env["data"].([]any); code != 200 || len(rows) != 2 {
		t.Errorf("labels of one session = %d %v", code, env)
	}
	if code, env := do(withRole(adminInfo), http.MethodGet, "/api/online/labels?metric=rouge", ""); code != 200 || len(env["data"].([]any)) != 0 {
		t.Errorf("labels of another metric = %d %v", code, env)
	}
	if code, env := do(withRole(adminInfo), http.MethodGet, "/api/online/labels?tenant=globex", ""); code != 200 || len(env["data"].([]any)) != 1 {
		t.Errorf("tenant pick = %d %v", code, env)
	}
	if code, env := do(withRole(member("acme")), http.MethodGet, "/api/online/labels", ""); code != 200 || len(env["data"].([]any)) != 2 {
		t.Errorf("a tenant-limited list = %d %v", code, env)
	}
	if code, _ := do(withRole(adminInfo), http.MethodGet, "/api/online/labels?since=whenever", ""); code != 400 {
		t.Errorf("bad since = %d, want 400", code)
	}

	rec := httptest.NewRecorder()
	onlineLabelsHandler(nil)(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != 503 {
		t.Errorf("unconfigured = %d, want 503", rec.Code)
	}
}

// labelAll labels each session, the first `agree` of them agree and the rest disagree, step apart.
func labelAll(t *testing.T, f *findingsFixture, ids []string, agree int, step time.Duration) {
	t.Helper()
	for i, id := range ids {
		v := online.VerdictDisagree
		if i < agree {
			v = online.VerdictAgree
		}
		f.now = f.now.Add(step)
		if _, err := f.e.LabelSession(adminInfo, "", labelIn(id, v)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAgreementGateKeepsAnUncalibratedMetricFromWritingFindings(t *testing.T) {
	// Nine labels are too few to doubt the judge: the finding is written.
	a := newFindingsFixture(t, findingsConfig)
	ids := a.seedBad("acme", 12)
	labelAll(t, a, ids[:9], 3, time.Minute)
	a.e.findingsMaintain(context.Background(), a.now)
	if st := a.e.status(); len(a.findings(t, "")) != 1 || st.Findings.Skipped.Uncalibrated != 0 || st.Labels.PerMetric["hallucinations_v1"].Gated {
		t.Errorf("nine labels gated the metric: %+v findings %d", st.Labels, len(a.findings(t, "")))
	}

	// Ten labels at 3 agree / 7 disagree: agreement 0.3 is under 0.7, so the metric is gated.
	b := newFindingsFixture(t, findingsConfig)
	ids = b.seedBad("acme", 12)
	labelAll(t, b, ids[:10], 3, time.Minute)
	b.e.findingsMaintain(context.Background(), b.now)
	st := b.e.status()
	m := st.Labels.PerMetric["hallucinations_v1"]
	if len(b.findings(t, "")) != 0 || len(b.judge.prompts) != 0 || st.Findings.Skipped.Uncalibrated != 1 {
		t.Fatalf("a gated metric wrote a finding: %d findings, %d calls, skipped %+v", len(b.findings(t, "")), len(b.judge.prompts), st.Findings.Skipped)
	}
	if !m.Gated || m.Agree != 3 || m.Disagree != 7 || m.Agreement == nil || math.Abs(*m.Agreement-0.3) > 1e-9 {
		t.Errorf("status labels = %+v", m)
	}
	// The wire shape the UI reads.
	raw, _ := json.Marshal(st.Labels)
	for _, want := range []string{`"perMetric"`, `"agree":3`, `"disagree":7`, `"agreement":0.3`, `"gated":true`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("status JSON lacks %s: %s", want, raw)
		}
	}

	// People change their minds on five of the disagreements: 8 of 10 agree, the gate opens, the next sweep writes.
	labelAll(t, b, ids[3:8], 5, time.Minute)
	b.now = b.now.Add(7 * time.Hour)
	b.e.findingsMaintain(context.Background(), b.now)
	if st := b.e.status(); st.Labels.PerMetric["hallucinations_v1"].Gated || st.Labels.PerMetric["hallucinations_v1"].Agree != 8 || len(b.findings(t, "")) != 1 {
		t.Errorf("after the gate opened: %+v findings %d", st.Labels.PerMetric["hallucinations_v1"], len(b.findings(t, "")))
	}
}

func TestAgreementOnlyCountsTheLastSevenDays(t *testing.T) {
	f := newFindingsFixture(t, findingsConfig)
	ids := f.seedBad("acme", 3)
	old := f.now.Add(-8 * 24 * time.Hour)
	if err := f.archive.InsertOnlineLabel(&online.Label{ID: "old", SessionID: ids[0], Tenant: "acme", Kind: "rca", Metric: "hallucinations_v1",
		JudgeScore: 0.3, Verdict: online.VerdictDisagree, CreatedAt: old}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.e.LabelSession(adminInfo, "", labelIn(ids[1], online.VerdictAgree)); err != nil {
		t.Fatal(err)
	}
	m := f.e.status().Labels.PerMetric["hallucinations_v1"]
	if m.Agree != 1 || m.Disagree != 0 {
		t.Errorf("counted %+v, the eight-day-old label belongs to no window", m)
	}
	// A tenant-limited caller counts its own tenants' labels only, and sees no drift.
	f.seed("globex", 0.5, f.now.Add(-time.Hour), "v1", "other", "get_pods")
	if _, err := f.e.LabelSession(adminInfo, "", labelIn("otlp-"+fmt.Sprintf("%012d", f.n), online.VerdictDisagree)); err != nil {
		t.Fatal(err)
	}
	if got := f.e.statusFor(member("acme")).Labels.PerMetric["hallucinations_v1"]; got.Agree != 1 || got.Disagree != 0 {
		t.Errorf("tenant-limited agreement = %+v", got)
	}
	if got := f.e.status().Labels.PerMetric["hallucinations_v1"]; got.Agree != 1 || got.Disagree != 1 {
		t.Errorf("unrestricted agreement = %+v", got)
	}
}

const anchorConfig = `{
	"budget": {"judgeTokensPerDay": 1000000},
	"sampling": {"perKindPerHour": 1000, "boostPerKindPerHour": 1000},
	"profiles": [{"kind": "rca", "evaluators": [
		{"name": "rubric_based_final_response_quality_v1", "rubrics": ["The response is helpful"]}
	]}]
}`

const rubricMetric = "rubric_based_final_response_quality_v1"

const judgeSaysNo = "ID: rubric_0\nProperty: helpful\nRationale: not at all\nVerdict: no\n"

// anchorEngine scores n sessions for real (the fake judge says yes: score 1) and labels them one minute apart: the
// even ones with a score of their own (0.8), the odd ones only a verdict.
func anchorEngine(t *testing.T, n int) (*OnlineEngine, *usageJudge, []string, *time.Time) {
	t.Helper()
	e, sessions, _, fake := newTestEngine(t, anchorConfig)
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	e.now = func() time.Time { return now }
	var ids []string
	for i := 0; i < n; i++ {
		// Letters, not digits: the sampler's dedup fingerprint normalises numbers away.
		trace := fmt.Sprintf("%012d0001", i+1)
		sessions.Ingest(onlineSessionBody(trace, now.UnixMilli(), "rca", fmt.Sprintf("why is service %c%c down", 'a'+i/26, 'a'+i%26), "an answer"))
		e.process(context.Background(), "otlp-"+trace[:12])
		ids = append(ids, "otlp-"+trace[:12])
	}
	for i, id := range ids {
		now = now.Add(time.Minute)
		in := mcptools.LabelInput{SessionID: id, Metric: rubricMetric, Verdict: online.VerdictAgree}
		if i%2 == 0 {
			in.Verdict, in.Score = online.VerdictDisagree, ptr(0.8)
		}
		if _, err := e.LabelSession(adminInfo, "", in); err != nil {
			t.Fatalf("labelling %s: %v", id, err)
		}
	}
	return e, fake, ids, &now
}

func anchorTime(t *testing.T, e *OnlineEngine, id string) int64 {
	t.Helper()
	v, err := e.store.OnlineState(anchorPrefix + rubricMetric + "/" + id)
	if err != nil || v == "" {
		return 0
	}
	ms, _ := strconv.ParseInt(v, 10, 64)
	return ms
}

func TestAnchorRescoringTakesTheOldestFirstAndReportsDrift(t *testing.T) {
	e, fake, ids, now := anchorEngine(t, 25)
	if fake.calls != 25 {
		t.Fatalf("scoring calls = %d, want one per session", fake.calls)
	}
	fake.reply = judgeSaysNo // the judge has drifted: every session now scores 0
	ctx := context.Background()

	first := now.Add(time.Hour)
	e.recalibrate(ctx, first)
	// Up to 20 anchors per metric, one judge call each, the 20 oldest labels.
	if fake.calls != 25+20 {
		t.Fatalf("re-scoring made %d calls, want 20", fake.calls-25)
	}
	for i, id := range ids {
		if got := anchorTime(t, e, id); (i < 20) != (got == first.UnixMilli()) {
			t.Errorf("session %d re-scored at %d, first pass was %d", i, got, first.UnixMilli())
		}
	}
	// Even sessions: |0 - 0.8| = 0.8, odd ones have no score of their own so the judge's old 1.0 is the reference.
	d := e.status().Labels.PerMetric[rubricMetric]
	if d.Drift == nil || math.Abs(*d.Drift-0.9) > 1e-9 || d.DriftN != 20 || d.DriftAt == nil {
		t.Fatalf("drift = %+v", d)
	}
	// The re-scoring is judge spend like any other.
	if spent := e.status().Budget.Spent.JudgeTokens; spent < 25*250+20*250 {
		t.Errorf("budget spent %d tokens, the re-scoring was not metered", spent)
	}

	// Inside recalibrateEvery nothing runs.
	e.recalibrate(ctx, first.Add(time.Hour))
	if fake.calls != 45 {
		t.Errorf("a second pass ran inside the interval: %d calls", fake.calls-45)
	}

	// The next pass takes the five never re-scored first, then the 15 re-scored longest ago.
	second := first.Add(25 * time.Hour)
	e.recalibrate(ctx, second)
	if fake.calls != 65 {
		t.Fatalf("second pass made %d calls, want 20", fake.calls-45)
	}
	for i, id := range ids {
		want := first.UnixMilli()
		if i >= 20 || i < 15 {
			want = second.UnixMilli()
		}
		if got := anchorTime(t, e, id); got != want {
			t.Errorf("session %d last re-scored at %d, want %d", i, got, want)
		}
	}
	// 5 + 15 anchors: indexes 20..24 and 0..14. Even ones differ by 0.8, odd by 1.0.
	if d := e.status().Labels.PerMetric[rubricMetric]; d.Drift == nil || math.Abs(*d.Drift-0.89) > 1e-9 {
		t.Errorf("second drift = %+v", d)
	}
}

func TestAnchorRescoringSkipsWhenTheBudgetIsSpent(t *testing.T) {
	e, fake, _, now := anchorEngine(t, 3)
	fake.reply = judgeSaysNo
	at := now.Add(time.Hour)
	e.budget.Restore(online.Cost{JudgeTokens: 1_000_000}, at)
	e.recalibrate(context.Background(), at)
	if fake.calls != 3 || e.status().Labels.Skipped.Budget != 1 {
		t.Fatalf("calls = %d, skipped = %+v: the pass spent past the cap", fake.calls-3, e.status().Labels.Skipped)
	}
	if d := e.status().Labels.PerMetric[rubricMetric]; d.Drift != nil {
		t.Errorf("drift reported without a re-score: %+v", d)
	}
	// Not marked done: it is retried on the next tick once there is budget.
	e.budget.Restore(online.Cost{}, at)
	e.recalibrate(context.Background(), at.Add(10*time.Minute))
	if fake.calls != 6 {
		t.Errorf("the retry made %d calls, want 3", fake.calls-3)
	}
	if d := e.status().Labels.PerMetric[rubricMetric]; d.Drift == nil || d.DriftN != 3 {
		t.Errorf("drift after the retry = %+v", d)
	}
}

func TestLabelSessionIsTheSameOverRESTAndInProcess(t *testing.T) {
	f := newFindingsFixture(t, findingsConfig)
	acme := f.seed("acme", 0.4, f.now.Add(-time.Hour), "v1", "q one", "get_pods")
	globex := f.seed("globex", 0.8, f.now.Add(-time.Hour), "v1", "q two", "get_pods")

	info := member("acme")
	mux := http.NewServeMux()
	mux.HandleFunc("/api/online/labels", onlineLabelsHandler(f.e))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(withRoleInfo(r.Context(), info)))
	}))
	defer srv.Close()
	rest := mcptools.NewREST(srv.URL, "", srv.Client())
	backend := newMCPBackend(f.sessions, f.archive, f.e, nil)
	ctx := asRole(info)

	in := mcptools.LabelInput{SessionID: acme, Metric: "hallucinations_v1", Verdict: "disagree", Score: ptr(0.9), Note: "figure is in the output"}
	viaREST, err := rest.LabelSession(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	inProcess, err := backend.LabelSession(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	// Two labels of the same session: ids differ, everything else is the same.
	if viaREST.ID == inProcess.ID {
		t.Error("two labels share an id")
	}
	a, b := *viaREST, *inProcess
	a.ID, b.ID = "", ""
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) || a.JudgeScore != 0.4 || a.Tenant != "acme" || a.Verdict != "disagree" {
		t.Errorf("label_session differs:\n rest       %s\n in-process %s", ja, jb)
	}

	// The same refusals on both: a foreign tenant's session and a metric with no score.
	for name, bad := range map[string]mcptools.LabelInput{
		"foreign session": {SessionID: globex, Metric: "hallucinations_v1", Verdict: "agree"},
		"no such score":   {SessionID: acme, Metric: "rouge", Verdict: "agree"},
		"bad verdict":     {SessionID: acme, Metric: "hallucinations_v1", Verdict: "meh"},
	} {
		if _, err := rest.LabelSession(ctx, bad); err == nil {
			t.Errorf("%s: REST accepted it", name)
		}
		if _, err := backend.LabelSession(ctx, bad); err == nil {
			t.Errorf("%s: the in-process backend accepted it", name)
		}
	}
	if _, err := backend.LabelSession(asRole(RoleInfo{Role: RoleViewer}), in); err == nil || !strings.Contains(err.Error(), "cannot label") {
		t.Errorf("viewer = %v", err)
	}
}

// label_session and rsi_loop over the MCP endpoint: the tool takes snake_case arguments, the prompt returns the
// embedded skill text.
func TestMCPLabelToolAndRSILoopPromptOverTheEndpoint(t *testing.T) {
	f := newFindingsFixture(t, findingsConfig)
	acme := f.seed("acme", 0.4, f.now.Add(-time.Hour), "v1", "q one", "get_pods")
	h := mcpRoute("", nil, nil, "", nil, nil, newMCPBackend(f.sessions, f.archive, f.e, nil))

	_, res := callTool(t, h, "", "label_session", map[string]any{"session_id": acme, "metric": "hallucinations_v1", "verdict": "agree", "score": 0.0, "note": "right"})
	var l online.Label
	if err := json.Unmarshal(res.StructuredContent, &l); err != nil || res.IsError || l.Verdict != "agree" || l.JudgeScore != 0.4 || l.Score == nil || *l.Score != 0 {
		t.Fatalf("label_session = %+v %s %v", res, res.StructuredContent, err)
	}
	_, res = callTool(t, h, "", "label_session", map[string]any{"session_id": acme, "metric": "hallucinations_v1", "verdict": "agree"})
	var bare online.Label
	if err := json.Unmarshal(res.StructuredContent, &bare); err != nil || res.IsError || bare.Score != nil {
		t.Errorf("a label without a score kept one: %+v %v", bare, err)
	}
	if _, res = callTool(t, h, "", "label_session", map[string]any{"session_id": acme, "metric": "rouge", "verdict": "agree"}); !res.IsError {
		t.Error("a label on a metric with no score was accepted")
	}
	if _, res = callTool(t, h, "", "label_session", map[string]any{"session_id": acme, "metric": "hallucinations_v1"}); !res.IsError {
		t.Error("a label without a verdict was accepted")
	}

	rpc := func(method string, params any) map[string]any {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	list := rpc("prompts/list", map[string]any{})
	prompts, _ := list["result"].(map[string]any)["prompts"].([]any)
	if len(prompts) != 1 || prompts[0].(map[string]any)["name"] != "rsi_loop" {
		t.Fatalf("prompts/list = %v", list)
	}
	got := rpc("prompts/get", map[string]any{"name": "rsi_loop"})
	msgs, _ := got["result"].(map[string]any)["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("prompts/get = %v", got)
	}
	text, _ := msgs[0].(map[string]any)["content"].(map[string]any)["text"].(string)
	if text != skills.EgoRSI || !strings.HasPrefix(text, "---\nname: ego-rsi\n") {
		t.Errorf("rsi_loop returned %d bytes, want the embedded skill (%d)", len(text), len(skills.EgoRSI))
	}
}
