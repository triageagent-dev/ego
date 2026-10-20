package otlp

import (
	"encoding/hex"
	"testing"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

func TestDecodeProtobufTraces(t *testing.T) {
	traceID, _ := hex.DecodeString("3e289017fe03ffd7c4145316d2eb3d0d")
	spanID, _ := hex.DecodeString("e37fdd8f56146d31")

	req := &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{
			{
				Resource: &resourcepb.Resource{
					Attributes: []*commonpb.KeyValue{
						{Key: "service.name", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "test-agent"}}},
					},
				},
				ScopeSpans: []*tracepb.ScopeSpans{
					{
						Scope: &commonpb.InstrumentationScope{Name: "test-scope"},
						Spans: []*tracepb.Span{
							{
								TraceId:           traceID,
								SpanId:            spanID,
								Name:              "invoke_agent test",
								StartTimeUnixNano: 1_000_000_000,
								EndTimeUnixNano:   1_002_000_000,
								Attributes: []*commonpb.KeyValue{
									{Key: "gen_ai.request.model", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "gemini-2.0-flash"}}},
								},
							},
						},
					},
				},
			},
		},
	}

	raw, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("marshaling protobuf request: %v", err)
	}

	body, err := DecodeProtobufTraces(raw)
	if err != nil {
		t.Fatalf("DecodeProtobufTraces: %v", err)
	}

	traces := ParseExportRequest(body)
	if len(traces) != 1 {
		t.Fatalf("got %d traces, want 1", len(traces))
	}
	tr := traces[0]
	if tr.TraceID != "3e289017fe03ffd7c4145316d2eb3d0d" {
		t.Errorf("trace ID = %q, want hex-decoded value (fixProtobufIDFields should convert base64 -> hex)", tr.TraceID)
	}
	if len(tr.AllSpans) != 1 {
		t.Fatalf("got %d spans, want 1", len(tr.AllSpans))
	}
	span := tr.AllSpans[0]
	if span.SpanID != "e37fdd8f56146d31" {
		t.Errorf("span ID = %q", span.SpanID)
	}
	if span.OperationName != "invoke_agent test" {
		t.Errorf("operation name = %q", span.OperationName)
	}
	if span.Tags["gen_ai.request.model"] != "gemini-2.0-flash" {
		t.Errorf("gen_ai.request.model = %v", span.Tags["gen_ai.request.model"])
	}
	if span.Tags["service.name"] != "test-agent" {
		t.Errorf("service.name = %v", span.Tags["service.name"])
	}
}
