package api

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func postTraces(t *testing.T, body []byte, gz bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if gz {
		req.Header.Set("Content-Encoding", "gzip")
	}
	rec := httptest.NewRecorder()
	otlpTracesHandler(NewSessionStore(nil))(rec, req)
	return rec
}

// A body over the wire limit is refused with 413 instead of being read into memory.
func TestOTLPHTTP_RejectsOversizedBody(t *testing.T) {
	body := append([]byte(`{"a":"`), bytes.Repeat([]byte("a"), maxOTLPBody+1024)...)
	if rec := postTraces(t, body, false); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rec.Code)
	}
}

// A small gzip payload that expands past the decoded limit is refused with 413: the cap applies after
// decompression, so a compression bomb cannot exhaust memory.
func TestOTLPHTTP_RejectsGzipBomb(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write([]byte(`{"a":"`))
	chunk := bytes.Repeat([]byte("a"), 1<<20)
	for i := 0; i < maxOTLPDecoded/(1<<20)+2; i++ {
		_, _ = zw.Write(chunk)
	}
	_ = zw.Close()
	if buf.Len() > maxOTLPBody {
		t.Fatalf("test payload is %d bytes compressed, expected well under the wire limit", buf.Len())
	}
	if rec := postTraces(t, buf.Bytes(), true); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rec.Code)
	}
}

func TestOTLPHTTP_AcceptsNormalBody(t *testing.T) {
	rec := postTraces(t, []byte(`{"resourceSpans":[]}`), false)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
}

func TestClampLimit(t *testing.T) {
	for _, c := range []struct{ in, def, want int }{{0, 100, 100}, {-3, 100, 100}, {20, 100, 20}, {5000, 100, maxListLimit}} {
		if got := clampLimit(c.in, c.def); got != c.want {
			t.Errorf("clampLimit(%d,%d) = %d, want %d", c.in, c.def, got, c.want)
		}
	}
}

func TestSecurityHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	h := rec.Header()
	if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("X-Frame-Options") != "DENY" {
		t.Errorf("missing headers: %v", h)
	}
	if !strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Errorf("CSP = %q", h.Get("Content-Security-Policy"))
	}
}
