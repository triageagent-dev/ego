package api

import (
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

func gzipBytes(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(b); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func otlpHTTPTestRequestProto(t *testing.T, session string) []byte {
	t.Helper()
	traceID, _ := hex.DecodeString("3e289017fe03ffd7c4145316d2eb3d0d")
	spanID, _ := hex.DecodeString("e37fdd8f56146d31")
	req := &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
				{Key: sessionNameAttr, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: session}}},
			}},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Scope: &commonpb.InstrumentationScope{Name: "gcp.vertex.agent"},
				Spans: []*tracepb.Span{{
					TraceId:           traceID,
					SpanId:            spanID,
					Name:              "invoke_agent test",
					StartTimeUnixNano: 1_000_000_000,
					EndTimeUnixNano:   1_002_000_000,
				}},
			}},
		}},
	}
	b, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func otlpHTTPTestRequestJSON(session string) []byte {
	return []byte(`{"resourceSpans":[{"resource":{"attributes":[{"key":"` + sessionNameAttr +
		`","value":{"stringValue":"` + session + `"}}]},"scopeSpans":[{"scope":{"name":"gcp.vertex.agent"},` +
		`"spans":[{"traceId":"3e289017fe03ffd7c4145316d2eb3d0d","spanId":"e37fdd8f56146d31",` +
		`"name":"invoke_agent test","startTimeUnixNano":"1000000000","endTimeUnixNano":"1002000000"}]}]}]}`)
}

// TestOTLPTracesHandler_ContentEncoding covers the encodings a stock
// OpenTelemetry Collector sends: its otlphttp exporter gzips by default, so
// a receiver that only reads raw bodies rejects every export with a 400.
func TestOTLPTracesHandler_ContentEncoding(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		encoding    string
		body        func(t *testing.T) []byte
		wantStatus  int
		wantSession bool
	}{
		{"protobuf identity", "application/x-protobuf", "",
			func(t *testing.T) []byte { return otlpHTTPTestRequestProto(t, "s") }, http.StatusOK, true},
		{"protobuf gzip", "application/x-protobuf", "gzip",
			func(t *testing.T) []byte { return gzipBytes(t, otlpHTTPTestRequestProto(t, "s")) }, http.StatusOK, true},
		{"json gzip", "application/json", "gzip",
			func(t *testing.T) []byte { return gzipBytes(t, otlpHTTPTestRequestJSON("s")) }, http.StatusOK, true},
		{"gzip header, uppercase", "application/x-protobuf", "GZIP",
			func(t *testing.T) []byte { return gzipBytes(t, otlpHTTPTestRequestProto(t, "s")) }, http.StatusOK, true},
		{"unsupported encoding", "application/x-protobuf", "br",
			func(t *testing.T) []byte { return otlpHTTPTestRequestProto(t, "s") }, http.StatusUnsupportedMediaType, false},
		{"gzip header, body not gzip", "application/x-protobuf", "gzip",
			func(t *testing.T) []byte { return otlpHTTPTestRequestProto(t, "s") }, http.StatusBadRequest, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := NewSessionStore(nil)
			req := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(tc.body(t)))
			req.Header.Set("Content-Type", tc.contentType)
			if tc.encoding != "" {
				req.Header.Set("Content-Encoding", tc.encoding)
			}
			rec := httptest.NewRecorder()
			otlpTracesHandler(store)(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			sessions := store.List()
			if !tc.wantSession {
				if len(sessions) != 0 {
					t.Fatalf("got %d sessions, want 0", len(sessions))
				}
				return
			}
			if len(sessions) != 1 || sessions[0].SpanCount != 1 || sessions[0].SessionID != "s" {
				t.Fatalf("sessions = %+v, want one session \"s\" with 1 span", sessions)
			}
		})
	}
}
