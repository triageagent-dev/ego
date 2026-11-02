package api

import (
	"fmt"
	"net/http"
	"regexp"
	"sync"
	"time"
)

// Limits on the interactive evaluation endpoints (/api/evaluate and /api/evaluate/stream). Every judge metric
// is a paid model call, and any member (or anyone, with login off) can start one, so a request is bounded in
// size, only a few run at once, and the judge tokens they use count against a daily cap shared by all callers.
const (
	maxEvaluators        = 20
	maxJudgeSamples      = 8
	maxRubrics           = 50
	maxTraceFiles        = 50
	maxConcurrentEvals   = 4
	DefaultEvalDailyToks = 5_000_000
	// One signed-in member may have this many evaluations running and start this many a minute, so one caller
	// cannot hold every slot or loop on a judge that reports no token counts (Vertex, Jev), which the token share
	// does not see. An admin is not held to them.
	maxUserConcurrentEvals = 2
	userEvalsPerMinute     = 10
)

var judgeModelName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// validateEvalConfig rejects a request that asks for more work than one evaluation may do.
func validateEvalConfig(cfg evalConfigDTO, traceFiles int) string {
	if traceFiles > maxTraceFiles {
		return fmt.Sprintf("too many trace files: %d (at most %d per request)", traceFiles, maxTraceFiles)
	}
	if len(cfg.Evaluators) > maxEvaluators {
		return fmt.Sprintf("too many evaluators: %d (at most %d per request)", len(cfg.Evaluators), maxEvaluators)
	}
	for _, ev := range cfg.Evaluators {
		if ev.NumSamples < 0 || ev.NumSamples > maxJudgeSamples {
			return fmt.Sprintf("numSamples %d is out of range (0 to %d)", ev.NumSamples, maxJudgeSamples)
		}
		if len(ev.Rubrics) > maxRubrics {
			return fmt.Sprintf("too many rubrics: %d (at most %d per evaluator)", len(ev.Rubrics), maxRubrics)
		}
		if ev.JudgeModel != "" && !judgeModelName.MatchString(ev.JudgeModel) {
			return "invalid judgeModel"
		}
	}
	return ""
}

// evalGuard counts the judge tokens interactive evaluations use per UTC day and limits how many run at once.
// Vertex managed evaluation and Jev report no token counts, so they are limited by the concurrency and request
// caps only.
type evalGuard struct {
	mu       sync.Mutex
	dailyCap int64
	day      string
	spent    int64
	// byUser is what each caller has used today, so one signed-in member cannot spend the whole day's budget.
	byUser map[string]int64
	slots  chan struct{}
	// inflight counts a caller's running evaluations and started holds when the last ones began.
	inflight map[string]int
	started  map[string][]time.Time
	// limits are the per-member and per-model settings: read from the configuration file (limitsFromFile), else saved
	// by an admin on the settings page (limitsStored). byModel is the tokens each model used today, as the model
	// reported them.
	limits         EvalLimits
	limitsStored   bool
	limitsFromFile bool
	byModel        map[string]int64
}

// userShareDivisor: a caller who is not an admin may use a quarter of the day's budget.
const userShareDivisor = 4

func newEvalGuard(dailyCap int64) *evalGuard {
	return &evalGuard{dailyCap: dailyCap, slots: make(chan struct{}, maxConcurrentEvals), inflight: map[string]int{}, started: map[string][]time.Time{}}
}

// interactiveEval is shared by the handlers, which are built without a common owner.
var interactiveEval = newEvalGuard(DefaultEvalDailyToks)

// setLimits applies the per-member and per-model limits.
func (g *evalGuard) setLimits(l EvalLimits) { g.setLimitsStored(l, false) }

// setLimitsFile applies limits read from the server's configuration file; they are then not editable in the UI.
func (g *evalGuard) setLimitsFile(l EvalLimits) {
	g.mu.Lock()
	g.limits, g.limitsFromFile = l, true
	g.mu.Unlock()
}

// setLimitsStored is setLimits that also remembers whether an admin saved them.
func (g *evalGuard) setLimitsStored(l EvalLimits, stored bool) {
	g.mu.Lock()
	g.limits, g.limitsStored = l, stored
	g.mu.Unlock()
}

// fromFile reports whether the limits come from the server's configuration file.
func (g *evalGuard) fromFile() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.limitsFromFile
}

func (g *evalGuard) userConcurrentLimit() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.limits.userConcurrent()
}

func (g *evalGuard) userRateLimit() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.limits.userPerMinute()
}

// setDailyCap changes the daily judge-token cap; 0 means unlimited.
func (g *evalGuard) setDailyCap(tokens int64) {
	g.mu.Lock()
	g.dailyCap = tokens
	g.mu.Unlock()
}

func (g *evalGuard) rollLocked(now time.Time) {
	if day := now.UTC().Format("2006-01-02"); day != g.day {
		g.day, g.spent, g.byUser, g.byModel = day, 0, nil, nil
	}
}

// add records judge tokens used by one call.
func (g *evalGuard) add(tokens int64) { g.addFor("", tokens) }

// addFor records judge tokens used by one call for user ("" when the caller is not known), of no particular model.
func (g *evalGuard) addFor(user string, tokens int64) { g.addModel(user, "", tokens) }

// addModel records the tokens a call to model used. The day's budget and the member's share count them weighted by
// the model's weight, so a dearer model uses them up faster; the model's own cap counts them as reported.
func (g *evalGuard) addModel(user, model string, tokens int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.rollLocked(time.Now())
	weighted := g.limits.weighted(model, tokens)
	g.spent += weighted
	if user != "" {
		if g.byUser == nil {
			g.byUser = map[string]int64{}
		}
		g.byUser[user] += weighted
	}
	if model != "" {
		if g.byModel == nil {
			g.byModel = map[string]int64{}
		}
		g.byModel[model] += tokens
	}
}

// admit reserves a slot for one evaluation that will call the judge models named in models (none when it only runs
// built-in metrics). The returned release must be called when it finishes. When the day's cap or a model's cap is used
// up, the caller is over their share or their limits, or all slots are busy it returns a status and message instead. The checks and the reservation happen under one lock, so two requests at once cannot both pass a
// limit that only one of them fits under.
func (g *evalGuard) admit(now time.Time, user string, admin bool, models ...string) (release func(), status int, msg string) {
	counted := !admin && user != ""
	g.mu.Lock()
	g.rollLocked(now)
	// Callers who have not started anything for a minute leave no state behind.
	for u, times := range g.started {
		if len(times) == 0 || now.Sub(times[len(times)-1]) >= time.Minute {
			delete(g.started, u)
		}
	}
	switch {
	case g.dailyCap > 0 && g.spent >= g.dailyCap:
		g.mu.Unlock()
		return nil, http.StatusTooManyRequests, "the daily judge token budget for interactive evaluation is used up; it resets at 00:00 UTC"
	// An admin is not held to a share; any other known caller is.
	case counted && g.dailyCap > 0 && g.byUser[user] >= g.dailyCap/userShareDivisor:
		g.mu.Unlock()
		return nil, http.StatusTooManyRequests, "you have used your share of today's judge token budget for interactive evaluation; it resets at 00:00 UTC"
	}
	for _, m := range models {
		if cap := g.limits.model(m).DailyTokens; cap > 0 && g.byModel[m] >= cap {
			g.mu.Unlock()
			return nil, http.StatusTooManyRequests, fmt.Sprintf("the daily token budget for the judge model %s is used up; it resets at 00:00 UTC, or use another model", m)
		}
	}
	if counted {
		recent := g.started[user][:0]
		for _, t := range g.started[user] {
			if now.Sub(t) < time.Minute {
				recent = append(recent, t)
			}
		}
		g.started[user] = recent
		switch {
		case g.inflight[user] >= g.limits.userConcurrent():
			g.mu.Unlock()
			return nil, http.StatusTooManyRequests, fmt.Sprintf("you already have %d evaluations running; wait for one to finish", g.limits.userConcurrent())
		case len(recent) >= g.limits.userPerMinute():
			g.mu.Unlock()
			return nil, http.StatusTooManyRequests, fmt.Sprintf("you started %d evaluations in the last minute; try again shortly", g.limits.userPerMinute())
		}
	}
	select {
	case g.slots <- struct{}{}:
	default:
		g.mu.Unlock()
		return nil, http.StatusTooManyRequests, "too many evaluations are running; try again shortly"
	}
	if counted {
		g.inflight[user]++
		g.started[user] = append(g.started[user], now)
	}
	g.mu.Unlock()
	return func() {
		g.mu.Lock()
		if counted {
			if g.inflight[user]--; g.inflight[user] <= 0 {
				delete(g.inflight, user)
			}
			if len(g.started[user]) == 0 {
				delete(g.started, user)
			}
		}
		g.mu.Unlock()
		<-g.slots
	}, 0, ""
}
