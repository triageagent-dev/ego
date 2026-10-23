package api

import (
	"bytes"
	"context"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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

func newTestSenders(t *testing.T) (*SenderStore, string, string) {
	t.Helper()
	ts, err := NewSenderStore(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"acme", "globex"} {
		if _, err := ts.Create(id, ""); err != nil {
			t.Fatal(err)
		}
	}
	acme, _, err := ts.NewToken("acme", "ci", "")
	if err != nil {
		t.Fatal(err)
	}
	globex, _, err := ts.NewToken("globex", "", "")
	if err != nil {
		t.Fatal(err)
	}
	return ts, acme, globex
}

func TestIngestAuthMiddleware(t *testing.T) {
	var gotSender string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSender = ingestSender(r.Context())
		w.WriteHeader(http.StatusNoContent)
	})
	senders, acme, _ := newTestSenders(t)
	empty, _ := NewSenderStore(nil)

	for name, tc := range map[string]struct {
		auth       *ingestAuth
		header     string
		want       int
		wantSender string
	}{
		"no token configured":       {&ingestAuth{senders: empty}, "", http.StatusUnauthorized, ""},
		"no token, a bearer sent":   {&ingestAuth{senders: empty}, "Bearer anything", http.StatusUnauthorized, ""},
		"nil auth":                  {nil, "", http.StatusNoContent, ""},
		"shared token missing":      {&ingestAuth{globalToken: testIngestToken, senders: empty}, "", http.StatusUnauthorized, ""},
		"shared token wrong":        {&ingestAuth{globalToken: testIngestToken, senders: empty}, "Bearer nope", http.StatusUnauthorized, ""},
		"shared token wrong scheme": {&ingestAuth{globalToken: testIngestToken, senders: empty}, testIngestToken, http.StatusUnauthorized, ""},
		"shared token":              {&ingestAuth{globalToken: testIngestToken, senders: empty}, "Bearer " + testIngestToken, http.StatusNoContent, ""},
		"scheme case-insensitive":   {&ingestAuth{globalToken: testIngestToken, senders: empty}, "bearer " + testIngestToken, http.StatusNoContent, ""},
		"sender token only, none":   {&ingestAuth{senders: senders}, "", http.StatusUnauthorized, ""},
		"sender token":              {&ingestAuth{senders: senders}, "Bearer " + acme, http.StatusNoContent, "acme"},
		"sender token and shared":   {&ingestAuth{globalToken: testIngestToken, senders: senders}, "Bearer " + testIngestToken, http.StatusNoContent, ""},
		"unknown sender token":      {&ingestAuth{senders: senders}, "Bearer egs_unknown", http.StatusUnauthorized, ""},
	} {
		gotSender = "unset"
		req := httptest.NewRequest(http.MethodPost, "/v1/traces", nil)
		if tc.header != "" {
			req.Header.Set("Authorization", tc.header)
		}
		rec := httptest.NewRecorder()
		tc.auth.middleware(next).ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s: status %d, want %d", name, rec.Code, tc.want)
		}
		if tc.want == http.StatusNoContent && gotSender != tc.wantSender {
			t.Errorf("%s: tenant %q, want %q", name, gotSender, tc.wantSender)
		}
	}
}

func TestOTLPGRPCRequiresIngestToken(t *testing.T) {
	store := NewSessionStore(nil)
	senders, acme, _ := newTestSenders(t)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := newOTLPGRPCServer(store, &ingestAuth{globalToken: testIngestToken, senders: senders})
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

func TestAuditLogIsOffByDefault(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	cfg := &GitHubOAuthConfig{}
	cfg.auditf("signed in: %s", "alice")
	if buf.Len() != 0 {
		t.Errorf("logged %q with the audit log off", buf.String())
	}
	cfg.AuditLog = true
	cfg.auditf("signed in: %s", "alice")
	if !strings.Contains(buf.String(), "alice") {
		t.Errorf("audit log on but nothing logged: %q", buf.String())
	}
}

func TestRefusedPushIsLoggedWithoutTheToken(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	a := &ingestAuth{globalToken: "0123456789abcdef0123456789abcdef"}
	a.logRejected("grpc", "Bearer secret-value-that-must-not-leak")
	a.logRejected("grpc", "Bearer secret-value-that-must-not-leak")
	out := buf.String()
	if !strings.Contains(out, "refused 1 OTLP/grpc") || !strings.Contains(out, "unknown token") {
		t.Errorf("log = %q", out)
	}
	if strings.Contains(out, "secret-value") {
		t.Errorf("token leaked into the log: %q", out)
	}
	if strings.Count(out, "ingest: refused") != 1 {
		t.Errorf("not rate limited: %q", out)
	}
}
