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

func newTestTenants(t *testing.T) (*TenantStore, string, string) {
	t.Helper()
	ts, err := NewTenantStore(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"acme", "globex"} {
		if _, err := ts.Create(id, ""); err != nil {
			t.Fatal(err)
		}
	}
	acme, _, err := ts.NewToken("acme", "ci")
	if err != nil {
		t.Fatal(err)
	}
	globex, _, err := ts.NewToken("globex", "")
	if err != nil {
		t.Fatal(err)
	}
	return ts, acme, globex
}

func TestIngestAuthMiddleware(t *testing.T) {
	var gotTenant string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTenant = ingestTenant(r.Context())
		w.WriteHeader(http.StatusNoContent)
	})
	tenants, acme, _ := newTestTenants(t)
	empty, _ := NewTenantStore(nil)

	for name, tc := range map[string]struct {
		auth       *ingestAuth
		header     string
		want       int
		wantTenant string
	}{
		"open receiver":             {&ingestAuth{tenants: empty}, "", http.StatusNoContent, ""},
		"nil auth":                  {nil, "", http.StatusNoContent, ""},
		"shared token missing":      {&ingestAuth{globalToken: testIngestToken, tenants: empty}, "", http.StatusUnauthorized, ""},
		"shared token wrong":        {&ingestAuth{globalToken: testIngestToken, tenants: empty}, "Bearer nope", http.StatusUnauthorized, ""},
		"shared token wrong scheme": {&ingestAuth{globalToken: testIngestToken, tenants: empty}, testIngestToken, http.StatusUnauthorized, ""},
		"shared token":              {&ingestAuth{globalToken: testIngestToken, tenants: empty}, "Bearer " + testIngestToken, http.StatusNoContent, ""},
		"scheme case-insensitive":   {&ingestAuth{globalToken: testIngestToken, tenants: empty}, "bearer " + testIngestToken, http.StatusNoContent, ""},
		"tenant token only, none":   {&ingestAuth{tenants: tenants}, "", http.StatusUnauthorized, ""},
		"tenant token":              {&ingestAuth{tenants: tenants}, "Bearer " + acme, http.StatusNoContent, "acme"},
		"tenant token and shared":   {&ingestAuth{globalToken: testIngestToken, tenants: tenants}, "Bearer " + testIngestToken, http.StatusNoContent, ""},
		"unknown tenant token":      {&ingestAuth{tenants: tenants}, "Bearer egt_unknown", http.StatusUnauthorized, ""},
	} {
		gotTenant = "unset"
		req := httptest.NewRequest(http.MethodPost, "/v1/traces", nil)
		if tc.header != "" {
			req.Header.Set("Authorization", tc.header)
		}
		rec := httptest.NewRecorder()
		tc.auth.middleware(next).ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s: status %d, want %d", name, rec.Code, tc.want)
		}
		if tc.want == http.StatusNoContent && gotTenant != tc.wantTenant {
			t.Errorf("%s: tenant %q, want %q", name, gotTenant, tc.wantTenant)
		}
	}
}

func TestOTLPGRPCRequiresIngestToken(t *testing.T) {
	store := NewSessionStore(nil)
	tenants, acme, _ := newTestTenants(t)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := newOTLPGRPCServer(store, &ingestAuth{globalToken: testIngestToken, tenants: tenants})
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
	for _, tok := range []string{testIngestToken, acme} {
		authed := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+tok)
		if _, err := client.Export(authed, &coltracepb.ExportTraceServiceRequest{}); err != nil {
			t.Fatalf("Export with a valid token: %v", err)
		}
	}
}

func TestNormalizeIngestToken(t *testing.T) {
	for in, want := range map[string]string{
		testIngestToken:              testIngestToken,
		"Bearer " + testIngestToken:  testIngestToken,
		"bearer " + testIngestToken:  testIngestToken,
		" " + testIngestToken + "\n": testIngestToken,
		"":                           "",
	} {
		if got := normalizeIngestToken(in); got != want {
			t.Errorf("normalizeIngestToken(%q) = %q, want %q", in, got, want)
		}
	}
}
