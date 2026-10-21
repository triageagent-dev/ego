package mcptools

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A body over the cap is refused with 413 before the transport reads it; an ordinary call still works.
func TestHTTPHandlerCapsTheBody(t *testing.T) {
	h := NewHTTPHandler(NewREST("", "", nil), Options{})
	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	ok := post(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	if ok.Code != http.StatusOK || !strings.Contains(ok.Body.String(), "get_quality") {
		t.Fatalf("a normal call = %d %s", ok.Code, ok.Body.String())
	}

	big := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"pad":"` + strings.Repeat("a", 2<<20) + `"}}`
	if rec := post(big); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("a 2 MiB body = %d, want 413", rec.Code)
	}
}
