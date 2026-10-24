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
}

// userShareDivisor: a caller who is not an admin may use a quarter of the day's budget.
const userShareDivisor = 4

func newEvalGuard(dailyCap int64) *evalGuard {
	return &evalGuard{dailyCap: dailyCap, slots: make(chan struct{}, maxConcurrentEvals)}
}

// interactiveEval is shared by the handlers, which are built without a common owner.
var interactiveEval = newEvalGuard(DefaultEvalDailyToks)

// setDailyCap changes the daily judge-token cap; 0 means unlimited.
func (g *evalGuard) setDailyCap(tokens int64) {
	g.mu.Lock()
	g.dailyCap = tokens
	g.mu.Unlock()
}

func (g *evalGuard) rollLocked(now time.Time) {
	if day := now.UTC().Format("2006-01-02"); day != g.day {
		g.day, g.spent, g.byUser = day, 0, nil
	}
}

// add records judge tokens used by one call.
func (g *evalGuard) add(tokens int64) { g.addFor("", tokens) }

// addFor records judge tokens used by one call for user ("" when the caller is not known).
func (g *evalGuard) addFor(user string, tokens int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.rollLocked(time.Now())
	g.spent += tokens
	if user != "" {
		if g.byUser == nil {
			g.byUser = map[string]int64{}
		}
		g.byUser[user] += tokens
	}
}

// admit reserves a slot for one evaluation. The returned release must be called when it finishes. When the day's
// cap is used up or all slots are busy it returns a status and message instead.
func (g *evalGuard) admit(now time.Time, user string, admin bool) (release func(), status int, msg string) {
	g.mu.Lock()
	g.rollLocked(now)
	exhausted := g.dailyCap > 0 && g.spent >= g.dailyCap
	// An admin is not held to a share; any other known caller is.
	overShare := !admin && user != "" && g.dailyCap > 0 && g.byUser[user] >= g.dailyCap/userShareDivisor
	g.mu.Unlock()
	if exhausted {
		return nil, http.StatusTooManyRequests, "the daily judge token budget for interactive evaluation is used up; it resets at 00:00 UTC"
	}
	if overShare {
		return nil, http.StatusTooManyRequests, "you have used your share of today's judge token budget for interactive evaluation; it resets at 00:00 UTC"
	}
	select {
	case g.slots <- struct{}{}:
		return func() { <-g.slots }, 0, ""
	default:
		return nil, http.StatusTooManyRequests, "too many evaluations are running; try again shortly"
	}
}
