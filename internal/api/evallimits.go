package api

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// EvalLimits tunes the limits on the interactive evaluation endpoints beyond the daily judge-token cap
// (--eval-daily-tokens). Every field is optional; the zero value is the built-in behaviour.
//
//	{"userConcurrent": 2, "userPerMinute": 10,
//	 "models": {"gemini-2.5-pro": {"weight": 4, "dailyTokens": 1000000}, "*": {"weight": 1}}}
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

// ParseEvalLimits reads the JSON of --eval-limits; an empty string is the defaults.
func ParseEvalLimits(raw string) (EvalLimits, error) {
	var l EvalLimits
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return l, nil
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&l); err != nil {
		return EvalLimits{}, fmt.Errorf("eval limits: %w", err)
	}
	if l.UserConcurrent < 0 || l.UserPerMinute < 0 {
		return EvalLimits{}, fmt.Errorf("eval limits: userConcurrent and userPerMinute cannot be negative")
	}
	for name, m := range l.Models {
		if name != "*" && !judgeModelName.MatchString(name) {
			return EvalLimits{}, fmt.Errorf("eval limits: %q is not a model name", name)
		}
		if m.Weight < 0 || math.IsNaN(m.Weight) || math.IsInf(m.Weight, 0) || m.DailyTokens < 0 {
			return EvalLimits{}, fmt.Errorf("eval limits: model %q has a negative or invalid weight or cap", name)
		}
	}
	return l, nil
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
