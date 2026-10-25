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

// The record of an agent that works in turns, from its turns alone. The Quality view shows it for an agent no judge
// scores, and get_activity hands it to the agent itself, so what a person reads and what the agent reads agree.

const (
	defaultActivityHours = 24
	maxActivityHours     = 720
	// maxActivitySubjects bounds the agents one answer without an agent summarizes.
	maxActivitySubjects = 20
	// maxActivityTools bounds the tools listed for one agent.
	maxActivityTools = 40
)

func turnFailed(r TurnRow) bool { return r.Failed || r.FailedTools > 0 }

func percentileMs(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(float64(len(sorted))*p/100+0.999999) - 1
	return sorted[min(len(sorted)-1, max(0, i))]
}

// awakeShare is the part of [from, to] with a turn running, 0 to 1: two turns that overlap count once.
func awakeShare(rows []TurnRow, fromMs, toMs int64) float64 {
	type span struct{ a, b int64 }
	var spans []span
	for _, r := range rows {
		a, b := max(r.StartMs, fromMs), min(r.StartMs+r.DurationMs, toMs)
		if b > a {
			spans = append(spans, span{a, b})
		}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].a < spans[j].a })
	var awake int64
	end := int64(-1 << 62)
	for _, s := range spans {
		switch {
		case s.a > end:
			awake += s.b - s.a
		case s.b > end:
			awake += s.b - end
		}
		end = max(end, s.b)
	}
	return min(1, float64(awake)/float64(max(1, toMs-fromMs)))
}

// summarizeTurns reads a window of one agent's turns. A window over a week counts its turns per day, a shorter one
// per hour.
func summarizeTurns(res TurnsResult, hours int) mcptools.ActivitySummary {
	out := mcptools.ActivitySummary{Agent: res.Agent, Tenant: res.Tenant, Hours: hours, Turns: len(res.Turns),
		Tools: []mcptools.ToolRecord{}, Hourly: []mcptools.ActivityHour{}, Truncated: res.Truncated}
	durations := make([]int64, 0, len(res.Turns))
	tools := map[[2]string]*mcptools.ToolRecord{}
	toolMs := map[[2]string]int64{}
	step := time.Hour
	if hours > 24*7 {
		step = 24 * time.Hour
	}
	perStep := map[int64]*mcptools.ActivityHour{}
	var input, cached int64
	for _, r := range res.Turns {
		durations = append(durations, r.DurationMs)
		input += r.InputTokens
		cached += r.CacheReadTokens
		out.OutputTokens += r.OutputTokens
		out.ModelRequests += r.Requests
		failed := turnFailed(r)
		if !failed {
			out.TurnsWithoutFailure++
		}
		start := time.UnixMilli(r.StartMs).UTC().Truncate(step).UnixMilli()
		h := perStep[start]
		if h == nil {
			h = &mcptools.ActivityHour{Start: time.UnixMilli(start).UTC().Format(time.RFC3339)}
			perStep[start] = h
		}
		h.Turns++
		if failed {
			h.Failed++
		}
		for name, c := range r.ToolCalls {
			k := [2]string{c.Server, name}
			t := tools[k]
			if t == nil {
				t = &mcptools.ToolRecord{Tool: name, Server: c.Server}
				tools[k] = t
			}
			t.Calls += c.Calls
			t.Failed += c.Failed
			toolMs[k] += c.Ms
			out.ToolCalls += c.Calls
			out.FailedToolCalls += c.Failed
		}
	}
	out.InputTokens = input
	if input > 0 {
		out.CachedShare = float64(cached) / float64(input)
	}
	if out.Turns > 0 {
		s := float64(out.TurnsWithoutFailure) / float64(out.Turns)
		out.Success = &s
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	out.MedianTurnMs, out.P95TurnMs = percentileMs(durations, 50), percentileMs(durations, 95)
	out.Awake = awakeShare(res.Turns, res.FromMs, res.ToMs)
	for k, t := range tools {
		if t.Calls > 0 {
			t.AvgMs = float64(toolMs[k]) / float64(t.Calls)
			t.Failure = float64(t.Failed) / float64(t.Calls)
		}
		out.Tools = append(out.Tools, *t)
	}
	sort.Slice(out.Tools, func(i, j int) bool {
		a, b := out.Tools[i], out.Tools[j]
		if a.Failed != b.Failed {
			return a.Failed > b.Failed
		}
		if a.Calls != b.Calls {
			return a.Calls > b.Calls
		}
		return a.Server+a.Tool < b.Server+b.Tool
	})
	if len(out.Tools) > maxActivityTools {
		out.Tools = out.Tools[:maxActivityTools]
	}
	starts := make([]int64, 0, len(perStep))
	for s := range perStep {
		starts = append(starts, s)
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i] < starts[j] })
	for _, s := range starts {
		out.Hourly = append(out.Hourly, *perStep[s])
	}
	return out
}

// ActivitySummaries summarizes the agent the query names, or every agent the caller may see that worked in turns
// during the window, the one seen last first.
func (s *SessionStore) ActivitySummaries(q mcptools.ActivityQuery, info RoleInfo) []mcptools.ActivitySummary {
	window := time.Duration(q.Hours) * time.Hour
	subjects := []subjectKey{{q.Agent, q.Tenant}}
	if q.Agent == "" {
		subjects = subjects[:0]
		since := time.Now().Add(-window)
		for _, a := range s.Activity(mcptools.SessionFilter{Tenant: q.Tenant, Since: since}, info) {
			subjects = append(subjects, subjectKey{a.Agent, a.Tenant})
		}
		if len(subjects) > maxActivitySubjects {
			subjects = subjects[:maxActivitySubjects]
		}
	}
	out := make([]mcptools.ActivitySummary, 0, len(subjects))
	for _, sel := range subjects {
		out = append(out, summarizeTurns(s.Turns(sel, window, info), q.Hours))
	}
	return out
}

// egoScoreHandler implements GET /api/activity/score?agent=&tenant=&hours=: the score of each agent from its turns.
func egoScoreHandler(store *SessionStore) http.HandlerFunc {
	cache := newReadCache()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		v := r.URL.Query()
		q := mcptools.ActivityQuery{Agent: strings.TrimSpace(v.Get("agent")), Tenant: strings.TrimSpace(v.Get("tenant")), Hours: defaultActivityHours}
		if h := v.Get("hours"); h != "" {
			n, err := strconv.Atoi(h)
			if err != nil || n < 1 || n > maxActivityHours {
				writeEvaluateError(w, http.StatusBadRequest, "hours must be between 1 and "+strconv.Itoa(maxActivityHours))
				return
			}
			q.Hours = n
		}
		info := roleInfoOrOpen(r.Context())
		key := roleKey(info) + "|" + q.Agent + "|" + q.Tenant + "|" + strconv.Itoa(q.Hours) + "|" + strconv.FormatUint(store.changes(), 10)
		w.Header().Set("Content-Type", "application/json")
		if body, ok := cache.get(key); ok {
			_, _ = w.Write(body)
			return
		}
		if !cache.acquire() {
			w.Header().Set("Retry-After", "2")
			writeEvaluateError(w, http.StatusTooManyRequests, "busy: try again in a moment")
			return
		}
		defer cache.release()
		body, err := json.Marshal(map[string]any{"data": turnScores(store, info, q), "error": nil})
		if err != nil {
			writeEvaluateError(w, http.StatusInternalServerError, "failed to encode the ego score")
			return
		}
		body = append(body, '\n')
		cache.put(key, body)
		_, _ = w.Write(body)
	}
}

const (
	maxScoreAgents  = 20
	maxFailingTools = 5
)

// turnScores scores each agent the query names, or every agent the caller may see that worked in turns during the
// window: the compact answer an agent reads about itself.
func turnScores(store *SessionStore, info RoleInfo, q mcptools.ActivityQuery) []mcptools.TurnScore {
	out := []mcptools.TurnScore{}
	if store == nil {
		return out
	}
	for _, s := range store.ActivitySummaries(q, info) {
		if s.Turns == 0 {
			continue
		}
		out = append(out, scoreOf(s))
		if len(out) == maxScoreAgents {
			break
		}
	}
	return out
}

// scoreOf is the compact score of one agent's summary: the tools that failed are the only ones listed.
func scoreOf(s mcptools.ActivitySummary) mcptools.TurnScore {
	t := mcptools.TurnScore{Agent: s.Agent, Tenant: s.Tenant, Hours: s.Hours, Turns: s.Turns, Score: s.Success,
		ToolCalls: s.ToolCalls, FailedToolCalls: s.FailedToolCalls, FailingTools: []mcptools.ToolRecord{}}
	if s.ToolCalls > 0 {
		t.ErrorRate = float64(s.FailedToolCalls) / float64(s.ToolCalls)
	}
	for _, tool := range s.Tools {
		if tool.Failed > 0 && len(t.FailingTools) < maxFailingTools {
			t.FailingTools = append(t.FailingTools, tool)
		}
	}
	return t
}
