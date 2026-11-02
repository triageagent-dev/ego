package api

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"

	"github.com/triageagent-dev/ego/internal/judge"
	"github.com/triageagent-dev/ego/internal/logx"
)

// EvalLimits tunes the limits on the interactive evaluation endpoints beyond the daily judge-token cap
// (--eval-daily-tokens). An admin edits them on the settings page; they are saved in the archive and apply at once.
// Every field is optional; the zero value is the built-in behaviour.
type EvalLimits struct {
	// UserConcurrent is how many evaluations one signed-in member may have running (default 2), UserPerMinute how many
	// they may start in a minute (default 10). An admin is held to neither.
	UserConcurrent int `json:"userConcurrent,omitempty"`
	UserPerMinute  int `json:"userPerMinute,omitempty"`
	// Models limits a judge model by name; "*" covers a model not listed. The caps count tokens, not money, so a
	// model that costs more per token has a weight above 1 to use up the daily budget faster, or its own cap.
	Models map[string]ModelLimit `json:"models,omitempty"`
}

// ModelLimit is the limit on one judge model.
type ModelLimit struct {
	// Weight multiplies the tokens the model uses before they count against the daily budget and a member's share
	// (default 1; 0.5 for a cheaper model, 4 for a dearer one).
	Weight float64 `json:"weight,omitempty"`
	// DailyTokens caps the tokens (as the model reports them, unweighted) the model may use per UTC day, whoever
	// asks; 0 means no cap of its own. Once used up, a request that names the model is refused.
	DailyTokens int64 `json:"dailyTokens,omitempty"`
}

// Validate refuses limits that cannot be applied: a negative number, or a name that is no judge model.
func (l EvalLimits) Validate() error {
	if l.UserConcurrent < 0 || l.UserPerMinute < 0 {
		return fmt.Errorf("the per-member limits cannot be negative")
	}
	for name, m := range l.Models {
		if name != "*" && !judgeModelName.MatchString(name) {
			return fmt.Errorf("%q is not a model name", name)
		}
		if m.Weight < 0 || math.IsNaN(m.Weight) || math.IsInf(m.Weight, 0) || m.DailyTokens < 0 {
			return fmt.Errorf("model %q has a negative or invalid weight or cap", name)
		}
	}
	return nil
}

// evalLimitsKey is the online_state key holding the limits an admin saved; absent, the built-in limits apply.
const evalLimitsKey = "eval_limits"

// LoadEvalLimits applies the limits saved in the archive, if any. Saved limits that no longer validate are
// ignored and said so.
func LoadEvalLimits(store *SQLiteStore, g *evalGuard) {
	if store == nil {
		return
	}
	raw, err := store.OnlineState(evalLimitsKey)
	if err != nil {
		logx.Warnf("evaluation limits: reading the saved limits: %v", err)
		return
	}
	if raw == "" {
		return
	}
	var l EvalLimits
	if err := json.Unmarshal([]byte(raw), &l); err != nil {
		logx.Warnf("evaluation limits: the saved limits are unreadable, the built-in limits apply: %v", err)
		return
	}
	if err := l.Validate(); err != nil {
		logx.Warnf("evaluation limits: the saved limits are invalid, the built-in limits apply: %v", err)
		return
	}
	g.setLimitsStored(l, true)
}

type evalLimitsDTO struct {
	EvalLimits
	// Defaults are what an unset per-member limit is.
	Defaults struct {
		UserConcurrent int `json:"userConcurrent"`
		UserPerMinute  int `json:"userPerMinute"`
	} `json:"defaults"`
	// DefaultModel is the judge model a metric uses when it names none.
	DefaultModel string `json:"defaultModel"`
	// Source is "stored" once an admin saved limits, else "default".
	Source string `json:"source"`
	// Persisted is false when the server keeps no archive, so the limits are lost at a restart.
	Persisted bool `json:"persisted"`
}

func evalLimitsAnswer(g *evalGuard, store *SQLiteStore) evalLimitsDTO {
	g.mu.Lock()
	l := g.limits
	stored := g.limitsStored
	g.mu.Unlock()
	out := evalLimitsDTO{EvalLimits: l, DefaultModel: judge.DefaultModel, Source: "default", Persisted: store != nil}
	if stored {
		out.Source = "stored"
	}
	out.Defaults.UserConcurrent, out.Defaults.UserPerMinute = maxUserConcurrentEvals, userEvalsPerMinute
	return out
}

// evalLimitsHandler implements GET, PUT and DELETE /api/admin/eval-limits: read the limits of the interactive
// evaluation, replace them, go back to the built-in ones. Admin only; a change applies from the next evaluation.
func evalLimitsHandler(store *SQLiteStore) http.HandlerFunc {
	g := func() *evalGuard { return interactiveEval }
	return func(w http.ResponseWriter, r *http.Request) {
		if !roleInfoOrOpen(r.Context()).IsAdmin {
			writeRoleForbidden(w)
			return
		}
		switch r.Method {
		case http.MethodGet:
		case http.MethodPut:
			var l EvalLimits
			if err := decodeJSONStrict(w, r, &l); err != nil {
				writeEvaluateError(w, http.StatusBadRequest, "invalid limits: "+err.Error())
				return
			}
			if err := l.Validate(); err != nil {
				writeEvaluateError(w, http.StatusBadRequest, err.Error())
				return
			}
			raw, err := json.Marshal(l)
			if err != nil {
				writeEvaluateError(w, http.StatusInternalServerError, err.Error())
				return
			}
			if store != nil {
				if err := store.SetOnlineState(evalLimitsKey, string(raw)); err != nil {
					writeEvaluateError(w, http.StatusInternalServerError, "failed to save the limits: "+err.Error())
					return
				}
			}
			g().setLimitsStored(l, true)
			logx.Warnf("evaluation limits changed by %s: %s", callerName(r.Context()), raw)
		case http.MethodDelete:
			if store != nil {
				if err := store.SetOnlineState(evalLimitsKey, ""); err != nil {
					writeEvaluateError(w, http.StatusInternalServerError, "failed to reset the limits: "+err.Error())
					return
				}
			}
			g().setLimitsStored(EvalLimits{}, false)
			logx.Warnf("evaluation limits reset by %s", callerName(r.Context()))
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": evalLimitsAnswer(g(), store), "error": nil})
	}
}

// userConcurrent and userPerMinute are the effective per-member limits.
func (l EvalLimits) userConcurrent() int {
	if l.UserConcurrent > 0 {
		return l.UserConcurrent
	}
	return maxUserConcurrentEvals
}

func (l EvalLimits) userPerMinute() int {
	if l.UserPerMinute > 0 {
		return l.UserPerMinute
	}
	return userEvalsPerMinute
}

// model is the limit for a judge model: its own entry, else "*", else none.
func (l EvalLimits) model(name string) ModelLimit {
	if m, ok := l.Models[name]; ok {
		return m
	}
	return l.Models["*"]
}

// weighted is tokens times the model's weight, rounded up; a model with no weight counts as 1.
func (l EvalLimits) weighted(model string, tokens int64) int64 {
	w := l.model(model).Weight
	if w == 0 || w == 1 {
		return tokens
	}
	return int64(math.Ceil(float64(tokens) * w))
}

// judgeModelsOf lists the judge models a set of evaluators will call: the model each judge metric names, the default
// for one that names none. Metrics that call no judge (built-in, Vertex managed evaluation) name no model.
func judgeModelsOf(evaluators []evaluatorConfigDTO, defaultModel string) []string {
	seen := map[string]bool{}
	var out []string
	for _, ev := range evaluators {
		if !judgeMetrics[ev.Name] {
			continue
		}
		name := ev.JudgeModel
		if name == "" {
			name = defaultModel
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}
