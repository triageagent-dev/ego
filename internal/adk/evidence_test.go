package adk

import (
	"strings"
	"testing"

	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

func llmCall(id string, tokens int, input string, extra map[string]any) *tracepkg.Span {
	tags := map[string]any{
		"openinference.span.kind":   "LLM",
		"gen_ai.request.model":      "m",
		"gen_ai.usage.input_tokens": float64(tokens),
		"gen_ai.input.messages":     `[{"role":"user","content":"` + input + `"}]`,
		"gen_ai.output.messages":    `[{"role":"assistant","content":"done"}]`,
	}
	for k, v := range extra {
		tags[k] = v
	}
	return &tracepkg.Span{TraceID: "t", SpanID: id, OperationName: "llm.chat.completions", Tags: tags}
}

// More input tokens than the trace has bytes of input means part of the input was never recorded; a sender can say
// so itself. A turn with tool calls is left alone: the tools offered count as input and are rarely recorded.
func TestConvert_InputCut(t *testing.T) {
	cases := []struct {
		name  string
		span  *tracepkg.Span
		tools bool
		want  bool
	}{
		{"cut: 52590 tokens over 8000 bytes", llmCall("a", 52590, strings.Repeat("x", 8000), nil), false, true},
		{"whole: more bytes than tokens", llmCall("a", 2500, strings.Repeat("x", 9000), nil), false, false},
		{"small call: not guessed", llmCall("a", 1500, "hi", nil), false, false},
		{"marked by the sender", llmCall("a", 100, "hi", map[string]any{EgoInputTruncated: true}), false, true},
		{"tools: not guessed", llmCall("a", 52590, "hi", nil), true, false},
	}
	for _, c := range cases {
		spans := []*tracepkg.Span{c.span}
		if c.tools {
			tool := &tracepkg.Span{TraceID: "t", SpanID: "tool", ParentSpanID: "a", OperationName: "execute_tool search",
				Tags: map[string]any{"gen_ai.operation.name": "execute_tool", "gen_ai.tool.name": "search"}}
			c.span.Children = []*tracepkg.Span{tool}
			spans = append(spans, tool)
		}
		got := ConvertGenAITrace(&tracepkg.Trace{TraceID: "t", RootSpans: []*tracepkg.Span{c.span}, AllSpans: spans})
		if len(got.Invocations) != 1 {
			t.Fatalf("%s: %d invocations, warnings %v", c.name, len(got.Invocations), got.Warnings)
		}
		if got.Invocations[0].InputCut != c.want {
			t.Errorf("%s: InputCut = %v, want %v", c.name, got.Invocations[0].InputCut, c.want)
		}
	}
}
