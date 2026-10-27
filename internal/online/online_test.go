package online

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

func TestParse_DefaultsAndValidation(t *testing.T) {
	c, err := Parse([]byte(`{"budget":{"judgeTokensPerDay":1000},"profiles":[{"kind":"rca","evaluators":[{"name":"hallucinations_v1"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Sampling.PerKindPerHour != 6 || c.Detector.MinSamples != 12 || c.Detector.Window.D() != 6*time.Hour {
		t.Errorf("defaults not filled: %+v %+v", c.Sampling, c.Detector)
	}
	if c.Profiles[0].MaxInvocations != 3 || c.Profiles[0].Evaluators[0].NumSamples != 1 {
		t.Errorf("profile defaults not filled: %+v", c.Profiles[0])
	}

	for name, doc := range map[string]string{
		"vertex metric":     `{"profiles":[{"kind":"a","evaluators":[{"name":"safety_v1"}]}]}`,
		"reference metric":  `{"profiles":[{"kind":"a","evaluators":[{"name":"final_response_match_v2"}]}]}`,
		"no evaluators":     `{"profiles":[{"kind":"a","evaluators":[]}]}`,
		"duplicate":         `{"profiles":[{"kind":"a","evaluators":[{"name":"jev_grounded"}]},{"kind":"a","evaluators":[{"name":"jev_grounded"}]}]}`,
		"bad sink":          `{"sinks":[{"type":"email","url":"x"}]}`,
		"sink without url":  `{"sinks":[{"type":"discord"}]}`,
		"window >= base":    `{"detector":{"window":"7h","baseline":"6h"}}`,
		"unknown field":     `{"budgets":{}}`,
		"jev without cap":   `{"profiles":[{"kind":"a","evaluators":[{"name":"jev_grounded"}]}]}`,
		"judge without cap": `{"profiles":[{"kind":"a","evaluators":[{"name":"hallucinations_v1"}]}]}`,
	} {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestMatch_MostSpecificWins(t *testing.T) {
	c := &Config{Profiles: []Profile{
		{Kind: "rca", Evaluators: []Evaluator{{Name: "jev_grounded"}}},
		{Kind: "rca", Tenant: "shop", Evaluators: []Evaluator{{Name: "hallucinations_v1"}}},
	}}
	if p, ok := c.Match("rca", "svc", "shop"); !ok || p.Tenant != "shop" {
		t.Errorf("want the tenant profile, got %+v", p)
	}
	if p, ok := c.Match("rca", "svc", "other"); !ok || p.Tenant != "" {
		t.Errorf("want the kind profile, got %+v", p)
	}
	if _, ok := c.Match("voice", "svc", "shop"); ok {
		t.Error("no profile for voice")
	}
}

func llm(trace, kind, input, version string, tokens int, start int64) *tracepkg.Span {
	return &tracepkg.Span{TraceID: trace, SpanID: trace + kind, StartTime: start, Duration: 1000, Tags: map[string]any{
		"openinference.span.kind": "LLM", "gen_ai.agent.name": kind, "input.value": input,
		"llm.token_count.prompt": tokens, "llm.token_count.completion": 10, "service.name": "core",
		"service.version": version, "tenant.id": "shop", "llm.model_name": "m1",
	}}
}

func TestFeaturesOf_DominantKindAndFingerprint(t *testing.T) {
	spans := []*tracepkg.Span{
		{TraceID: "t1", SpanID: "root", OperationName: "pass", StartTime: 0, Duration: 5_000_000},
		llm("t1", "guardrail", "check event 123", "v1", 100, 10),
		llm("t1", "rca", "explain issue 42 at 10:31", "v1", 5000, 20),
	}
	f := FeaturesOf("s1", spans, "tenant.id")
	if f.Kind != "rca" || f.Tenant != "shop" || f.Version != "v1" || f.Model != "m1" || f.LLMCalls != 2 {
		t.Errorf("features = %+v", f)
	}
	if f.LatencyMs != 5000 {
		t.Errorf("latency = %d, want 5000", f.LatencyMs)
	}
	// Numbers are collapsed: the same prompt with other numbers is the same fingerprint.
	g := FeaturesOf("s2", []*tracepkg.Span{llm("t2", "guardrail", "check event 999", "v1", 100, 10)}, "")
	h := FeaturesOf("s3", []*tracepkg.Span{llm("t3", "guardrail", "check event 123", "v1", 100, 10)}, "")
	if g.Fingerprint != h.Fingerprint {
		t.Error("fingerprints differ only by numbers and must match")
	}
	if FeaturesOf("s4", []*tracepkg.Span{{TraceID: "x", SpanID: "y"}}, "").Kind != "" {
		t.Error("a session without LLM spans has no kind")
	}
}

func feat(kind, fp, version string, errs int, tokens int64) Features {
	return Features{Kind: kind, Tenant: "shop", Fingerprint: fp, Version: version, Model: "m", Errors: errs, Tokens: tokens, LatencyMs: 1000}
}

func TestSampler_RateDedupBoost(t *testing.T) {
	s := NewSampler(Sampling{PerKindPerHour: 2, BoostPerKindPerHour: 1, AfterChangeSessions: 2, DedupWindow: Duration(time.Hour)})
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

	if d := s.Decide(feat("rca", "a", "v1", 0, 100), now); !d.Sample || d.Lane != "base" {
		t.Fatalf("first: %+v", d)
	}
	if d := s.Decide(feat("rca", "a", "v1", 0, 100), now); d.Skip != SkipDuplicate {
		t.Errorf("same fingerprint: %+v, want duplicate", d)
	}
	if d := s.Decide(feat("rca", "b", "v1", 0, 100), now); !d.Sample {
		t.Errorf("second base: %+v", d)
	}
	if d := s.Decide(feat("rca", "c", "v1", 0, 100), now); d.Skip != SkipRate {
		t.Errorf("third base in the hour: %+v, want rate", d)
	}
	// Another kind has its own bucket.
	if d := s.Decide(feat("voice", "c", "v1", 0, 100), now); !d.Sample {
		t.Errorf("other kind: %+v", d)
	}
	// An error session takes the boost lane even when base is spent.
	if d := s.Decide(feat("rca", "d", "v1", 1, 100), now); !d.Sample || d.Lane != "boost" || d.Reasons[0] != ReasonError {
		t.Errorf("error session: %+v", d)
	}
	// The bucket refills over the hour.
	if d := s.Decide(feat("rca", "e", "v1", 0, 100), now.Add(31*time.Minute)); !d.Sample {
		t.Errorf("after refill: %+v", d)
	}
}

func TestSampler_ChangeStartsBurst(t *testing.T) {
	s := NewSampler(Sampling{PerKindPerHour: 0.0001, BoostPerKindPerHour: 10, AfterChangeSessions: 2, DedupWindow: Duration(time.Hour)})
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	s.Decide(feat("rca", "a", "v1", 0, 100), now) // first sight: no burst
	var boosted int
	for i, fp := range []string{"b", "c", "d"} {
		d := s.Decide(feat("rca", fp, "v2", 0, 100), now.Add(time.Duration(i)*time.Second))
		if d.Lane == "boost" {
			boosted++
			if d.Reasons[0] != ReasonChange {
				t.Errorf("reason = %v", d.Reasons)
			}
		}
	}
	if boosted != 2 {
		t.Errorf("boosted %d sessions after a version change, want afterChangeSessions=2", boosted)
	}
}

func TestSampler_Outlier(t *testing.T) {
	s := NewSampler(Sampling{PerKindPerHour: 0.0001, BoostPerKindPerHour: 10, AfterChangeSessions: 1, DedupWindow: Duration(time.Hour)})
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 40; i++ {
		s.Decide(feat("rca", "x", "v1", 0, int64(1000+i%5*10)), now)
	}
	d := s.Decide(feat("rca", "y", "v1", 0, 200_000), now)
	if d.Lane != "boost" || d.Reasons[0] != ReasonOutlier {
		t.Errorf("a 200x token session: %+v, want an outlier boost", d)
	}
}

func TestBudget_ReserveSettleAndDayRoll(t *testing.T) {
	b := NewBudgetMeter(Budget{JudgeTokensPerDay: 10_000, JevCallsPerDay: 5})
	p := Profile{Kind: "rca", Evaluators: []Evaluator{{Name: "hallucinations_v1"}, {Name: "jev_grounded"}}}
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

	r1, ok := b.Reserve(p, now)
	if !ok || r1.JudgeTokens != firstJudgeTokens || r1.JevCalls != firstJevCalls {
		t.Fatalf("first reserve = %+v %v", r1, ok)
	}
	if _, ok := b.Reserve(p, now); ok {
		t.Error("jev cap 5 cannot take a second estimate of 3 while the first is reserved")
	}
	b.Settle(p, r1, &Cost{JudgeTokens: 2000, JevCalls: 1}, now)
	if st := b.Status(now); st.Spent.JudgeTokens != 2000 || st.Reserved.JudgeTokens != 0 {
		t.Errorf("after settle: %+v", st)
	}
	// The estimate is now the actual cost, so more sessions fit.
	r2, ok := b.Reserve(p, now)
	if !ok || r2.JevCalls != 1 {
		t.Errorf("second reserve = %+v %v", r2, ok)
	}
	b.Settle(p, r2, nil, now)
	// A new UTC day resets spend.
	if st := b.Status(now.Add(13 * time.Hour)); st.Spent.JudgeTokens != 0 {
		t.Errorf("spend after midnight = %+v", st.Spent)
	}
}

func samples(scores ...float64) []Sample {
	out := make([]Sample, len(scores))
	for i, s := range scores {
		out[i] = Sample{SessionID: "s" + string(rune('a'+i)), Score: s, Version: "v1", Model: "m"}
	}
	return out
}

func repeat(v float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func TestDetector_RegressionNeedsSamplesDropAndSignificance(t *testing.T) {
	cfg := Detector{MinSamples: 5, MinDrop: 0.1}
	base := samples(0.9, 0.8, 0.9, 1, 0.85, 0.9, 0.95, 0.8)
	cur := samples(0.4, 0.5, 0.3, 0.5, 0.4, 0.45)
	for i := range cur {
		cur[i].Version = "v2"
	}
	r := Test(base, cur, cfg)
	if !r.Regressed || r.Suspect != "version v2" || len(r.Worst) != 3 || r.Worst[0] != "sc" {
		t.Errorf("clear drop: %+v", r)
	}
	if r := Test(base, cur[:4], cfg); r.Regressed || r.Enough {
		t.Errorf("4 current samples < minSamples 5: %+v", r)
	}
	if r := Test(base, samples(0.85, 0.9, 0.8, 0.9, 0.85), cfg); r.Regressed {
		t.Errorf("no drop: %+v", r)
	}
	// A noisy series with a small mean drop is not significant.
	noisy := samples(1, 0, 1, 0, 1, 0, 1, 0)
	if r := Test(noisy, samples(1, 0, 0, 1, 0, 0), cfg); r.Regressed {
		t.Errorf("noise: %+v", r)
	}
}

func TestStep_Lifecycle(t *testing.T) {
	cfg := Detector{MinSamples: 3, MinDrop: 0.1}
	key := SeriesKey{Kind: "rca", Metric: "m"}
	now := time.Now()
	id := func() string { return "a1" }
	bad := TestResult{Regressed: true, Drop: 0.2, NCur: 5, Enough: true}

	a, ev := Step(nil, key, TestResult{NCur: 5}, cfg, now, id)
	if a != nil || ev != "" {
		t.Fatalf("healthy series opened %v %q", a, ev)
	}
	a, ev = Step(nil, key, bad, cfg, now, id)
	if ev != EventOpen || a.Status != "open" {
		t.Fatalf("open: %q %+v", ev, a)
	}
	if _, ev = Step(a, key, bad, cfg, now, id); ev != "" {
		t.Errorf("same drop again notified %q", ev)
	}
	worse := bad
	worse.Drop = 0.35
	a, ev = Step(a, key, worse, cfg, now, id)
	if ev != EventUpdated || a.NotifiedDrop != 0.35 {
		t.Errorf("worse: %q %+v", ev, a)
	}
	ok := TestResult{Drop: 0.02, NCur: 5}
	a, ev = Step(a, key, ok, cfg, now, id)
	if ev != "" || a.QuietChecks != 1 {
		t.Errorf("first quiet check: %q %+v", ev, a)
	}
	a, ev = Step(a, key, ok, cfg, now, id)
	if ev != EventResolved || a.Status != "resolved" || a.ResolvedAt == nil {
		t.Errorf("second quiet check: %q %+v", ev, a)
	}
}

func TestNotifier_DiscordAndWebhook(t *testing.T) {
	var got []map[string]any
	var auth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		got = append(got, m)
		auth = append(auth, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	tok := t.TempDir() + "/token"
	if err := writeFile(tok, "secret\n"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_DISCORD_URL", srv.URL)
	n := &Notifier{Sinks: []Sink{{Type: "discord", URLEnv: "TEST_DISCORD_URL"}, {Type: "webhook", URL: srv.URL, BearerTokenFile: tok},
		{Type: "event", URL: srv.URL, BearerTokenFile: tok}}}
	cfg := &Config{Detector: Detector{Window: Duration(6 * time.Hour), Baseline: Duration(168 * time.Hour)}}
	a := &Alert{ID: "alert-1", Key: SeriesKey{Tenant: "shop", Kind: "rca", Metric: "jev_grounded"},
		Last: TestResult{BaseMean: 0.9, CurMean: 0.6, NBase: 40, NCur: 12, Drop: 0.3, Suspect: "version v2", Worst: []string{"s1"}}}
	if err := n.Alert(context.Background(), PayloadOf(EventOpen, a, cfg)); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d posts", len(got))
	}
	content, _ := got[0]["content"].(string)
	for _, want := range []string{"Quality drop", "shop · rca", "jev_grounded", "0.90 → 0.60", "version v2", "s1"} {
		if !strings.Contains(content, want) {
			t.Errorf("discord text missing %q:\n%s", want, content)
		}
	}
	if got[1]["type"] != WebhookType || got[1]["event"] != "open" || got[1]["kind"] != "rca" || auth[1] != "Bearer secret" {
		t.Errorf("webhook body %v auth %q", got[1], auth[1])
	}
	if auth[0] != "" {
		t.Error("discord must not get the bearer token")
	}
	ev := got[2]
	msg, _ := ev["message"].(string)
	if ev["type"] != EventTypeDegraded || ev["severity"] != "warning" || ev["source"] != "rca/jev_grounded" ||
		ev["tenant"] != "shop" || !strings.Contains(msg, "0.90 → 0.60") || auth[2] != "Bearer secret" {
		t.Errorf("event body %v auth %q", ev, auth[2])
	}
	if raw, _ := ev["raw"].(map[string]any); raw["alertId"] != "alert-1" {
		t.Errorf("event raw = %v", ev["raw"])
	}
	if r := EventOf(PayloadOf(EventResolved, a, cfg)); r.Type != EventTypeRecovered || r.Severity != "info" || r.Source != "rca/jev_grounded" {
		t.Errorf("resolved event = %+v", r)
	}
}

func TestDigestText(t *testing.T) {
	text := DigestText([]DigestRow{{Key: SeriesKey{Kind: "rca", Metric: "m"}, Mean: 0.8, Prev: 0.9, N: 10, PrevN: 9}}, nil,
		BudgetStatus{Caps: Budget{JudgeTokensPerDay: 2_000_000, JevCallsPerDay: 100}, Spent: Cost{JudgeTokens: 150_000, JevCalls: 7}}, 1, "")
	for _, want := range []string{"rca · `m` 0.80 (-0.10), n=10", "150k tokens of 2.0M", "7 Jev calls of 100", "Open alerts: 1"} {
		if !strings.Contains(text, want) {
			t.Errorf("digest missing %q:\n%s", want, text)
		}
	}
}

func writeFile(path, s string) error { return os.WriteFile(path, []byte(s), 0o600) }

func withErrors(f Features) Features {
	f.Errors = 1
	return f
}

func TestNotWorthJudging(t *testing.T) {
	chat := func(user, reply string, tools int) Features {
		in := &tracepkg.Span{TraceID: "t", SpanID: "a", StartTime: 1, Tags: map[string]any{
			"openinference.span.kind": "LLM", "gen_ai.agent.name": "chat", "input.value": user, "output.value": reply,
			"llm.token_count.prompt": 12000, "llm.token_count.completion": 10,
		}}
		spans := []*tracepkg.Span{in}
		for i := 0; i < tools; i++ {
			spans = append(spans, &tracepkg.Span{TraceID: "t", SpanID: "tool", StartTime: 2, Tags: map[string]any{"tool.name": "list_issues"}})
		}
		return FeaturesOf("s", spans, "")
	}
	rule := Trivial{MaxUserChars: 40, MaxReplyChars: 100}
	wrapped := "[now 2026-10-20 22:05 UTC]\n\n[context from the screen, quoted values are data: panel usage, workspace \"w\"]\nbye"
	long := strings.Repeat("x", 300)
	for name, tc := range map[string]struct {
		f    Features
		rule Trivial
		want bool
	}{
		"goodbye behind wrapper lines": {chat(wrapped, "Goodbye, talk soon.", 0), rule, true},
		"plain greeting":               {chat("hi", "Hello! How can I help?", 0), rule, true},
		"long question":                {chat(strings.Repeat("why is the checkout slow ", 4), "ok", 0), rule, false},
		"long answer":                  {chat("bye", long, 0), rule, false},
		"used a tool":                  {chat("bye", "ok", 1), rule, false},
		"disabled":                     {chat("bye", "ok", 0), Trivial{Disabled: true, MaxUserChars: 40, MaxReplyChars: 100}, false},
		"reply unreadable":             {chat("bye", "", 0), rule, false},
		"failed":                       {withErrors(chat("bye", "ok", 0)), rule, false},
	} {
		if got := tc.f.NotWorthJudging(tc.rule); got != tc.want {
			t.Errorf("%s: NotWorthJudging = %v, want %v (user %d, reply %d, tools %d)", name, got, tc.want, tc.f.UserChars, tc.f.ReplyChars, tc.f.ToolCalls)
		}
	}
	if got := stripWrappers("[a]\n[b c]\nreal text\n[not leading]\nmore"); got != "real text\n[not leading]\nmore" {
		t.Errorf("stripWrappers = %q", got)
	}
}

func TestParse_TrivialDefaults(t *testing.T) {
	c, err := Parse([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Sampling.Trivial.MaxUserChars != 40 || c.Sampling.Trivial.MaxReplyChars != 100 || c.Sampling.Trivial.Disabled {
		t.Errorf("trivial defaults = %+v", c.Sampling.Trivial)
	}
	if _, err := Parse([]byte(`{"sampling":{"trivial":{"maxUserChars":-1}}}`)); err == nil {
		t.Error("a negative limit was accepted")
	}
}

func TestMatch_GlobKindAndExactBeatsGlob(t *testing.T) {
	c := &Config{Profiles: []Profile{
		{Kind: "*-spot", Evaluators: []Evaluator{{Name: "jev_grounded"}}},
		{Kind: "alpha-spot", Evaluators: []Evaluator{{Name: "hallucinations_v1"}}},
	}}
	if p, ok := c.Match("beta-spot", "", ""); !ok || p.Kind != "*-spot" {
		t.Errorf("a spot should take the glob, got %+v", p)
	}
	if p, ok := c.Match("alpha-spot", "", ""); !ok || p.Kind != "alpha-spot" {
		t.Errorf("the named spot should take its own profile, got %+v", p)
	}
	if _, ok := c.Match("chat", "", ""); ok {
		t.Error("chat matches no spot profile")
	}
}

func TestWithJobs_ReplacesAndValidates(t *testing.T) {
	base, err := Parse([]byte(`{"budget":{"judgeTokensPerDay":1000,"jevCallsPerDay":10},"profiles":[{"kind":"rca","evaluators":[{"name":"jev_grounded"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	next, err := base.WithJobs(Jobs{PerKindPerHour: 2, Profiles: []Profile{{Kind: "*-spot", PerHour: 1, Evaluators: []Evaluator{{Name: "jev_task_resolved"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if next.Sampling.PerKindPerHour != 2 || len(next.Profiles) != 1 || next.Profiles[0].MaxInvocations != 3 || next.Profiles[0].Evaluators[0].NumSamples != 1 {
		t.Errorf("jobs not filled: %+v", next.Profiles)
	}
	if len(base.Profiles) != 1 || base.Profiles[0].Kind != "rca" {
		t.Error("the base configuration must not change")
	}
	for name, j := range map[string]Jobs{
		"bad pattern":   {Profiles: []Profile{{Kind: "[", Evaluators: []Evaluator{{Name: "jev_grounded"}}}}},
		"no evaluators": {Profiles: []Profile{{Kind: "chat"}}},
		"negative rate": {Profiles: []Profile{{Kind: "chat", PerHour: -1, Evaluators: []Evaluator{{Name: "jev_grounded"}}}}},
		"reference":     {Profiles: []Profile{{Kind: "chat", Evaluators: []Evaluator{{Name: "response_match_score"}}}}},
	} {
		if _, err := base.WithJobs(j); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

// A profile that cannot run is refused when it is loaded or saved, not when a session first reaches it.
func TestConfigValidate_Evaluators(t *testing.T) {
	cfg := func(evaluator string) string {
		return `{"budget":{"judgeTokensPerDay":1000,"jevCallsPerDay":10},"profiles":[{"kind":"chat","evaluators":[` + evaluator + `]}]}`
	}
	for name, ev := range map[string]string{
		"hallucinations":  `{"name":"hallucinations_v1"}`,
		"rubric":          `{"name":"rubric_based_final_response_quality_v1","rubrics":["Answers the question"]}`,
		"tool use rubric": `{"name":"rubric_based_tool_use_quality_v1","rubrics":["Uses the right tool"]}`,
		"jev builtin":     `{"name":"jev_grounded"}`,
		"jev custom":      `{"name":"jev_custom","questions":{"q":{"question":"Was it polite?"}}}`,
	} {
		if _, err := Parse([]byte(cfg(ev))); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, ev := range map[string]string{
		"typo":                `{"name":"hallucination_v1"}`,
		"jev typo":            `{"name":"jev_ground"}`,
		"not an evaluator":    `{"name":"tool_trajectory_avg_score"}`,
		"rubric without any":  `{"name":"rubric_based_final_response_quality_v1"}`,
		"rubric of blanks":    `{"name":"rubric_based_tool_use_quality_v1","rubrics":[" ",""]}`,
		"custom no questions": `{"name":"jev_custom"}`,
	} {
		if _, err := Parse([]byte(cfg(ev))); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestSampler_DecideAtUsesTheJobsRate(t *testing.T) {
	s := NewSampler(Sampling{PerKindPerHour: 5, BoostPerKindPerHour: 1, AfterChangeSessions: 1, DedupWindow: Duration(time.Hour)})
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	if d := s.DecideAt(feat("chat", "a", "v1", 0, 100), 1, now); !d.Sample {
		t.Fatalf("first: %+v", d)
	}
	if d := s.DecideAt(feat("chat", "b", "v1", 0, 100), 1, now); d.Skip != SkipRate {
		t.Errorf("a job at one an hour takes one session: %+v", d)
	}
	s.SetConfig(Sampling{PerKindPerHour: 5, BoostPerKindPerHour: 1, AfterChangeSessions: 1, DedupWindow: Duration(time.Hour)})
	if d := s.DecideAt(feat("chat", "c", "v1", 0, 100), 0, now.Add(2*time.Hour)); !d.Sample {
		t.Errorf("after the hour: %+v", d)
	}
}
