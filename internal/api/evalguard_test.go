package api

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestValidateEvalConfig(t *testing.T) {
	ok := evalConfigDTO{Evaluators: []evaluatorConfigDTO{{Name: "x", NumSamples: 4, JudgeModel: "gemini-2.5-flash"}}}
	if msg := validateEvalConfig(ok, 3); msg != "" {
		t.Fatalf("valid config rejected: %s", msg)
	}
	cases := map[string]evalConfigDTO{
		"samples":    {Evaluators: []evaluatorConfigDTO{{NumSamples: maxJudgeSamples + 1}}},
		"negative":   {Evaluators: []evaluatorConfigDTO{{NumSamples: -1}}},
		"rubrics":    {Evaluators: []evaluatorConfigDTO{{Rubrics: make([]string, maxRubrics+1)}}},
		"model":      {Evaluators: []evaluatorConfigDTO{{JudgeModel: "../../x y"}}},
		"evaluators": {Evaluators: make([]evaluatorConfigDTO, maxEvaluators+1)},
	}
	for name, cfg := range cases {
		if validateEvalConfig(cfg, 1) == "" {
			t.Errorf("%s: config accepted, want rejection", name)
		}
	}
	if !strings.Contains(validateEvalConfig(ok, maxTraceFiles+1), "trace files") {
		t.Error("too many trace files accepted")
	}
}

func TestEvalGuardBudgetAndConcurrency(t *testing.T) {
	g := newEvalGuard(100)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

	var releases []func()
	for i := 0; i < maxConcurrentEvals; i++ {
		rel, status, _ := g.admit(now)
		if rel == nil {
			t.Fatalf("slot %d refused with %d", i, status)
		}
		releases = append(releases, rel)
	}
	if rel, status, _ := g.admit(now); rel != nil || status != http.StatusTooManyRequests {
		t.Fatalf("a fifth concurrent evaluation was admitted (status %d)", status)
	}
	for _, r := range releases {
		r()
	}

	g.add(150)
	if rel, status, msg := g.admit(now); rel != nil || status != http.StatusTooManyRequests || !strings.Contains(msg, "budget") {
		t.Fatalf("admitted after the budget was spent: status %d %q", status, msg)
	}
	g.day = "2000-01-01" // a new UTC day starts from zero.
	if rel, _, _ := g.admit(now); rel == nil {
		t.Fatal("the budget did not reset on a new day")
	}

	g.setDailyCap(0)
	g.add(1 << 40)
	if rel, _, _ := g.admit(now); rel == nil {
		t.Fatal("a cap of 0 must mean unlimited")
	}
}
