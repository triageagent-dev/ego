package api

import (
	"context"
	"encoding/hex"
	"net"
	"testing"
	"time"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// TestOTLPGRPCReceiver_LiveRoundTrip starts a real gRPC server (via
// newOTLPGRPCServer, the same constructor Serve uses) on a real TCP
// listener, dials it with the standard OTLP collector client stub, and
// asserts the exported spans land in the SessionStore exactly as the
// OTLP/HTTP path does - proving the two receivers are equivalent ingestion
// routes into the same store, not just that Export doesn't error.
func TestOTLPGRPCReceiver_LiveRoundTrip(t *testing.T) {
	store := NewSessionStore(nil)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	server := newOTLPGRPCServer(store, "")
	go func() {
		_ = server.Serve(lis)
	}()
	defer server.Stop()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dialing %s: %v", lis.Addr(), err)
	}
	defer conn.Close()
	client := coltracepb.NewTraceServiceClient(conn)

	traceID, _ := hex.DecodeString("3e289017fe03ffd7c4145316d2eb3d0d")
	spanID, _ := hex.DecodeString("e37fdd8f56146d31")

	req := &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{
			{
				Resource: &resourcepb.Resource{
					Attributes: []*commonpb.KeyValue{
						{Key: sessionNameAttr, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "grpc-sess"}}},
						{Key: "service.name", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "test-agent"}}},
					},
				},
				ScopeSpans: []*tracepb.ScopeSpans{
					{
						Scope: &commonpb.InstrumentationScope{Name: "gcp.vertex.agent"},
						Spans: []*tracepb.Span{
							{
								TraceId:           traceID,
								SpanId:            spanID,
								Name:              "invoke_agent test",
								StartTimeUnixNano: 1_000_000_000,
								EndTimeUnixNano:   1_002_000_000,
							},
						},
					},
				},
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := client.Export(ctx, req); err != nil {
		t.Fatalf("Export: %v", err)
	}

	sessions := store.List()
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(sessions))
	}
	if sessions[0].SessionID != "grpc-sess" {
		t.Errorf("session ID = %q, want %q", sessions[0].SessionID, "grpc-sess")
	}
	if sessions[0].SpanCount != 1 {
		t.Errorf("span count = %d, want 1", sessions[0].SpanCount)
	}
	if sessions[0].TraceID != "3e289017fe03ffd7c4145316d2eb3d0d" {
		t.Errorf("trace ID = %q", sessions[0].TraceID)
	}
}
