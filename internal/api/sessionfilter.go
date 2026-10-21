package api

import (
	"strings"
	"time"

	"github.com/triageagent-dev/ego/internal/mcptools"
)

// filterSessions keeps the summaries that match f. It runs on a list already narrowed to what the caller's role may
// see, never before, so a filter cannot widen anything.
func filterSessions(list []SessionSummary, f mcptools.SessionFilter) []SessionSummary {
	if f.Agent == "" && f.Tenant == "" && f.Since.IsZero() && f.Query == "" && f.Complete == nil {
		return list
	}
	out := make([]SessionSummary, 0, len(list))
	for _, s := range list {
		if sessionMatches(s, f) {
			out = append(out, s)
		}
	}
	return out
}

func sessionMatches(s SessionSummary, f mcptools.SessionFilter) bool {
	if f.Complete != nil && s.IsComplete != *f.Complete {
		return false
	}
	if f.Agent != "" && !containsString(s.Agents, f.Agent) {
		return false
	}
	if f.Tenant != "" && !sessionInTenant(s.Tenants, f.Tenant) {
		return false
	}
	if !f.Since.IsZero() {
		started, err := time.Parse(time.RFC3339, s.StartedAt)
		if err != nil || started.Before(f.Since) {
			return false
		}
	}
	if f.Query != "" && !sessionMentions(s, strings.ToLower(f.Query)) {
		return false
	}
	return true
}

func containsString(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// sessionInTenant reports whether one of the session's tenant keys is the wanted key, or belongs to the sender in
// "<sender>/*". The short form "<sender>/" means the same: gateways refuse "/*" in a query string.
func sessionInTenant(tenants []string, want string) bool {
	if strings.HasSuffix(want, "/") {
		want += "*"
	}
	for _, k := range tenants {
		if tenantPatternMatches(want, k) {
			return true
		}
	}
	return false
}

// sessionMentions looks for needle (lower case) in the session id, agent names, service and, once the session has
// completed, its first user message: the invocations are only extracted at completion, so an open session cannot be
// found by what the user said.
func sessionMentions(s SessionSummary, needle string) bool {
	has := func(v string) bool { return strings.Contains(strings.ToLower(v), needle) }
	if has(s.SessionID) || has(s.Service) {
		return true
	}
	for _, a := range s.Agents {
		if has(a) {
			return true
		}
	}
	for _, inv := range s.Invocations {
		if inv.UserText != "" {
			return has(inv.UserText)
		}
	}
	return false
}
