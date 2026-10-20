package incremental

import (
	"testing"

	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

func llmSpan(spanID string, tags map[string]any) *tracepkg.Span {
	base := map[string]any{"otel.scope.name": "gcp.vertex.agent"}
	for k, v := range tags {
		base[k] = v
	}
	return &tracepkg.Span{SpanID: spanID, OperationName: "call_llm", StartTime: 1_000_000, Duration: 500_000, Tags: base}
}

func TestProcessSpan_UserInputEmittedOnce(t *testing.T) {
	e := New()
	span := llmSpan("s1", map[string]any{
		"gcp.vertex.agent.llm_request": `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
	})

	first := e.ProcessSpan(span)
	if len(first) != 1 || first[0].Type != "user_input" {
		t.Fatalf("first ProcessSpan = %+v, want one user_input update", first)
	}

	// A second span for the same invocation (same parentless span, so its own SpanID is the invocation ID) with
	// identical text must not re-emit user_input.
	second := e.ProcessSpan(span)
	if len(second) != 0 {
		t.Errorf("second ProcessSpan (same span again) = %+v, want no updates (deduped)", second)
	}
}

func TestProcessSpan_ToolCallAndResultPairing(t *testing.T) {
	e := New()
	span := &tracepkg.Span{
		SpanID: "tool-1", ParentSpanID: "inv-1", OperationName: "execute_tool lookup",
		StartTime: 1_000_000, Duration: 200_000,
		Tags: map[string]any{
			"gen_ai.tool.name":                "lookup",
			"gcp.vertex.agent.tool_call_args": `{"q":"weather"}`,
			"gcp.vertex.agent.tool_response":  `{"result":"sunny"}`,
		},
	}

	updates := e.ProcessSpan(span)
	if len(updates) != 2 {
		t.Fatalf("got %d updates, want 2 (tool_call, tool_result); got %+v", len(updates), updates)
	}
	if updates[0].Type != "tool_call" || updates[0].ToolCall["name"] != "lookup" {
		t.Errorf("updates[0] = %+v, want tool_call for lookup", updates[0])
	}
	if updates[1].Type != "tool_result" || updates[1].Response["result"] != "sunny" {
		t.Errorf("updates[1] = %+v, want tool_result with result=sunny", updates[1])
	}

	// Re-processing the same tool span must not re-emit (seen_tool_calls dedup).
	if got := e.ProcessSpan(span); len(got) != 0 {
		t.Errorf("re-processed tool span = %+v, want no updates (deduped)", got)
	}
}

func TestProcessSpan_IgnoresNonAgentSpans(t *testing.T) {
	e := New()
	span := &tracepkg.Span{SpanID: "http-1", OperationName: "GET", Tags: map[string]any{"http.method": "GET"}}
	if got := e.ProcessSpan(span); len(got) != 0 {
		t.Errorf("plain HTTP span produced updates: %+v, want none", got)
	}
}

func TestProcessSpan_TokenTotalsAccumulate(t *testing.T) {
	e := New()
	span1 := llmSpan("s1", map[string]any{
		"gcp.vertex.agent.llm_response": `{"usage_metadata":{"prompt_token_count":5,"candidates_token_count":3}}`,
	})
	updates := e.ProcessSpan(span1)

	var tokenUpdates int
	for _, u := range updates {
		if u.Type == "token_update" {
			tokenUpdates++
			if u.InputTokens != 5 || u.OutputTokens != 3 {
				t.Errorf("token_update = %+v, want inputTokens=5 outputTokens=3", u)
			}
		}
	}
	if tokenUpdates != 1 {
		t.Fatalf("got %d token_update events, want 1", tokenUpdates)
	}
}
