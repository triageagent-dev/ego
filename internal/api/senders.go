package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// DefaultTenantAttr is the span attribute that carries a span's tenant label when --usage-tenant-attr names no
// other. A sender labels its spans; ego never trusts a label beyond the sender's own namespace.
const DefaultTenantAttr = "ego.tenant"

// defaultTenantLabel names the tenant of a sender's spans that carry no label.
const defaultTenantLabel = "default"

var senderIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Tenants. A sender is whoever holds an ingest token: a core, an agent, a plain OpenTelemetry exporter. One sender
// can send many tenants, each named by the label on its spans. The tenant key is "<sender>/<label>", so two senders
// that both send a tenant called "acme" have two different tenants. Spans sent with the shared --otlp-token belong to
// no named sender: their key is the bare label.
//
// A grant (a role binding or a token's scope) names tenants by key, "<sender>/*" for every tenant of a sender, or "*"
// for all of them.

// tenantKey is the key of a tenant label sent by sender ("" for the shared token).
func tenantKey(sender, label string) string {
	if sender == "" {
		return label
	}
	if label == "" {
		label = defaultTenantLabel
	}
	return sender + "/" + label
}

// tenantPatternMatches reports whether grant pattern p covers tenant key k.
func tenantPatternMatches(p, k string) bool {
	switch {
	case p == "*":
		return true
	case p == k:
		return true
	case strings.HasSuffix(p, "/*"):
		return strings.HasPrefix(k, strings.TrimSuffix(p, "*"))
	}
	return false
}

// ParseTenantList validates a comma-separated list of tenant grants: "*", "<sender>/*", "<sender>/<label>" or a bare
// label (for sessions from the shared token). It is used by the --tenants flag of mint-token.
func ParseTenantList(s string) ([]string, error) {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if err := validTenantPattern(p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func validTenantPattern(p string) error {
	if p == "*" {
		return nil
	}
	if strings.ContainsAny(p, " \t\n,|") {
		return fmt.Errorf("invalid tenant %q: no spaces, commas or bars", p)
	}
	if i := strings.Index(p, "/"); i >= 0 {
		if !senderIDPattern.MatchString(p[:i]) || p[i+1:] == "" {
			return fmt.Errorf("invalid tenant %q: want <sender>/<tenant>, <sender>/* or *", p)
		}
	}
	return nil
}

// Sender is one ingest identity.
type Sender struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	CreatedAt string `json:"createdAt"`
}

// SenderToken is the metadata of an ingest token; the token itself is shown once, when it is created.
type SenderToken struct {
	ID        string `json:"id"`
	Sender    string `json:"sender"`
	Label     string `json:"label,omitempty"`
	CreatedAt string `json:"createdAt"`
}

type senderTokenRecord struct {
	SenderToken
	hash string
}

// SenderStore holds the senders and their ingest tokens: in memory always, and in SQLite when an archive is
// configured. Tokens are kept as SHA-256 hashes; a presented token is hashed and looked up.
type SenderStore struct {
	mu      sync.Mutex
	senders map[string]Sender
	tokens  map[string]senderTokenRecord // by token id
	byHash  map[string]string            // token hash -> token id
	archive *SQLiteStore
}

func NewSenderStore(archive *SQLiteStore) (*SenderStore, error) {
	s := &SenderStore{
		senders: map[string]Sender{}, tokens: map[string]senderTokenRecord{}, byHash: map[string]string{}, archive: archive,
	}
	if archive == nil {
		return s, nil
	}
	senders, tokens, err := archive.loadSenders()
	if err != nil {
		return nil, fmt.Errorf("loading senders: %w", err)
	}
	for _, t := range senders {
		s.senders[t.ID] = t
	}
	for _, t := range tokens {
		s.tokens[t.ID] = t
		s.byHash[t.hash] = t.ID
	}
	return s, nil
}

func hashSenderToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// HasTokens reports whether any sender has an ingest token, which makes ingest require one.
func (s *SenderStore) HasTokens() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tokens) > 0
}

// Resolve returns the sender a presented ingest token belongs to.
func (s *SenderStore) Resolve(token string) (sender string, ok bool) {
	if s == nil || token == "" {
		return "", false
	}
	h := hashSenderToken(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	id, found := s.byHash[h]
	if !found {
		return "", false
	}
	return s.tokens[id].Sender, true
}

func (s *SenderStore) List() []Sender {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Sender, 0, len(s.senders))
	for _, t := range s.senders {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *SenderStore) Exists(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.senders[id]
	return ok
}

// Create adds a sender. The id is a lowercase slug, since it prefixes the ids of the sender's sessions and the keys
// of its tenants.
func (s *SenderStore) Create(id, name string) (Sender, error) {
	if !senderIDPattern.MatchString(id) {
		return Sender{}, fmt.Errorf("sender id must be lowercase letters, digits and dashes, 1 to 63 characters, starting with a letter or digit")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.senders[id]; ok {
		return Sender{}, fmt.Errorf("sender %q already exists", id)
	}
	t := Sender{ID: id, Name: strings.TrimSpace(name), CreatedAt: nowRFC3339()}
	if s.archive != nil {
		if err := s.archive.saveSender(t); err != nil {
			return Sender{}, err
		}
	}
	s.senders[id] = t
	return t, nil
}

// Delete removes a sender and its tokens. Its sessions stay, visible to unrestricted roles.
func (s *SenderStore) Delete(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.senders[id]; !ok {
		return false, nil
	}
	if s.archive != nil {
		if err := s.archive.deleteSender(id); err != nil {
			return false, err
		}
	}
	delete(s.senders, id)
	for tid, t := range s.tokens {
		if t.Sender == id {
			delete(s.byHash, t.hash)
			delete(s.tokens, tid)
		}
	}
	return true, nil
}

// NewToken mints an ingest token for sender and returns it; it is not stored in clear and cannot be shown again.
func (s *SenderStore) NewToken(sender, label string) (token string, meta SenderToken, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", SenderToken{}, err
	}
	token = "egs_" + base64.RawURLEncoding.EncodeToString(buf)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.senders[sender]; !ok {
		return "", SenderToken{}, fmt.Errorf("sender %q does not exist", sender)
	}
	rec := senderTokenRecord{
		SenderToken: SenderToken{ID: newRandomID("st"), Sender: sender, Label: strings.TrimSpace(label), CreatedAt: nowRFC3339()},
		hash:        hashSenderToken(token),
	}
	if s.archive != nil {
		if err := s.archive.saveSenderToken(rec); err != nil {
			return "", SenderToken{}, err
		}
	}
	s.tokens[rec.ID] = rec
	s.byHash[rec.hash] = rec.ID
	return token, rec.SenderToken, nil
}

func (s *SenderStore) Tokens(sender string) []SenderToken {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []SenderToken{}
	for _, t := range s.tokens {
		if t.Sender == sender {
			out = append(out, t.SenderToken)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out
}

// RevokeToken deletes one token of sender.
func (s *SenderStore) RevokeToken(sender, tokenID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tokens[tokenID]
	if !ok || t.Sender != sender {
		return false, nil
	}
	if s.archive != nil {
		if err := s.archive.deleteSenderToken(tokenID); err != nil {
			return false, err
		}
	}
	delete(s.byHash, t.hash)
	delete(s.tokens, tokenID)
	return true, nil
}
