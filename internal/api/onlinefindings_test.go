package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/triageagent-dev/ego/internal/judge"
	"github.com/triageagent-dev/ego/internal/mcptools"
	"github.com/triageagent-dev/ego/internal/online"
)

// scriptedJudge answers the findings call with canned replies and reports a fixed token cost to the engine's
// usage hook, so the budget bookkeeping is the real one.
type scriptedJudge struct {
	mu      sync.Mutex
	replies []string // the next reply is taken from the front; the last one repeats
	err     error
	tokens  int64
	prompts []string
	onUsage func(int64)
}

func (j *scriptedJudge) Generate(ctx context.Context, prompt string) (string, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.prompts = append(j.prompts, prompt)
	if j.onUsage != nil {
		j.onUsage(j.tokens)
	}
	if j.err != nil {
		return "", j.err
	}
	r := ""
	if len(j.replies) > 0 {
		r = j.replies[0]
		if len(j.replies) > 1 {
			j.replies = j.replies[1:]
		}
	}
	return r, nil
}

const goodDiagnosis = `{"failure_mode": "claims memory use the tool output does not contain",
 "summary": "The lowest sessions all state a memory figure no tool returned.",
 "suggested_correction": "Only state figures that appear in a tool result; say so when a tool was not called.",
 "confidence": "high",
 "evidence": [{"session_id": "otlp-000000000001", "excerpt": "Memory use is at 90 percent.", "why": "no tool output reports memory"}]}`

const findingsConfig = `{
	"budget": {"judgeTokensPerDay": 100000},
	"profiles": [{"kind": "rca", "evaluators": [{"name": "hallucinations_v1"}]}],
	"detector": {"window": "6h", "baseline": "48h", "minSamples": 5, "minDrop": 0.1},
	"findings": {"minSessions": 8, "maxSessions": 4, "minScore": 0.7, "window": "24h", "every": "6h", "verifyWithin": "48h"}
}`

type findingsFixture struct {
	e        *OnlineEngine
	sessions *SessionStore
	archive  *SQLiteStore
	judge    *scriptedJudge
	now      time.Time
	n        int
	posts    *postLog
}

// postLog records the bodies a webhook sink receives.
type postLog struct {
	mu   sync.Mutex
	list []map[string]any
}

func (p *postLog) ofType(typ string) []map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []map[string]any
	for _, m := range p.list {
		if m["type"] == typ {
			out = append(out, m)
		}
	}
	return out
}

func newFindingsFixture(t *testing.T, cfgJSON string) *findingsFixture {
	t.Helper()
	posts := &postLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		posts.mu.Lock()
		posts.list = append(posts.list, m)
		posts.mu.Unlock()
	}))
	t.Cleanup(srv.Close)
	cfgJSON = strings.Replace(cfgJSON, `"detector"`, `"sinks": [{"type": "webhook", "url": "`+srv.URL+`"}], "detector"`, 1)

	archive, err := NewSQLiteStore(filepath.Join(t.TempDir(), "findings.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { archive.Close() })
	sessions := NewSessionStoreWithArchive(nil, archive, SessionLimits{UsageTenantAttr: "tenant.id"})
	cfg, err := online.Parse([]byte(cfgJSON))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Sampling.Trivial.Disabled = true
	f := &findingsFixture{sessions: sessions, archive: archive, judge: &scriptedJudge{replies: []string{goodDiagnosis}, tokens: 700},
		now: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC), posts: posts}
	f.e = NewOnlineEngine(cfg, archive, sessions, "tenant.id")
	f.e.now = func() time.Time { return f.now }
	f.e.newJudge = func(ctx context.Context, model string, onUsage func(int64)) (judge.Model, error) {
		f.judge.onUsage = onUsage
		return f.judge, nil
	}
	return f
}

// findingSessionBody is onlineSessionBody plus one tool call.
func findingSessionBody(traceID string, startMs int64, kind, input, tool string) map[string]any {
	body := onlineSessionBody(traceID, startMs, kind, input, "The pod is fine.")
	spans := body["resourceSpans"].([]any)[0].(map[string]any)["scopeSpans"].([]any)[0].(map[string]any)
	ns := func(ms int64) string { return strconvI(ms * 1_000_000) }
	spans["spans"] = append(spans["spans"].([]any), map[string]any{
		"traceId": traceID, "spanId": "tool-" + traceID, "parentSpanId": "agent-" + traceID, "name": "tool.call",
		"startTimeUnixNano": ns(startMs + 100), "endTimeUnixNano": ns(startMs + 900),
		"attributes": []any{
			map[string]any{"key": "openinference.span.kind", "value": map[string]any{"stringValue": "TOOL"}},
			map[string]any{"key": "tool.name", "value": map[string]any{"stringValue": tool}},
		},
	})
	return body
}

// seed stores one scored session of (tenant, rca, hallucinations_v1): its trace, its run result with the judge's
// details, and its score row. The session id is otlp-<12 digits>.
func (f *findingsFixture) seed(tenant string, score float64, at time.Time, version, user, tool string) string {
	f.n++
	trace := fmt.Sprintf("%012d0001", f.n)
	id := "otlp-" + trace[:12]
	f.sessions.Ingest(findingSessionBody(trace, at.UnixMilli(), "rca", user, tool))
	run, err := f.archive.CreateRun(runSpecSummaryDTO{Approach: "online", Target: map[string]any{"tenant": tenant}})
	if err != nil {
		panic(err)
	}
	if err := f.archive.InsertRunResults(run.RunID, []runResultRowDTO{{
		ResultID: newRandomID("res"), RunID: run.RunID, EvalSetItemID: id, EvalSetItemName: id, EvaluatorName: "hallucinations_v1",
		EvaluatorType: "builtin", Status: "failed", Score: ptr(score), Details: hallucinationDetails(), CreatedAt: nowRFC3339(),
	}}); err != nil {
		panic(err)
	}
	if err := f.archive.InsertOnlineEval(onlineEvalRow{SessionID: id, TraceID: trace, AtMs: at.UnixMilli(), Tenant: tenant, Kind: "rca", Service: "ops-agent", RunID: run.RunID},
		version, "gemini-test", []onlineScoreRow{{Metric: "hallucinations_v1", Score: ptr(score), Status: "failed"}}); err != nil {
		panic(err)
	}
	return id
}

// seedBad stores n sessions scoring 0.30 + 0.02*i, a few minutes apart ending an hour ago.
func (f *findingsFixture) seedBad(tenant string, n int) []string {
	var ids []string
	for i := 0; i < n; i++ {
		ids = append(ids, f.seed(tenant, 0.30+0.02*float64(i), f.now.Add(-time.Hour+time.Duration(i)*time.Minute), "v1",
			fmt.Sprintf("question number %d about checkout", i), "get_pods"))
	}
	return ids
}

func (f *findingsFixture) findings(t *testing.T, state string) []online.Finding {
	t.Helper()
	out, err := f.e.Findings(RoleInfo{Role: RoleAdmin, IsAdmin: true}, "", state, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestFindingsSweep_WritesOneFindingFromTheLowestSessions(t *testing.T) {
	f := newFindingsFixture(t, findingsConfig)
	ids := f.seedBad("acme", 9)
	// Other series the sweep must leave alone: healthy, too short, and another tenant that is bad but short.
	for i := 0; i < 9; i++ {
		f.seed("globex", 0.9, f.now.Add(-time.Hour+time.Duration(i)*time.Minute), "v1", "fine question", "get_pods")
	}
	for i := 0; i < 3; i++ {
		f.seed("initech", 0.1, f.now.Add(-time.Hour+time.Duration(i)*time.Minute), "v1", "fine question", "get_pods")
	}

	f.e.findingsMaintain(context.Background(), f.now)

	got := f.findings(t, "")
	if len(got) != 1 {
		t.Fatalf("findings = %d, want only the bad series with enough sessions", len(got))
	}
	fd := got[0]
	if fd.Tenant != "acme" || fd.Kind != "rca" || fd.Metric != "hallucinations_v1" || fd.State != online.FindingOpen || fd.AlertID != "" {
		t.Errorf("finding = %+v", fd)
	}
	// maxSessions is 4: the four lowest scores, worst first.
	if want := ids[:4]; !slices.Equal(fd.Sessions, want) {
		t.Errorf("evidence sessions = %v, want the four lowest %v", fd.Sessions, want)
	}
	if fd.Diagnosis.FailureMode != "claims memory use the tool output does not contain" || fd.Diagnosis.Confidence != "high" ||
		len(fd.Diagnosis.Evidence) != 1 || fd.Model != judge.DefaultModel || fd.ModelError != "" || fd.Tokens != 700 {
		t.Errorf("diagnosis = %+v model %q err %q tokens %d", fd.Diagnosis, fd.Model, fd.ModelError, fd.Tokens)
	}
	if fd.Window.N != 9 || fd.Window.Below != 9 || fd.Window.Mean < 0.37 || fd.Window.Mean > 0.39 {
		t.Errorf("window = %+v", fd.Window)
	}

	// One model call, and its prompt carries the lowest sessions' evidence but not the others'.
	if len(f.judge.prompts) != 1 {
		t.Fatalf("judge calls = %d, want one", len(f.judge.prompts))
	}
	p := f.judge.prompts[0]
	for _, want := range []string{"question number 0 about checkout", "question number 3 about checkout", "tool calls: get_pods",
		"judge rationale: no tool output reports memory", "failing verdict: unsupported sentence", "<session id=\"otlp-000000000001\""} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if strings.Contains(p, "question number 4 about checkout") || strings.Contains(p, "fine question") {
		t.Error("prompt quotes sessions beyond the lowest four")
	}

	// The call is metered against the judge budget, and counted.
	st := f.e.status()
	if st.Budget.Spent.JudgeTokens != 700 || st.Findings.Written != 1 || st.Findings.Tokens != 700 || st.Findings.Skipped.Budget != 0 {
		t.Errorf("budget %+v findings %+v", st.Budget.Spent, st.Findings)
	}

	// A second sweep inside findings.every does nothing; after it, the series already has an open finding.
	f.e.findingsMaintain(context.Background(), f.now.Add(time.Hour))
	f.e.findingsMaintain(context.Background(), f.now.Add(7*time.Hour))
	if len(f.findings(t, "")) != 1 || len(f.judge.prompts) != 1 {
		t.Errorf("a series with an open finding was diagnosed again: %d findings, %d calls", len(f.findings(t, "")), len(f.judge.prompts))
	}
	opened := f.posts.ofType("ego.finding.v1")
	if len(opened) != 1 || opened[0]["event"] != "open" || opened[0]["failureMode"] != fd.Diagnosis.FailureMode ||
		opened[0]["findingId"] != fd.ID || len(opened[0]["sessions"].([]any)) != 4 {
		t.Errorf("finding payloads = %v", opened)
	}
}

func TestFindingsSweep_MalformedAnswerStillWritesFromTheEvidence(t *testing.T) {
	f := newFindingsFixture(t, findingsConfig)
	f.judge.replies = []string{"The agent seems to hallucinate. Sorry, no JSON today."}
	f.seedBad("acme", 9)
	f.e.findingsMaintain(context.Background(), f.now)

	got := f.findings(t, "")
	if len(got) != 1 {
		t.Fatalf("findings = %d", len(got))
	}
	fd := got[0]
	if fd.Diagnosis.FailureMode != "9 of 9 sessions score below 0.7 on hallucinations_v1" || fd.Diagnosis.SuggestedCorrection != "" {
		t.Errorf("fallback diagnosis = %+v", fd.Diagnosis)
	}
	// The model is noted as failed: its name stays, with the reason, and its tokens were still spent.
	if fd.Model != judge.DefaultModel || !strings.Contains(fd.ModelError, "unusable answer") || fd.Tokens != 700 {
		t.Errorf("model %q error %q tokens %d", fd.Model, fd.ModelError, fd.Tokens)
	}
	if len(fd.Sessions) != 4 {
		t.Errorf("sessions = %v", fd.Sessions)
	}

	// A model error is the same: finding written, error noted.
	g := newFindingsFixture(t, findingsConfig)
	g.judge.err = errors.New("quota exceeded")
	g.seedBad("acme", 9)
	g.e.findingsMaintain(context.Background(), g.now)
	if gf := g.findings(t, ""); len(gf) != 1 || !strings.Contains(gf[0].ModelError, "quota exceeded") || gf[0].Diagnosis.Confidence != "low" {
		t.Errorf("after a model error: %+v", gf)
	}
}

func TestFindings_NoJudgeModelAndBudgetCap(t *testing.T) {
	// No judge model can be built: written from the evidence alone, model empty, nothing spent.
	f := newFindingsFixture(t, findingsConfig)
	f.e.newJudge = func(context.Context, string, func(int64)) (judge.Model, error) {
		return nil, errors.New("no credentials")
	}
	f.seedBad("acme", 9)
	f.e.findingsMaintain(context.Background(), f.now)
	got := f.findings(t, "")
	if len(got) != 1 || got[0].Model != "" || got[0].Tokens != 0 || got[0].Diagnosis.FailureMode != "9 of 9 sessions score below 0.7 on hallucinations_v1" {
		t.Fatalf("without a model: %+v", got)
	}

	// The cap is reached: the finding is skipped with reason budget, and the call never happens.
	g := newFindingsFixture(t, strings.Replace(findingsConfig, `"judgeTokensPerDay": 100000`, `"judgeTokensPerDay": 3000`, 1))
	g.seedBad("acme", 9)
	g.e.findingsMaintain(context.Background(), g.now)
	if len(g.findings(t, "")) != 0 || len(g.judge.prompts) != 0 {
		t.Errorf("a finding was written past the cap: %d findings, %d calls", len(g.findings(t, "")), len(g.judge.prompts))
	}
	if st := g.e.status(); st.Findings.Skipped.Budget != 1 || st.Findings.Written != 0 {
		t.Errorf("findings status = %+v", st.Findings)
	}

	// A series whose sessions have no stored evidence is skipped as noEvidence.
	h := newFindingsFixture(t, findingsConfig)
	for i := 0; i < 9; i++ {
		s := 0.3
		if err := insertScoreOnly(h.archive, fmt.Sprintf("gone-%d", i), h.now.Add(-time.Hour), "acme", "rca", "hallucinations_v1", s); err != nil {
			t.Fatal(err)
		}
	}
	h.e.findingsMaintain(context.Background(), h.now)
	if st := h.e.status(); st.Findings.Skipped.NoEvidence != 1 || len(h.findings(t, "")) != 0 {
		t.Errorf("noEvidence: %+v", st.Findings)
	}

	// enabled:false writes none.
	d := newFindingsFixture(t, strings.Replace(findingsConfig, `"findings": {`, `"findings": {"enabled": false, `, 1))
	d.seedBad("acme", 9)
	d.e.findingsMaintain(context.Background(), d.now)
	if len(d.findings(t, "")) != 0 {
		t.Error("findings.enabled=false still wrote a finding")
	}
}

// An alert opens a finding, refreshes it when the drop grows, and resolving the alert resolves the finding; a sweep
// finding of the same series is adopted rather than duplicated.
func TestFindings_FollowTheirAlert(t *testing.T) {
	f := newFindingsFixture(t, findingsConfig)
	for i := 0; i < 10; i++ {
		f.seed("acme", 0.9+float64(i%2)*0.05, f.now.Add(-24*time.Hour+time.Duration(i)*time.Minute), "v1", "healthy question", "get_pods")
	}
	for i := 0; i < 6; i++ {
		f.seed("acme", 0.4+float64(i%2)*0.05, f.now.Add(-time.Hour+time.Duration(i)*time.Minute), "v2", fmt.Sprintf("bad question %d", i), "get_logs")
	}
	ctx := context.Background()
	f.e.Tick(ctx)

	open := f.findings(t, "open")
	if len(open) != 1 || open[0].AlertID == "" {
		t.Fatalf("findings after the alert opened = %+v", open)
	}
	alertID := open[0].AlertID
	if a, ok, _ := f.archive.OnlineFindingByAlert(alertID); !ok || a.ID != open[0].ID {
		t.Error("the finding is not found by its alert")
	}
	f.e.Tick(ctx) // nothing changed: no refresh, no second finding
	if len(f.judge.prompts) != 1 {
		t.Errorf("an unchanged alert re-diagnosed: %d calls", len(f.judge.prompts))
	}

	// The drop grows: the alert updates and the finding is refreshed in place.
	f.now = f.now.Add(10 * time.Minute)
	for i := 0; i < 6; i++ {
		f.seed("acme", 0.05, f.now.Add(-5*time.Minute+time.Duration(i)*time.Second), "v2", fmt.Sprintf("worse question %d", i), "get_logs")
	}
	f.e.Tick(ctx)
	after := f.findings(t, "open")
	if len(after) != 1 || after[0].ID != open[0].ID || len(f.judge.prompts) != 2 || after[0].Tokens != 1400 {
		t.Fatalf("after the update: %d findings, %d calls, tokens %d", len(after), len(f.judge.prompts), after[0].Tokens)
	}
	events := []string{}
	for _, p := range f.posts.ofType("ego.finding.v1") {
		events = append(events, p["event"].(string))
	}
	if !slices.Equal(events, []string{"open", "updated"}) {
		t.Errorf("finding events = %v", events)
	}

	// The series recovers: the alert resolves after two quiet checks and the finding with it.
	f.now = f.now.Add(7 * time.Hour)
	for i := 0; i < 6; i++ {
		f.seed("acme", 0.93, f.now.Add(-time.Hour+time.Duration(i)*time.Minute), "v3", "recovered question", "get_pods")
	}
	f.e.Tick(ctx)
	f.e.Tick(ctx)
	if len(f.findings(t, "open")) != 0 || len(f.findings(t, "resolved")) != 1 {
		t.Fatalf("after recovery: open %d resolved %d", len(f.findings(t, "open")), len(f.findings(t, "resolved")))
	}
	events = nil
	for _, p := range f.posts.ofType("ego.finding.v1") {
		events = append(events, p["event"].(string))
	}
	if !slices.Equal(events, []string{"open", "updated", "resolved"}) {
		t.Errorf("finding events = %v", events)
	}
}

func TestFindings_GoStaleWhenTheSeriesRecoversUncorrected(t *testing.T) {
	f := newFindingsFixture(t, findingsConfig)
	f.seedBad("acme", 9)
	ctx := context.Background()
	f.e.findingsMaintain(ctx, f.now)
	if len(f.findings(t, "open")) != 1 {
		t.Fatal("no finding to start from")
	}
	// A day later the window holds only good sessions.
	f.now = f.now.Add(25 * time.Hour)
	for i := 0; i < 9; i++ {
		f.seed("acme", 0.92, f.now.Add(-time.Hour+time.Duration(i)*time.Minute), "v1", "fine again", "get_pods")
	}
	f.e.findingsMaintain(ctx, f.now)
	if len(f.findings(t, "stale")) != 1 || len(f.findings(t, "open")) != 0 {
		t.Errorf("stale %d open %d", len(f.findings(t, "stale")), len(f.findings(t, "open")))
	}
	evs := f.posts.ofType("ego.finding.v1")
	if len(evs) != 2 || evs[1]["event"] != "stale" {
		t.Errorf("payloads = %v", evs)
	}
}

func member(tenants ...string) RoleInfo { return RoleInfo{Role: RoleMember, Tenants: tenants} }

func TestRegisterCorrection_TenantImpliedAndForeignRefused(t *testing.T) {
	f := newFindingsFixture(t, findingsConfig)
	f.seedBad("acme", 9)
	f.seedBad("globex", 9)
	f.e.findingsMaintain(context.Background(), f.now)
	all := f.findings(t, "open")
	if len(all) != 2 {
		t.Fatalf("findings = %d", len(all))
	}
	var acme, globex online.Finding
	for _, x := range all {
		if x.Tenant == "acme" {
			acme = x
		} else {
			globex = x
		}
	}

	// A role limited to exactly one tenant: the tenant is implied (kind is required, finding optional).
	c, err := f.e.RegisterCorrection(member("acme"), mcptools.CorrectionInput{Kind: "rca", What: "tell the agent to quote tool output", Version: "v2", By: "ops-agent"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Tenant != "acme" || c.Kind != "rca" || c.State != online.CorrectionPending || c.Version != "v2" || c.By != "ops-agent" || c.FindingID != "" || c.Metric != "" {
		t.Errorf("correction = %+v", c)
	}
	if !c.Verification.WindowEnd.Equal(f.now.Add(48*time.Hour)) || c.Verification.Before.N != 9 {
		t.Errorf("verification window = %+v", c.Verification)
	}

	// With a finding: tenant and kind come from it, the metric too, and the finding becomes corrected with the
	// correction in its history.
	c2, err := f.e.RegisterCorrection(member("acme"), mcptools.CorrectionInput{FindingID: acme.ID, Kind: "rca", What: "added a rule"})
	if err != nil {
		t.Fatal(err)
	}
	if c2.FindingID != acme.ID || c2.Metric != "hallucinations_v1" || c2.Tenant != "acme" || c2.Verification.Before.N != 9 {
		t.Errorf("correction with finding = %+v", c2)
	}
	fd, _, _ := f.archive.OnlineFinding(acme.ID)
	if fd.State != online.FindingCorrected || len(fd.History) != 1 || fd.History[0].CorrectionID != c2.ID || fd.History[0].State != online.CorrectionPending {
		t.Errorf("finding after the correction = %+v", fd)
	}

	// Refusals: a foreign tenant, a foreign finding (looks missing), a tenant that does not match the finding, a
	// role with several tenants and none named, no kind, no text, text over 2000 characters, a viewer.
	refused := map[string]struct {
		info RoleInfo
		in   mcptools.CorrectionInput
		code int
	}{
		"foreign tenant":     {member("acme"), mcptools.CorrectionInput{Tenant: "globex", Kind: "rca", What: "x"}, 403},
		"foreign finding":    {member("acme"), mcptools.CorrectionInput{FindingID: globex.ID, Kind: "rca", What: "x"}, 404},
		"mismatched tenant":  {member("acme", "globex"), mcptools.CorrectionInput{FindingID: globex.ID, Tenant: "acme", Kind: "rca", What: "x"}, 400},
		"tenant not implied": {member("acme", "globex"), mcptools.CorrectionInput{Kind: "rca", What: "x"}, 400},
		"pattern not single": {member("acme/*"), mcptools.CorrectionInput{Kind: "rca", What: "x"}, 400},
		"no kind":            {member("acme"), mcptools.CorrectionInput{What: "x"}, 400},
		"no text":            {member("acme"), mcptools.CorrectionInput{Kind: "rca"}, 400},
		"too long":           {member("acme"), mcptools.CorrectionInput{Kind: "rca", What: strings.Repeat("y", 2001)}, 400},
		"viewer":             {RoleInfo{Role: RoleViewer, Tenants: []string{"acme"}}, mcptools.CorrectionInput{Kind: "rca", What: "x"}, 403},
		"agent scoped":       {RoleInfo{Role: RoleMember, Agents: []string{"ops-agent"}}, mcptools.CorrectionInput{Tenant: "acme", Kind: "rca", What: "x"}, 403},
	}
	for name, c := range refused {
		_, err := f.e.RegisterCorrection(c.info, c.in)
		var se *statusError
		if !errors.As(err, &se) || se.Status != c.code {
			t.Errorf("%s: err = %v, want status %d", name, err, c.code)
		}
	}
	if _, err := f.e.RegisterCorrection(RoleInfo{Role: RoleAdmin, IsAdmin: true}, mcptools.CorrectionInput{Tenant: "acme", Kind: "rca", What: strings.Repeat("y", 2000)}); err != nil {
		t.Errorf("exactly 2000 characters was refused: %v", err)
	}
	// An unrestricted role names the tenant.
	if _, err := f.e.RegisterCorrection(RoleInfo{Role: RoleAdmin, IsAdmin: true}, mcptools.CorrectionInput{Kind: "rca", What: "x"}); err == nil {
		t.Error("an unrestricted role with no tenant and no finding was accepted")
	}
}

func TestRegisterCorrection_StartsAChangeBurst(t *testing.T) {
	f := newFindingsFixture(t, findingsConfig)
	if _, err := f.e.RegisterCorrection(RoleInfo{Role: RoleAdmin, IsAdmin: true}, mcptools.CorrectionInput{Tenant: "acme", Kind: "rca", What: "changed the prompt"}); err != nil {
		t.Fatal(err)
	}
	d := f.e.sampler.Decide(online.Features{Tenant: "acme", Kind: "rca", Version: "v9", Model: "m", Fingerprint: "x"}, f.now)
	if !d.Sample || d.Lane != "boost" || !slices.Contains(d.Reasons, online.ReasonChange) {
		t.Errorf("the next session after a correction = %+v, want a boosted change sample", d)
	}
}

// verifyFixture has a finding written for acme and a correction registered against it at f.now; it returns both.
func verifyFixture(t *testing.T, version string) (*findingsFixture, online.Finding, *online.Correction) {
	t.Helper()
	f := newFindingsFixture(t, findingsConfig)
	f.seedBad("acme", 9) // mean 0.38
	f.e.findingsMaintain(context.Background(), f.now)
	fs := f.findings(t, "open")
	if len(fs) != 1 {
		t.Fatalf("findings = %d", len(fs))
	}
	c, err := f.e.RegisterCorrection(member("acme"), mcptools.CorrectionInput{FindingID: fs[0].ID, Kind: "rca", What: "quote tool output only", Version: version, By: "ops-agent"})
	if err != nil {
		t.Fatal(err)
	}
	return f, fs[0], c
}

func (f *findingsFixture) seedAfter(n int, score float64, version string, from time.Duration) {
	for i := 0; i < n; i++ {
		f.seed("acme", score, f.now.Add(from+time.Duration(i)*time.Minute), version, fmt.Sprintf("after question %d", i), "get_pods")
	}
}

func (f *findingsFixture) correction(t *testing.T, id string) *online.Correction {
	t.Helper()
	c, ok, err := f.archive.OnlineCorrection(id)
	if err != nil || !ok {
		t.Fatalf("correction %s: %v %v", id, ok, err)
	}
	return c
}

func TestVerification_VerifiedResolvesTheFinding(t *testing.T) {
	f, fd, c := verifyFixture(t, "")
	ctx := context.Background()
	t0 := f.now
	f.now = t0.Add(2 * time.Hour)
	f.seedAfter(5, 0.85, "v2", -time.Hour) // after the correction (t0), five sessions as minSamples needs
	f.e.Tick(ctx)

	got := f.correction(t, c.ID)
	if got.State != online.CorrectionVerified || got.VerifiedAt == nil || got.Verification.After.N != 5 ||
		got.Verification.After.Mean < 0.84 || got.Verification.Before.N != 9 {
		t.Fatalf("correction = %+v", got)
	}
	fresh, _, _ := f.archive.OnlineFinding(fd.ID)
	if fresh.State != online.FindingResolved || len(fresh.History) != 1 || fresh.History[0].State != online.CorrectionVerified {
		t.Errorf("finding = %+v", fresh)
	}
	cp := f.posts.ofType("ego.correction.v1")
	fp := f.posts.ofType("ego.finding.v1")
	if len(cp) != 1 || cp[0]["event"] != "verified" || cp[0]["correctionId"] != c.ID || fp[len(fp)-1]["event"] != "resolved" {
		t.Errorf("payloads: correction %v finding %v", cp, fp)
	}
	// A verdict is final: another tick changes nothing.
	f.e.Tick(ctx)
	if len(f.posts.ofType("ego.correction.v1")) != 1 {
		t.Error("a verified correction was announced twice")
	}
}

func TestVerification_FailedReopensTheFindingWithItsHistory(t *testing.T) {
	f, fd, c := verifyFixture(t, "")
	f.now = f.now.Add(2 * time.Hour)
	f.seedAfter(6, 0.40, "v2", -time.Hour)
	f.e.Tick(context.Background())

	got := f.correction(t, c.ID)
	if got.State != online.CorrectionFailed || got.Verification.After.N != 6 {
		t.Fatalf("correction = %+v", got)
	}
	fresh, _, _ := f.archive.OnlineFinding(fd.ID)
	if fresh.State != online.FindingOpen || len(fresh.History) != 1 || fresh.History[0].State != online.CorrectionFailed ||
		fresh.History[0].What != "quote tool output only" || fresh.History[0].Verification == nil {
		t.Errorf("reopened finding = %+v", fresh)
	}
	cp := f.posts.ofType("ego.correction.v1")
	if len(cp) != 1 || cp[0]["event"] != "failed" {
		t.Errorf("correction payloads = %v", cp)
	}
	// The reopened finding is open work again: get_findings (state open) lists it.
	if open := f.findings(t, "open"); len(open) != 1 || open[0].ID != fd.ID {
		t.Errorf("open findings = %+v", open)
	}
}

func TestVerification_PendingUntilEnoughSessionsThenTimesOut(t *testing.T) {
	f, fd, c := verifyFixture(t, "")
	ctx := context.Background()
	t0 := f.now
	f.now = t0.Add(time.Hour)
	f.seedAfter(3, 0.9, "v2", -30*time.Minute)
	f.e.Tick(ctx)

	got := f.correction(t, c.ID)
	if got.State != online.CorrectionPending || got.Verification.After.N != 3 || got.Verification.After.Mean < 0.89 {
		t.Fatalf("pending correction = %+v", got)
	}
	if fresh, _, _ := f.archive.OnlineFinding(fd.ID); fresh.State != online.FindingCorrected {
		t.Errorf("finding state = %s, want corrected while pending", fresh.State)
	}
	if len(f.posts.ofType("ego.correction.v1")) != 0 {
		t.Error("a pending correction was announced")
	}

	// The window runs out with too few sessions: failed, and the finding reopens.
	f.now = t0.Add(49 * time.Hour)
	f.e.Tick(ctx)
	got = f.correction(t, c.ID)
	if got.State != online.CorrectionFailed || !strings.Contains(got.Verification.Reason, "too few") {
		t.Errorf("after the window = %+v", got)
	}
	if fresh, _, _ := f.archive.OnlineFinding(fd.ID); fresh.State != online.FindingOpen {
		t.Errorf("finding state = %s, want open", fresh.State)
	}
}

func TestVerification_OnlyCountsSessionsAtTheCorrectionsVersion(t *testing.T) {
	f, _, c := verifyFixture(t, "v2")
	f.now = f.now.Add(2 * time.Hour)
	// Plenty of good sessions at the old version after the correction, and only two at the new one.
	f.seedAfter(8, 0.95, "v1", -time.Hour)
	f.seedAfter(2, 0.95, "v2", -30*time.Minute)
	f.e.Tick(context.Background())
	got := f.correction(t, c.ID)
	if got.State != online.CorrectionPending || got.Verification.After.N != 2 {
		t.Fatalf("with a version set, sessions at other versions must not count: %+v", got)
	}

	// Three more at v2 reach minSamples; the mean of the v2 sessions alone decides.
	f.seedAfter(3, 0.9, "v2", -20*time.Minute)
	f.e.Tick(context.Background())
	got = f.correction(t, c.ID)
	if got.State != online.CorrectionVerified || got.Verification.After.N != 5 {
		t.Errorf("after enough v2 sessions: %+v", got)
	}

	// Sessions before the correction never count either.
	g, _, c2 := verifyFixture(t, "")
	g.now = g.now.Add(2 * time.Hour)
	g.e.Tick(context.Background())
	if got := g.correction(t, c2.ID); got.Verification.After.N != 0 || got.State != online.CorrectionPending {
		t.Errorf("sessions from before the correction counted: %+v", got)
	}
}

// With findings off, corrections can still be registered and verified.
func TestVerification_RunsWithFindingsDisabled(t *testing.T) {
	f := newFindingsFixture(t, strings.Replace(findingsConfig, `"findings": {`, `"findings": {"enabled": false, `, 1))
	f.seedBad("acme", 9)
	c, err := f.e.RegisterCorrection(member("acme"), mcptools.CorrectionInput{Kind: "rca", What: "tuned the prompt"})
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(2 * time.Hour)
	f.seedAfter(5, 0.9, "v2", -time.Hour)
	f.e.Tick(context.Background())
	// No finding: before is the baseline span's mean (0.38, n=9), after 0.9.
	got := f.correction(t, c.ID)
	if got.State != online.CorrectionVerified || got.Verification.Before.N != 9 || len(f.findings(t, "")) != 0 {
		t.Errorf("correction %+v, findings %d", got, len(f.findings(t, "")))
	}
}

func TestOnlineFindingsHandlers(t *testing.T) {
	f, fd, _ := verifyFixture(t, "")
	f.seedBad("globex", 9)
	f.now = f.now.Add(7 * time.Hour)
	f.e.findingsMaintain(context.Background(), f.now)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/online/findings", onlineFindingsHandler(f.e))
	mux.HandleFunc("/api/online/findings/{id}", onlineFindingHandler(f.e))
	mux.HandleFunc("/api/online/corrections", onlineCorrectionsHandler(f.e))
	do := func(info RoleInfo, method, path, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req.WithContext(withRoleInfo(req.Context(), info)))
		var env map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &env)
		return rec.Code, env
	}
	admin := RoleInfo{Role: RoleAdmin, IsAdmin: true}

	code, env := do(admin, http.MethodGet, "/api/online/findings", "")
	if rows, _ := env["data"].([]any); code != 200 || len(rows) != 2 {
		t.Errorf("all findings = %d %v", code, env)
	}
	code, env = do(admin, http.MethodGet, "/api/online/findings?state=corrected&kind=rca&tenant=acme", "")
	if rows, _ := env["data"].([]any); code != 200 || len(rows) != 1 || rows[0].(map[string]any)["finding_id"] != fd.ID {
		t.Errorf("corrected findings of acme = %d %v", code, env)
	}
	if code, _ := do(admin, http.MethodGet, "/api/online/findings?state=nope", ""); code != 400 {
		t.Errorf("bad state = %d", code)
	}
	if code, _ := do(admin, http.MethodGet, "/api/online/findings?since=whenever", ""); code != 400 {
		t.Errorf("bad since = %d", code)
	}
	if code, env := do(admin, http.MethodGet, "/api/online/findings?since=1h", ""); code != 200 || len(env["data"].([]any)) != 1 {
		t.Errorf("since=1h (only the globex finding was written in the last hour): %d %v", code, env)
	}

	// One finding by id; a tenant-limited role gets 404 for another tenant's, like a missing one.
	if code, env := do(member("acme"), http.MethodGet, "/api/online/findings/"+fd.ID, ""); code != 200 || env["data"].(map[string]any)["state"] != "corrected" {
		t.Errorf("own finding = %d %v", code, env)
	}
	var other online.Finding
	for _, x := range f.findings(t, "open") {
		if x.Tenant == "globex" {
			other = x
		}
	}
	if code, _ := do(member("acme"), http.MethodGet, "/api/online/findings/"+other.ID, ""); code != 404 {
		t.Errorf("foreign finding = %d, want 404", code)
	}
	if code, env := do(member("acme"), http.MethodGet, "/api/online/findings", ""); len(env["data"].([]any)) != 1 || code != 200 {
		t.Errorf("limited list = %d %v", code, env)
	}

	// POST: 201 with the correction; a viewer 403; a foreign tenant 403; the tenant implied for a single-tenant role.
	body := `{"findingId": "` + other.ID + `", "kind": "rca", "what": "added a rule", "version": "v5"}`
	code, env = do(admin, http.MethodPost, "/api/online/corrections", body)
	if c, _ := env["data"].(map[string]any); code != 201 || c["state"] != "pending" || c["tenant"] != "globex" || c["version"] != "v5" {
		t.Errorf("POST = %d %v", code, env)
	}
	if code, _ := do(RoleInfo{Role: RoleViewer}, http.MethodPost, "/api/online/corrections", `{"tenant":"acme","kind":"rca","what":"x"}`); code != 403 {
		t.Errorf("viewer POST = %d, want 403", code)
	}
	if code, _ := do(member("acme"), http.MethodPost, "/api/online/corrections", `{"tenant":"globex","kind":"rca","what":"x"}`); code != 403 {
		t.Errorf("foreign tenant POST = %d, want 403", code)
	}
	if code, env := do(member("acme"), http.MethodPost, "/api/online/corrections", `{"kind":"rca","what":"x"}`); code != 201 || env["data"].(map[string]any)["tenant"] != "acme" {
		t.Errorf("implied tenant POST = %d %v", code, env)
	}
	if code, _ := do(admin, http.MethodPost, "/api/online/corrections", `{not json`); code != 400 {
		t.Errorf("bad JSON = %d", code)
	}
	if code, _ := do(admin, http.MethodDelete, "/api/online/corrections", ``); code != 405 {
		t.Errorf("DELETE = %d", code)
	}

	// GET corrections is tenant-filtered and filterable by state.
	code, env = do(member("acme"), http.MethodGet, "/api/online/corrections?state=pending", "")
	if rows := env["data"].([]any); code != 200 || len(rows) != 2 {
		t.Errorf("acme's pending corrections = %d %v", code, env)
	}
	if code, env := do(admin, http.MethodGet, "/api/online/corrections?kind=rca", ""); len(env["data"].([]any)) != 3 || code != 200 {
		t.Errorf("all corrections = %d %v", code, env)
	}
	if code, _ := do(admin, http.MethodGet, "/api/online/corrections?state=bogus", ""); code != 400 {
		t.Errorf("bad state = %d", code)
	}

	// Unconfigured: 503.
	rec := httptest.NewRecorder()
	onlineFindingsHandler(nil)(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != 503 {
		t.Errorf("unconfigured = %d", rec.Code)
	}
}

// The tools return the same through the stdio client (REST) as through the in-process backend, and the role limits
// hold on the endpoints too.
func TestRESTClientSeesWhatTheInProcessBackendSeesForFindings(t *testing.T) {
	f := newFindingsFixture(t, findingsConfig)
	f.seedBad("acme", 9)
	f.seedBad("globex", 9)
	f.e.findingsMaintain(context.Background(), f.now)

	info := member("acme")
	mux := http.NewServeMux()
	mux.HandleFunc("/api/online/findings", onlineFindingsHandler(f.e))
	mux.HandleFunc("/api/online/corrections", onlineCorrectionsHandler(f.e))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(withRoleInfo(r.Context(), info)))
	}))
	defer srv.Close()
	rest := mcptools.NewREST(srv.URL, "", srv.Client())
	backend := newMCPBackend(f.sessions, f.archive, f.e, nil)
	ctx := asRole(info)

	same := func(name string, a, b any) {
		t.Helper()
		ja, _ := json.Marshal(a)
		jb, _ := json.Marshal(b)
		if string(ja) != string(jb) {
			t.Errorf("%s differs:\n rest       %s\n in-process %s", name, ja, jb)
		}
	}
	gotF, err := rest.Findings(ctx, "rca", "open", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	wantF, _ := backend.Findings(ctx, "rca", "open", time.Time{})
	if len(wantF) != 1 || wantF[0].Tenant != "acme" {
		t.Fatalf("in-process findings = %+v", wantF)
	}
	same("get_findings", gotF, wantF)

	// register_correction over REST, then list_corrections both ways.
	reg, err := rest.RegisterCorrection(ctx, mcptools.CorrectionInput{FindingID: wantF[0].ID, Kind: "rca", What: "quote tool output", Version: "v2", By: "ops-agent"})
	if err != nil {
		t.Fatal(err)
	}
	if reg.State != online.CorrectionPending || reg.Tenant != "acme" || reg.Verification.Before.N != 9 {
		t.Errorf("registered = %+v", reg)
	}
	gotC, err := rest.Corrections(ctx, "", "")
	if err != nil {
		t.Fatal(err)
	}
	wantC, _ := backend.Corrections(ctx, "", "all")
	if len(wantC) != 1 || wantC[0].ID != reg.ID {
		t.Fatalf("in-process corrections = %+v", wantC)
	}
	same("list_corrections", gotC, wantC)
	same("register_correction vs list", reg, &wantC[0])
	gotP, _ := rest.Corrections(ctx, "rca", "pending")
	same("list_corrections pending", gotP, wantC)

	// The finding is now corrected: get_findings' default (open) no longer lists it, state corrected does.
	if open, _ := backend.Findings(ctx, "", "open", time.Time{}); len(open) != 0 {
		t.Errorf("open findings after the correction = %+v", open)
	}
	gotCorr, _ := rest.Findings(ctx, "", "corrected", time.Time{})
	wantCorr, _ := backend.Findings(ctx, "", "corrected", time.Time{})
	same("get_findings corrected", gotCorr, wantCorr)
	if len(wantCorr) != 1 || len(wantCorr[0].History) != 1 {
		t.Errorf("corrected findings = %+v", wantCorr)
	}

	// Limits over REST: a foreign tenant is refused with 403.
	if _, err := rest.RegisterCorrection(ctx, mcptools.CorrectionInput{Tenant: "globex", Kind: "rca", What: "x"}); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("foreign tenant over REST = %v, want a 403", err)
	}
}

func TestMCPToolsListIncludesTheFindingTools(t *testing.T) {
	f := newFindingsFixture(t, findingsConfig)
	names := toolNames(mcptools.Tools(newMCPBackend(f.sessions, f.archive, f.e, nil), MCPToolOptions()))
	for _, want := range []string{"get_findings", "register_correction", "list_corrections"} {
		if !slices.Contains(names, want) {
			t.Errorf("serve lacks %s", want)
		}
	}
}

// insertScoreOnly stores a score for a session that has no scoring record, as pruning leaves it.
func insertScoreOnly(s *SQLiteStore, sessionID string, at time.Time, tenant, kind, metric string, score float64) error {
	_, err := s.db.Exec(`INSERT INTO online_scores (session_id, at_ms, tenant, kind, version, model, metric, score, status)
		VALUES (?, ?, ?, ?, 'v1', 'm', ?, ?, 'failed')`, sessionID, at.UnixMilli(), tenant, kind, metric, score)
	return err
}

// The tools over /mcp: get_findings lists open work by default, register_correction takes the tool's snake_case
// arguments, and list_corrections shows the verification window.
func TestMCPFindingToolsOverTheEndpoint(t *testing.T) {
	f := newFindingsFixture(t, findingsConfig)
	f.seedBad("acme", 9)
	f.e.findingsMaintain(context.Background(), f.now)
	h := mcpRoute("", nil, nil, "", nil, nil, newMCPBackend(f.sessions, f.archive, f.e, nil))

	code, res := callTool(t, h, "", "get_findings", map[string]any{"kind": "rca"})
	var fs []online.Finding
	if err := json.Unmarshal(res.StructuredContent, &fs); err != nil || code != 200 || res.IsError || len(fs) != 1 || fs[0].State != "open" {
		t.Fatalf("get_findings = %d %+v %v", code, res, err)
	}
	_, res = callTool(t, h, "", "register_correction", map[string]any{"finding_id": fs[0].ID, "kind": "rca", "what": "added a rule", "version": "v2", "by": "ops-agent"})
	var c online.Correction
	if err := json.Unmarshal(res.StructuredContent, &c); err != nil || res.IsError || c.State != "pending" || c.FindingID != fs[0].ID || c.Verification.WindowEnd.IsZero() {
		t.Fatalf("register_correction = %+v %v", res, err)
	}
	_, res = callTool(t, h, "", "get_findings", nil)
	if err := json.Unmarshal(res.StructuredContent, &fs); err != nil || len(fs) != 0 {
		t.Errorf("the corrected finding is still listed as open: %+v", fs)
	}
	_, res = callTool(t, h, "", "get_findings", map[string]any{"state": "corrected", "since": "48h"})
	if err := json.Unmarshal(res.StructuredContent, &fs); err != nil || len(fs) != 1 {
		t.Errorf("corrected findings = %+v %v", fs, err)
	}
	_, res = callTool(t, h, "", "list_corrections", map[string]any{"state": "pending"})
	var cs []online.Correction
	if err := json.Unmarshal(res.StructuredContent, &cs); err != nil || len(cs) != 1 || cs[0].ID != c.ID {
		t.Errorf("list_corrections = %+v %v", cs, err)
	}
	if _, res = callTool(t, h, "", "get_findings", map[string]any{"since": "yesterday-ish"}); !res.IsError {
		t.Error("an unreadable since was accepted")
	}
	if _, res = callTool(t, h, "", "register_correction", map[string]any{"kind": "rca"}); !res.IsError {
		t.Error("register_correction without what was accepted")
	}
}
