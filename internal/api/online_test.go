package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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
	prompts []string
}

func (j *usageJudge) Generate(ctx context.Context, prompt string) (string, error) {
	j.calls++
	j.prompts = append(j.prompts, prompt)
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
	// The fixtures are one-line exchanges; the not-worth-judging rule has its own test below.
	cfg.Sampling.Trivial.Disabled = true
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
		// The findings pass shares the sink; this test is about the alerts.
		if m["type"] != online.WebhookType {
			return
		}
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

func TestOnlineEngine_SkipsSessionsNotWorthJudging(t *testing.T) {
	e, sessions, _, fake := newTestEngine(t, testOnlineConfig)
	e.cfg.Sampling.Trivial.Disabled = false
	now := time.Date(2026, 10, 4, 12, 30, 0, 0, time.UTC)
	e.now = func() time.Time { return now }

	sessions.Ingest(onlineSessionBody("cccccccccccc0003", now.UnixMilli(), "rca", "bye", "Goodbye, talk soon."))
	e.process(context.Background(), "otlp-cccccccccccc")
	if fake.calls != 0 {
		t.Errorf("a goodbye was judged %d time(s)", fake.calls)
	}
	st := e.status()
	if st.Counts[online.SkipTrivial] != 1 || st.Counts["sampled_base"] != 0 {
		t.Errorf("counts = %v, want one trivial skip and nothing sampled", st.Counts)
	}

	// It used none of the kind's allowance: a real session right after is still sampled.
	long := "The payment service is out of memory: the heap grew past its limit after the 14:02 deploy, and the pod was killed three times in ten minutes."
	sessions.Ingest(onlineSessionBody("dddddddddddd0004", now.UnixMilli(), "rca", "why is the checkout down right now? give me the cause", long))
	e.process(context.Background(), "otlp-dddddddddddd")
	if e.status().Counts["sampled_base"] != 1 || fake.calls == 0 {
		t.Errorf("the real session was not judged: counts = %v", e.status().Counts)
	}
}

// The count of sessions scored in 24 hours comes from the store, so a restart keeps it, and it leaves out what is
// older than a day and what a tenant-limited role may not see.
func TestOnlineStatus_SessionsScored24hIsPersisted(t *testing.T) {
	e, _, archive, _ := newTestEngine(t, testOnlineConfig)
	now := time.Date(2026, 10, 4, 12, 30, 0, 0, time.UTC)
	e.now = func() time.Time { return now }

	insert := func(id, tenant string, at time.Time) {
		t.Helper()
		if err := archive.InsertOnlineEval(onlineEvalRow{SessionID: id, AtMs: at.UnixMilli(), Tenant: tenant, Kind: "rca"}, "v1", "m", nil); err != nil {
			t.Fatal(err)
		}
	}
	insert("s1", "core/acme", now.Add(-1*time.Hour))
	insert("s2", "core/acme", now.Add(-23*time.Hour))
	insert("s3", "core/other", now.Add(-2*time.Hour))
	insert("old", "core/acme", now.Add(-25*time.Hour))

	if got := e.status().SessionsScored24h; got != 3 {
		t.Errorf("sessions scored in 24h = %d, want 3 (the one from 25 hours ago is out)", got)
	}
	limited := RoleInfo{Role: RoleViewer, Tenants: []string{"core/acme"}}
	if got := e.statusFor(limited).SessionsScored24h; got != 2 {
		t.Errorf("a role limited to one tenant sees %d, want 2", got)
	}

	// A restart: a new engine over the same store starts its in-memory counters at zero but the count stays.
	e2 := NewOnlineEngine(e.cfg, archive, e.sessions, "tenant.id")
	e2.now = e.now
	if got := e2.status().SessionsScored24h; got != 3 {
		t.Errorf("after a restart the count = %d, want 3", got)
	}
}

// withThreadAttr puts ego.thread on every resource of an export.
func withThreadAttr(body map[string]any, thread string) map[string]any {
	res := body["resourceSpans"].([]any)[0].(map[string]any)["resource"].(map[string]any)
	res["attributes"] = append(res["attributes"].([]any),
		map[string]any{"key": threadAttr, "value": map[string]any{"stringValue": thread}})
	return body
}

// The online engine hears about a segment once, when it closes, and judges part n with the end of part n-1 as
// fenced context.
func TestOnlineEngine_ScoresASegmentAtCloseWithTheEarlierPart(t *testing.T) {
	e, sessions, archive, fake := newTestEngine(t, testOnlineConfig)
	sessions.SetOnComplete(e.Enqueue)
	sessions.idleTimeout, sessions.completionGrace = time.Hour, time.Hour
	now := time.Date(2026, 10, 4, 12, 30, 0, 0, time.UTC)
	e.now = func() time.Time { return now }
	ctx := context.Background()

	sessions.Ingest(withThreadAttr(onlineSessionBody("aaaaaaaaaaaa0001", now.UnixMilli(), "rca", "why is checkout down?", "The payment service is out of memory."), "pod"))
	if len(e.queue) != 0 {
		t.Fatalf("queued %d before the segment closed, want none", len(e.queue))
	}
	sessions.completeSession("pod") // the quiet state is not the close
	if len(e.queue) != 0 {
		t.Fatalf("queued %d at the quiet state, want none", len(e.queue))
	}
	sessions.Ingest(withThreadAttr(onlineSessionBody("bbbbbbbbbbbb0002", now.Add(3*time.Hour).UnixMilli(), "rca", "and now?", "Memory is back to normal."), "pod"))
	if len(e.queue) != 1 || <-e.queue != "pod" {
		t.Fatal("part 1 must be queued exactly once, when part 2 opened")
	}

	e.process(ctx, "pod")
	if fake.calls == 0 {
		t.Fatal("part 1 was not judged")
	}
	for _, p := range fake.prompts {
		if strings.Contains(p, "<earlier_part>") {
			t.Fatalf("part 1 has no earlier part, yet its prompt carries one: %q", p)
		}
	}
	fake.prompts = nil
	e.process(ctx, "pod~2")
	if len(fake.prompts) == 0 {
		t.Fatal("part 2 was not judged")
	}
	for _, p := range fake.prompts {
		for _, want := range []string{"<earlier_part>", "why is checkout down?", "The payment service is out of memory."} {
			if !strings.Contains(p, want) {
				t.Errorf("part 2's prompt lacks %q: %q", want, p)
			}
		}
	}
	// Scored once each, whatever is enqueued again.
	e.process(ctx, "pod")
	if st := e.status(); st.Counts[online.SkipAlreadySeen] != 1 {
		t.Errorf("counts = %v, want one skip as already seen", st.Counts)
	}
	rows, _ := archive.OnlineSamples(0)
	if len(rows) != 2 {
		t.Errorf("%d scores stored, want 2 (one per segment)", len(rows))
	}
}
