package api

import (
	"net/http"
	"sort"
	"strings"
)

// narrowTenants applies the tenant a user picked in the switcher (?tenant=) to what their role allows. No pick
// leaves the role's tenants as they are. A pick the role does not cover leaves nothing: it never widens access.
func narrowTenants(allowed []string, want string) []string {
	want = strings.TrimSpace(want)
	if want == "" || want == "*" {
		return allowed
	}
	if allowed == nil {
		return []string{want}
	}
	for _, p := range allowed {
		if p == want || tenantPatternMatches(p, want) {
			return []string{want}
		}
	}
	return []string{}
}

// tenantOption is one entry of the tenant switcher.
type tenantOption struct {
	Key      string `json:"key"`
	Sender   string `json:"sender,omitempty"`
	Label    string `json:"label"`
	Sessions int    `json:"sessions"`
}

func newTenantOption(key string, sessions int) tenantOption {
	o := tenantOption{Key: key, Label: key, Sessions: sessions}
	if i := strings.Index(key, "/"); i > 0 {
		o.Sender, o.Label = key[:i], key[i+1:]
	}
	return o
}

// tenantsHandler implements GET /api/tenants: the tenants the caller may switch between. They come from the
// sessions in memory, narrowed to the caller's grants, so a limited user is only ever offered their own. A tenant
// that left only usage rows behind is not listed, and neither are senders with no data yet.
func tenantsHandler(store *SessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		info := roleInfoOrOpen(r.Context())
		if info.Scoped() {
			writeEvaluateError(w, http.StatusForbidden, "this view is not available to an agent-scoped role")
			return
		}
		counts := map[string]int{}
		for _, s := range store.List() {
			for _, k := range s.Tenants {
				if info.AllowsAnyTenant([]string{k}) {
					counts[k]++
				}
			}
		}
		out := make([]tenantOption, 0, len(counts))
		for k, n := range counts {
			out = append(out, newTenantOption(k, n))
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
		writeJSON(w, http.StatusOK, map[string]any{"data": out, "error": nil})
	}
}
