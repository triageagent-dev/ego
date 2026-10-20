package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// githubAPIStub spins up a fake GitHub API server for exactly the two
// endpoints githubTokenValidator hits (fetchGitHubLogin/checkOrgMembership)
// and points githubAPIBaseURL at it for the duration of the test, so no
// real network call to api.github.com ever happens here.
func githubAPIStub(t *testing.T, login string, isMember bool) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token valid-gh-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"login":"` + login + `"}`))
	})
	mux.HandleFunc("/orgs/triageagent-dev/members/"+login, func(w http.ResponseWriter, r *http.Request) {
		if isMember {
			w.WriteHeader(http.StatusNoContent)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	original := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	t.Cleanup(func() { githubAPIBaseURL = original })
}

func TestGitHubTokenValidator_AcceptsActiveOrgMember(t *testing.T) {
	githubAPIStub(t, "octocat", true)
	v := newGitHubTokenValidator(&GitHubOAuthConfig{Org: "triageagent-dev"})

	username, ok := v.validate("valid-gh-token")
	if !ok || username != "octocat" {
		t.Fatalf("validate() = (%q, %v), want (\"octocat\", true)", username, ok)
	}
}

func TestGitHubTokenValidator_RejectsNonMember(t *testing.T) {
	githubAPIStub(t, "octocat", false)
	v := newGitHubTokenValidator(&GitHubOAuthConfig{Org: "triageagent-dev"})

	if _, ok := v.validate("valid-gh-token"); ok {
		t.Fatal("validate() succeeded for a token belonging to a non-member")
	}
}

func TestGitHubTokenValidator_RejectsInvalidToken(t *testing.T) {
	githubAPIStub(t, "octocat", true)
	v := newGitHubTokenValidator(&GitHubOAuthConfig{Org: "triageagent-dev"})

	if _, ok := v.validate("garbage-token"); ok {
		t.Fatal("validate() succeeded for a token the fake GitHub API rejects")
	}
}

func TestGitHubTokenValidator_NilValidatorFailsClosed(t *testing.T) {
	var v *githubTokenValidator
	if _, ok := v.validate("anything"); ok {
		t.Fatal("nil validator must fail closed, not authenticate")
	}
}

func TestGitHubTokenValidator_CachesResult(t *testing.T) {
	calls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"login":"octocat"}`))
	})
	mux.HandleFunc("/orgs/triageagent-dev/members/octocat", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	original := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	t.Cleanup(func() { githubAPIBaseURL = original })

	v := newGitHubTokenValidator(&GitHubOAuthConfig{Org: "triageagent-dev"})
	for i := 0; i < 3; i++ {
		if _, ok := v.validate("valid-gh-token"); !ok {
			t.Fatalf("validate() call %d failed", i)
		}
	}
	if calls != 1 {
		t.Fatalf("GitHub /user was hit %d times, want 1 (cache should absorb the other calls)", calls)
	}
}

// TestExtractSessionUsername_GitHubTokenFallback exercises the full path
// used by requireSession: a bearer value that isn't a valid signed
// session token but is a valid GitHub token for an org member still
// authenticates.
func TestExtractSessionUsername_GitHubTokenFallback(t *testing.T) {
	githubAPIStub(t, "octocat", true)
	ghTokens := newGitHubTokenValidator(&GitHubOAuthConfig{Org: "triageagent-dev"})

	req := httptest.NewRequest(http.MethodGet, "/api/runs", nil)
	req.Header.Set("Authorization", "Bearer valid-gh-token")

	username, ok := extractSessionUsername(req, "session-secret", ghTokens)
	if !ok || username != "octocat" {
		t.Fatalf("extractSessionUsername() = (%q, %v), want (\"octocat\", true)", username, ok)
	}
}

func TestRequireSession_AcceptsGitHubTokenBearer(t *testing.T) {
	githubAPIStub(t, "octocat", true)
	ghTokens := newGitHubTokenValidator(&GitHubOAuthConfig{Org: "triageagent-dev"})

	var sawUsername string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawUsername, _ = authenticatedUsername(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	handler := requireSession("session-secret", ghTokens, inner)

	req := httptest.NewRequest(http.MethodGet, "/api/runs", nil)
	req.Header.Set("Authorization", "Bearer valid-gh-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if sawUsername != "octocat" {
		t.Fatalf("authenticatedUsername in request context = %q, want octocat", sawUsername)
	}
}

func TestRequireSession_RejectsInvalidGitHubToken(t *testing.T) {
	githubAPIStub(t, "octocat", true)
	ghTokens := newGitHubTokenValidator(&GitHubOAuthConfig{Org: "triageagent-dev"})
	handler := requireSession("session-secret", ghTokens, okHandler())

	req := httptest.NewRequest(http.MethodGet, "/api/runs", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}
