package api

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"unicode"

	"github.com/triageagent-dev/ego/internal/logx"
)

const (
	maxTenantKeyLen  = 200
	maxTenantNameLen = 80
)

// TenantNames maps a tenant key to a name people recognise. A sender may label its tenants with ids (a workspace
// id, say); an admin gives each one a display name here. The key stays the identity: grants, filters and storage
// all use it, and the name is only what the switcher and the session badges show.
type TenantNames struct {
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

func printable(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
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
	if len(name) > maxTenantNameLen || !printable(name) {
		return fmt.Errorf("name must be at most %d printable characters", maxTenantNameLen)
	}
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
	if err := t.Set(key, name); err != nil {
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
