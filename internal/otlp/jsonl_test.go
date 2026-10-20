package otlp_test

import (
	"testing"

	"github.com/triageagent-dev/ego/internal/otlp"
	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

// TestEncodeParseJSONLRoundTrip verifies EncodeTraceJSONL/ParseJSONLSpans
// round-trip a trace's spans and attributes, the same path
// /api/streaming/get-trace's response takes when re-uploaded to
// /api/evaluate.
func TestEncodeParseJSONLRoundTrip(t *testing.T) {
	root := &tracepkg.Span{
		TraceID:       "trace-1",
		SpanID:        "root",
		OperationName: "invoke_agent",
		StartTime:     1_000_000,
		Duration:      500_000,
		Tags: map[string]any{
			"service.name":         "my-agent",
			"gen_ai.request.model": "gemini-2.5-flash",
			"some.int":             int64(42),
			"some.bool":            true,
		},
	}
	child := &tracepkg.Span{
		TraceID: "trace-1", SpanID: "child", ParentSpanID: "root",
		OperationName: "call_llm",
		StartTime:     1_100_000, Duration: 100_000,
		Tags: map[string]any{"input.value": "hello"},
	}
	root.Children = []*tracepkg.Span{child}
	tr := &tracepkg.Trace{TraceID: "trace-1", RootSpans: []*tracepkg.Span{root}, AllSpans: []*tracepkg.Span{root, child}}

	content, err := otlp.EncodeTraceJSONL(tr)
	if err != nil {
		t.Fatalf("EncodeTraceJSONL: %v", err)
	}

	traces, err := otlp.ParseJSONLSpans(content)
	if err != nil {
		t.Fatalf("ParseJSONLSpans: %v", err)
	}
	if len(traces) != 1 {
		t.Fatalf("expected 1 trace, got %d", len(traces))
	}
	got := traces[0]
	if got.TraceID != "trace-1" {
		t.Errorf("TraceID = %q, want trace-1", got.TraceID)
	}
	if len(got.AllSpans) != 2 {
		t.Fatalf("expected 2 spans, got %d", len(got.AllSpans))
	}

	byID := map[string]*tracepkg.Span{}
	for _, s := range got.AllSpans {
		byID[s.SpanID] = s
	}

	rootGot, ok := byID["root"]
	if !ok {
		t.Fatalf("root span missing after round-trip")
	}
	if rootGot.OperationName != "invoke_agent" {
		t.Errorf("OperationName = %q, want invoke_agent", rootGot.OperationName)
	}
	if rootGot.Tags["service.name"] != "my-agent" {
		t.Errorf("service.name = %v, want my-agent", rootGot.Tags["service.name"])
	}
	if rootGot.Tags["gen_ai.request.model"] != "gemini-2.5-flash" {
		t.Errorf("gen_ai.request.model = %v", rootGot.Tags["gen_ai.request.model"])
	}
	if rootGot.Tags["some.int"] != int64(42) {
		t.Errorf("some.int = %v (%T), want int64(42)", rootGot.Tags["some.int"], rootGot.Tags["some.int"])
	}
	if rootGot.Tags["some.bool"] != true {
		t.Errorf("some.bool = %v, want true", rootGot.Tags["some.bool"])
	}
	if rootGot.StartTime != 1_000_000 || rootGot.Duration != 500_000 {
		t.Errorf("StartTime/Duration = %d/%d, want 1000000/500000", rootGot.StartTime, rootGot.Duration)
	}

	childGot, ok := byID["child"]
	if !ok {
		t.Fatalf("child span missing after round-trip")
	}
	if childGot.ParentSpanID != "root" {
		t.Errorf("ParentSpanID = %q, want root", childGot.ParentSpanID)
	}
	if childGot.Tags["input.value"] != "hello" {
		t.Errorf("input.value = %v, want hello", childGot.Tags["input.value"])
	}
}

func TestParseOTLPContent_FullExport(t *testing.T) {
	content := []byte(`{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"svc"}}]},"scopeSpans":[{"spans":[{"traceId":"t1","spanId":"s1","name":"op","startTimeUnixNano":"1000000000","endTimeUnixNano":"2000000000","attributes":[]}]}]}]}`)
	traces, err := otlp.ParseOTLPContent(content)
	if err != nil {
		t.Fatalf("ParseOTLPContent: %v", err)
	}
	if len(traces) != 1 || len(traces[0].AllSpans) != 1 {
		t.Fatalf("expected 1 trace with 1 span, got %+v", traces)
	}
	if traces[0].AllSpans[0].Tags["service.name"] != "svc" {
		t.Errorf("expected resource attribute merged in, got %v", traces[0].AllSpans[0].Tags)
	}
}

func TestParseOTLPContent_JSONL(t *testing.T) {
	content := []byte(`{"traceId":"t1","spanId":"s1","name":"op","startTimeUnixNano":"1000000000","endTimeUnixNano":"2000000000","attributes":[{"key":"foo","value":{"stringValue":"bar"}}]}` + "\n")
	traces, err := otlp.ParseOTLPContent(content)
	if err != nil {
		t.Fatalf("ParseOTLPContent: %v", err)
	}
	if len(traces) != 1 || len(traces[0].AllSpans) != 1 {
		t.Fatalf("expected 1 trace with 1 span, got %+v", traces)
	}
	if traces[0].AllSpans[0].Tags["foo"] != "bar" {
		t.Errorf("expected attribute foo=bar, got %v", traces[0].AllSpans[0].Tags)
	}
}
