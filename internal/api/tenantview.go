package api

import (
	"errors"
	"net/http"
	"sort"
	"strings"
)

// narrowTenants applies the tenant a user picked in the switcher (?tenant=) to what their role allows. No pick
// leaves the role's tenants as they are. A pick the role does not cover leaves nothing: it never widens access.
// A pick of "<sender>/*" means all of that sender's tenants the role covers. It travels as "<sender>/" because
// gateways and WAFs refuse a "/*" in a query string as a comment signature.
func narrowTenants(allowed []string, want string) []string {
	want = strings.TrimSpace(want)
	if want == "" || want == "*" {
		return allowed
	}
	if strings.HasSuffix(want, "/") {
		want += "*"
	}
	if prefix, ok := strings.CutSuffix(want, "*"); ok && strings.HasSuffix(prefix, "/") {
		if allowed == nil {
			return []string{want}
		}
		out := []string{}
		for _, p := range allowed {
			if p == "*" || p == want {
				return []string{want}
			}
			if strings.HasPrefix(p, prefix) {
				out = append(out, p)
			}
		}
		return out
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
	Name     string `json:"name,omitempty"`
	Sessions int    `json:"sessions"`
	// Services are the programs that reported into the tenant, by the service.name their sender declares, the one
	// with most sessions first. A filter on the session list, never a grant: the role is checked on the tenant.
	Services []serviceOption `json:"services,omitempty"`
}

// serviceOption is one service of a tenant and how many of its sessions it reported.
type serviceOption struct {
	Name     string `json:"name"`
	Sessions int    `json:"sessions"`
}

func newTenantOption(key string, sessions int) tenantOption {
	o := tenantOption{Key: key, Label: key, Sessions: sessions}
	if i := strings.Index(key, "/"); i > 0 {
		o.Sender, o.Label = key[:i], key[i+1:]
	}
	return o
}

var errTenantsScoped = errors.New("this view is not available to an agent-scoped role")

// tenantOptions lists the tenants info may switch between, by key. They come from the sessions in the archive (in
// memory without one), narrowed to the caller's grants, so a limited user is only ever offered their own. A tenant
// that left only usage rows behind is not listed, and neither are senders with no data yet. An agent-scoped role
// has no tenants to switch between and is refused. GET /api/tenants and the list_tenants tool both answer with it.
func tenantOptions(store *SessionStore, names *TenantNames, info RoleInfo) ([]tenantOption, error) {
	if info.Scoped() {
		return nil, errTenantsScoped
	}
	counts := store.TenantCounts()
	services := store.ServiceCounts()
	out := make([]tenantOption, 0, len(counts))
	for k, n := range counts {
		if !info.AllowsAnyTenant([]string{k}) {
			continue
		}
		o := newTenantOption(k, n)
		o.Name = names.Name(k)
		for svc, c := range services[k] {
			o.Services = append(o.Services, serviceOption{Name: svc, Sessions: c})
		}
		sort.Slice(o.Services, func(i, j int) bool {
			if o.Services[i].Sessions != o.Services[j].Sessions {
				return o.Services[i].Sessions > o.Services[j].Sessions
			}
			return o.Services[i].Name < o.Services[j].Name
		})
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// tenantsHandler implements GET /api/tenants: the tenants the caller may switch between (tenantOptions).
func tenantsHandler(store *SessionStore, names *TenantNames) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		out, err := tenantOptions(store, names, roleInfoOrOpen(r.Context()))
		if err != nil {
			writeEvaluateError(w, http.StatusForbidden, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": out, "error": nil})
	}
}
