package adk_test

import (
	"path/filepath"
	"testing"

	"github.com/triageagent-dev/ego/internal/adk"
	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

// TestExtractTraceMetadata_ADK exercises the ADK-format path against the
// real samples/helm.json fixture used elsewhere in this package's tests.
func TestExtractTraceMetadata_ADK(t *testing.T) {
	dir := samplesDir(t)
	traces, err := tracepkg.LoadJaegerJSON(filepath.Join(dir, "helm.json"))
	if err != nil {
		t.Fatalf("loading helm.json: %v", err)
	}
	if len(traces) != 1 {
		t.Fatalf("got %d traces, want 1", len(traces))
	}

	result := adk.ConvertTrace(traces[0])
	meta := adk.ExtractTraceMetadata(traces[0], result.Invocations)

	if meta.StartTime == 0 {
		t.Error("StartTime = 0, want a nonzero start time")
	}
	if meta.AgentName == "" {
		t.Error("AgentName is empty, want a non-empty fallback (root span operation name at worst)")
	}
}

// TestExtractTraceMetadata_GenAI exercises the GenAI/OpenInference-format
// path (reusing genai_converter_test.go's buildOpenInferenceTrace fixture:
// a voice.session root span with two gemini-live LLM children).
func TestExtractTraceMetadata_GenAI(t *testing.T) {
	tr := buildOpenInferenceTrace()

	result := adk.ConvertTrace(tr)
	meta := adk.ExtractTraceMetadata(tr, result.Invocations)

	if meta.AgentName != "voice.session" {
		t.Errorf("AgentName = %q, want %q (root span operation name fallback)", meta.AgentName, "voice.session")
	}
	if meta.Model != "gemini-live" {
		t.Errorf("Model = %q, want %q", meta.Model, "gemini-live")
	}
	if meta.StartTime != 1_000_000 {
		t.Errorf("StartTime = %d, want 1000000", meta.StartTime)
	}
	if meta.UserInputPreview != "what time is it in tokyo" {
		t.Errorf("UserInputPreview = %q, want %q", meta.UserInputPreview, "what time is it in tokyo")
	}
	if meta.FinalOutputPreview != "it is 10am in tokyo" {
		t.Errorf("FinalOutputPreview = %q, want %q", meta.FinalOutputPreview, "it is 10am in tokyo")
	}
}

// TestExtractTraceMetadata_GenAI_AssistantGreetsFirst reproduces the
// production bug where a real multi-turn voice.session trace's Conversation
// column never stopped spinning: the trace's very first LLM span
// chronologically is a one-sided assistant greeting (output.value only, no
// input.value), so the old first/last-LLM-span heuristic found no user text
// at all. ExtractTraceMetadata must fall through to a later invocation's
// user message instead of giving up.
func TestExtractTraceMetadata_GenAI_AssistantGreetsFirst(t *testing.T) {
	root := &tracepkg.Span{
		TraceID: "trace-2", SpanID: "root", OperationName: "voice.session",
		StartTime: 1_000_000, Duration: 9_000_000, Tags: map[string]any{},
	}
	greeting := &tracepkg.Span{
		TraceID: "trace-2", SpanID: "g1", ParentSpanID: "root",
		OperationName: "llm.gemini_realtime",
		StartTime:     1_000_000, Duration: 50_000,
		Tags: map[string]any{"output.value": "hi, how can I help you today", "gen_ai.request.model": "gemini-live"},
	}
	userSpan := &tracepkg.Span{
		TraceID: "trace-2", SpanID: "s1", ParentSpanID: "root",
		OperationName: "llm.gemini_realtime",
		StartTime:     1_200_000, Duration: 100_000,
		Tags: map[string]any{"input.value": "what time is it in tokyo", "gen_ai.request.model": "gemini-live"},
	}
	assistantSpan := &tracepkg.Span{
		TraceID: "trace-2", SpanID: "s2", ParentSpanID: "root",
		OperationName: "llm.gemini_realtime",
		StartTime:     1_500_000, Duration: 100_000,
		Tags: map[string]any{"output.value": "it is 10am in tokyo", "gen_ai.request.model": "gemini-live"},
	}
	root.Children = []*tracepkg.Span{greeting, userSpan, assistantSpan}
	tr := &tracepkg.Trace{
		TraceID:   "trace-2",
		RootSpans: []*tracepkg.Span{root},
		AllSpans:  []*tracepkg.Span{root, greeting, userSpan, assistantSpan},
	}

	result := adk.ConvertTrace(tr)
	meta := adk.ExtractTraceMetadata(tr, result.Invocations)

	if meta.UserInputPreview != "what time is it in tokyo" {
		t.Errorf("UserInputPreview = %q, want %q (must skip the greeting-only leading invocation)", meta.UserInputPreview, "what time is it in tokyo")
	}
	if meta.FinalOutputPreview != "it is 10am in tokyo" {
		t.Errorf("FinalOutputPreview = %q, want %q", meta.FinalOutputPreview, "it is 10am in tokyo")
	}
}
