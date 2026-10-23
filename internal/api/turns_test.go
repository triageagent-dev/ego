package api

import (
	"testing"

	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

func ccSpan(id, op string, startMs, durMs int64, tags map[string]any, children ...*tracepkg.Span) *tracepkg.Span {
	if tags == nil {
		tags = map[string]any{}
	}
	return &tracepkg.Span{TraceID: "t1", SpanID: id, OperationName: op, StartTime: startMs * 1000, Duration: durMs * 1000, Tags: tags, Children: children}
}

func TestTurnOfSumsHowTheTimeWentAndWhatFailed(t *testing.T) {
	llm := func(id string, in, out, cache int64) *tracepkg.Span {
		return ccSpan(id, "claude_code.llm_request", 0, 3000, map[string]any{
			"gen_ai.request.model": "m", "gen_ai.usage.input_tokens": in, "gen_ai.usage.output_tokens": out,
			"gen_ai.usage.cache_read.input_tokens": cache,
		})
	}
	okTool := ccSpan("tool1", "claude_code.tool", 0, 800, map[string]any{"gen_ai.tool.name": "Bash"},
		ccSpan("w1", opToolWait, 0, 700, map[string]any{"decision": "accept"}),
		ccSpan("r1", "claude_code.tool.execution", 0, 100, nil))
	badTool := ccSpan("tool2", "claude_code.tool", 0, 300, map[string]any{"gen_ai.tool.name": "mcp__core__say_in_workspace"},
		ccSpan("w2", opToolWait, 0, 100, map[string]any{"decision": "accept"}),
		ccSpan("r2", "claude_code.tool.execution", 0, 200, map[string]any{"otel.status_code": "ERROR"}))
	root := ccSpan("root", "claude_code.interaction", 5000, 9000, map[string]any{"user_prompt_length": int64(245)},
		llm("l1", 1000, 50, 800), okTool, llm("l2", 2000, 60, 1900), badTool)

	got, ok := turnOf("s", 2, root)
	if !ok {
		t.Fatal("a Claude Code interaction is a turn")
	}
	if got.Requests != 2 || got.Tools != 2 || got.FailedTools != 1 || !got.Failed {
		t.Errorf("counts = %+v", got)
	}
	if got.ModelMs != 6000 || got.ApprovalMs != 800 || got.ToolMs != 300 {
		t.Errorf("time = model %d tool %d approval %d, want 6000 300 800", got.ModelMs, got.ToolMs, got.ApprovalMs)
	}
	if got.InputTokens != 3000 || got.OutputTokens != 110 || got.CacheReadTokens != 2700 || got.PromptChars != 245 {
		t.Errorf("tokens = %+v", got)
	}
	if c := got.ToolCalls["say_in_workspace"]; c.Calls != 1 || c.Failed != 1 || c.Server != "core" || c.Ms != 300 {
		t.Errorf("tool counts = %+v", got.ToolCalls)
	}
	if c := got.ModelCalls["m"]; c.Calls != 2 || c.Ms != 6000 || c.Failed != 0 {
		t.Errorf("model counts = %+v", got.ModelCalls)
	}
	if c := got.ToolCalls["Bash"]; c.Calls != 1 || c.Failed != 0 {
		t.Errorf("tool counts = %+v", got.ToolCalls)
	}
	if got.StartMs != 5000 || got.DurationMs != 9000 || got.Part != 2 {
		t.Errorf("place = %+v", got)
	}
}

func TestTurnOfSkipsAStraySpan(t *testing.T) {
	if _, ok := turnOf("s", 1, ccSpan("x", "http.request", 0, 10, nil)); ok {
		t.Fatal("a root with no model request or tool call is not a turn")
	}
}

func TestTurnOfCountsARejectedToolAsFailed(t *testing.T) {
	tool := ccSpan("t", "claude_code.tool", 0, 50, map[string]any{"gen_ai.tool.name": "Bash"},
		ccSpan("w", opToolWait, 0, 40, map[string]any{"decision": "reject"}))
	got, _ := turnOf("s", 1, ccSpan("root", "claude_code.interaction", 0, 100, nil, tool))
	if got.FailedTools != 1 {
		t.Fatalf("failed tools = %d, want 1", got.FailedTools)
	}
}
