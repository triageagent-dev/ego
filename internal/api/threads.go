package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/triageagent-dev/ego/internal/mcptools"
)

// Threads (segments.go) listed one row each: when the thread was first and last seen, how many segments it has, the
// tokens its LLM calls used and the score of its newest scored segment. Totals come from the stored thread columns of
// the session index and the usage ledger, so a row costs indexed lookups, never a read of the sessions themselves.

const (
	defaultThreadRows = 50
	maxThreadRows     = 200
)

// ThreadSummary is one row of GET /api/threads.
type ThreadSummary struct {
	Thread    string `json:"thread"`
	FirstSeen string `json:"firstSeen"`
	LastSeen  string `json:"lastSeen"`
	// Segments counts the thread's sessions; Open is true while its newest one still takes new traces.
	Segments int  `json:"segments"`
	Open     bool `json:"open"`
	// TotalTokens sums the input and output tokens of the thread's LLM calls in the usage ledger (0 when the caller's
	// role has no usage view). LastScore is the mean score of the newest segment online evaluation scored.
	TotalTokens int64    `json:"totalTokens"`
	LastScore   *float64 `json:"lastScore,omitempty"`
	Tenants     []string `json:"tenants,omitempty"`
	Agents      []string `json:"agents,omitempty"`
}

type threadSeg struct {
	thread          string
	firstMs, endMs  int64
	part            int
	closed          bool
	tenants, agents []string
}

// Threads lists the threads f matches that the caller's role may see, the one seen last first. A thread is listed
// when one of its segments matches f; its row counts every segment the role may see.
func (s *SessionStore) Threads(f mcptools.SessionFilter, limit int, info RoleInfo) ([]ThreadSummary, error) {
	if limit <= 0 || limit > maxThreadRows {
		limit = defaultThreadRows
	}
	allow := allowedBy(info)
	if s.store == nil {
		return s.threadsFromMemory(f, limit, allow), nil
	}
	cands, err := s.threadCandidates(f, limit, info, allow)
	if err != nil {
		return nil, err
	}
	segs, err := s.store.threadSegmentRows(cands)
	if err != nil {
		return nil, err
	}
	byThread := map[string]*ThreadSummary{}
	var newest = map[string]int{}
	for _, g := range segs {
		if !allow(SessionSummary{Agents: g.agents, Tenants: g.tenants}) {
			continue
		}
		t := byThread[g.thread]
		if t == nil {
			t = &ThreadSummary{Thread: g.thread}
			byThread[g.thread] = t
			newest[g.thread] = -1
		}
		t.Segments++
		first, last := msTime(g.firstMs), msTime(g.endMs)
		if t.FirstSeen == "" || first < t.FirstSeen {
			t.FirstSeen = first
		}
		if last > t.LastSeen {
			t.LastSeen = last
		}
		if g.part > newest[g.thread] {
			newest[g.thread], t.Open = g.part, !g.closed
		}
		t.Tenants = appendMissing(t.Tenants, g.tenants...)
		t.Agents = appendMissing(t.Agents, g.agents...)
	}
	out := make([]ThreadSummary, 0, len(byThread))
	for _, id := range cands {
		if t := byThread[id]; t != nil {
			out = append(out, *t)
		}
	}
	s.fillThreadTotals(out, info)
	sortThreads(out)
	return out, nil
}

func msTime(ms int64) string { return time.UnixMilli(ms).UTC().Format(time.RFC3339) }

func sortThreads(out []ThreadSummary) {
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastSeen > out[j].LastSeen })
}

func appendMissing(list []string, add ...string) []string {
	for _, v := range add {
		if v != "" && !containsString(list, v) && len(list) < maxIdentities {
			list = append(list, v)
		}
	}
	return list
}

// threadCandidates picks the threads to list: one SQL query when the caller's role allows every session, otherwise
// the newest index rows are read and checked against the role, up to the scan cap.
func (s *SessionStore) threadCandidates(f mcptools.SessionFilter, limit int, info RoleInfo, allow func(SessionSummary) bool) ([]string, error) {
	where := sessionPageFilter{tenant: f.Tenant, agent: f.Agent, query: strings.ToLower(f.Query)}
	cond, args := where.where(nil)
	if !f.Since.IsZero() {
		cond += " AND ended_ms >= ?"
		args = append(args, f.Since.UnixMilli())
	}
	if !info.Scoped() && !info.TenantScoped() && where.sqlExact() {
		return s.store.recentThreads(cond, args, limit)
	}
	rows, err := s.store.db.Query(`SELECT thread, tenants, agents FROM session_index WHERE 1 = 1`+cond+` ORDER BY ended_ms DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	seen := map[string]bool{}
	for scanned := 0; rows.Next() && scanned < countScanCap && len(out) < limit; scanned++ {
		var thread, tenants, agents string
		if err := rows.Scan(&thread, &tenants, &agents); err != nil {
			return nil, err
		}
		sum := SessionSummary{Tenants: splitIndexList(tenants), Agents: splitIndexList(agents)}
		if seen[thread] || !allow(sum) || (f.Agent != "" && !containsString(sum.Agents, f.Agent)) ||
			(f.Tenant != "" && !sessionInTenant(sum.Tenants, f.Tenant)) {
			continue
		}
		seen[thread] = true
		out = append(out, thread)
	}
	return out, rows.Err()
}

// recentThreads is the threads with a segment matching cond, the one seen last first.
func (s *SQLiteStore) recentThreads(cond string, args []any, limit int) ([]string, error) {
	rows, err := s.db.Query(`SELECT thread FROM session_index WHERE 1 = 1`+cond+` GROUP BY thread ORDER BY MAX(ended_ms) DESC LIMIT ?`,
		append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var th string
		if err := rows.Scan(&th); err != nil {
			return nil, err
		}
		out = append(out, th)
	}
	return out, rows.Err()
}

// threadSegmentRows reads every indexed segment of the given threads.
func (s *SQLiteStore) threadSegmentRows(threads []string) ([]threadSeg, error) {
	var out []threadSeg
	for _, chunk := range chunkStrings(threads, 400) {
		rows, err := s.db.Query(`SELECT thread, first_ms, ended_ms, part, closed, tenants, agents FROM session_index WHERE thread IN (?`+
			strings.Repeat(",?", len(chunk)-1)+`)`, anyArgs(chunk)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var g threadSeg
			var closed int
			var tenants, agents string
			if err := rows.Scan(&g.thread, &g.firstMs, &g.endMs, &g.part, &closed, &tenants, &agents); err != nil {
				rows.Close()
				return nil, err
			}
			g.closed, g.tenants, g.agents = closed == 1, splitIndexList(tenants), splitIndexList(agents)
			if g.endMs < g.firstMs {
				g.endMs = g.firstMs
			}
			out = append(out, g)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func chunkStrings(list []string, n int) [][]string {
	var out [][]string
	for len(list) > 0 {
		k := min(n, len(list))
		out = append(out, list[:k])
		list = list[k:]
	}
	return out
}

func anyArgs(list []string) []any {
	out := make([]any, len(list))
	for i, v := range list {
		out[i] = v
	}
	return out
}

// fillThreadTotals adds the ledger's tokens and the newest score to the rows. A role limited to agents has no usage
// view, so its rows carry no tokens.
func (s *SessionStore) fillThreadTotals(rows []ThreadSummary, info RoleInfo) {
	if len(rows) == 0 {
		return
	}
	threads := make([]string, len(rows))
	for i, r := range rows {
		threads[i] = r.Thread
	}
	var tokens map[string]int64
	if !info.Scoped() {
		tokens = s.store.threadTokens(threads, info.Tenants)
	}
	scores := s.store.threadLastScores(threads)
	for i := range rows {
		rows[i].TotalTokens = tokens[rows[i].Thread]
		if v, ok := scores[rows[i].Thread]; ok {
			rows[i].LastScore = &v
		}
	}
}

// threadTokens sums the tokens of the threads' LLM calls in the usage ledger, limited to the tenants the role may see.
func (s *SQLiteStore) threadTokens(threads []string, tenants []string) map[string]int64 {
	out := map[string]int64{}
	tc, targs := tenantClause(tenants)
	for _, chunk := range chunkStrings(threads, 400) {
		rows, err := s.db.Query(`SELECT thread, SUM(input_tokens + output_tokens) FROM llm_calls WHERE thread IN (?`+
			strings.Repeat(",?", len(chunk)-1)+`)`+tc+` GROUP BY thread`, append(anyArgs(chunk), targs...)...)
		if err != nil {
			return out
		}
		for rows.Next() {
			var th string
			var n int64
			if rows.Scan(&th, &n) == nil {
				out[th] = n
			}
		}
		rows.Close()
	}
	return out
}

// threadLastScores is, per thread, the mean score of the segment online evaluation scored last.
func (s *SQLiteStore) threadLastScores(threads []string) map[string]float64 {
	out := map[string]float64{}
	for _, chunk := range chunkStrings(threads, 400) {
		rows, err := s.db.Query(`SELECT i.thread, AVG(o.score) FROM online_scores o JOIN session_index i ON i.session_id = o.session_id
			WHERE i.thread IN (?`+strings.Repeat(",?", len(chunk)-1)+`) AND o.score IS NOT NULL
			AND o.at_ms = (SELECT MAX(o2.at_ms) FROM online_scores o2 JOIN session_index i2 ON i2.session_id = o2.session_id
				WHERE i2.thread = i.thread AND o2.score IS NOT NULL)
			GROUP BY i.thread`, anyArgs(chunk)...)
		if err != nil {
			return out
		}
		for rows.Next() {
			var th string
			var v float64
			if rows.Scan(&th, &v) == nil {
				out[th] = v
			}
		}
		rows.Close()
	}
	return out
}

// threadsFromMemory is Threads without an archive: the sessions in memory, grouped by thread.
func (s *SessionStore) threadsFromMemory(f mcptools.SessionFilter, limit int, allow func(SessionSummary) bool) []ThreadSummary {
	all := s.List()
	match := map[string]bool{}
	for _, sum := range all {
		if allow(sum) && sessionMatches(sum, mcptools.SessionFilter{Agent: f.Agent, Tenant: f.Tenant, Since: f.Since, Query: f.Query}) {
			match[sum.Thread] = true
		}
	}
	byThread := map[string]*ThreadSummary{}
	newest := map[string]int{}
	for _, sum := range all {
		if !match[sum.Thread] || !allow(sum) {
			continue
		}
		t := byThread[sum.Thread]
		if t == nil {
			t = &ThreadSummary{Thread: sum.Thread}
			byThread[sum.Thread] = t
		}
		end := sum.StartedAt
		if sum.DurationMs > 0 {
			if st, err := time.Parse(time.RFC3339, sum.StartedAt); err == nil {
				end = st.Add(time.Duration(sum.DurationMs) * time.Millisecond).UTC().Format(time.RFC3339)
			}
		}
		t.Segments++
		if t.FirstSeen == "" || sum.StartedAt < t.FirstSeen {
			t.FirstSeen = sum.StartedAt
		}
		if end > t.LastSeen {
			t.LastSeen = end
		}
		if sum.Part > newest[sum.Thread] {
			newest[sum.Thread], t.Open = sum.Part, !sum.Closed
		}
		t.TotalTokens += sum.TotalTokens
		t.Tenants = appendMissing(t.Tenants, sum.Tenants...)
		t.Agents = appendMissing(t.Agents, sum.Agents...)
	}
	out := make([]ThreadSummary, 0, len(byThread))
	for _, t := range byThread {
		out = append(out, *t)
	}
	sortThreads(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// threadsHandler implements GET /api/threads?limit=&since=&tenant=&agent=&q=.
func threadsHandler(store *SessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		filter, err := mcptools.ParseSessionFilter(r.URL.Query(), time.Now())
		if err != nil {
			writeEvaluateError(w, http.StatusBadRequest, err.Error())
			return
		}
		limit := defaultThreadRows
		if v := r.URL.Query().Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				writeEvaluateError(w, http.StatusBadRequest, "limit must be a positive number")
				return
			}
			limit = n
		}
		rows, err := store.Threads(filter, limit, roleInfoOrOpen(r.Context()))
		if err != nil {
			writeEvaluateError(w, http.StatusInternalServerError, "failed to read threads: "+err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": rows, "error": nil})
	}
}
