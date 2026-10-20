package api

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// TestReadSessionCookie_KnownCookie validates a fixed cookie: base64url of "username|expiry|signature", where
// signature is the hex HMAC-SHA256 of "username|expiry" under the secret. The expiry is far in the future so the
// fixture doesn't expire; readSessionCookie's expiry check is covered by TestReadSessionCookie_RejectsExpired.
func TestReadSessionCookie_KnownCookie(t *testing.T) {
	const secret = "test-secret-value"
	const cookie = "b2N0b2NhdHw0OTQzNzg4NTA0fDQwZDIzNDVkYmUzMDkyM2U3ZGUzMjU1YTRhOWFiNmUxZTMxNjUzZTY4MGQyOGJjZDQwNGE2NTZmMTM5NmFiNTA="

	username, ok := readSessionCookie(cookie, secret)
	if !ok {
		t.Fatal("expected the known cookie to validate, got ok=false")
	}
	if username != "octocat" {
		t.Errorf("username = %q, want %q", username, "octocat")
	}
}

func TestReadSessionCookie_RejectsWrongSecret(t *testing.T) {
	const cookie = "b2N0b2NhdHw0OTQzNzg4NTA0fDQwZDIzNDVkYmUzMDkyM2U3ZGUzMjU1YTRhOWFiNmUxZTMxNjUzZTY4MGQyOGJjZDQwNGE2NTZmMTM5NmFiNTA="
	if _, ok := readSessionCookie(cookie, "wrong-secret"); ok {
		t.Error("expected validation to fail with the wrong secret")
	}
}

func TestReadSessionCookie_RejectsGarbage(t *testing.T) {
	for _, bad := range []string{"", "not-base64!!!", "b2N0b2NhdA=="} { // last one decodes but has no "|"
		if _, ok := readSessionCookie(bad, "any-secret"); ok {
			t.Errorf("readSessionCookie(%q) = ok, want rejected", bad)
		}
	}
}

func TestReadSessionCookie_RejectsExpired(t *testing.T) {
	const secret = "expiry-test-secret"
	payload := "alice|" + timeUnix(-1) // 1 second in the past
	sig := signSessionPayload(secret, payload)
	cookie := base64.URLEncoding.EncodeToString([]byte(payload + "|" + sig))

	if _, ok := readSessionCookie(cookie, secret); ok {
		t.Error("expected an already-expired cookie to be rejected")
	}
}

func TestRequireSession_DisabledWhenSecretEmpty(t *testing.T) {
	handler := requireSession("", nil, okHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/streaming/sessions", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (auth disabled with empty secret)", rec.Code)
	}
}

func TestRequireSession_APIPathGets401JSON(t *testing.T) {
	handler := requireSession("secret", nil, okHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/streaming/sessions", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json (a fetch() call must see JSON, not be redirected into HTML)", ct)
	}
}

func TestRequireSession_PagePathRedirectsToLogin(t *testing.T) {
	handler := requireSession("secret", nil, okHandler())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/auth/login" {
		t.Errorf("Location = %q, want /auth/login", loc)
	}
}

func TestRequireSession_ValidCookieGrantsAccess(t *testing.T) {
	const secret = "cookie-grant-secret"
	handler := requireSession(secret, nil, okHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/streaming/sessions", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: mintTestCookie(t, "alice", secret)})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestRequireSession_ValidBearerTokenGrantsAccess(t *testing.T) {
	const secret = "bearer-grant-secret"
	handler := requireSession(secret, nil, okHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/streaming/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+mintTestCookie(t, "alice", secret))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (Authorization: Bearer fallback should work like the cookie)", rec.Code)
	}
}

func mintTestCookie(t *testing.T, username, secret string) string {
	t.Helper()
	payload := username + "|" + timeUnix(3600)
	sig := signSessionPayload(secret, payload)
	return base64.URLEncoding.EncodeToString([]byte(payload + "|" + sig))
}

// TestMintSessionToken_RoundTrip covers the exported entry point
// cmd/ego's `auth mint-token` calls: a token minted with a custom
// TTL (unlike mintSessionCookie's fixed sessionTTL) must read back via
// readSessionCookie exactly like a normal session cookie, and via
// requireSession's Authorization: Bearer path.
func TestMintSessionToken_RoundTrip(t *testing.T) {
	const secret = "mint-token-secret"
	token := MintSessionToken("mcp-client", secret, 3650*24*time.Hour)

	username, ok := readSessionCookie(token, secret)
	if !ok || username != "mcp-client" {
		t.Fatalf("readSessionCookie(MintSessionToken(...)) = (%q, %v), want (\"mcp-client\", true)", username, ok)
	}

	handler := requireSession(secret, nil, okHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/streaming/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (a minted token should work as a bearer token)", rec.Code)
	}
}

func TestMintSessionToken_ExpiresAfterTTL(t *testing.T) {
	const secret = "mint-token-expiry-secret"
	token := MintSessionToken("mcp-client", secret, -1*time.Hour)
	if _, ok := readSessionCookie(token, secret); ok {
		t.Error("readSessionCookie(already-expired token) = ok, want rejected")
	}
}

func timeUnix(deltaSeconds int64) string {
	return strconv.FormatInt(time.Now().Unix()+deltaSeconds, 10)
}
