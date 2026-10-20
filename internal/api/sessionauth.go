package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
)

const sessionCookieName = "ego_session"

// authenticatedUsernameKey stashes the username requireSession already
// validated into the request context, so a handler further down the
// chain - the in-process MCP mount (see mcp.go), currently the only user -
// can act on whose request this is without re-parsing the cookie/bearer
// value or re-checking it against GitHub a second time.
type identityContextKey struct{}

// identity is who a request authenticated as. Kind is the token kind (tokenKindSession, tokenKindAPI) or
// identityGitHub for a raw GitHub access token.
type identity struct {
	Username string
	Kind     string
	Role     Role // API tokens: the role the token was minted with.
	Tenants  []string
	TokenID  string
	Expiry   int64
}

const identityGitHub = "github"

func withIdentity(ctx context.Context, id identity) context.Context {
	return context.WithValue(ctx, identityContextKey{}, id)
}

func identityFromContext(ctx context.Context) (identity, bool) {
	id, ok := ctx.Value(identityContextKey{}).(identity)
	return id, ok && id.Username != ""
}

// authenticatedUsername returns the username requireSession validated for
// this request, if any. Empty/false when auth is disabled (requireSession
// never wraps the handler at all in that case) or - defensively - if
// called somewhere requireSession never ran.
func authenticatedUsername(ctx context.Context) (string, bool) {
	id, ok := identityFromContext(ctx)
	return id.Username, ok
}

func signSessionPayload(secret, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// authenticate identifies the caller. A session cookie is accepted only as the cookie and an API token only as a
// bearer value, so a copied cookie is not a bearer token; both must be unrevoked. A bearer value that is not one
// of this service's own tokens gets one more try as a raw GitHub access token (ghTokens is nil when no GitHub
// OAuth config is set) - see githubtoken.go.
func authenticate(r *http.Request, secret string, ghTokens *githubTokenValidator, revoked *revocations) (identity, bool) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		if t, ok := parseToken(cookie.Value, secret); ok && t.Kind == tokenKindSession && !revoked.revoked(t) {
			return identity{Username: t.Username, Kind: t.Kind, TokenID: t.ID, Expiry: t.Expiry}, true
		}
	}
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		token := strings.TrimSpace(auth[7:])
		if t, ok := parseToken(token, secret); ok {
			if t.Kind == tokenKindAPI && !revoked.revoked(t) {
				return identity{Username: t.Username, Kind: t.Kind, Role: t.Role, Tenants: t.Tenants, TokenID: t.ID, Expiry: t.Expiry}, true
			}
			return identity{}, false
		}
		if username, ok := ghTokens.validate(token); ok {
			return identity{Username: username, Kind: identityGitHub}, true
		}
	}
	return identity{}, false
}

// apiPathPrefixes.
var apiPathPrefixes = []string{"/api/", "/stream"}

func isAPIPath(path string) bool {
	for _, prefix := range apiPathPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// If secret is empty, auth is disabled and handler is returned unwrapped -
// the default for local/port-forward use. ghTokens (nil unless a GitHub
// OAuth config is set) additionally accepts a raw GitHub access token as
// the bearer value - see githubtoken.go/extractSessionUsername - so a
// non-browser client can authenticate with `gh auth token`'s output
// instead of a separately minted ego bearer token.
func requireSession(secret string, ghTokens *githubTokenValidator, revoked *revocations, handler http.Handler) http.Handler {
	if secret == "" {
		return handler
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, ok := authenticate(r, secret, ghTokens, revoked); ok {
			r = r.WithContext(withIdentity(r.Context(), id))
			handler.ServeHTTP(w, r)
			return
		}
		if isAPIPath(r.URL.Path) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "not authenticated"})
			return
		}
		http.Redirect(w, r, "/auth/login", http.StatusFound)
	})
}
