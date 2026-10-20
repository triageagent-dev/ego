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

// DefaultTenantAttr is the span attribute that carries a span's tenant when --usage-tenant-attr names no other.
// Ingest overwrites it with the tenant of the sender's token, so a sender cannot claim another tenant's name.
const DefaultTenantAttr = "ego.tenant"

var tenantIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Tenant is one sender (or group of senders) whose data only its own users, and admins, can see.
type Tenant struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	CreatedAt string `json:"createdAt"`
}

// TenantToken is the metadata of an ingest token; the token itself is shown once, when it is created.
type TenantToken struct {
	ID        string `json:"id"`
	Tenant    string `json:"tenant"`
	Label     string `json:"label,omitempty"`
	CreatedAt string `json:"createdAt"`
}

type tenantTokenRecord struct {
	TenantToken
	hash string
}

// TenantStore holds the tenants and their ingest tokens: in memory always, and in SQLite when an archive is
// configured. Tokens are kept as SHA-256 hashes; a presented token is hashed and looked up.
type TenantStore struct {
	mu      sync.Mutex
	tenants map[string]Tenant
	tokens  map[string]tenantTokenRecord // by token id
	byHash  map[string]string            // token hash -> token id
	archive *SQLiteStore
}

func NewTenantStore(archive *SQLiteStore) (*TenantStore, error) {
	s := &TenantStore{
		tenants: map[string]Tenant{}, tokens: map[string]tenantTokenRecord{}, byHash: map[string]string{}, archive: archive,
	}
	if archive == nil {
		return s, nil
	}
	tenants, tokens, err := archive.loadTenants()
	if err != nil {
		return nil, fmt.Errorf("loading tenants: %w", err)
	}
	for _, t := range tenants {
		s.tenants[t.ID] = t
	}
	for _, t := range tokens {
		s.tokens[t.ID] = t
		s.byHash[t.hash] = t.ID
	}
	return s, nil
}

func hashTenantToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// HasTokens reports whether any tenant has an ingest token, which makes ingest require one.
func (s *TenantStore) HasTokens() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tokens) > 0
}

// Resolve returns the tenant a presented ingest token belongs to.
func (s *TenantStore) Resolve(token string) (tenant string, ok bool) {
	if s == nil || token == "" {
		return "", false
	}
	h := hashTenantToken(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	id, found := s.byHash[h]
	if !found {
		return "", false
	}
	return s.tokens[id].Tenant, true
}

func (s *TenantStore) List() []Tenant {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Tenant, 0, len(s.tenants))
	for _, t := range s.tenants {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *TenantStore) Exists(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.tenants[id]
	return ok
}

// Create adds a tenant. The id is a lowercase slug, since it prefixes the ids of the tenant's sessions.
func (s *TenantStore) Create(id, name string) (Tenant, error) {
	if !tenantIDPattern.MatchString(id) {
		return Tenant{}, fmt.Errorf("tenant id must be lowercase letters, digits and dashes, 1 to 63 characters, starting with a letter or digit")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tenants[id]; ok {
		return Tenant{}, fmt.Errorf("tenant %q already exists", id)
	}
	t := Tenant{ID: id, Name: strings.TrimSpace(name), CreatedAt: nowRFC3339()}
	if s.archive != nil {
		if err := s.archive.saveTenant(t); err != nil {
			return Tenant{}, err
		}
	}
	s.tenants[id] = t
	return t, nil
}

// Delete removes a tenant and its tokens. Its sessions stay, visible to unrestricted roles.
func (s *TenantStore) Delete(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tenants[id]; !ok {
		return false, nil
	}
	if s.archive != nil {
		if err := s.archive.deleteTenant(id); err != nil {
			return false, err
		}
	}
	delete(s.tenants, id)
	for tid, t := range s.tokens {
		if t.Tenant == id {
			delete(s.byHash, t.hash)
			delete(s.tokens, tid)
		}
	}
	return true, nil
}

// NewToken mints an ingest token for tenant and returns it; it is not stored in clear and cannot be shown again.
func (s *TenantStore) NewToken(tenant, label string) (token string, meta TenantToken, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", TenantToken{}, err
	}
	token = "egt_" + base64.RawURLEncoding.EncodeToString(buf)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tenants[tenant]; !ok {
		return "", TenantToken{}, fmt.Errorf("tenant %q does not exist", tenant)
	}
	rec := tenantTokenRecord{
		TenantToken: TenantToken{ID: newRandomID("tt"), Tenant: tenant, Label: strings.TrimSpace(label), CreatedAt: nowRFC3339()},
		hash:        hashTenantToken(token),
	}
	if s.archive != nil {
		if err := s.archive.saveTenantToken(rec); err != nil {
			return "", TenantToken{}, err
		}
	}
	s.tokens[rec.ID] = rec
	s.byHash[rec.hash] = rec.ID
	return token, rec.TenantToken, nil
}

func (s *TenantStore) Tokens(tenant string) []TenantToken {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []TenantToken{}
	for _, t := range s.tokens {
		if t.Tenant == tenant {
			out = append(out, t.TenantToken)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out
}

// RevokeToken deletes one token of tenant.
func (s *TenantStore) RevokeToken(tenant, tokenID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tokens[tokenID]
	if !ok || t.Tenant != tenant {
		return false, nil
	}
	if s.archive != nil {
		if err := s.archive.deleteTenantToken(tokenID); err != nil {
			return false, err
		}
	}
	delete(s.byHash, t.hash)
	delete(s.tokens, tokenID)
	return true, nil
}

// ParseTenantList splits a comma-separated list of tenant ids (for example the --tenants flag of mint-token).
func ParseTenantList(s string) ([]string, error) {
	var out []string
	for _, t := range strings.Split(s, ",") {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if !tenantIDPattern.MatchString(t) {
			return nil, fmt.Errorf("invalid tenant %q: lowercase letters, digits and dashes only", t)
		}
		out = append(out, t)
	}
	return out, nil
}
