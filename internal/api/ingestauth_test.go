package api

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const testIngestToken = "0123456789abcdef0123456789abcdef"

func TestRequireIngestToken(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	for name, tc := range map[string]struct {
		token, header string
		want          int
	}{
		"no token configured":     {"", "", http.StatusNoContent},
		"missing header":          {testIngestToken, "", http.StatusUnauthorized},
		"wrong token":             {testIngestToken, "Bearer nope", http.StatusUnauthorized},
		"wrong scheme":            {testIngestToken, testIngestToken, http.StatusUnauthorized},
		"right token":             {testIngestToken, "Bearer " + testIngestToken, http.StatusNoContent},
		"scheme case-insensitive": {testIngestToken, "bearer " + testIngestToken, http.StatusNoContent},
	} {
		req := httptest.NewRequest(http.MethodPost, "/v1/traces", nil)
		if tc.header != "" {
			req.Header.Set("Authorization", tc.header)
		}
		rec := httptest.NewRecorder()
		requireIngestToken(tc.token, ok).ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s: status %d, want %d", name, rec.Code, tc.want)
		}
	}
}

func TestOTLPGRPCRequiresIngestToken(t *testing.T) {
	store := NewSessionStore(nil)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := newOTLPGRPCServer(store, testIngestToken)
	go func() { _ = server.Serve(lis) }()
	defer server.Stop()
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := coltracepb.NewTraceServiceClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := client.Export(ctx, &coltracepb.ExportTraceServiceRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("Export without a token = %v, want Unauthenticated", err)
	}
	authed := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+testIngestToken)
	if _, err := client.Export(authed, &coltracepb.ExportTraceServiceRequest{}); err != nil {
		t.Fatalf("Export with the token: %v", err)
	}
}
