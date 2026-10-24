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
	now := time.Now().UTC() // add() reads the wall clock, so a fixed date here would be a different day.

	var releases []func()
	for i := 0; i < maxConcurrentEvals; i++ {
		rel, status, _ := g.admit(now, "", true)
		if rel == nil {
			t.Fatalf("slot %d refused with %d", i, status)
		}
		releases = append(releases, rel)
	}
	if rel, status, _ := g.admit(now, "", true); rel != nil || status != http.StatusTooManyRequests {
		t.Fatalf("a fifth concurrent evaluation was admitted (status %d)", status)
	}
	for _, r := range releases {
		r()
	}

	g.add(150)
	if rel, status, msg := g.admit(now, "", true); rel != nil || status != http.StatusTooManyRequests || !strings.Contains(msg, "budget") {
		t.Fatalf("admitted after the budget was spent: status %d %q", status, msg)
	}
	g.day = "2000-01-01" // a new UTC day starts from zero.
	if rel, _, _ := g.admit(now, "", true); rel == nil {
		t.Fatal("the budget did not reset on a new day")
	}

	g.setDailyCap(0)
	g.add(1 << 40)
	if rel, _, _ := g.admit(now, "", true); rel == nil {
		t.Fatal("a cap of 0 must mean unlimited")
	}
}

// A member who is not an admin may use a quarter of the day's budget; another member and an admin are not held to
// that member's share.
func TestEvalGuardSharesTheDailyBudgetPerCaller(t *testing.T) {
	g := newEvalGuard(1000)
	now := time.Now()
	g.addFor("alice", 250)
	if rel, status, msg := g.admit(now, "alice", false); rel != nil || status != http.StatusTooManyRequests || !strings.Contains(msg, "share") {
		t.Errorf("alice past her share = %v %d %q, want refused", rel != nil, status, msg)
	}
	if rel, _, _ := g.admit(now, "bob", false); rel == nil {
		t.Error("bob was refused for alice's use")
	} else {
		rel()
	}
	if rel, _, _ := g.admit(now, "alice", true); rel == nil {
		t.Error("an admin was held to a share")
	} else {
		rel()
	}
	// A caller that is not known is held to the day's cap only.
	if rel, _, _ := g.admit(now, "", false); rel == nil {
		t.Error("an unknown caller was refused")
	} else {
		rel()
	}
}
