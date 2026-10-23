package mcptools

import (
	"sort"
	"strings"
)

// Defaults and bounds of get_usage. The ledger keeps 90 days, so a longer window would only repeat that.
const (
	defaultUsageHours = 24
	maxUsageHours     = 2160
	defaultUsageTop   = 10
	maxUsageTop       = 100
	// UsageSessionRows is how many (session, model) rows a backend reads to rank sessions. The ledger ranks per
	// model, so a session that used two models is two rows; reading the endpoint's whole page keeps the merged top
	// N right except for a session whose rows both fall outside the 500 largest.
	UsageSessionRows = 500
)

// Usage groupings of get_usage.
const (
	UsageByKind    = "kind"
	UsageByTenant  = "tenant"
	UsageByModel   = "model"
	UsageByService = "service"
)

// noKey names the group of rows that carry no value for the chosen grouping.
const noKey = "(none)"

// UsageQuery is what get_usage asks a backend. The handler has already applied the defaults and bounds.
type UsageQuery struct {
	Hours  int
	Tenant string
	Group  string
	Top    int
}

// UsageCell is one (time bucket, service, tenant, kind, model) cell of the usage ledger, as GET /api/usage returns
// it. Its fields mirror the api package's bucket DTO one for one so the in-process backend can convert directly.
type UsageCell struct {
	Start        int64  `json:"start"`
	Service      string `json:"service"`
	Tenant       string `json:"tenant"`
	Kind         string `json:"kind"`
	Model        string `json:"model"`
	Calls        int64  `json:"calls"`
	Errors       int64  `json:"errors"`
	InputTokens  int64  `json:"inputTokens"`
	OutputTokens int64  `json:"outputTokens"`
	// CacheReadTokens and CacheWriteTokens are the parts of InputTokens served from and written to the prompt cache.
	CacheReadTokens  int64 `json:"cacheReadTokens"`
	CacheWriteTokens int64 `json:"cacheWriteTokens"`
	DurationMs       int64 `json:"durationMs"`
}

// UsageSessionRow is one row of GET /api/usage/calls?group=session: a session's calls on one model, summed. Kinds
// and Tenants are comma separated.
type UsageSessionRow struct {
	SessionID    string `json:"sessionId"`
	Start        int64  `json:"start"`
	End          int64  `json:"end"`
	Calls        int64  `json:"calls"`
	Errors       int64  `json:"errors"`
	Kinds        string `json:"kinds"`
	Tenants      string `json:"tenants"`
	Model        string `json:"model"`
	InputTokens  int64  `json:"inputTokens"`
	OutputTokens int64  `json:"outputTokens"`
	PeakTokens   int64  `json:"peakTokens"`
	DurationMs   int64  `json:"durationMs"`
	// CacheReadTokens and CacheWriteTokens are the parts of InputTokens served from and written to the prompt cache.
	CacheReadTokens  int64 `json:"cacheReadTokens"`
	CacheWriteTokens int64 `json:"cacheWriteTokens"`
}

// Usage is the result of get_usage.
type Usage struct {
	Hours        int            `json:"hours"`
	Group        string         `json:"group"`
	Tenant       string         `json:"tenant,omitempty"`
	InputTokens  int64          `json:"input_tokens"`
	OutputTokens int64          `json:"output_tokens"`
	TotalTokens  int64          `json:"total_tokens"`
	Calls        int64          `json:"calls"`
	ErrorCalls   int64          `json:"error_calls"`
	Breakdown    []UsageGroup   `json:"breakdown"`
	TopSessions  []UsageSession `json:"top_sessions"`
}

// UsageGroup is the usage of one key of the chosen grouping.
type UsageGroup struct {
	Key          string `json:"key"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	Tokens       int64  `json:"tokens"`
	Calls        int64  `json:"calls"`
}

// UsageSession is one of the sessions that used the most tokens.
type UsageSession struct {
	SessionID    string `json:"session_id"`
	Agent        string `json:"agent,omitempty"`
	Tenant       string `json:"tenant,omitempty"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	Tokens       int64  `json:"tokens"`
	Calls        int64  `json:"calls"`
}

// BuildUsage turns the ledger rows the Usage view reads into the get_usage answer. Both backends fetch the same
// two row sets and come here, so the totals and the ranking cannot differ between the stdio and the HTTP transport.
func BuildUsage(q UsageQuery, cells []UsageCell, sessions []UsageSessionRow) *Usage {
	out := &Usage{Hours: q.Hours, Group: q.Group, Tenant: q.Tenant, Breakdown: []UsageGroup{}, TopSessions: []UsageSession{}}

	groups := map[string]*UsageGroup{}
	for _, c := range cells {
		out.InputTokens += c.InputTokens
		out.OutputTokens += c.OutputTokens
		out.Calls += c.Calls
		out.ErrorCalls += c.Errors

		key := cellKey(c, q.Group)
		g := groups[key]
		if g == nil {
			g = &UsageGroup{Key: key}
			groups[key] = g
		}
		g.InputTokens += c.InputTokens
		g.OutputTokens += c.OutputTokens
		g.Calls += c.Calls
	}
	out.TotalTokens = out.InputTokens + out.OutputTokens
	for _, g := range groups {
		g.Tokens = g.InputTokens + g.OutputTokens
		out.Breakdown = append(out.Breakdown, *g)
	}
	sort.Slice(out.Breakdown, func(i, j int) bool {
		a, b := out.Breakdown[i], out.Breakdown[j]
		if a.Tokens != b.Tokens {
			return a.Tokens > b.Tokens
		}
		return a.Key < b.Key
	})

	// A session that used two models arrives as two rows; one session is one entry.
	bySession := map[string]*UsageSession{}
	var order []string
	for _, r := range sessions {
		s := bySession[r.SessionID]
		if s == nil {
			s = &UsageSession{SessionID: r.SessionID}
			bySession[r.SessionID] = s
			order = append(order, r.SessionID)
		}
		s.InputTokens += r.InputTokens
		s.OutputTokens += r.OutputTokens
		s.Calls += r.Calls
		s.Agent = joinDistinct(s.Agent, r.Kinds)
		s.Tenant = joinDistinct(s.Tenant, r.Tenants)
	}
	for _, id := range order {
		s := bySession[id]
		s.Tokens = s.InputTokens + s.OutputTokens
		out.TopSessions = append(out.TopSessions, *s)
	}
	sort.SliceStable(out.TopSessions, func(i, j int) bool {
		a, b := out.TopSessions[i], out.TopSessions[j]
		if a.Tokens != b.Tokens {
			return a.Tokens > b.Tokens
		}
		return a.SessionID < b.SessionID
	})
	if q.Top > 0 && len(out.TopSessions) > q.Top {
		out.TopSessions = out.TopSessions[:q.Top]
	}
	return out
}

func cellKey(c UsageCell, group string) string {
	var k string
	switch group {
	case UsageByTenant:
		k = c.Tenant
	case UsageByModel:
		k = c.Model
	case UsageByService:
		k = c.Service
	default:
		k = c.Kind
	}
	if k == "" {
		return noKey
	}
	return k
}

// joinDistinct adds the comma separated values of more to the ", " separated list have, keeping each once.
func joinDistinct(have, more string) string {
	var list []string
	seen := map[string]bool{}
	for _, part := range strings.Split(have, ", ") {
		if part != "" && !seen[part] {
			seen[part] = true
			list = append(list, part)
		}
	}
	for _, part := range strings.Split(more, ",") {
		if part = strings.TrimSpace(part); part != "" && !seen[part] {
			seen[part] = true
			list = append(list, part)
		}
	}
	return strings.Join(list, ", ")
}
