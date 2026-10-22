package api

import (
	"encoding/json"
	"github.com/triageagent-dev/ego/internal/judge"
	"github.com/triageagent-dev/ego/internal/online"
	tracepkg "github.com/triageagent-dev/ego/internal/trace"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSummarizeResultRowsNamesModels(t *testing.T) {
	rows := []runResultRowDTO{
		{EvalSetItemID: "a", EvaluatorName: "m", Status: "passed", Details: withSource(nil, "svc", "gemini-2.5-flash", 1200)},
		{EvalSetItemID: "b", EvaluatorName: "m", Status: "failed", Details: withSource(map[string]any{"x": 1}, "svc", "gemini-2.5-pro", 800)},
		{EvalSetItemID: "c", EvaluatorName: "m", Status: "passed", Details: withSource(nil, "svc", "gemini-2.5-flash", 1200)},
		{EvalSetItemID: "d", EvaluatorName: "m", Status: "passed"},
	}
	got := summarizeResultRows(rows)
	if want := []string{"gemini-2.5-flash", "gemini-2.5-pro"}; !reflect.DeepEqual(got.Models, want) {
		t.Fatalf("models = %v, want %v", got.Models, want)
	}
	if got.DurationMs != 3200 {
		t.Fatalf("duration = %d ms, want the scoring time of each session added up, 3200", got.DurationMs)
	}
	if got := summarizeResultRows(rows[3:]); got.Models != nil {
		t.Fatalf("a run without models has %v", got.Models)
	}
}

func TestDistinctModels(t *testing.T) {
	got := distinctModels([]traceResultDTO{{Model: "b"}, {Model: ""}, {Model: "a"}, {Model: "b"}})
	if want := []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("models = %v, want %v", got, want)
	}
	if distinctModels([]traceResultDTO{{}}) != nil {
		t.Fatal("no model should be nil")
	}
}

func TestBackfillRunModelsFromOnlineScores(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "backfill.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	run, err := store.CreateRun(runSpecSummaryDTO{Approach: "online", EvalSet: map[string]any{"name": "online · acme · chat"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(run.RunID, "succeeded", &runSummaryDTO{TraceCount: 2}, nil); err != nil {
		t.Fatal(err)
	}
	for i, model := range []string{"gemini-b", "gemini-a"} {
		e := onlineEvalRow{SessionID: string(rune('a' + i)), TraceID: "t", Tenant: "acme", Kind: "chat", RunID: run.RunID}
		if err := store.InsertOnlineEval(e, "v1", model, []onlineScoreRow{{Metric: "m", Status: "passed"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := backfillRunModels(store.db); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.GetRun(run.RunID)
	if err != nil || !ok {
		t.Fatalf("GetRun: %v %v", ok, err)
	}
	if want := []string{"gemini-a", "gemini-b"}; !reflect.DeepEqual(got.Summary.Models, want) || got.Summary.TraceCount != 2 {
		t.Fatalf("summary = %+v, want models %v and the trace count kept", got.Summary, want)
	}
}

func TestListRunsFollowsThePickedTenant(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "tenantruns.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, tenant := range []string{"core/acme", "core/beta", "edge/acme"} {
		run, err := store.CreateRun(runSpecSummaryDTO{Approach: "online", EvalSet: map[string]any{"name": "online · " + tenant}})
		if err != nil {
			t.Fatal(err)
		}
		e := onlineEvalRow{SessionID: tenant, TraceID: "t", Tenant: tenant, Kind: "chat", RunID: run.RunID}
		if err := store.InsertOnlineEval(e, "v1", "m", nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.CreateRun(runSpecSummaryDTO{Approach: "evaluate"}); err != nil {
		t.Fatal(err)
	}
	count := func(tenant string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/runs?tenant="+tenant, nil)
		rec := httptest.NewRecorder()
		listRunsHandler(store).ServeHTTP(rec, req)
		var body struct {
			Data []runDTO `json:"data"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return len(body.Data)
	}
	for tenant, want := range map[string]int{"": 4, "core/acme": 1, "core/": 2, "edge/": 1, "nobody": 0} {
		if got := count(tenant); got != want {
			t.Errorf("tenant %q lists %d runs, want %d", tenant, got, want)
		}
	}
}

func TestResolvedEvaluatorsFillWhatTheProfileLeavesEmpty(t *testing.T) {
	custom := 0.9
	p := online.Profile{Evaluators: []online.Evaluator{
		{Name: "jev_grounded"},
		{Name: "rubric_based_final_response_quality_v1"},
		{Name: "hallucinations_v1", JudgeModel: "gemini-x", Threshold: &custom},
		{Name: "tool_trajectory_avg_score"},
		{Name: "safety_v1"},
	}}
	got := resolvedEvaluators(p, []metricResultDTO{{MetricName: "jev_grounded", JudgeModel: "jev-model"}})
	want := []struct {
		model, match string
		threshold    float64
	}{
		{"jev-model", "", defaultThreshold},
		{judge.DefaultModel, "", defaultThreshold},
		{"gemini-x", "", custom},
		{"", "EXACT", defaultThreshold},
		{vertexEvalServiceLabel, "", defaultThreshold},
	}
	for i, w := range want {
		g := got[i]
		if g.Type != "builtin" || g.JudgeModel != w.model || g.TrajectoryMatchType != w.match || g.Threshold == nil || *g.Threshold != w.threshold {
			t.Errorf("%s resolved to %+v, want model %q match %q threshold %v", g.Name, g, w.model, w.match, w.threshold)
		}
	}
	if p.Evaluators[0].Threshold != nil {
		t.Error("the profile itself was changed")
	}
}

func TestSessionSummaryCountsClaudeCodePrompts(t *testing.T) {
	span := func(op string) *tracepkg.Span { return &tracepkg.Span{OperationName: op} }
	s := &Session{ID: "s", Spans: []*tracepkg.Span{
		span("claude_code.interaction"), span("claude_code.llm_request"), span("claude_code.tool"),
		span("claude_code.interaction"), span("claude_code.llm_request"),
	}}
	if got := summarize(s).Turns; got != 2 {
		t.Fatalf("turns = %d, want 2", got)
	}
	other := &Session{ID: "o", Spans: []*tracepkg.Span{span("invoke_agent"), span("chat")}}
	if got := summarize(other).Turns; got != 0 {
		t.Fatalf("a session that is not Claude Code's has %d turns", got)
	}
}

func TestSessionSummaryGivesTheTokenSplit(t *testing.T) {
	llm := func(in, out int64) *tracepkg.Span {
		return &tracepkg.Span{OperationName: "chat", Tags: map[string]any{
			"gen_ai.request.model": "m", "gen_ai.usage.input_tokens": in, "gen_ai.usage.output_tokens": out,
		}}
	}
	sum := summarize(&Session{ID: "s", Spans: []*tracepkg.Span{llm(1000, 10), llm(3000, 20)}})
	if sum.InputTokens != 4000 || sum.OutputTokens != 30 || sum.TotalTokens != 4030 {
		t.Fatalf("tokens = in %d out %d total %d", sum.InputTokens, sum.OutputTokens, sum.TotalTokens)
	}
}
