package adk_test

import (
	"testing"

	"github.com/triageagent-dev/ego/internal/adk"
	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

// buildOpenInferenceTrace reproduces triage-core's real span shape (see
// internal/voice/session.go in the core repo): a "voice.session" root span
// with no input.value/output.value, "llm.gemini_realtime" child spans each
// carrying exactly one of input.value (user utterance) or output.value
// (assistant utterance), and "tool.<name>" child spans carrying
// tool.name/input.value(args)/output.value(result).
func buildOpenInferenceTrace() *tracepkg.Trace {
	root := &tracepkg.Span{
		TraceID:       "trace-1",
		SpanID:        "root",
		OperationName: "voice.session",
		StartTime:     1_000_000,
		Duration:      9_000_000,
		Tags:          map[string]any{},
	}
	userSpan := &tracepkg.Span{
		TraceID: "trace-1", SpanID: "s1", ParentSpanID: "root",
		OperationName: "llm.gemini_realtime",
		StartTime:     1_000_000, Duration: 100_000,
		Tags: map[string]any{"input.value": "what time is it in tokyo", "gen_ai.request.model": "gemini-live"},
	}
	toolSpan := &tracepkg.Span{
		TraceID: "trace-1", SpanID: "s2", ParentSpanID: "root",
		OperationName: "tool.get_time",
		StartTime:     1_200_000, Duration: 50_000,
		Tags: map[string]any{
			"tool.name":    "get_time",
			"input.value":  `{"tz": "Asia/Tokyo"}`,
			"output.value": `{"time": "10:00"}`,
		},
	}
	assistantSpan := &tracepkg.Span{
		TraceID: "trace-1", SpanID: "s3", ParentSpanID: "root",
		OperationName: "llm.gemini_realtime",
		StartTime:     1_500_000, Duration: 100_000,
		Tags: map[string]any{"output.value": "it is 10am in tokyo", "gen_ai.request.model": "gemini-live"},
	}
	root.Children = []*tracepkg.Span{userSpan, toolSpan, assistantSpan}

	return &tracepkg.Trace{
		TraceID:   "trace-1",
		RootSpans: []*tracepkg.Span{root},
		AllSpans:  []*tracepkg.Span{root, userSpan, toolSpan, assistantSpan},
	}
}

func TestConvertTrace_OpenInferenceVoiceSession(t *testing.T) {
	tr := buildOpenInferenceTrace()

	if !adk.DetectGenAIFormat(tr) {
		t.Fatalf("expected DetectGenAIFormat to fire on gen_ai.request.model present on the LLM spans (matches triage-core's real llm.gemini_realtime spans)")
	}

	result := adk.ConvertTrace(tr)
	if len(result.Warnings) > 0 {
		t.Fatalf("unexpected warnings: %v", result.Warnings)
	}
	if len(result.Invocations) != 1 {
		t.Fatalf("expected 1 invocation, got %d: %+v", len(result.Invocations), result.Invocations)
	}

	inv := result.Invocations[0]
	if got := inv.UserContent.Text(); got != "what time is it in tokyo" {
		t.Errorf("UserContent = %q, want %q", got, "what time is it in tokyo")
	}
	if got := inv.FinalResponse.Text(); got != "it is 10am in tokyo" {
		t.Errorf("FinalResponse = %q, want %q", got, "it is 10am in tokyo")
	}
	if inv.IntermediateData == nil || len(inv.IntermediateData.ToolUses) != 1 {
		t.Fatalf("expected 1 tool use, got %+v", inv.IntermediateData)
	}
	tc := inv.IntermediateData.ToolUses[0]
	if tc.Name != "get_time" {
		t.Errorf("tool call name = %q, want get_time", tc.Name)
	}
	if tc.Args["tz"] != "Asia/Tokyo" {
		t.Errorf("tool call args = %+v, want tz=Asia/Tokyo", tc.Args)
	}
	if len(inv.IntermediateData.ToolResponses) != 1 {
		t.Fatalf("expected 1 tool response, got %+v", inv.IntermediateData.ToolResponses)
	}
	if inv.IntermediateData.ToolResponses[0].Response["time"] != "10:00" {
		t.Errorf("tool response = %+v, want time=10:00", inv.IntermediateData.ToolResponses[0].Response)
	}
}

// TestConvertTrace_OpenInferenceMultiTurn confirms tool spans get attached
// to the correct turn by time window when a session contains several
// user/assistant exchanges (not just one), matching
// _extract_openinference_turns' window semantics.
func TestConvertTrace_OpenInferenceMultiTurn(t *testing.T) {
	root := &tracepkg.Span{
		TraceID: "trace-2", SpanID: "root", OperationName: "voice.session",
		StartTime: 0, Duration: 10_000_000, Tags: map[string]any{},
	}
	spans := []*tracepkg.Span{
		{TraceID: "trace-2", SpanID: "u1", ParentSpanID: "root", OperationName: "llm.gemini_realtime",
			StartTime: 100_000, Duration: 10_000, Tags: map[string]any{"input.value": "turn one question", "gen_ai.request.model": "gemini-live"}},
		{TraceID: "trace-2", SpanID: "t1", ParentSpanID: "root", OperationName: "tool.lookup",
			StartTime: 150_000, Duration: 10_000,
			Tags: map[string]any{"tool.name": "lookup", "input.value": `{"q":"1"}`, "output.value": `{"r":"a"}`}},
		{TraceID: "trace-2", SpanID: "a1", ParentSpanID: "root", OperationName: "llm.gemini_realtime",
			StartTime: 200_000, Duration: 10_000, Tags: map[string]any{"output.value": "turn one answer", "gen_ai.request.model": "gemini-live"}},
		{TraceID: "trace-2", SpanID: "u2", ParentSpanID: "root", OperationName: "llm.gemini_realtime",
			StartTime: 300_000, Duration: 10_000, Tags: map[string]any{"input.value": "turn two question", "gen_ai.request.model": "gemini-live"}},
		{TraceID: "trace-2", SpanID: "t2", ParentSpanID: "root", OperationName: "tool.lookup",
			StartTime: 350_000, Duration: 10_000,
			Tags: map[string]any{"tool.name": "lookup", "input.value": `{"q":"2"}`, "output.value": `{"r":"b"}`}},
		{TraceID: "trace-2", SpanID: "a2", ParentSpanID: "root", OperationName: "llm.gemini_realtime",
			StartTime: 400_000, Duration: 10_000, Tags: map[string]any{"output.value": "turn two answer", "gen_ai.request.model": "gemini-live"}},
	}
	root.Children = spans
	tr := &tracepkg.Trace{TraceID: "trace-2", RootSpans: []*tracepkg.Span{root}, AllSpans: append([]*tracepkg.Span{root}, spans...)}

	result := adk.ConvertTrace(tr)
	if len(result.Invocations) != 2 {
		t.Fatalf("expected 2 invocations, got %d: %+v", len(result.Invocations), result.Invocations)
	}

	first, second := result.Invocations[0], result.Invocations[1]
	if got := first.UserContent.Text(); got != "turn one question" {
		t.Errorf("turn 1 user = %q", got)
	}
	if got := first.FinalResponse.Text(); got != "turn one answer" {
		t.Errorf("turn 1 assistant = %q", got)
	}
	if len(first.IntermediateData.ToolUses) != 1 || first.IntermediateData.ToolUses[0].Args["q"] != "1" {
		t.Errorf("turn 1 tool use wrong: %+v", first.IntermediateData.ToolUses)
	}

	if got := second.UserContent.Text(); got != "turn two question" {
		t.Errorf("turn 2 user = %q", got)
	}
	if got := second.FinalResponse.Text(); got != "turn two answer" {
		t.Errorf("turn 2 assistant = %q", got)
	}
	if len(second.IntermediateData.ToolUses) != 1 || second.IntermediateData.ToolUses[0].Args["q"] != "2" {
		t.Errorf("turn 2 tool use wrong: %+v", second.IntermediateData.ToolUses)
	}
}

// buildOpenInferenceAgentTrace reproduces an OpenInference producer's shape
// for an agent pass: an AGENT root span carrying the clean prompt and
// answer, and an llm.chat.completions child that has only OpenInference
// markers (Vertex path: no gen_ai.*, no input/output values).
func buildOpenInferenceAgentTrace() *tracepkg.Trace {
	root := &tracepkg.Span{
		TraceID: "trace-c", SpanID: "agent", OperationName: "agent.reflection",
		StartTime: 0, Duration: 3_000_000,
		Tags: map[string]any{
			"openinference.span.kind": "AGENT",
			"input.value":             "cart is failing, what is the cause?",
			"output.value":            "redis is down",
		},
	}
	llm := &tracepkg.Span{
		TraceID: "trace-c", SpanID: "llm", ParentSpanID: "agent", OperationName: "llm.chat.completions",
		StartTime: 100_000, Duration: 2_000_000,
		Tags: map[string]any{
			"openinference.span.kind":    "LLM",
			"llm.model_name":             "gemini-2.5-flash",
			"llm.token_count.prompt":     int64(1200),
			"llm.token_count.completion": int64(80),
		},
	}
	root.Children = []*tracepkg.Span{llm}
	return &tracepkg.Trace{TraceID: "trace-c", RootSpans: []*tracepkg.Span{root}, AllSpans: []*tracepkg.Span{root, llm}}
}

func TestConvertTrace_OpenInferenceAgentWithBareLLMChild(t *testing.T) {
	tr := buildOpenInferenceAgentTrace()
	if !adk.DetectGenAIFormat(tr) {
		t.Fatal("expected DetectGenAIFormat to fire on openinference.span.kind=LLM / llm.model_name")
	}
	result := adk.ConvertTrace(tr)
	if len(result.Invocations) != 1 {
		t.Fatalf("expected 1 invocation, got %d (warnings %v)", len(result.Invocations), result.Warnings)
	}
	inv := result.Invocations[0]
	if got := inv.UserContent.Text(); got != "cart is failing, what is the cause?" {
		t.Errorf("UserContent = %q, want the agent span's input.value", got)
	}
	if got := inv.FinalResponse.Text(); got != "redis is down" {
		t.Errorf("FinalResponse = %q, want the agent span's output.value", got)
	}
}

// A request/response LLM span carrying both input.value and output.value is
// one complete call, not one side of a voice turn: its output must not be
// dropped by the turn pairing.
func TestConvertTrace_OpenInferenceTwoSidedLLMSpan(t *testing.T) {
	root := &tracepkg.Span{
		TraceID: "trace-d", SpanID: "rca", OperationName: "rca.build_rca",
		StartTime: 0, Duration: 2_000_000, Tags: map[string]any{"openinference.span.kind": "CHAIN"},
	}
	llm := &tracepkg.Span{
		TraceID: "trace-d", SpanID: "llm", ParentSpanID: "rca", OperationName: "llm.chat.completions",
		StartTime: 10, Duration: 1_000_000,
		Tags: map[string]any{
			"openinference.span.kind": "LLM",
			"llm.model_name":          "m",
			"input.value":             "why is checkout down",
			"output.value":            "OOM kill",
		},
	}
	root.Children = []*tracepkg.Span{llm}
	tr := &tracepkg.Trace{TraceID: "trace-d", RootSpans: []*tracepkg.Span{root}, AllSpans: []*tracepkg.Span{root, llm}}

	result := adk.ConvertTrace(tr)
	if len(result.Invocations) != 1 {
		t.Fatalf("expected 1 invocation, got %d (warnings %v)", len(result.Invocations), result.Warnings)
	}
	inv := result.Invocations[0]
	if got := inv.UserContent.Text(); got != "why is checkout down" {
		t.Errorf("UserContent = %q", got)
	}
	if got := inv.FinalResponse.Text(); got != "OOM kill" {
		t.Errorf("FinalResponse = %q, want the span's output.value", got)
	}
}

func TestExtractTokenUsageFromAttrs_OpenInference(t *testing.T) {
	in, out, model := adk.ExtractTokenUsageFromAttrs(map[string]any{
		"llm.model_name":             "gemini-2.5-flash",
		"llm.token_count.prompt":     int64(1200),
		"llm.token_count.completion": int64(80),
	})
	if in != 1200 || out != 80 || model != "gemini-2.5-flash" {
		t.Errorf("got (%d, %d, %q), want (1200, 80, gemini-2.5-flash)", in, out, model)
	}
}
