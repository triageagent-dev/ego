package api

import (
	"net/http"
	"time"
)

// GET /api/usage/limits: the caps and limits this server runs with and, where it counts them, how much of each
// is used now. The usage page shows them beside the spend, so a number that is about to stop work is not a surprise.

// limitDTO is one cap or limit. Limit 0 means there is none; Used is absent for a limit that is not counted
// (a size or an age rather than a running total).
type limitDTO struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Group string `json:"group"`
	Limit int64  `json:"limit"`
	Used  *int64 `json:"used,omitempty"`
	// Unit is what Limit counts: tokens, calls, count or days.
	Unit string `json:"unit"`
	Note string `json:"note,omitempty"`
}

func counted(v int64) *int64 { return &v }

// snapshot is the guard's daily cap, what interactive evaluations used today and how many run now.
func (g *evalGuard) snapshot(now time.Time) (dailyCap, spent int64, running int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.rollLocked(now)
	return g.dailyCap, g.spent, len(g.slots)
}

// limitsSnapshot is how many completed sessions are held in memory against their cap, and the two retention periods.
func (s *SessionStore) limitsSnapshot() (maxMemory, inMemory int, sessionRetention, usageRetention time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sess := range s.sessions {
		if sess.IsComplete {
			inMemory++
		}
	}
	return s.maxMemory, inMemory, s.sessionRetention, s.usageRetention
}

func days(d time.Duration) int64 { return int64(d / (24 * time.Hour)) }

func usageLimits(store *SessionStore, online *OnlineEngine, info RoleInfo, now time.Time) []limitDTO {
	out := []limitDTO{}

	capTokens, spent, running := interactiveEval.snapshot(now)
	out = append(out,
		limitDTO{Key: "eval.tokens", Label: "Judge tokens per day", Group: "Evaluation you start", Limit: capTokens, Used: counted(spent), Unit: "tokens",
			Note: "Shared by everyone who evaluates here; resets at 00:00 UTC."},
		limitDTO{Key: "eval.running", Label: "Evaluations at once", Group: "Evaluation you start", Limit: maxConcurrentEvals, Used: counted(int64(running)), Unit: "count"},
		limitDTO{Key: "eval.traces", Label: "Traces in one evaluation", Group: "Evaluation you start", Limit: maxTraceFiles, Unit: "count"},
		limitDTO{Key: "eval.metrics", Label: "Metrics in one evaluation", Group: "Evaluation you start", Limit: maxEvaluators, Unit: "count"},
		limitDTO{Key: "eval.samples", Label: "Judge samples per metric", Group: "Evaluation you start", Limit: maxJudgeSamples, Unit: "count"},
		limitDTO{Key: "eval.rubrics", Label: "Rubrics per metric", Group: "Evaluation you start", Limit: maxRubrics, Unit: "count"},
	)

	// The online budget is not per tenant, so a role limited to tenants or agents does not see it, as on the quality page.
	if online != nil && !info.Scoped() && !info.TenantScoped() {
		b := online.budget.Status(now)
		out = append(out,
			limitDTO{Key: "online.tokens", Label: "Judge tokens per day", Group: "Online scoring", Limit: b.Caps.JudgeTokensPerDay, Used: counted(b.Spent.JudgeTokens), Unit: "tokens",
				Note: "Scoring of live sessions and the findings pass; resets at 00:00 UTC."},
			limitDTO{Key: "online.jev", Label: "Jev calls per day", Group: "Online scoring", Limit: b.Caps.JevCallsPerDay, Used: counted(b.Spent.JevCalls), Unit: "calls"},
		)
	}

	if store != nil {
		maxMem, inMem, sessRet, usageRet := store.limitsSnapshot()
		out = append(out,
			limitDTO{Key: "sessions.memory", Label: "Finished sessions kept in memory", Group: "Storage", Limit: int64(maxMem), Used: counted(int64(inMem)), Unit: "count",
				Note: "Older ones are read from the database when opened."},
			limitDTO{Key: "sessions.retention", Label: "Sessions are kept for", Group: "Storage", Limit: days(sessRet), Unit: "days"},
			limitDTO{Key: "usage.retention", Label: "Token usage is kept for", Group: "Storage", Limit: days(usageRet), Unit: "days"},
		)
	}
	return out
}

func usageLimitsHandler(store *SessionStore, online *OnlineEngine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": usageLimits(store, online, roleInfoOrOpen(r.Context()), time.Now()), "error": nil})
	}
}
