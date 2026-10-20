package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestGitHubOAuthConfigFromParams_DisabledWhenNoClientCreds(t *testing.T) {
	cfg, err := GitHubOAuthConfigFromParams("", "", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg != nil {
		t.Errorf("cfg = %+v, want nil (auth disabled)", cfg)
	}
}

func TestGitHubOAuthConfigFromParams_RequiresOrg(t *testing.T) {
	_, err := GitHubOAuthConfigFromParams("id", "secret", "", "session-secret", "https://example.com")
	if err == nil {
		t.Fatal("expected an error when org is missing")
	}
}

func TestGitHubOAuthConfigFromParams_RequiresSessionSecret(t *testing.T) {
	_, err := GitHubOAuthConfigFromParams("id", "secret", "my-org", "", "https://example.com")
	if err == nil {
		t.Fatal("expected an error when session secret is missing")
	}
}

func TestGitHubOAuthConfigFromParams_RequiresPublicURL(t *testing.T) {
	_, err := GitHubOAuthConfigFromParams("id", "secret", "my-org", "session-secret", "")
	if err == nil {
		t.Fatal("expected an error when public URL is missing")
	}
}

func TestGitHubOAuthConfigFromParams_Valid(t *testing.T) {
	cfg, err := GitHubOAuthConfigFromParams("id", "secret", "my-org", "session-secret", "https://example.com/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("cfg = nil, want non-nil")
	}
	if want := "https://example.com/auth/callback"; cfg.callbackURL() != want {
		t.Errorf("callbackURL() = %q, want %q (trailing slash on PublicURL must be trimmed)", cfg.callbackURL(), want)
	}
}

func TestMintSessionCookie_RoundTripsWithReadSessionCookie(t *testing.T) {
	const secret = "test-secret"
	cookie := mintSessionCookie("octocat", secret)
	username, ok := readSessionCookie(cookie, secret)
	if !ok {
		t.Fatal("expected the freshly minted cookie to validate")
	}
	if username != "octocat" {
		t.Errorf("username = %q, want %q", username, "octocat")
	}
}

func TestMintSessionCookie_RejectsWrongSecret(t *testing.T) {
	cookie := mintSessionCookie("octocat", "secret-a")
	if _, ok := readSessionCookie(cookie, "secret-b"); ok {
		t.Error("expected validation to fail with the wrong secret")
	}
}

func TestVerifyState(t *testing.T) {
	tests := []struct {
		name        string
		cookie, qry string
		want        bool
	}{
		{"match", "abc123", "abc123", true},
		{"mismatch", "abc123", "xyz789", false},
		{"empty cookie", "", "abc123", false},
		{"empty query", "abc123", "", false},
		{"both empty", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := verifyState(tt.cookie, tt.qry); got != tt.want {
				t.Errorf("verifyState(%q, %q) = %v, want %v", tt.cookie, tt.qry, got, tt.want)
			}
		})
	}
}

func TestMakeStateToken_UniqueAndNonEmpty(t *testing.T) {
	a, err := makeStateToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, err := makeStateToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a == "" || b == "" {
		t.Fatal("expected non-empty state tokens")
	}
	if a == b {
		t.Error("expected two calls to produce different tokens")
	}
}

func TestAuthLoginHandler_RedirectsToGitHubWithStateCookie(t *testing.T) {
	cfg := &GitHubOAuthConfig{ClientID: "my-client-id", PublicURL: "https://example.com"}
	req := httptest.NewRequest(http.MethodGet, "/auth/login", nil)
	rec := httptest.NewRecorder()

	authLoginHandler(cfg)(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parsing Location header: %v", err)
	}
	if got := loc.Scheme + "://" + loc.Host + loc.Path; got != githubAuthorizeURL {
		t.Errorf("redirect target = %q, want %q", got, githubAuthorizeURL)
	}
	q := loc.Query()
	if q.Get("client_id") != cfg.ClientID {
		t.Errorf("client_id = %q, want %q", q.Get("client_id"), cfg.ClientID)
	}
	if q.Get("redirect_uri") != cfg.callbackURL() {
		t.Errorf("redirect_uri = %q, want %q", q.Get("redirect_uri"), cfg.callbackURL())
	}
	if q.Get("scope") != "read:org" {
		t.Errorf("scope = %q, want %q", q.Get("scope"), "read:org")
	}
	state := q.Get("state")
	if state == "" {
		t.Fatal("expected a non-empty state param")
	}

	var stateCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == stateCookieName {
			stateCookie = c
		}
	}
	if stateCookie == nil {
		t.Fatal("expected a state cookie to be set")
	}
	if stateCookie.Value != state {
		t.Errorf("state cookie value = %q, want it to match the redirect's state param %q", stateCookie.Value, state)
	}
	if !stateCookie.HttpOnly || !stateCookie.Secure || stateCookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("state cookie flags = %+v, want HttpOnly+Secure+SameSite=Lax", stateCookie)
	}
}

func TestAuthLogoutHandler_ClearsSessionCookieAndRedirects(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/auth/logout", nil)
	rec := httptest.NewRecorder()

	authLogoutHandler()(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	if got := rec.Header().Get("Location"); got != "/evals" {
		t.Errorf("Location = %q, want %q", got, "/evals")
	}
	var cleared *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			cleared = c
		}
	}
	if cleared == nil || cleared.MaxAge >= 0 {
		t.Errorf("expected the session cookie to be cleared (MaxAge < 0), got %+v", cleared)
	}
}

// TestAuthCallbackHandler_FullFlow exercises the entire callback against a
// fake GitHub (httptest.Server standing in for github.com's token/user/org
// endpoints), including the "only 204 means member" rule.
func TestAuthCallbackHandler_FullFlow(t *testing.T) {
	tests := []struct {
		name           string
		membershipCode int
		wantStatus     int
		wantCookieSet  bool
	}{
		{"member (204)", http.StatusNoContent, http.StatusFound, true},
		{"not a member (404)", http.StatusNotFound, http.StatusForbidden, false},
		{"membership hidden (302 must still deny)", http.StatusFound, http.StatusForbidden, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/login/oauth/access_token":
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "fake-token"})
				case r.URL.Path == "/user":
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]string{"login": "octocat"})
				case r.URL.Path == "/orgs/my-org/members/octocat":
					w.WriteHeader(tt.membershipCode)
				default:
					t.Errorf("unexpected request to %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer fake.Close()

			origAuthorize, origToken, origAPI := githubAuthorizeURL, githubTokenURL, githubAPIBaseURL
			githubAuthorizeURL = fake.URL + "/login/oauth/authorize"
			githubTokenURL = fake.URL + "/login/oauth/access_token"
			githubAPIBaseURL = fake.URL
			defer func() { githubAuthorizeURL, githubTokenURL, githubAPIBaseURL = origAuthorize, origToken, origAPI }()

			cfg := &GitHubOAuthConfig{
				ClientID:      "my-client-id",
				ClientSecret:  "my-client-secret",
				Org:           "my-org",
				SessionSecret: "my-session-secret",
				PublicURL:     "https://example.com",
			}

			req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=abc&state=xyz", nil)
			req.AddCookie(&http.Cookie{Name: stateCookieName, Value: "xyz"})
			rec := httptest.NewRecorder()

			authCallbackHandler(cfg, nil)(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tt.wantStatus, rec.Body.String())
			}

			var sessionCookie *http.Cookie
			for _, c := range rec.Result().Cookies() {
				if c.Name == sessionCookieName {
					sessionCookie = c
				}
			}
			if tt.wantCookieSet {
				if sessionCookie == nil {
					t.Fatal("expected a session cookie to be set")
				}
				username, ok := readSessionCookie(sessionCookie.Value, cfg.SessionSecret)
				if !ok || username != "octocat" {
					t.Errorf("minted cookie readback = (%q, %v), want (\"octocat\", true)", username, ok)
				}
				if got := rec.Header().Get("Location"); got != "/evals" {
					t.Errorf("Location = %q, want %q", got, "/evals")
				}
			} else if sessionCookie != nil {
				t.Errorf("expected no session cookie to be set on denial, got %+v", sessionCookie)
			}
		})
	}
}

func TestAuthCallbackHandler_RejectsBadState(t *testing.T) {
	cfg := &GitHubOAuthConfig{ClientID: "id", ClientSecret: "secret", Org: "my-org", SessionSecret: "s", PublicURL: "https://example.com"}
	req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=abc&state=xyz", nil)
	req.AddCookie(&http.Cookie{Name: stateCookieName, Value: "different"})
	rec := httptest.NewRecorder()

	authCallbackHandler(cfg, nil)(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestAuthCallbackHandler_RejectsMissingCode(t *testing.T) {
	cfg := &GitHubOAuthConfig{ClientID: "id", ClientSecret: "secret", Org: "my-org", SessionSecret: "s", PublicURL: "https://example.com"}
	req := httptest.NewRequest(http.MethodGet, "/auth/callback?state=xyz", nil)
	req.AddCookie(&http.Cookie{Name: stateCookieName, Value: "xyz"})
	rec := httptest.NewRecorder()

	authCallbackHandler(cfg, nil)(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}
