package api

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"regexp"
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

// githubTeamRetryTTL is how long a failed team lookup is remembered before the next request tries again.
const githubTeamRetryTTL = 30 * time.Second

type githubTokenCacheEntry struct {
	username string
	ok       bool
	teams    []string // "org/slug" GitHub team memberships within cfg.Org - see roles.go.
	// teamsKnown is false when the team lookup failed, so teams is not an answer but a gap.
	teamsKnown bool
	expires    time.Time
	// cacheable is false for a lookup that was not made (too many in flight), which must not be remembered.
	cacheable bool
}

// githubLookup is one lookup in flight that concurrent callers of the same token share.
type githubLookup struct {
	done  chan struct{}
	entry githubTokenCacheEntry
}

// Bounds on what unauthenticated callers can make this server do against GitHub.
const (
	maxGitHubCacheEntries = 1000
	maxGitHubLookups      = 8
)

// githubTokenValidator authenticates a bearer value that isn't one of this service's own signed session tokens
// (see extractSessionUsername) by treating it as a live GitHub access token instead - the same check
// authCallbackHandler does after GitHub's OAuth code exchange, just skipping the exchange step itself
// (fetchGitHubLogin + checkOrgMembership are the exact same helpers).
type githubTokenValidator struct {
	cfg    *GitHubOAuthConfig
	client *http.Client

	mu        sync.Mutex
	cache     map[string]githubTokenCacheEntry
	inflight  map[string]*githubLookup
	lookups   chan struct{}
	lastSweep time.Time
}

func newGitHubTokenValidator(cfg *GitHubOAuthConfig) *githubTokenValidator {
	return &githubTokenValidator{
		cfg:      cfg,
		client:   &http.Client{Timeout: 10 * time.Second},
		cache:    make(map[string]githubTokenCacheEntry),
		inflight: make(map[string]*githubLookup),
		lookups:  make(chan struct{}, maxGitHubLookups),
	}
}

// validate returns the GitHub login for token if it is both a valid
// GitHub access token and belongs to an active member of cfg.Org - the
// same two conditions the OAuth callback enforces before minting a
// session cookie. A nil validator (no GitHub OAuth configured) or an
// empty token always fails closed.
func (v *githubTokenValidator) validate(token string) (username string, ok bool) {
	if v == nil || v.cfg == nil || !plausibleGitHubToken(token) {
		return "", false
	}

	key := githubTokenCacheKey(token)
	v.mu.Lock()
	if entry, found := v.cache[key]; found && time.Now().Before(entry.expires) {
		v.mu.Unlock()
		return entry.username, entry.ok
	}
	v.sweepExpiredLocked()
	// Callers presenting the same token wait for the one lookup in flight instead of each calling GitHub.
	if call, found := v.inflight[key]; found {
		v.mu.Unlock()
		<-call.done
		return call.entry.username, call.entry.ok
	}
	call := &githubLookup{done: make(chan struct{})}
	v.inflight[key] = call
	v.mu.Unlock()

	call.entry = v.lookup(token)

	v.mu.Lock()
	delete(v.inflight, key)
	if call.entry.cacheable {
		v.storeLocked(key, call.entry)
	}
	v.mu.Unlock()
	close(call.done)
	return call.entry.username, call.entry.ok
}

// lookup asks GitHub who the token belongs to, whether they are in the org and which teams they are in. At most
// maxGitHubLookups run at once; beyond that the token is refused for now (and not cached), so a flood of distinct
// values cannot pile up requests or push this server's address into GitHub's abuse limits.
func (v *githubTokenValidator) lookup(token string) githubTokenCacheEntry {
	select {
	case v.lookups <- struct{}{}:
		defer func() { <-v.lookups }()
	default:
		return githubTokenCacheEntry{}
	}

	var entry githubTokenCacheEntry
	entry.cacheable = true
	login, err := fetchGitHubLogin(v.client, token)
	if err == nil {
		if member, memberErr := checkOrgMembership(v.client, v.cfg.Org, login, token); memberErr == nil && member {
			entry.username, entry.ok = login, true
		}
	}

	if entry.ok {
		// A failure here (rate limit, transient network error) never fails validate() itself, since org-membership
		// auth must not depend on this role-only lookup. It is remembered as unknown, though, and for a short time
		// only: roles must not fall back to the default role, and the lookup should be retried soon.
		var terr error
		entry.teams, terr = FetchUserTeams(v.client, v.cfg.Org, token)
		entry.teamsKnown = terr == nil
	}

	ttl := githubTokenCacheTTL
	if entry.ok && !entry.teamsKnown {
		ttl = githubTeamRetryTTL
	}
	entry.expires = time.Now().Add(ttl)
	return entry
}

// storeLocked caches an entry, dropping expired ones first and then an arbitrary one when the cache is full.
func (v *githubTokenValidator) storeLocked(key string, entry githubTokenCacheEntry) {
	if len(v.cache) >= maxGitHubCacheEntries {
		now := time.Now()
		for k, e := range v.cache {
			if now.After(e.expires) {
				delete(v.cache, k)
			}
		}
		for k := range v.cache {
			if len(v.cache) < maxGitHubCacheEntries {
				break
			}
			delete(v.cache, k)
		}
	}
	v.cache[key] = entry
}

// githubTokenShape matches what GitHub issues: prefixed OAuth, user, server, refresh and app tokens, fine-grained
// personal access tokens, and the 40-hex-digit classic form. A value that is none of these cannot be a GitHub
// token, so it is refused without calling GitHub.
var githubTokenShape = regexp.MustCompile(`^(gh[pousr]_[A-Za-z0-9]{20,255}|github_pat_[A-Za-z0-9_]{20,255}|[0-9a-f]{40})$`)

func plausibleGitHubToken(token string) bool {
	return githubTokenShape.MatchString(token)
}

// teamsForCached returns the GitHub team memberships cached for token by
// the most recent validate() call - roles.go's resolveTeamsForRequest
// always calls validate(token) first (itself cheap on a cache hit), so
// this never needs to fetch anything on its own.
func (v *githubTokenValidator) teamsForCached(token string) ([]string, bool) {
	key := githubTokenCacheKey(token)
	v.mu.Lock()
	defer v.mu.Unlock()
	if entry, ok := v.cache[key]; ok {
		return entry.teams, entry.teamsKnown
	}
	return nil, false
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
