package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/triageagent-dev/ego/internal/logx"
	"github.com/triageagent-dev/ego/internal/mcptools"
)

// Activity is the view of agents that work in turns: one subject per agent and tenant whose sessions hold turns
// (Claude Code makes a claude_code.interaction trace per prompt) or that declared a thread. The sessions of a subject
// are what the Activity graph draws, so an always-on agent shows the work it has already done, with no setting on the
// sender and no new trace needed.

const (
	// activityScanCap bounds the index rows read to find the turn-based sessions.
	activityScanCap = 5000
)

// ActivitySubject is one row of GET /api/activity.
type ActivitySubject struct {
	Agent     string `json:"agent"`
	Tenant    string `json:"tenant,omitempty"`
	Sessions  int    `json:"sessions"`
	Turns     int    `json:"turns"`
	FirstSeen string `json:"firstSeen"`
	LastSeen  string `json:"lastSeen"`
}

// activityRow is what is known of one session without reading it.
type activityRow struct {
	id, thread      string
	part            int
	agents, tenants []string
	startMs, endMs  int64
	turns           int
}

// subjectKey names a subject: the agent that ran, or the thread for a session no agent signed.
type subjectKey struct{ agent, tenant string }

func (r activityRow) keys() []subjectKey {
	agent := r.thread
	if len(r.agents) > 0 {
		agent = r.agents[0]
	}
	if len(r.tenants) == 0 {
		return []subjectKey{{agent, ""}}
	}
	out := make([]subjectKey, len(r.tenants))
	for i, t := range r.tenants {
		out[i] = subjectKey{agent, t}
	}
	return out
}

// activityIndexRows reads the turn-based and declared-thread sessions of the archive that ended since sinceMs.
func (s *SQLiteStore) activityIndexRows(sinceMs int64) ([]activityRow, error) {
	rows, err := s.db.Query(`SELECT session_id, thread, part, agents, tenants, first_ms, ended_ms, COALESCE(turns, 0)
		FROM session_index WHERE (segmented = 1 OR turns > 0) AND ended_ms >= ? ORDER BY ended_ms DESC LIMIT ?`, sinceMs, activityScanCap)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []activityRow
	for rows.Next() {
		var r activityRow
		var agents, tenants string
		if err := rows.Scan(&r.id, &r.thread, &r.part, &agents, &tenants, &r.startMs, &r.endMs, &r.turns); err != nil {
			return nil, err
		}
		r.agents, r.tenants = splitIndexList(agents), splitIndexList(tenants)
		if r.endMs < r.startMs {
			r.endMs = r.startMs
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// activityRows lists the sessions of turn-based agents the role may see: those in memory, then the older ones of the
// archive.
func (s *SessionStore) activityRows(since time.Time, info RoleInfo) []activityRow {
	allow := allowedBy(info)
	var sinceMs int64
	if !since.IsZero() {
		sinceMs = since.UnixMilli()
	}
	seen := map[string]bool{}
	var out []activityRow
	add := func(r activityRow) {
		if seen[r.id] || r.endMs < sinceMs || !allow(SessionSummary{Agents: r.agents, Tenants: r.tenants}) {
			return
		}
		seen[r.id] = true
		out = append(out, r)
	}
	for _, sum := range s.List() {
		if !sum.Segmented && sum.Turns == 0 {
			continue
		}
		start, _ := time.Parse(time.RFC3339, sum.StartedAt)
		add(activityRow{id: sum.SessionID, thread: sum.Thread, part: sum.Part, agents: sum.Agents, tenants: sum.Tenants,
			startMs: start.UnixMilli(), endMs: start.UnixMilli() + sum.DurationMs, turns: sum.Turns})
	}
	if s.store != nil {
		archived, err := s.store.activityIndexRows(sinceMs)
		if err != nil {
			logx.Warnf("session archive: reading activity: %v", err)
		}
		for _, r := range archived {
			add(r)
		}
	}
	return out
}

// Activity lists the subjects the filter matches (its tenant and since), the one seen last first.
func (s *SessionStore) Activity(f mcptools.SessionFilter, info RoleInfo) []ActivitySubject {
	by := map[subjectKey]*ActivitySubject{}
	for _, r := range s.activityRows(f.Since, info) {
		for _, k := range r.keys() {
			if f.Tenant != "" && !sessionInTenant([]string{k.tenant}, f.Tenant) {
				continue
			}
			a := by[k]
			if a == nil {
				a = &ActivitySubject{Agent: k.agent, Tenant: k.tenant}
				by[k] = a
			}
			a.Sessions++
			a.Turns += r.turns
			first, last := msTime(r.startMs), msTime(r.endMs)
			if a.FirstSeen == "" || first < a.FirstSeen {
				a.FirstSeen = first
			}
			if last > a.LastSeen {
				a.LastSeen = last
			}
		}
	}
	out := make([]ActivitySubject, 0, len(by))
	for _, a := range by {
		out = append(out, *a)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].LastSeen != out[j].LastSeen {
			return out[i].LastSeen > out[j].LastSeen
		}
		return out[i].Agent+out[i].Tenant < out[j].Agent+out[j].Tenant
	})
	return out
}

// activityHandler implements GET /api/activity?tenant=&since=.
func activityHandler(store *SessionStore) http.HandlerFunc {
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
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": store.Activity(filter, roleInfoOrOpen(r.Context())), "error": nil})
	}
}
