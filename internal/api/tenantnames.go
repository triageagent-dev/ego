package api

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/triageagent-dev/ego/internal/logx"
)

const (
	maxTenantKeyLen  = 200
	maxTenantNameLen = 80
	// maxTenantNames bounds the names table, which a sender can grow one tenant at a time.
	maxTenantNames = 10000
)

// TenantNames maps a tenant key to a name people recognise. A sender may label its tenants with ids (a workspace
// id, say); an admin gives each one a display name here. The key stays the identity: grants, filters and storage
// all use it, and the name is only what the switcher and the session badges show.
type TenantNames struct {
	// opMu serialises Set and Fill, so Fill's check and its write are one step against an admin's Set.
	opMu    sync.Mutex
	mu      sync.RWMutex
	names   map[string]string
	archive *SQLiteStore
}

func NewTenantNames(archive *SQLiteStore) (*TenantNames, error) {
	t := &TenantNames{names: map[string]string{}, archive: archive}
	if archive == nil {
		return t, nil
	}
	loaded, err := archive.loadTenantNames()
	if err != nil {
		return nil, fmt.Errorf("loading tenant names: %w", err)
	}
	t.names = loaded
	return t, nil
}

// printable reports that s holds no control character and no format character (bidirectional overrides, zero-width
// joiners and the like), which let one name pass for another.
func printable(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
			return false
		}
	}
	return true
}

// Set names a tenant key; an empty name clears it.
func (t *TenantNames) Set(key, name string) error {
	key, name = strings.TrimSpace(key), strings.TrimSpace(name)
	if key == "" || len(key) > maxTenantKeyLen || !printable(key) {
		return fmt.Errorf("key must be 1-%d printable characters", maxTenantKeyLen)
	}
	if utf8.RuneCountInString(name) > maxTenantNameLen || !printable(name) {
		return fmt.Errorf("name must be at most %d printable characters", maxTenantNameLen)
	}
	t.opMu.Lock()
	defer t.opMu.Unlock()
	return t.setLocked(key, name)
}

// setLocked stores the name; the caller holds opMu.
func (t *TenantNames) setLocked(key, name string) error {
	if t.archive != nil {
		var err error
		if name == "" {
			err = t.archive.deleteTenantName(key)
		} else {
			err = t.archive.saveTenantName(key, name)
		}
		if err != nil {
			return err
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if name == "" {
		delete(t.names, key)
	} else {
		t.names[key] = name
	}
	return nil
}

// Fill names a tenant the way its sender called it, when nobody has named it: a name set by hand always wins, and a
// name that is not acceptable is ignored.
func (t *TenantNames) Fill(key, name string) {
	if t == nil || t.Name(key) != "" {
		return
	}
	key, name = strings.TrimSpace(key), strings.TrimSpace(name)
	if key == "" || len(key) > maxTenantKeyLen || !printable(key) || !printable(name) {
		logx.Debugf("tenant name from %q ignored: not printable", key)
		return
	}
	// A name that is too long is cut, not dropped: the tenant still gets a name.
	if r := []rune(name); len(r) > maxTenantNameLen {
		name = string(r[:maxTenantNameLen])
	}
	t.opMu.Lock()
	defer t.opMu.Unlock()
	// The check again, now that no admin's Set can come between it and the write.
	t.mu.RLock()
	have, full := t.names[key] != "", len(t.names) >= maxTenantNames
	t.mu.RUnlock()
	if have {
		return
	}
	if full {
		logx.Debugf("tenant name from %q ignored: the names table is full", key)
		return
	}
	if err := t.setLocked(key, name); err != nil {
		logx.Debugf("tenant name from %q ignored: %v", key, err)
	}
}

func (t *TenantNames) Name(key string) string {
	if t == nil {
		return ""
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.names[key]
}

func (t *TenantNames) All() map[string]string {
	out := map[string]string{}
	if t == nil {
		return out
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	for k, v := range t.names {
		out[k] = v
	}
	return out
}

type tenantNameRequest struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// adminTenantNamesHandler implements GET (list) and POST (set; an empty name clears) on /api/admin/tenant-names.
// Admin only.
func adminTenantNamesHandler(names *TenantNames) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !roleInfoOrOpen(r.Context()).IsAdmin {
			writeRoleForbidden(w)
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, map[string]any{"data": names.All(), "error": nil})
		case http.MethodPost:
			var req tenantNameRequest
			if err := decodeJSON(w, r, &req); err != nil {
				writeEvaluateError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
				return
			}
			if err := names.Set(req.Key, req.Name); err != nil {
				writeEvaluateError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": map[string]string{"key": strings.TrimSpace(req.Key), "name": strings.TrimSpace(req.Name)}, "error": nil})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}
