package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOriginGuard(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	guard := originGuard("ego.example.com", ok)
	for name, tc := range map[string]struct {
		method string
		hdr    map[string]string
		want   int
	}{
		"post without origin (cli)":   {"POST", nil, 204},
		"post same origin":            {"POST", map[string]string{"Origin": "http://ego.test"}, 204},
		"post public host":            {"POST", map[string]string{"Origin": "https://ego.example.com"}, 204},
		"post forwarded host":         {"POST", map[string]string{"Origin": "https://gw.example.org", "X-Forwarded-Host": "gw.example.org"}, 204},
		"post other site":             {"POST", map[string]string{"Origin": "https://evil.test"}, 403},
		"post null origin":            {"POST", map[string]string{"Origin": "null"}, 403},
		"post cross-site fetch meta":  {"POST", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		"post same-origin fetch meta": {"POST", map[string]string{"Sec-Fetch-Site": "same-origin"}, 204},
		"get from other site":         {"GET", map[string]string{"Origin": "https://evil.test"}, 204},
		"websocket from other site":   {"GET", map[string]string{"Origin": "https://evil.test", "Upgrade": "websocket"}, 403},
		"websocket same origin":       {"GET", map[string]string{"Origin": "http://ego.test", "Upgrade": "websocket"}, 204},
		"delete from other site":      {"DELETE", map[string]string{"Origin": "https://evil.test"}, 403},
	} {
		req := httptest.NewRequest(tc.method, "http://ego.test/api/evaluate", nil)
		for k, v := range tc.hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		guard.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s: status %d, want %d", name, rec.Code, tc.want)
		}
	}
}

func TestLogoutIsPostOnly(t *testing.T) {
	h := authLogoutHandler()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/auth/logout", nil))
	if rec.Code != http.StatusMethodNotAllowed || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("GET /auth/logout = %d with %d cookies, want 405 and no cookie change", rec.Code, len(rec.Result().Cookies()))
	}
	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/auth/logout", nil))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /auth/logout = %d, want 303", rec.Code)
	}
}
