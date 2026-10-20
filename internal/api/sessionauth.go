package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const sessionCookieName = "ego_session"

// authenticatedUsernameKey stashes the username requireSession already
// validated into the request context, so a handler further down the
// chain - the in-process MCP mount (see mcp.go), currently the only user -
// can act on whose request this is without re-parsing the cookie/bearer
// value or re-checking it against GitHub a second time.
type authenticatedUsernameContextKey struct{}

func withAuthenticatedUsername(ctx context.Context, username string) context.Context {
	return context.WithValue(ctx, authenticatedUsernameContextKey{}, username)
}

// authenticatedUsername returns the username requireSession validated for
// this request, if any. Empty/false when auth is disabled (requireSession
// never wraps the handler at all in that case) or - defensively - if
// called somewhere requireSession never ran.
func authenticatedUsername(ctx context.Context) (string, bool) {
	username, ok := ctx.Value(authenticatedUsernameContextKey{}).(string)
	return username, ok && username != ""
}

// readSessionCookie validates a cookie/bearer value against secret and returns the username it authenticates,
// or ok=false. No GitHub API call happens here: org membership w.
func readSessionCookie(value, secret string) (username string, ok bool) {
	raw, err := base64.URLEncoding.DecodeString(value)
	if err != nil {
		return "", false
	}

	s := string(raw)
	sigSep := strings.LastIndexByte(s, '|')
	if sigSep < 0 {
		return "", false
	}
	sig := s[sigSep+1:]
	rest := s[:sigSep]

	expirySep := strings.LastIndexByte(rest, '|')
	if expirySep < 0 {
		return "", false
	}
	expiryStr := rest[expirySep+1:]
	username = rest[:expirySep]

	expiry, err := strconv.ParseInt(expiryStr, 10, 64)
	if err != nil {
		return "", false
	}

	expected := signSessionPayload(secret, username+"|"+expiryStr)
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return "", false
	}
	if time.Now().Unix() > expiry {
		return "", false
	}
	return username, true
}

func signSessionPayload(secret, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// extractSessionUsername. If that bearer value doesn't validate as one of this service's own signed tokens,
// ghTokens (nil when no GitHub OAuth config is set) gets one more try at it as a raw GitHub access token - see
// githubtoken.go.
func extractSessionUsername(r *http.Request, secret string, ghTokens *githubTokenValidator) (string, bool) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		if username, ok := readSessionCookie(cookie.Value, secret); ok {
			return username, true
		}
	}
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		token := strings.TrimSpace(auth[7:])
		if username, ok := readSessionCookie(token, secret); ok {
			return username, true
		}
		if username, ok := ghTokens.validate(token); ok {
			return username, true
		}
	}
	return "", false
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
func requireSession(secret string, ghTokens *githubTokenValidator, handler http.Handler) http.Handler {
	if secret == "" {
		return handler
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if username, ok := extractSessionUsername(r, secret, ghTokens); ok {
			r = r.WithContext(withAuthenticatedUsername(r.Context(), username))
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
