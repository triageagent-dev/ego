package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// readSessionCookie returns the username a token or cookie authenticates, whatever its kind.
func readSessionCookie(value, secret string) (string, bool) {
	t, ok := parseToken(value, secret)
	return t.Username, ok
}

func mintTestCookie(t *testing.T, username, secret string) string {
	t.Helper()
	return mintSessionCookie(username, secret)
}

func serve(h http.Handler, mutate func(*http.Request)) int {
	req := httptest.NewRequest(http.MethodGet, "/api/streaming/sessions", nil)
	mutate(req)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestParseToken_RejectsWrongSecretGarbageAndExpired(t *testing.T) {
	good := mintSessionCookie("octocat", "test-secret-value")
	if name, ok := readSessionCookie(good, "test-secret-value"); !ok || name != "octocat" {
		t.Fatalf("a fresh cookie did not validate: %q %v", name, ok)
	}
	if _, ok := readSessionCookie(good, "wrong-secret"); ok {
		t.Error("accepted with the wrong secret")
	}
	for _, bad := range []string{"", "not-base64!!!", "b2N0b2NhdA=="} {
		if _, ok := readSessionCookie(bad, "any-secret"); ok {
			t.Errorf("accepted %q", bad)
		}
	}
	expired, _ := MintAPIToken("ci", "s", RoleMember, -time.Hour)
	if _, ok := readSessionCookie(expired, "s"); ok {
		t.Error("accepted an expired token")
	}
	// The old username|expiry|signature format is gone.
	legacy := "b2N0b2NhdHw0OTQzNzg4NTA0fDQwZDIzNDVkYmUzMDkyM2U3ZGUzMjU1YTRhOWFiNmUxZTMxNjUzZTY4MGQyOGJjZDQwNGE2NTZmMTM5NmFiNTA="
	if _, ok := readSessionCookie(legacy, "test-secret-value"); ok {
		t.Error("accepted a token in the old format")
	}
}

func TestParseToken_UsernameMayContainSeparator(t *testing.T) {
	tok, _ := MintAPIToken("team|ci", "s", RoleViewer, time.Hour)
	got, ok := parseToken(tok, "s")
	if !ok || got.Username != "team|ci" || got.Role != RoleViewer {
		t.Fatalf("parseToken = %+v, %v", got, ok)
	}
}

func TestRequireSession_DisabledWhenSecretEmpty(t *testing.T) {
	handler := requireSession("", nil, nil, okHandler())
	if code := serve(handler, func(*http.Request) {}); code != http.StatusOK {
		t.Errorf("status = %d, want 200 (auth disabled with empty secret)", code)
	}
}

func TestRequireSession_APIPathGets401JSON(t *testing.T) {
	handler := requireSession("secret", nil, nil, okHandler())
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
	handler := requireSession("secret", nil, nil, okHandler())
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

// A cookie works only as the cookie and an API token only as a bearer value, so a copied cookie is not a
// bearer credential.
func TestRequireSession_CookieAndTokenAreNotInterchangeable(t *testing.T) {
	const secret = "kind-secret"
	handler := requireSession(secret, nil, nil, okHandler())
	cookie := mintSessionCookie("alice", secret)
	token, _ := MintAPIToken("ci", secret, RoleMember, time.Hour)

	if code := serve(handler, func(r *http.Request) { r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: cookie}) }); code != http.StatusOK {
		t.Errorf("cookie as cookie = %d, want 200", code)
	}
	if code := serve(handler, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }); code != http.StatusOK {
		t.Errorf("token as bearer = %d, want 200", code)
	}
	if code := serve(handler, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+cookie) }); code != http.StatusUnauthorized {
		t.Errorf("cookie as bearer = %d, want 401", code)
	}
	if code := serve(handler, func(r *http.Request) { r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token}) }); code != http.StatusUnauthorized {
		t.Errorf("token as cookie = %d, want 401", code)
	}
}

func TestRevocation_ByIDAndBySubject(t *testing.T) {
	const secret = "revoke-secret"
	revoked, err := newRevocations(nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := requireSession(secret, nil, revoked, okHandler())
	bearer := func(tok string) func(*http.Request) {
		return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
	}

	a, aID := MintAPIToken("ci", secret, RoleMember, time.Hour)
	b, _ := MintAPIToken("ci", secret, RoleMember, time.Hour)
	other, _ := MintAPIToken("other", secret, RoleMember, time.Hour)

	if err := revoked.RevokeToken(aID, time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if serve(handler, bearer(a)) != http.StatusUnauthorized {
		t.Error("a revoked token still works")
	}
	if serve(handler, bearer(b)) != http.StatusOK {
		t.Error("revoking one token revoked its sibling")
	}

	// Tokens carry whole-second timestamps; the cutoff covers the second it was set in.
	if err := revoked.RevokeSubject("CI", time.Now()); err != nil {
		t.Fatal(err)
	}
	if serve(handler, bearer(b)) != http.StatusUnauthorized {
		t.Error("revoking the subject left an older token working")
	}
	if serve(handler, bearer(other)) != http.StatusOK {
		t.Error("revoking one subject affected another")
	}
}

func TestRevocation_PersistsAcrossRestart(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir() + "/r.db")
	if err != nil {
		t.Fatal(err)
	}
	first, err := newRevocations(store)
	if err != nil {
		t.Fatal(err)
	}
	_ = first.RevokeToken("tok-1", time.Now().Add(time.Hour).Unix())
	_ = first.RevokeToken("tok-old", time.Now().Add(-time.Hour).Unix())
	_ = first.RevokeSubject("alice", time.Now())

	second, err := newRevocations(store)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	if !second.revoked(authToken{ID: "tok-1", Username: "x", IssuedAt: now}) {
		t.Error("a revoked token id was forgotten on restart")
	}
	if !second.revoked(authToken{ID: "t", Username: "Alice", IssuedAt: now - 5}) {
		t.Error("a subject revocation was forgotten on restart")
	}
	if second.revoked(authToken{ID: "tok-old", Username: "x", IssuedAt: now}) {
		t.Error("an entry past its expiry should have been dropped")
	}
}

func TestLogoutRevokesTheCookie(t *testing.T) {
	const secret = "logout-secret"
	revoked, _ := newRevocations(nil)
	cookie := mintSessionCookie("alice", secret)
	handler := requireSession(secret, nil, revoked, okHandler())
	withCookie := func(r *http.Request) { r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: cookie}) }

	if serve(handler, withCookie) != http.StatusOK {
		t.Fatal("fresh cookie rejected")
	}
	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: cookie})
	authLogoutHandler(secret, revoked)(httptest.NewRecorder(), req)
	if serve(handler, withCookie) != http.StatusUnauthorized {
		t.Error("a copy of the cookie still works after logout")
	}
}

func TestAPITokenRoleComesFromTheTokenNotFromBindings(t *testing.T) {
	store, err := NewRoleStore(nil, RoleMember, []string{"octocat"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	token := identity{Username: "octocat", Kind: tokenKindAPI, Role: RoleViewer}
	if info := roleForIdentity(req, store, "", nil, token); info.Role != RoleViewer || info.IsAdmin {
		t.Errorf("a token labelled like an admin got %+v, want the viewer role it was minted with", info)
	}
	person := identity{Username: "octocat", Kind: tokenKindSession}
	if info := roleForIdentity(req, store, "", nil, person); !info.IsAdmin {
		t.Errorf("the user octocat got %+v, want admin from the binding", info)
	}
}
