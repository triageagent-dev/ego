package api

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// identityPinPrefix starts the online_state keys that hold the GitHub account id first seen for a login.
const identityPinPrefix = "github_id:"

// identityPins remembers which GitHub account (its numeric id, which never changes) first signed in under each login.
// Role bindings and bootstrap admins name logins, and GitHub lets a login that was renamed away be taken by another
// account: that account is refused here until an admin forgets the pin. Kept in the archive when there is one, else
// in memory for the life of the process. A nil *identityPins accepts everyone.
type identityPins struct {
	mu    sync.Mutex
	store *SQLiteStore
	mem   map[string]string
}

func newIdentityPins(store *SQLiteStore) *identityPins {
	return &identityPins{store: store, mem: map[string]string{}}
}

// check reports whether id is the account pinned to login, pinning it when the login is new. An id of 0 (GitHub sent
// none) is accepted and pins nothing.
func (p *identityPins) check(login string, id int64) (bool, error) {
	if p == nil || id == 0 {
		return true, nil
	}
	key, want := identityPinPrefix+strings.ToLower(login), strconv.FormatInt(id, 10)
	p.mu.Lock()
	defer p.mu.Unlock()
	have := p.mem[key]
	if have == "" && p.store != nil {
		v, err := p.store.OnlineState(key)
		if err != nil {
			return false, err
		}
		have = v
	}
	if have == "" {
		if p.store != nil {
			if err := p.store.SetOnlineState(key, want); err != nil {
				return false, err
			}
		}
		have = want
	}
	p.mem[key] = have
	return have == want, nil
}

// forget drops the pin of login, so the next account to sign in under it is pinned.
func (p *identityPins) forget(login string) error {
	if p == nil {
		return nil
	}
	key := identityPinPrefix + strings.ToLower(login)
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.mem, key)
	if p.store != nil {
		return p.store.SetOnlineState(key, "")
	}
	return nil
}

// adminIdentityHandler implements DELETE /api/admin/identities/{login}: forget which GitHub account a login was
// pinned to, for a login that legitimately changed hands. Admin only.
func adminIdentityHandler(pins *identityPins) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !roleInfoOrOpen(r.Context()).IsAdmin {
			writeRoleForbidden(w)
			return
		}
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		login := r.PathValue("login")
		if err := pins.forget(login); err != nil {
			writeEvaluateError(w, http.StatusInternalServerError, "failed to forget the account: "+err.Error())
			return
		}
		auditLog(r.Context(), "identity.forget", "login", login)
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"login": login}, "error": nil})
	}
}
