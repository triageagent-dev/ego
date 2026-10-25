package mcptools

import (
	"strings"
	"testing"
)

func TestRenderEgoScoreReadsAtAGlance(t *testing.T) {
	score := 0.36
	rows := []TurnScore{{Agent: "spot", Tenant: "a/b", Hours: 24, Turns: 100, Score: &score, ToolCalls: 1000, FailedToolCalls: 100,
		ModelRequests: 800, InputTokens: 42_700_000, OutputTokens: 500_000, CachedShare: 0.72, MedianTurnMs: 22_000, P95TurnMs: 190_000, Awake: 0.01,
		FailingTools: []ToolRecord{{Tool: "logs", Server: "kube", Calls: 200, Failed: 36, AvgMs: 113}}}}
	got := RenderEgoScore(rows, ActivityQuery{Hours: 24})
	for _, want := range []string{"## ego · spot (a/b) · last 24h", "score: 36% (36 of 100 turns ended without a failure)",
		"tool_error_rate: 10% (100/1000) of tool calls failed", "tokens: input 42.7M (72% from cache), output 500.0k", "- kube/logs: 18% (36/200) failed, avg 113ms", "next: get_ego_tools"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
}

func TestRenderEgoScoreBySessionIsATable(t *testing.T) {
	score := 0.5
	rows := []TurnScore{{Agent: "spot", Hours: 24, Session: "spot~52", From: "2026-10-25T21:05:00Z", To: "2026-10-25T21:49:00Z", Turns: 4, Score: &score,
		ToolCalls: 10, FailedToolCalls: 2, InputTokens: 3_100_000, OutputTokens: 20_000, FailingTools: []ToolRecord{{Tool: "logs", Calls: 5, Failed: 2}}}}
	got := RenderEgoScore(rows, ActivityQuery{Hours: 24, By: ScoreBySession})
	if !strings.Contains(got, "| spot~52 | 10-25 21:05Z | 10-25 21:49Z | 4 | 50% | 20% (2/10) | 3.1M/20.0k | logs 2/5 |") {
		t.Fatalf("\n%s", got)
	}
}

func TestRenderEmpty(t *testing.T) {
	if got := RenderEgoScore(nil, ActivityQuery{Agent: "spot", Hours: 48, Session: "s~1"}); !strings.Contains(got, "no turns for spot in the last 48h in session s~1") {
		t.Fatal(got)
	}
}

func TestRenderErrorsAndTools(t *testing.T) {
	e := RenderEgoErrors([]EgoErrors{{Agent: "spot", Hours: 24, Errors: []ErrorGroup{{Tool: "logs", Server: "kube", Message: "exit status 1", Count: 3,
		FirstAt: "2026-10-25T20:00:00Z", LastAt: "2026-10-25T22:00:00Z", Session: "spot~52"}}}}, ActivityQuery{Hours: 24})
	if !strings.Contains(e, `- 3× kube/logs: "exit status 1"`) || !strings.Contains(e, "latest in session spot~52") {
		t.Fatal(e)
	}
	tl := RenderEgoTools([]EgoTools{{Agent: "spot", Hours: 24, Tools: []ToolRecord{{Tool: "logs", Server: "kube", Calls: 10, Failed: 2, Failure: 0.2, AvgMs: 1500}}}}, ActivityQuery{Hours: 24})
	if !strings.Contains(tl, "| kube/logs | 10 | 2 | 20% | 1.5s |") {
		t.Fatal(tl)
	}
}
