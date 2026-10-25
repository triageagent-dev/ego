package api

import (
	"math"
	"testing"

	"github.com/triageagent-dev/ego/internal/mcptools"
	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

func TestSummarizeTurns(t *testing.T) {
	const hour = 3_600_000
	base := int64(1_700_000_000_000) / hour * hour
	res := TurnsResult{
		Agent: "spot", FromMs: base, ToMs: base + 2*hour,
		Turns: []TurnRow{
			{StartMs: base + 1000, DurationMs: 2000, Requests: 3, InputTokens: 1000, CacheReadTokens: 800, OutputTokens: 50,
				ToolCalls: map[string]ToolCount{"logs": {Calls: 2, Failed: 1, Ms: 200, Server: "kube"}, "finish": {Calls: 1}}, FailedTools: 1},
			{StartMs: base + 1500, DurationMs: 2000, Requests: 2, InputTokens: 500, OutputTokens: 20,
				ToolCalls: map[string]ToolCount{"logs": {Calls: 1, Ms: 100, Server: "kube"}}},
			{StartMs: base + hour + 10, DurationMs: 1000, Requests: 1, Failed: true},
		},
	}
	s := summarizeTurns(res, 24)
	if s.Turns != 3 || s.TurnsWithoutFailure != 1 || s.Success == nil || math.Abs(*s.Success-1.0/3) > 1e-9 {
		t.Fatalf("turns %d without failure %d success %v", s.Turns, s.TurnsWithoutFailure, s.Success)
	}
	if s.ToolCalls != 4 || s.FailedToolCalls != 1 || s.ModelRequests != 6 {
		t.Fatalf("calls %d failed %d requests %d", s.ToolCalls, s.FailedToolCalls, s.ModelRequests)
	}
	if s.InputTokens != 1500 || s.OutputTokens != 70 || math.Abs(s.CachedShare-800.0/1500) > 1e-9 {
		t.Fatalf("tokens in %d out %d cached %v", s.InputTokens, s.OutputTokens, s.CachedShare)
	}
	if len(s.Tools) != 2 || s.Tools[0].Tool != "logs" || s.Tools[0].Calls != 3 || s.Tools[0].Failed != 1 || s.Tools[0].AvgMs != 100 {
		t.Fatalf("tools %+v", s.Tools)
	}
	if len(s.Hourly) != 2 || s.Hourly[0].Turns != 2 || s.Hourly[0].Failed != 1 || s.Hourly[1].Failed != 1 {
		t.Fatalf("hourly %+v", s.Hourly)
	}
	// Two turns overlap from 1.5s to 3s: the agent was awake from 1s to 3.5s, then 1s more in the second hour.
	if want := 3500.0 / (2 * hour); math.Abs(s.Awake-want) > 1e-9 {
		t.Fatalf("awake %v, want %v", s.Awake, want)
	}
	if s.MedianTurnMs != 2000 || s.P95TurnMs != 2000 {
		t.Fatalf("median %d p95 %d", s.MedianTurnMs, s.P95TurnMs)
	}
}

func TestSummarizeNoTurns(t *testing.T) {
	s := summarizeTurns(TurnsResult{Agent: "spot", FromMs: 0, ToMs: 1000}, 24)
	if s.Turns != 0 || s.Success != nil || len(s.Tools) != 0 || s.Hourly == nil {
		t.Fatalf("%+v", s)
	}
}

func TestScoreOfListsOnlyFailingTools(t *testing.T) {
	sum := mcptools.ActivitySummary{Agent: "spot", Turns: 10, ToolCalls: 40, FailedToolCalls: 10, Tools: []mcptools.ToolRecord{
		{Tool: "a", Calls: 20, Failed: 6}, {Tool: "b", Calls: 10, Failed: 3}, {Tool: "c", Calls: 6, Failed: 1},
		{Tool: "d", Calls: 4}, {Tool: "e", Calls: 2, Failed: 1}, {Tool: "f", Calls: 2, Failed: 1}, {Tool: "g", Calls: 2, Failed: 1},
	}}
	sc := scoreOf(sum)
	if sc.ErrorRate != 0.25 || len(sc.FailingTools) != maxFailingTools || sc.FailingTools[0].Tool != "a" {
		t.Fatalf("%+v", sc)
	}
	for _, f := range sc.FailingTools {
		if f.Failed == 0 {
			t.Fatalf("a tool without failures listed: %+v", f)
		}
	}
	if none := scoreOf(mcptools.ActivitySummary{Agent: "x"}); none.ErrorRate != 0 || none.FailingTools == nil {
		t.Fatalf("%+v", none)
	}
}

func TestErrorGroupsMergeDigitsAndOrderByCount(t *testing.T) {
	res := TurnsResult{Turns: []TurnRow{
		{Session: "s1", Errors: []TurnError{
			{Tool: "logs", Server: "kube", Message: "logs pod-1 failed: exit status 1", AtMs: 1000},
			{Tool: "query", Message: "timeout", AtMs: 1500},
		}},
		{Session: "s2", Errors: []TurnError{
			{Tool: "logs", Server: "kube", Message: "logs pod-2 failed: exit status 1", AtMs: 3000},
			{Tool: "logs", Server: "kube", Message: "logs pod-3 failed: exit status 1", AtMs: 2000},
		}},
	}}
	g := errorGroups(res)
	if len(g) != 2 || g[0].Tool != "logs" || g[0].Count != 3 || g[1].Tool != "query" || g[1].Count != 1 {
		t.Fatalf("%+v", g)
	}
	if g[0].Session != "s2" || g[0].Message != "logs pod-2 failed: exit status 1" {
		t.Fatalf("the latest one gives the example and the session: %+v", g[0])
	}
	if g[0].FirstAt >= g[0].LastAt {
		t.Fatalf("first %s last %s", g[0].FirstAt, g[0].LastAt)
	}
}

func TestErrorTextReadsTheToolResult(t *testing.T) {
	sp := &tracepkg.Span{Tags: map[string]any{
		"otel.status_code":        "ERROR",
		"otel.status_description": "tool error",
		"gen_ai.tool.call.result": `{"isError":true,"result":"[Kubernetes] logs x failed: exit status 1"}`,
	}}
	if got := errorText(sp); got != "[Kubernetes] logs x failed: exit status 1" {
		t.Fatalf("%q", got)
	}
	plain := &tracepkg.Span{Tags: map[string]any{"otel.status_code": "ERROR", "exception.message": "boom"}}
	if got := errorText(plain); got != "boom" {
		t.Fatalf("%q", got)
	}
	if got := errorText(&tracepkg.Span{Tags: map[string]any{"decision": "reject"}}); got != "rejected" {
		t.Fatalf("%q", got)
	}
}
