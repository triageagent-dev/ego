package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/triageagent-dev/ego/internal/judge"
	"github.com/triageagent-dev/ego/internal/online"
)

// onlineSessionBody is one OTLP export: an AGENT turn with input/output text
// and a child LLM span carrying tokens, a tenant and gen_ai.agent.name.
func onlineSessionBody(traceID string, startMs int64, kind, input, output string) map[string]any {
	ns := func(ms int64) string { return strconvI(ms * 1_000_000) }
	attr := func(k string, v map[string]any) map[string]any { return map[string]any{"key": k, "value": v} }
	str := func(s string) map[string]any { return map[string]any{"stringValue": s} }
	num := func(n int) map[string]any { return map[string]any{"intValue": strconvI(int64(n))} }
	return map[string]any{"resourceSpans": []any{map[string]any{
		"resource": map[string]any{"attributes": []any{
			attr("service.name", str("ops-agent")), attr("service.version", str("v1")),
		}},
		"scopeSpans": []any{map[string]any{"scope": map[string]any{}, "spans": []any{
			map[string]any{
				"traceId": traceID, "spanId": "agent-" + traceID, "name": "agent.turn",
				"startTimeUnixNano": ns(startMs), "endTimeUnixNano": ns(startMs + 3000),
				"attributes": []any{
					attr("openinference.span.kind", str("AGENT")),
					attr("input.value", str(input)),
					attr("output.value", str(output)),
				},
			},
			map[string]any{
				"traceId": traceID, "spanId": "llm-" + traceID, "parentSpanId": "agent-" + traceID, "name": "llm.call",
				"startTimeUnixNano": ns(startMs + 10), "endTimeUnixNano": ns(startMs + 2010),
				"attributes": []any{
					attr("openinference.span.kind", str("LLM")),
					attr("llm.model_name", str("gemini-2.5-flash")),
					attr("llm.token_count.prompt", num(800)),
					attr("llm.token_count.completion", num(80)),
					attr("input.value", str(input)),
					attr("output.value", str(output)),
					attr("tenant.id", str("shop")),
					attr("gen_ai.agent.name", str(kind)),
				},
			},
		}}},
	}}}
}

// usageJudge answers every rubric with yes and reports 250 tokens a call.
type usageJudge struct {
	onUsage func(int64)
	calls   int
	reply   string // "" answers yes
}

func (j *usageJudge) Generate(ctx context.Context, prompt string) (string, error) {
	j.calls++
	j.onUsage(250)
	if j.reply != "" {
		return j.reply, nil
	}
	return "ID: rubric_0\nProperty: helpful\nRationale: fine\nVerdict: yes\n", nil
}

func newTestEngine(t *testing.T, cfgJSON string) (*OnlineEngine, *SessionStore, *SQLiteStore, *usageJudge) {
	t.Helper()
	archive, err := NewSQLiteStore(filepath.Join(t.TempDir(), "online.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { archive.Close() })
	sessions := NewSessionStoreWithArchive(nil, archive, SessionLimits{UsageTenantAttr: "tenant.id"})
	cfg, err := online.Parse([]byte(cfgJSON))
	if err != nil {
		t.Fatal(err)
	}
	e := NewOnlineEngine(cfg, archive, sessions, "tenant.id")
	fake := &usageJudge{}
	e.newJudge = func(ctx context.Context, model string, onUsage func(int64)) (judge.Model, error) {
		fake.onUsage = onUsage
		return fake, nil
	}
	return e, sessions, archive, fake
}

const testOnlineConfig = `{
	"budget": {"judgeTokensPerDay": 100000},
	"profiles": [{"kind": "rca", "evaluators": [
		{"name": "rubric_based_final_response_quality_v1", "rubrics": ["The response is helpful"]}
	]}]
}`

func TestOnlineEngine_ScoresStoresAndRecordsRun(t *testing.T) {
	e, sessions, archive, fake := newTestEngine(t, testOnlineConfig)
	now := time.Date(2026, 10, 4, 12, 30, 0, 0, time.UTC)
	e.now = func() time.Time { return now }

	sessions.Ingest(onlineSessionBody("aaaaaaaaaaaa0001", now.UnixMilli(), "rca", "why is checkout down?", "The payment service is out of memory."))
	sessions.Ingest(onlineSessionBody("bbbbbbbbbbbb0002", now.UnixMilli(), "voice", "hello", "hi"))
	ctx := context.Background()
	e.process(ctx, "otlp-aaaaaaaaaaaa")
	e.process(ctx, "otlp-bbbbbbbbbbbb")
	e.process(ctx, "otlp-aaaaaaaaaaaa") // completed again after a reopen

	if fake.calls == 0 {
		t.Fatal("judge never called")
	}
	st := e.status()
	if st.Counts["sampled_base"] != 1 || st.Counts[online.SkipNoProfile] != 1 || st.Counts[online.SkipAlreadySeen] != 1 {
		t.Errorf("counts = %v", st.Counts)
	}
	if st.Budget.Spent.JudgeTokens != int64(250*fake.calls) {
		t.Errorf("spent = %+v, want %d judge tokens", st.Budget.Spent, 250*fake.calls)
	}

	rows, err := archive.OnlineSamples(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Key != (online.SeriesKey{Tenant: "shop", Kind: "rca", Metric: "rubric_based_final_response_quality_v1"}) ||
		rows[0].Sample.Score != 1 || rows[0].Sample.Version != "v1" {
		t.Fatalf("samples = %+v", rows)
	}

	runs, _ := archive.ListRuns([]string{"running"}, 10, "")
	if len(runs) != 1 || runs[0].Spec.Approach != "online" || runs[0].Spec.EvalSet["name"] != "online · shop · rca" {
		t.Fatalf("running runs = %+v", runs)
	}
	// The next hour's tick closes the run with a summary.
	now = now.Add(time.Hour)
	e.Tick(ctx)
	run, _, _ := archive.GetRun(runs[0].RunID)
	if run.Status != "succeeded" || run.Summary == nil || run.Summary.TraceCount != 1 ||
		run.Summary.ResultCounts.Passed != 1 || len(run.Summary.Agents) != 1 || run.Summary.Agents[0] != "ops-agent" {
		t.Errorf("finished run = %+v summary %+v", run, run.Summary)
	}
}

func TestOnlineEngine_BudgetStopsScoring(t *testing.T) {
	e, sessions, _, fake := newTestEngine(t, `{
		"budget": {"judgeTokensPerDay": 3000},
		"profiles": [{"kind": "rca", "evaluators": [{"name": "rubric_based_final_response_quality_v1", "rubrics": ["r"]}]}]
	}`)
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	e.now = func() time.Time { return now }
	sessions.Ingest(onlineSessionBody("cccccccccccc0001", now.UnixMilli(), "rca", "a", "b"))
	e.process(context.Background(), "otlp-cccccccccccc")
	if fake.calls != 0 || e.status().Counts[online.SkipBudget] != 1 {
		t.Errorf("first estimate (4000) exceeds the 3000 cap: calls=%d counts=%v", fake.calls, e.status().Counts)
	}
}

func TestOnlineEngine_DetectsRegressionAndNotifies(t *testing.T) {
	var mu sync.Mutex
	var posts []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		mu.Lock()
		posts = append(posts, m)
		mu.Unlock()
	}))
	defer srv.Close()
	e, _, archive, _ := newTestEngine(t, `{
		"budget": {"jevCallsPerDay": 100},
		"profiles": [{"kind": "rca", "evaluators": [{"name": "jev_grounded"}]}],
		"detector": {"window": "6h", "baseline": "48h", "minSamples": 5, "minDrop": 0.1},
		"sinks": [{"type": "webhook", "url": "`+srv.URL+`"}]
	}`)
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	e.now = func() time.Time { return now }

	add := func(id string, at time.Time, score float64, version string) {
		s := score
		if err := archive.InsertOnlineEval(onlineEvalRow{SessionID: id, AtMs: at.UnixMilli(), Tenant: "shop", Kind: "rca"},
			version, "m", []onlineScoreRow{{Metric: "jev_grounded", Score: &s, Status: "passed"}}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 10; i++ {
		add("base"+strconvI(int64(i)), now.Add(-24*time.Hour+time.Duration(i)*time.Minute), 0.9+float64(i%2)*0.05, "v1")
	}
	for i := 0; i < 6; i++ {
		add("cur"+strconvI(int64(i)), now.Add(-time.Hour+time.Duration(i)*time.Minute), 0.4+float64(i%2)*0.05, "v2")
	}
	ctx := context.Background()
	e.Tick(ctx)
	e.Tick(ctx) // same result: no second notification

	alerts, _ := archive.OnlineAlerts("open", 10)
	if len(alerts) != 1 || alerts[0].Last.Suspect != "version v2" {
		t.Fatalf("open alerts = %+v", alerts)
	}
	mu.Lock()
	if len(posts) != 1 || posts[0]["event"] != "open" || posts[0]["metric"] != "jev_grounded" {
		t.Errorf("posts = %v", posts)
	}
	mu.Unlock()

	// Recovery: the current window fills with good scores; two quiet checks resolve.
	now = now.Add(7 * time.Hour)
	for i := 0; i < 6; i++ {
		add("ok"+strconvI(int64(i)), now.Add(-time.Hour+time.Duration(i)*time.Minute), 0.92, "v3")
	}
	e.Tick(ctx)
	e.Tick(ctx)
	if open, _ := archive.OnlineAlerts("open", 10); len(open) != 0 {
		t.Errorf("still open after recovery: %+v", open)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(posts) != 2 || posts[1]["event"] != "resolved" {
		t.Errorf("posts after recovery = %v", posts)
	}
}

func TestOnlineHandlers_UnconfiguredIs503(t *testing.T) {
	for _, h := range []http.HandlerFunc{onlineStatusHandler(nil), onlineSeriesHandler(nil), onlineAlertsHandler(nil)} {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("code = %d", rec.Code)
		}
	}
}

// A judge reply with no readable verdict is an errored result without a
// score, never a 0 the detector would read as a quality drop.
func TestOnlineEngine_UnreadableJudgeIsErrorNotZero(t *testing.T) {
	e, sessions, archive, fake := newTestEngine(t, testOnlineConfig)
	fake.reply = "no verdict here"
	now := time.Date(2026, 10, 4, 12, 30, 0, 0, time.UTC)
	e.now = func() time.Time { return now }
	sessions.Ingest(onlineSessionBody("dddddddddddd0001", now.UnixMilli(), "rca", "q", "a"))
	e.process(context.Background(), "otlp-dddddddddddd")

	if fake.calls != 2 {
		t.Errorf("judge calls = %d, want 2 (one retry)", fake.calls)
	}
	if rows, _ := archive.OnlineSamples(0); len(rows) != 0 {
		t.Errorf("samples = %+v, want none", rows)
	}
	runs, _ := archive.ListRuns([]string{"running"}, 10, "")
	results, _ := archive.GetRunResults(runs[0].RunID)
	if len(results) != 1 || results[0].Status != "errored" || results[0].Score != nil || results[0].ErrorText == nil {
		t.Fatalf("results = %+v", results)
	}
}
