package api

import (
	"crypto/hmac"
	"encoding/base64"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Tokens are what the browser cookie and the bearer credential of non-browser clients are made of. Both are
// HMAC-signed under the session secret and carry:
//
//   - a kind: a session cookie (set at login, accepted only as the cookie), an API token (minted with
//     `ego auth mint-token`, accepted only as a bearer value) or a delegate token (the same, minted with --delegate,
//     accepted at /mcp only; see delegate.go), so a leaked cookie is not a bearer token;
//   - an id, so one token can be revoked;
//   - the time it was issued, so everything issued to a subject before a moment can be revoked at once;
//   - for an API token, the role it acts with and the tenants it is limited to, so its name does not pick up the
//     role of a GitHub user of the same name.
const (
	tokenKindSession = "s"
	tokenKindAPI     = "t"
	// tokenKindDelegate is an API token for a client that acts for one tenant at a time and names it on every call.
	tokenKindDelegate = "d"
	tokenVersion      = "3"
)

type authToken struct {
	Kind     string
	ID       string
	Username string
	Role     Role     // API and delegate tokens only.
	Tenants  []string // API tokens: empty means every tenant. Delegate tokens: the grants a call may pick its tenant from, never empty.
	IssuedAt int64
	Expiry   int64
}

func (t authToken) encode(secret string) string {
	payload := strings.Join([]string{
		tokenVersion, t.Kind, t.ID,
		strconv.FormatInt(t.IssuedAt, 10), strconv.FormatInt(t.Expiry, 10),
		string(t.Role), strings.Join(t.Tenants, ","), t.Username,
	}, "|")
	return base64.URLEncoding.EncodeToString([]byte(payload + "|" + signSessionPayload(secret, payload)))
}

// parseToken checks the signature and the lifetime of value. It does not check revocation (see
// revocations.revoked) or GitHub org membership.
func parseToken(value, secret string) (authToken, bool) {
	raw, err := base64.URLEncoding.DecodeString(value)
	if err != nil {
		return authToken{}, false
	}
	s := string(raw)
	sigSep := strings.LastIndexByte(s, '|')
	if sigSep < 0 {
		return authToken{}, false
	}
	payload, sig := s[:sigSep], s[sigSep+1:]
	if !hmac.Equal([]byte(sig), []byte(signSessionPayload(secret, payload))) {
		return authToken{}, false
	}
	// The username comes last, so it may itself contain the separator.
	f := strings.SplitN(payload, "|", 8)
	if len(f) != 8 || f[0] != tokenVersion || (f[1] != tokenKindSession && f[1] != tokenKindAPI && f[1] != tokenKindDelegate) || f[2] == "" || f[7] == "" {
		return authToken{}, false
	}
	issued, err1 := strconv.ParseInt(f[3], 10, 64)
	expiry, err2 := strconv.ParseInt(f[4], 10, 64)
	if err1 != nil || err2 != nil || time.Now().Unix() > expiry {
		return authToken{}, false
	}
	t := authToken{Kind: f[1], ID: f[2], IssuedAt: issued, Expiry: expiry, Role: Role(f[5]), Username: f[7]}
	if f[6] != "" {
		t.Tenants = strings.Split(f[6], ",")
	}
	if t.Kind == tokenKindAPI || t.Kind == tokenKindDelegate {
		if _, err := ParseRole(f[5]); err != nil {
			return authToken{}, false
		}
		if t.Kind == tokenKindDelegate && len(t.Tenants) == 0 {
			return authToken{}, false
		}
	} else if t.Role != "" || len(t.Tenants) > 0 {
		return authToken{}, false
	}
	return t, true
}

// MintAPIToken signs an API token for a non-browser client, valid for ttl, acting with role and limited to tenants
// (none: every tenant). name is a label,
// logged wherever the token authenticates. The id is returned so the token can be revoked later.
func MintAPIToken(name, secret string, role Role, tenants []string, ttl time.Duration) (token, id string) {
	now := time.Now()
	t := authToken{Kind: tokenKindAPI, ID: newRandomID("tok"), Username: name, Role: role, Tenants: tenants, IssuedAt: now.Unix(), Expiry: now.Add(ttl).Unix()}
	return t.encode(secret), t.ID
}

// MintDelegateToken signs a delegate token: an API token that acts for one tenant per call, chosen from tenants.
// It needs at least one grant, and a delegate that could pick any tenant of any sender is not one.
func MintDelegateToken(name, secret string, role Role, tenants []string, ttl time.Duration) (token, id string, err error) {
	if len(tenants) == 0 {
		return "", "", fmt.Errorf("a delegate token needs the tenants it may act for")
	}
	if slices.Contains(tenants, "*") {
		return "", "", fmt.Errorf("a delegate token cannot be granted every tenant (*): name <sender>/* or tenant keys")
	}
	if !slices.ContainsFunc(tenants, func(g string) bool { return !strings.HasSuffix(g, sameLabelSuffix) }) {
		return "", "", fmt.Errorf("a delegate token needs a grant the call can name (<sender>/* or a tenant key) besides <sender>/= ones")
	}
	now := time.Now()
	t := authToken{Kind: tokenKindDelegate, ID: newRandomID("tok"), Username: name, Role: role, Tenants: tenants, IssuedAt: now.Unix(), Expiry: now.Add(ttl).Unix()}
	return t.encode(secret), t.ID, nil
}

func mintSessionCookie(username, secret string) string {
	now := time.Now()
	t := authToken{Kind: tokenKindSession, ID: newRandomID("ses"), Username: username, IssuedAt: now.Unix(), Expiry: now.Add(sessionTTL).Unix()}
	return t.encode(secret)
}

// revocations is the list of tokens that must no longer be accepted although their signature and lifetime are
// fine: single tokens by id, and every token of a subject issued before a moment. Held in memory and, when an
// archive is configured, in SQLite so it survives a restart. A nil *revocations revokes nothing.
type revocations struct {
	mu       sync.Mutex
	ids      map[string]int64 // token id -> its expiry; after that the entry is useless.
	subjects map[string]int64 // subject -> tokens issued at or before this unix second are revoked.
	archive  *SQLiteStore
}

func newRevocations(archive *SQLiteStore) (*revocations, error) {
	r := &revocations{ids: map[string]int64{}, subjects: map[string]int64{}, archive: archive}
	if archive == nil {
		return r, nil
	}
	rows, err := archive.listRevocations()
	if err != nil {
		return nil, fmt.Errorf("loading revocations: %w", err)
	}
	now := time.Now().Unix()
	for _, row := range rows {
		switch row.kind {
		case revokeID:
			if row.value > now {
				r.ids[row.key] = row.value
			}
		case revokeSubject:
			r.subjects[row.key] = row.value
		}
	}
	return r, nil
}

func (r *revocations) revoked(t authToken) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.ids[t.ID]; ok {
		return true
	}
	cutoff, ok := r.subjects[strings.ToLower(t.Username)]
	return ok && t.IssuedAt <= cutoff
}

// RevokeToken stops one token from being accepted until it would have expired anyway.
func (r *revocations) RevokeToken(id string, expiry int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ids[id] = expiry
	if r.archive != nil {
		return r.archive.saveRevocation(revokeID, id, expiry)
	}
	return nil
}

// RevokeSubject stops every token issued to subject up to now from being accepted. Tokens minted afterwards
// work, so this is also how a user is signed out everywhere without locking them out.
func (r *revocations) RevokeSubject(subject string, now time.Time) error {
	key := strings.ToLower(subject)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.subjects[key] = now.Unix()
	if r.archive != nil {
		return r.archive.saveRevocation(revokeSubject, key, now.Unix())
	}
	return nil
}

type revocationDTO struct {
	Kind  string `json:"kind"` // "token" or "subject"
	Key   string `json:"key"`
	Until int64  `json:"until,omitempty"`  // token: its expiry.
	Since int64  `json:"before,omitempty"` // subject: issued up to this second.
}

func (r *revocations) List() []revocationDTO {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]revocationDTO, 0, len(r.ids)+len(r.subjects))
	for id, exp := range r.ids {
		out = append(out, revocationDTO{Kind: "token", Key: id, Until: exp})
	}
	for s, ts := range r.subjects {
		out = append(out, revocationDTO{Kind: "subject", Key: s, Since: ts})
	}
	return out
}
