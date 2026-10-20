package api

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sync"
	"time"
)

// githubTokenCacheTTL bounds how long a validated raw GitHub token (e.g.
// `gh auth token`'s output) is trusted before this service re-checks it
// against GitHub's API - long enough that a burst of calls in one MCP
// session or browser session costs one GitHub API round trip, not one per
// call, short enough that a revoked token or org membership change is
// re-checked promptly rather than trusted indefinitely the way our own
// long-lived mint-token tokens are (those are trusted until EGO_
// SESSION_SECRET itself is rotated).
const githubTokenCacheTTL = 5 * time.Minute

type githubTokenCacheEntry struct {
	username string
	ok       bool
	teams    []string // "org/slug" GitHub team memberships within cfg.Org - see roles.go.
	expires  time.Time
}

// githubTokenValidator authenticates a bearer value that isn't one of this service's own signed session tokens
// (see extractSessionUsername) by treating it as a live GitHub access token instead - the same check
// authCallbackHandler does after GitHub's OAuth code exchange, just skipping the exchange step itself
// (fetchGitHubLogin + checkOrgMembership are the exact same helpers).
type githubTokenValidator struct {
	cfg    *GitHubOAuthConfig
	client *http.Client

	mu        sync.Mutex
	cache     map[string]githubTokenCacheEntry
	lastSweep time.Time
}

func newGitHubTokenValidator(cfg *GitHubOAuthConfig) *githubTokenValidator {
	return &githubTokenValidator{
		cfg:    cfg,
		client: &http.Client{Timeout: 10 * time.Second},
		cache:  make(map[string]githubTokenCacheEntry),
	}
}

// validate returns the GitHub login for token if it is both a valid
// GitHub access token and belongs to an active member of cfg.Org - the
// same two conditions the OAuth callback enforces before minting a
// session cookie. A nil validator (no GitHub OAuth configured) or an
// empty token always fails closed.
func (v *githubTokenValidator) validate(token string) (username string, ok bool) {
	if v == nil || v.cfg == nil || token == "" {
		return "", false
	}

	key := githubTokenCacheKey(token)
	v.mu.Lock()
	if entry, found := v.cache[key]; found && time.Now().Before(entry.expires) {
		v.mu.Unlock()
		return entry.username, entry.ok
	}
	v.sweepExpiredLocked()
	v.mu.Unlock()

	login, err := fetchGitHubLogin(v.client, token)
	if err == nil {
		if member, memberErr := checkOrgMembership(v.client, v.cfg.Org, login, token); memberErr == nil && member {
			username, ok = login, true
		}
	}

	var teams []string
	if ok {
		// Best-effort: a failure here (rate limit, transient network error)
		// just means team-based role bindings won't apply to this token
		// yet, same as if none had been configured - it never fails
		// validate() itself, since org-membership auth must not depend on
		// this extra, role-only lookup succeeding.
		teams, _ = FetchUserTeams(v.client, v.cfg.Org, token)
	}

	v.mu.Lock()
	v.cache[key] = githubTokenCacheEntry{username: username, ok: ok, teams: teams, expires: time.Now().Add(githubTokenCacheTTL)}
	v.mu.Unlock()
	return username, ok
}

// teamsForCached returns the GitHub team memberships cached for token by
// the most recent validate() call - roles.go's resolveTeamsForRequest
// always calls validate(token) first (itself cheap on a cache hit), so
// this never needs to fetch anything on its own.
func (v *githubTokenValidator) teamsForCached(token string) []string {
	key := githubTokenCacheKey(token)
	v.mu.Lock()
	defer v.mu.Unlock()
	if entry, ok := v.cache[key]; ok {
		return entry.teams
	}
	return nil
}

// sweepExpiredLocked drops expired cache entries, at most once per
// githubTokenCacheTTL, so a long-running server that sees many distinct
// bearer tokens over time (many different MCP/CLI callers, each with
// their own `gh auth token` value) doesn't grow this map unboundedly -
// entries are otherwise only ever added, never removed on their own.
// Must be called with v.mu held.
func (v *githubTokenValidator) sweepExpiredLocked() {
	now := time.Now()
	if now.Sub(v.lastSweep) < githubTokenCacheTTL {
		return
	}
	v.lastSweep = now
	for key, entry := range v.cache {
		if now.After(entry.expires) {
			delete(v.cache, key)
		}
	}
}

// githubTokenCacheKey hashes token rather than using it verbatim as a map
// key, so a raw GitHub access token is never held in memory longer than
// the single validate() call that receives it.
func githubTokenCacheKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
