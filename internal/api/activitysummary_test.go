package api

import (
	"math"
	"testing"
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
