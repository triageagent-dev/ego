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

// One member holds at most maxUserConcurrentEvals slots, so the others keep theirs; an admin is not held to it.
func TestEvalGuardLimitsRunningEvaluationsPerCaller(t *testing.T) {
	g := newEvalGuard(0)
	now := time.Now()
	var held []func()
	for i := 0; i < maxUserConcurrentEvals; i++ {
		rel, status, msg := g.admit(now, "alice", false)
		if rel == nil {
			t.Fatalf("evaluation %d refused: %d %q", i, status, msg)
		}
		held = append(held, rel)
	}
	if rel, status, msg := g.admit(now, "alice", false); rel != nil || status != http.StatusTooManyRequests || !strings.Contains(msg, "running") {
		t.Fatalf("alice past her running limit = %v %d %q", rel != nil, status, msg)
	}
	if rel, _, _ := g.admit(now, "bob", false); rel == nil {
		t.Error("bob was refused although a slot is free")
	} else {
		rel()
	}
	if rel, _, _ := g.admit(now, "alice", true); rel == nil {
		t.Error("an admin was held to the per-caller limit")
	} else {
		rel()
	}
	held[0]()
	if rel, _, _ := g.admit(now, "alice", false); rel == nil {
		t.Error("alice was refused after one of hers finished")
	} else {
		rel()
	}
	held[1]()
}

// A member may start only so many evaluations a minute, however fast each one finishes.
func TestEvalGuardLimitsTheStartRatePerCaller(t *testing.T) {
	g := newEvalGuard(0)
	now := time.Now()
	for i := 0; i < userEvalsPerMinute; i++ {
		rel, status, msg := g.admit(now.Add(time.Duration(i)*time.Second), "alice", false)
		if rel == nil {
			t.Fatalf("start %d refused: %d %q", i, status, msg)
		}
		rel()
	}
	if rel, status, msg := g.admit(now.Add(30*time.Second), "alice", false); rel != nil || status != http.StatusTooManyRequests || !strings.Contains(msg, "last minute") {
		t.Fatalf("an eleventh start in the minute = %v %d %q", rel != nil, status, msg)
	}
	if rel, _, _ := g.admit(now.Add(30*time.Second), "bob", false); rel == nil {
		t.Error("bob was held to alice's rate")
	} else {
		rel()
	}
	if rel, _, _ := g.admit(now.Add(90*time.Second), "alice", false); rel == nil {
		t.Error("alice was still refused a minute later")
	} else {
		rel()
	}
	if rel, _, _ := g.admit(now.Add(10*time.Minute), "carol", false); rel == nil {
		t.Error("carol was refused")
	} else {
		rel()
	}
	if len(g.started) > 1 || len(g.inflight) != 0 {
		t.Errorf("the guard keeps state for idle callers: %d started, %d inflight", len(g.started), len(g.inflight))
	}
}

// freshEvalGuard gives a test its own interactive-evaluation guard. The handlers share a global one and the tests share
// one fake client address, so without it the per-caller limits would count across tests.
func freshEvalGuard(t *testing.T) {
	t.Helper()
	old := interactiveEval
	interactiveEval = newEvalGuard(DefaultEvalDailyToks)
	t.Cleanup(func() { interactiveEval = old })
}

func TestParseEvalLimits(t *testing.T) {
	l, err := ParseEvalLimits(`{"userConcurrent":3,"userPerMinute":5,"models":{"big-model":{"weight":4,"dailyTokens":1000},"*":{"weight":0.5}}}`)
	if err != nil {
		t.Fatal(err)
	}
	if l.userConcurrent() != 3 || l.userPerMinute() != 5 || l.model("big-model").Weight != 4 || l.model("other").Weight != 0.5 {
		t.Errorf("parsed %+v", l)
	}
	if d := (EvalLimits{}); d.userConcurrent() != maxUserConcurrentEvals || d.userPerMinute() != userEvalsPerMinute {
		t.Error("the defaults are not the built-in limits")
	}
	if l, err := ParseEvalLimits(""); err != nil || l.Models != nil {
		t.Errorf("an empty string = %+v %v, want the defaults", l, err)
	}
	for _, bad := range []string{`{"usersConcurrent":2}`, `{"userPerMinute":-1}`, `{"models":{"a b":{}}}`, `{"models":{"m":{"weight":-1}}}`, `{"models":{"m":{"dailyTokens":-5}}}`, `not json`} {
		if _, err := ParseEvalLimits(bad); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

// A dearer model uses the day's budget and a member's share up faster; the model's own cap counts what it reports.
func TestEvalGuardWeighsModelsAndCapsThemSeparately(t *testing.T) {
	g := newEvalGuard(1000)
	g.setLimits(EvalLimits{Models: map[string]ModelLimit{"big": {Weight: 4, DailyTokens: 100}, "*": {}}})
	now := time.Now()

	g.addModel("alice", "big", 60)
	g.addModel("alice", "small", 60)
	if _, spent, _ := g.snapshot(now); spent != 60*4+60 {
		t.Errorf("spent = %d, want 300 (60 weighted by 4, plus 60)", spent)
	}
	// alice's share is a quarter of 1000: 300 is over it, although she reported only 120 tokens.
	if rel, status, msg := g.admit(now, "alice", false, "small"); rel != nil || status != http.StatusTooManyRequests || !strings.Contains(msg, "share") {
		t.Errorf("alice past her weighted share = %v %d %q", rel != nil, status, msg)
	}

	g.addModel("bob", "big", 50) // 110 reported in all: over the model's own cap of 100.
	if rel, status, msg := g.admit(now, "carol", false, "small", "big"); rel != nil || status != http.StatusTooManyRequests || !strings.Contains(msg, "big") {
		t.Errorf("a request naming a model past its cap = %v %d %q", rel != nil, status, msg)
	}
	if rel, _, _ := g.admit(now, "carol", true, "big"); rel != nil {
		t.Error("an admin was let past a model's cap")
	}
	if rel, _, _ := g.admit(now, "carol", false, "small"); rel == nil {
		t.Error("a request that does not name the capped model was refused")
	} else {
		rel()
	}
	if rel, _, _ := g.admit(now, "carol", false); rel == nil {
		t.Error("a request with no judge model was refused")
	} else {
		rel()
	}
	g.day = "2000-01-01" // the next UTC day starts every count from zero.
	if rel, _, _ := g.admit(now, "carol", false, "big"); rel == nil {
		t.Error("the model's cap did not reset on a new day")
	} else {
		rel()
	}
}

func TestEvalGuardTakesTheMemberLimitsFromTheSettings(t *testing.T) {
	g := newEvalGuard(0)
	g.setLimits(EvalLimits{UserConcurrent: 1, UserPerMinute: 2})
	now := time.Now()
	rel, _, _ := g.admit(now, "alice", false)
	if rel == nil {
		t.Fatal("the first evaluation was refused")
	}
	if r, _, msg := g.admit(now, "alice", false); r != nil || !strings.Contains(msg, "1 evaluations running") {
		t.Errorf("a second at once = %v %q, want refused at 1", r != nil, msg)
	}
	rel()
	rel, _, _ = g.admit(now.Add(time.Second), "alice", false)
	rel()
	if r, _, msg := g.admit(now.Add(2*time.Second), "alice", false); r != nil || !strings.Contains(msg, "2 evaluations in the last minute") {
		t.Errorf("a third in the minute = %v %q, want refused at 2", r != nil, msg)
	}
}

func TestJudgeModelsOfNamesOnlyTheModelsAJudgeCalls(t *testing.T) {
	got := judgeModelsOf([]evaluatorConfigDTO{
		{Name: "tool_trajectory_avg_score"},
		{Name: "hallucinations_v1"},
		{Name: "hallucinations_v1", JudgeModel: "other-model"},
		{Name: "hallucinations_v1", JudgeModel: "other-model"},
	}, "default-model")
	if len(got) != 2 || got[0] != "default-model" || got[1] != "other-model" {
		t.Errorf("models = %v, want the default and other-model once each, and none for a built-in metric", got)
	}
}
