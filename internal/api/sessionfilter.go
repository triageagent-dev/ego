package api

import (
	"sort"
	"strings"
	"time"

	"github.com/triageagent-dev/ego/internal/logx"
	"github.com/triageagent-dev/ego/internal/mcptools"
)

// archiveSearchRows bounds one text search of the archive to its newest rows that mention the query, and
// archiveSearchHits the sessions it returns from there. An older session still opens by its link.
const (
	archiveSearchRows = 2000
	archiveSearchHits = 200
)

// Find lists the sessions f matches, newest first: the ones in memory, the archived ones that mention a query,
// and, for scored: true, the ones online evaluation scored in the window, wherever they live. allow is the
// caller's role; it is checked on every summary before the filter, so a filter can only narrow what the role may
// see. Without a query or scored: true only memory is read, as the list has always been.
func (s *SessionStore) Find(f mcptools.SessionFilter, allow func(SessionSummary) bool) []SessionSummary {
	var cand []SessionSummary
	seen := map[string]bool{}
	add := func(sum SessionSummary) {
		if !seen[sum.SessionID] {
			seen[sum.SessionID] = true
			cand = append(cand, sum)
		}
	}
	for _, sum := range s.List() {
		add(sum)
	}
	// The flag is decided last, so the archive passes are matched without it.
	unflagged := f
	unflagged.Scored = nil
	if f.Query != "" && s.store != nil {
		hits := 0
		err := s.store.SearchSnapshots(f.Query, f.Since, archiveSearchRows, func(snap sessionSnapshot) bool {
			if seen[snap.SessionID] {
				return true
			}
			sum := summarize(restoreSession(snap, s.usageTenantAttr))
			if allow(sum) && sessionMatches(sum, unflagged) {
				add(sum)
				hits++
			}
			return hits < archiveSearchHits
		})
		if err != nil {
			logx.Warnf("session archive: search %q: %v", f.Query, err)
		}
	}
	if f.Thread != "" && s.store != nil {
		// A thread's older segments may have left memory: read them back, newest first, up to the search bound.
		segs, err := s.store.ThreadSegments(f.Thread, false)
		if err != nil {
			logx.Warnf("session archive: reading thread %q: %v", f.Thread, err)
		}
		for i := len(segs) - 1; i >= 0 && len(segs)-i <= archiveSearchHits; i-- {
			if seen[segs[i].ID] {
				continue
			}
			if sum, ok := s.Summary(segs[i].ID); ok {
				add(sum)
			}
		}
	}
	if f.Scored != nil && *f.Scored && s.store != nil {
		var sinceMs int64
		if !f.Since.IsZero() {
			sinceMs = f.Since.UnixMilli()
		}
		for _, id := range s.store.OnlineScoredSince(sinceMs, archiveSearchHits) {
			if seen[id] {
				continue
			}
			if sum, ok := s.Summary(id); ok {
				add(sum)
			}
		}
	}
	s.markScored(cand)
	out := make([]SessionSummary, 0, len(cand))
	for _, sum := range cand {
		if allow(sum) && sessionMatches(sum, f) {
			out = append(out, sum)
		}
	}
	started := func(sum SessionSummary) time.Time {
		t, _ := time.Parse(time.RFC3339, sum.StartedAt)
		return t
	}
	sort.SliceStable(out, func(i, j int) bool { return started(out[i]).After(started(out[j])) })
	return out
}

// markScored sets Scored on the summaries online evaluation has scored.
func (s *SessionStore) markScored(sums []SessionSummary) {
	if s.store == nil || len(sums) == 0 {
		return
	}
	ids := make([]string, len(sums))
	for i := range sums {
		ids[i] = sums[i].SessionID
	}
	scored := s.store.OnlineScoredAmong(ids)
	for i := range sums {
		sums[i].Scored = scored[sums[i].SessionID]
	}
}

// allowedBy is the role check Find takes: everything for an unscoped role, otherwise the session's agents and
// tenants must be in scope.
func allowedBy(info RoleInfo) func(SessionSummary) bool {
	if !info.Scoped() && !info.TenantScoped() {
		return func(SessionSummary) bool { return true }
	}
	return func(sum SessionSummary) bool { return info.AllowsSession(sum.Agents, sum.Tenants) }
}

// filterSessions keeps the summaries that match f. It runs on a list already narrowed to what the caller's role may
// see, never before, so a filter cannot widen anything.
func filterSessions(list []SessionSummary, f mcptools.SessionFilter) []SessionSummary {
	if f.Agent == "" && f.Tenant == "" && f.Since.IsZero() && f.Query == "" && f.Complete == nil && f.Thread == "" {
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
	if f.Scored != nil && s.Scored != *f.Scored {
		return false
	}
	if f.Thread != "" && s.Thread != f.Thread {
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
	if has(s.SessionID) || has(s.Thread) || has(s.Service) {
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
