package api

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/triageagent-dev/ego/internal/adk"
	"github.com/triageagent-dev/ego/internal/judge"
	"github.com/triageagent-dev/ego/internal/logx"
	"github.com/triageagent-dev/ego/internal/mcptools"
	"github.com/triageagent-dev/ego/internal/online"
)

// The findings pass and the verification of corrections (docs/rsi.md, sections 2 and 3). The decisions live in
// internal/online/findings.go and corrections.go; this file reads the stores, makes the one judge call, and sends
// the notifications.

const (
	findingsSweepKey = "findings_sweep_ms"
	// findingsSpendKey remembers today's findings tokens, which online_evals (what the budget is restored from)
	// does not hold.
	findingsSpendKey = "findings_spent"
	// findingsBudgetName keys the findings call's own running cost estimate in the budget meter.
	findingsBudgetName = "~findings"
	findingTimeout     = 2 * time.Minute
	// sweepNowTimeout bounds a manual sweep, which runs inside the request.
	sweepNowTimeout = 5 * time.Minute
	maxListed       = 500
	maxVerdictLines = 10
)

// findingsStatusDTO is the `findings` part of GET /api/online/status.
type findingsStatusDTO struct {
	Enabled bool  `json:"enabled"`
	Written int64 `json:"written"`
	Skipped struct {
		Budget     int64 `json:"budget"`
		NoEvidence int64 `json:"noEvidence"`
		// Uncalibrated counts series left alone because people disagree with their metric's judge too often.
		Uncalibrated int64 `json:"uncalibrated"`
	} `json:"skipped"`
	Tokens int64 `json:"tokens"`
}

func (e *OnlineEngine) findingsStatus() findingsStatusDTO {
	var st findingsStatusDTO
	st.Enabled = e.conf().Findings.Enabled
	st.Written = e.fWritten.Load()
	st.Skipped.Budget = e.fSkippedBudget.Load()
	st.Skipped.NoEvidence = e.fSkippedEvidence.Load()
	st.Skipped.Uncalibrated = e.fSkippedUncalibrated.Load()
	st.Tokens = e.fTokensTotal.Load()
	return st
}

// restoreFindingsSpend returns today's findings tokens saved before a restart, so the daily cap still counts them.
func (e *OnlineEngine) restoreFindingsSpend(now time.Time) int64 {
	v, err := e.store.OnlineState(findingsSpendKey)
	if err != nil || v == "" {
		return 0
	}
	day, n, ok := strings.Cut(v, ":")
	tokens, perr := strconv.ParseInt(n, 10, 64)
	if !ok || perr != nil || day != now.UTC().Format("2006-01-02") {
		return 0
	}
	e.mu.Lock()
	e.fSpendDay, e.fSpendToday = day, tokens
	e.mu.Unlock()
	return tokens
}

func (e *OnlineEngine) recordFindingsSpend(now time.Time, tokens int64) {
	if tokens <= 0 {
		return
	}
	e.fTokensTotal.Add(tokens)
	e.persistSpend(now, tokens)
}

// persistSpend adds judge tokens that online_evals does not hold (findings, anchor re-scoring) to today's total,
// which survives a restart.
func (e *OnlineEngine) persistSpend(now time.Time, tokens int64) {
	if tokens <= 0 {
		return
	}
	day := now.UTC().Format("2006-01-02")
	e.mu.Lock()
	if e.fSpendDay != day {
		e.fSpendDay, e.fSpendToday = day, 0
	}
	e.fSpendToday += tokens
	v := day + ":" + strconv.FormatInt(e.fSpendToday, 10)
	e.mu.Unlock()
	if err := e.store.SetOnlineState(findingsSpendKey, v); err != nil {
		logx.Warnf("online: saving findings spend: %v", err)
	}
}

// findingsMaintain runs on every detector tick, after the detector: it verifies pending corrections, resolves
// findings whose alert resolved, writes or refreshes the findings of open alerts, and sweeps the series that were
// never bad enough to alert. With findings disabled only the verification runs.
func (e *OnlineEngine) findingsMaintain(ctx context.Context, now time.Time) {
	e.verifyCorrections(ctx, now)
	if !e.conf().Findings.Enabled {
		return
	}
	e.resolveFindingsOfResolvedAlerts(ctx, now)
	e.alertFindings(ctx, now)
	e.sweepFindings(ctx, now)
}

// sessionDigest reads a session's first user message and its tool-call names from its trace; both are empty when
// the trace is gone.
func (e *OnlineEngine) sessionDigest(sessionID string) (firstUser string, tools []string) {
	tr, ok := e.sessions.Trace(sessionID)
	if !ok {
		return "", nil
	}
	for _, inv := range adk.ConvertTrace(tr).Invocations {
		if firstUser == "" {
			firstUser = strings.TrimSpace(inv.UserText())
		}
		for _, tc := range inv.ToolCalls() {
			tools = append(tools, tc.Name)
		}
	}
	return firstUser, tools
}

// failingVerdicts states, one line each, what the judge found wrong in a metric's evidence.
func failingVerdicts(m mcptools.Evidence) []string {
	var out []string
	for _, s := range m.Sentences {
		if failingSentenceLabels[s.Label] {
			out = append(out, fmt.Sprintf("%s sentence %q: %s", s.Label, s.Sentence, s.Rationale))
		}
	}
	for _, r := range m.Rubrics {
		if r.Verdict == "no" {
			out = append(out, fmt.Sprintf("rubric not met %q: %s", r.Rubric, r.Rationale))
		}
	}
	for _, c := range m.Comparisons {
		if !c.Matched {
			out = append(out, fmt.Sprintf("tool calls differ: expected %s, got %s", callNames(c.Expected), callNames(c.Actual)))
		}
	}
	for _, d := range m.Decisions {
		if d.Score != nil && *d.Score < 1 {
			out = append(out, fmt.Sprintf("decision %s on %q", d.Answer, d.Question))
		}
	}
	if len(out) > maxVerdictLines {
		out = out[:maxVerdictLines]
	}
	return out
}

// evidenceFor assembles what the model is shown about each chosen session. A session whose scoring record is gone
// is left out.
func (e *OnlineEngine) evidenceFor(key online.SeriesKey, worst []online.Sample) []online.EvidenceSession {
	var out []online.EvidenceSession
	for _, s := range worst {
		ev, ok, err := e.sessionEvidence(s.SessionID)
		if err != nil {
			logx.Warnf("online: finding evidence for %s: %v", s.SessionID, err)
			continue
		}
		if !ok {
			continue
		}
		es := online.EvidenceSession{SessionID: s.SessionID, Score: s.Score, Human: s.Human, Note: s.Note}
		es.FirstUser, es.Tools = e.sessionDigest(s.SessionID)
		for _, m := range ev.Metrics {
			if m.Metric == key.Metric {
				es.Rationale = m.Rationale
				es.Failing = failingVerdicts(m)
			}
		}
		out = append(out, es)
	}
	return out
}

// findingsModel is the judge model for diagnoses. It has its own usage hook: the scoring worker's model reports to
// a counter that process() resets, and the two run on different goroutines.
func (e *OnlineEngine) findingsModel() (judge.Model, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.fjudge != nil {
		return e.fjudge, nil
	}
	m, err := e.newJudge(context.Background(), judge.DefaultModel, func(n int64) { e.findingsTokens.Add(n) })
	if err != nil {
		return nil, err
	}
	e.fjudge = m
	return m, nil
}

type diagnosisResult struct {
	d          online.Diagnosis
	model      string
	modelError string
	tokens     int64
}

// diagnose makes the one judge call. skipBudget is true when the daily cap leaves no room for it. Without a judge
// model (none can be built, or the budget has no judge allowance at all) the diagnosis comes from the evidence
// alone and the model is empty; when the model fails or answers unreadably the same fallback is written and the
// model's name stays, with the reason in modelError.
func (e *OnlineEngine) diagnose(ctx context.Context, now time.Time, in online.PromptInput) (res diagnosisResult, skipBudget bool) {
	fallback := func() { res.d = online.FallbackDiagnosis(in.Stats, in.MinScore, in.Evidence) }
	if e.conf().Budget.JudgeTokensPerDay <= 0 {
		fallback()
		return res, false
	}
	model, err := e.findingsModel()
	if err != nil {
		logx.Warnf("online: findings: no judge model, writing from the evidence alone: %v", err)
		fallback()
		return res, false
	}
	reserved, ok := e.budget.ReserveJudge(findingsBudgetName, now)
	if !ok {
		return res, true
	}
	res.model = judge.DefaultModel
	before := e.findingsTokens.Load()
	callCtx, cancel := context.WithTimeout(ctx, findingTimeout)
	defer cancel()
	reply, err := model.Generate(callCtx, online.BuildPrompt(in))
	res.tokens = e.findingsTokens.Load() - before
	if err != nil {
		e.budget.SettleJudge(findingsBudgetName, reserved, &res.tokens, now)
		e.recordFindingsSpend(now, res.tokens)
		res.modelError = "model call failed: " + err.Error()
		fallback()
		return res, false
	}
	e.budget.SettleJudge(findingsBudgetName, reserved, &res.tokens, now)
	e.recordFindingsSpend(now, res.tokens)
	known := make([]string, len(in.Evidence))
	for i, ev := range in.Evidence {
		known[i] = ev.SessionID
	}
	d, perr := online.ParseDiagnosis(reply, known, len(known))
	if perr != nil {
		res.modelError = "unusable answer: " + perr.Error()
		fallback()
		return res, false
	}
	res.d = d
	return res, false
}

// writeFinding diagnoses a series and creates its finding, or refreshes existing with the new diagnosis. alertID
// names the alert it answers ("" for a sweep finding). Nothing is written when the series has no evidence left or
// the budget is spent; both are counted.
func (e *OnlineEngine) writeFinding(ctx context.Context, now time.Time, key online.SeriesKey, alertID string, existing *online.Finding) {
	cfg := e.conf().Findings
	// A judge that people keep disagreeing with must not drive corrections (docs/rsi.md, section 4).
	if e.uncalibrated(key.Metric, key.Tenant, now) {
		e.fSkippedUncalibrated.Add(1)
		return
	}
	from := now.Add(-cfg.Window.D())
	samples, err := e.store.OnlineSamplesFor(key.Tenant, key.Kind, key.Metric, "", from.UnixMilli(), now.UnixMilli()+1)
	if err != nil {
		logx.Errorf("online: finding samples: %v", err)
		return
	}
	stats := online.StatsOf(key, samples, cfg.MinScore)
	evidence := e.evidenceFor(key, online.LowestSessions(samples, cfg.MaxSessions, cfg.MinScore))
	if len(evidence) == 0 {
		e.fSkippedEvidence.Add(1)
		return
	}
	res, skip := e.diagnose(ctx, now, online.PromptInput{Key: key, Window: cfg.Window.D(), MinScore: cfg.MinScore, Stats: stats, Evidence: evidence})
	if skip {
		e.fSkippedBudget.Add(1)
		return
	}
	sessions := make([]string, len(evidence))
	for i, ev := range evidence {
		sessions[i] = ev.SessionID
	}
	apply := func(f *online.Finding) {
		f.Diagnosis = res.d
		f.Sessions = sessions
		f.Window = online.FindingWindow{From: from.UTC(), To: now.UTC(), N: stats.Sessions, Mean: stats.Mean, Below: stats.Below}
		f.Model, f.ModelError = res.model, res.modelError
		f.Confirmed = online.Confirmed(evidence)
		f.Tokens += res.tokens
		f.UpdatedAt = now.UTC()
		if alertID != "" {
			f.AlertID = alertID
		}
	}

	e.fmu.Lock()
	var f *online.Finding
	event := online.EventOpen
	if existing != nil {
		// Re-read: a correction may have been registered while the model was thinking.
		fresh, ok, err := e.store.OnlineFinding(existing.ID)
		if err != nil || !ok {
			e.fmu.Unlock()
			return
		}
		f, event = fresh, online.EventUpdated
	} else {
		f = &online.Finding{
			ID: newRandomID("finding"), Tenant: key.Tenant, Kind: key.Kind, Metric: key.Metric,
			State: online.FindingOpen, CreatedAt: now.UTC(),
		}
		f.History = []online.CorrectionNote{}
	}
	apply(f)
	err = e.store.UpsertOnlineFinding(f)
	e.fmu.Unlock()
	if err != nil {
		logx.Errorf("online: storing finding: %v", err)
		return
	}
	e.fWritten.Add(1)
	logx.Infof("online: finding %s %s %s/%s/%s: %s", f.ID, event, key.Tenant, key.Kind, key.Metric, f.Diagnosis.FailureMode)
	e.notifyFinding(ctx, event, f)
}

func (e *OnlineEngine) notifyFinding(ctx context.Context, event string, f *online.Finding) {
	if err := e.notifier.Finding(ctx, online.FindingPayloadOf(event, f, e.conf())); err != nil {
		logx.Warnf("online: notifying finding %s: %v", f.ID, err)
	}
}

// moveFinding applies a trigger to a finding and notifies the new state. note, when set, replaces the entry of the
// same correction in the finding's history. A reopened finding is only reopened when no other correction of it is
// still pending.
func (e *OnlineEngine) moveFinding(ctx context.Context, now time.Time, id, trigger string, note *online.CorrectionNote) {
	e.fmu.Lock()
	f, ok, err := e.store.OnlineFinding(id)
	if err != nil || !ok {
		e.fmu.Unlock()
		return
	}
	changed := false
	if note != nil {
		for i := range f.History {
			if f.History[i].CorrectionID == note.CorrectionID {
				f.History[i] = *note
				changed = true
			}
		}
	}
	event := ""
	if next, ok := online.NextFindingState(f.State, trigger); ok && next != f.State {
		if trigger == online.TriggerFailed {
			for _, h := range f.History {
				if h.State == online.CorrectionPending {
					next = f.State
				}
			}
		}
		if next != f.State {
			f.State = next
			changed = true
			switch next {
			case online.FindingResolved:
				event = online.EventResolved
			case online.FindingStale:
				event = online.EventStale
			case online.FindingOpen:
				event = online.EventOpen
			}
		}
	}
	if changed {
		f.UpdatedAt = now.UTC()
		err = e.store.UpsertOnlineFinding(f)
	}
	e.fmu.Unlock()
	if err != nil {
		logx.Errorf("online: storing finding: %v", err)
		return
	}
	if event != "" {
		logx.Infof("online: finding %s %s", f.ID, event)
		e.notifyFinding(ctx, event, f)
	}
}

// resolveFindingsOfResolvedAlerts closes the open findings whose alert has resolved: the problem went away.
func (e *OnlineEngine) resolveFindingsOfResolvedAlerts(ctx context.Context, now time.Time) {
	active, err := e.store.OnlineActiveFindings()
	if err != nil {
		logx.Errorf("online: reading findings: %v", err)
		return
	}
	for _, f := range active {
		if f.AlertID == "" || f.State != online.FindingOpen {
			continue
		}
		if a, ok, err := e.store.OnlineAlert(f.AlertID); err == nil && ok && a.Status == "resolved" {
			e.moveFinding(ctx, now, f.ID, online.TriggerAlertResolved, nil)
		}
	}
}

// alertFindings writes the finding of each open alert that has none, and refreshes the finding of an alert that
// grew since. A sweep finding of the same series that has no alert yet is adopted instead of duplicated.
func (e *OnlineEngine) alertFindings(ctx context.Context, now time.Time) {
	e.mu.Lock()
	alerts := make([]*online.Alert, 0, len(e.alerts))
	for _, a := range e.alerts {
		alerts = append(alerts, a)
	}
	e.mu.Unlock()
	sort.Slice(alerts, func(i, j int) bool { return alerts[i].ID < alerts[j].ID })

	active, err := e.store.OnlineActiveFindings()
	if err != nil {
		logx.Errorf("online: reading findings: %v", err)
		return
	}
	for _, a := range alerts {
		f, found, err := e.store.OnlineFindingByAlert(a.ID)
		if err != nil {
			logx.Errorf("online: reading finding of alert %s: %v", a.ID, err)
			continue
		}
		switch {
		case found && f.State == online.FindingOpen && a.UpdatedAt.After(f.UpdatedAt):
			e.writeFinding(ctx, now, a.Key, a.ID, f)
		case found:
		default:
			var adopt *online.Finding
			for _, af := range active {
				if af.Key() == a.Key && af.AlertID == "" {
					adopt = af
				}
			}
			switch {
			case adopt == nil:
				e.writeFinding(ctx, now, a.Key, a.ID, nil)
			case adopt.State == online.FindingOpen:
				e.writeFinding(ctx, now, a.Key, a.ID, adopt)
			default:
				// Already corrected: only record which alert it answers.
				e.fmu.Lock()
				if fresh, ok, err := e.store.OnlineFinding(adopt.ID); err == nil && ok {
					fresh.AlertID = a.ID
					_ = e.store.UpsertOnlineFinding(fresh)
				}
				e.fmu.Unlock()
			}
		}
	}
}

// sweepFindings writes a finding for every series with enough sessions and a low mean that has none, and marks
// stale the open findings (no correction) whose series stayed healthy for the window. It runs every
// findings.every, remembered across restarts.
func (e *OnlineEngine) sweepFindings(ctx context.Context, now time.Time) {
	if v, err := e.store.OnlineState(findingsSweepKey); err == nil && v != "" {
		if ms, perr := strconv.ParseInt(v, 10, 64); perr == nil && now.Sub(time.UnixMilli(ms)) < e.conf().Findings.Every.D() {
			return
		}
	}
	// A sweep already running (a manual one) is left to finish; the ticker has nothing to report.
	e.runSweep(ctx, now)
}

// SweepNow runs one sweep at once, whatever the cadence, and reports what it did. It lets an admin see the loop
// act on today's sessions instead of waiting for findings.every; it spends judge budget like a scheduled sweep and
// restarts the cadence from now.
func (e *OnlineEngine) SweepNow(ctx context.Context, info RoleInfo) (*mcptools.SweepReport, error) {
	if e == nil {
		return nil, errOnlineUnavailable
	}
	if !info.IsAdmin {
		return nil, &statusError{403, "admin role required"}
	}
	if !e.conf().Findings.Enabled {
		return nil, &statusError{409, "findings are disabled in the online config"}
	}
	// Bounded, and tied to the request so a caller that went away stops the model calls.
	ctx, cancel := context.WithTimeout(ctx, sweepNowTimeout)
	defer cancel()
	rep, ran := e.runSweep(ctx, e.now())
	if !ran {
		return nil, &statusError{http.StatusConflict, "a sweep is already running"}
	}
	return &rep, nil
}

func (e *OnlineEngine) sweepCounters() [4]int64 {
	return [4]int64{e.fWritten.Load(), e.fSkippedBudget.Load(), e.fSkippedEvidence.Load(), e.fSkippedUncalibrated.Load()}
}

// runSweep is one sweep: it remembers its time, marks stale the findings whose series recovered, and writes a
// finding for every candidate series. The report is the difference it made to the counters; ran is false when
// another sweep was already running, so a manual caller can say so instead of reporting zeros.
func (e *OnlineEngine) runSweep(ctx context.Context, now time.Time) (rep mcptools.SweepReport, ran bool) {
	// One sweep at a time: a manual sweep that overlaps the ticker's would see the same candidates before either
	// wrote a finding, and diagnose each series twice.
	if !e.sweeping.CompareAndSwap(false, true) {
		return rep, false
	}
	ran = true
	defer e.sweeping.Store(false)
	cfg := e.conf().Findings
	before := e.sweepCounters()
	defer func() {
		after := e.sweepCounters()
		rep.Written = int(after[0] - before[0])
		rep.SkippedBudget = int(after[1] - before[1])
		rep.SkippedNoEvidence = int(after[2] - before[2])
		rep.SkippedUncalibrated = int(after[3] - before[3])
	}()
	if err := e.store.SetOnlineState(findingsSweepKey, strconv.FormatInt(now.UnixMilli(), 10)); err != nil {
		logx.Warnf("online: saving findings sweep time: %v", err)
	}

	rows, err := e.store.OnlineSamples(now.Add(-cfg.Window.D()).UnixMilli())
	if err != nil {
		logx.Errorf("online: finding sweep samples: %v", err)
		return
	}
	series := map[online.SeriesKey][]online.Sample{}
	for _, r := range rows {
		series[r.Key] = append(series[r.Key], r.Sample)
	}
	active, err := e.store.OnlineActiveFindings()
	if err != nil {
		logx.Errorf("online: reading findings: %v", err)
		return
	}
	has := map[online.SeriesKey]bool{}
	for _, f := range active {
		has[f.Key()] = true
	}

	// Stale first: a finding that goes stale frees its series for the candidates below, which only matter if it
	// went bad again.
	e.mu.Lock()
	openAlert := map[string]bool{}
	for _, a := range e.alerts {
		openAlert[a.ID] = true
	}
	e.mu.Unlock()
	for _, f := range active {
		if f.State != online.FindingOpen || len(f.History) > 0 || (f.AlertID != "" && openAlert[f.AlertID]) {
			continue
		}
		if online.Healthy(online.StatsOf(f.Key(), series[f.Key()], cfg.MinScore), cfg) {
			e.moveFinding(ctx, now, f.ID, online.TriggerHealthy, nil)
		}
	}

	candidates := online.SweepCandidates(series, cfg, func(k online.SeriesKey) bool { return has[k] })
	rep.Candidates = len(candidates)
	for _, c := range candidates {
		if ctx.Err() != nil {
			break
		}
		alertID := ""
		e.mu.Lock()
		if a := e.alerts[c.Key]; a != nil {
			alertID = a.ID
		}
		e.mu.Unlock()
		e.writeFinding(ctx, now, c.Key, alertID, nil)
	}
	return rep, true
}

// RegisterCorrection records a change made in answer to a finding, marks the finding corrected, starts the change
// burst that fills its verification window, and returns the correction.
func (e *OnlineEngine) RegisterCorrection(info RoleInfo, in mcptools.CorrectionInput) (*online.Correction, error) {
	if e == nil {
		return nil, errOnlineUnavailable
	}
	if info.Scoped() {
		return nil, errAgentScopedOnline
	}
	if info.Role != RoleAdmin && info.Role != RoleMember {
		return nil, &statusError{403, "this role cannot register corrections"}
	}
	in.What, in.Kind, in.Tenant = strings.TrimSpace(in.What), strings.TrimSpace(in.Kind), strings.TrimSpace(in.Tenant)
	in.Version, in.By, in.FindingID = strings.TrimSpace(in.Version), strings.TrimSpace(in.By), strings.TrimSpace(in.FindingID)
	switch {
	case in.What == "":
		return nil, &statusError{400, "what is required: say what was changed"}
	case len([]rune(in.What)) > online.MaxWhatChars:
		return nil, &statusError{400, fmt.Sprintf("what is at most %d characters", online.MaxWhatChars)}
	case len([]rune(in.By)) > online.MaxByChars || len([]rune(in.Version)) > online.MaxVersion:
		return nil, &statusError{400, "by and version are short labels"}
	case len([]rune(in.Kind)) > online.MaxKindChars || len([]rune(in.Tenant)) > online.MaxTenantChars:
		return nil, &statusError{400, "kind and tenant are short labels"}
	}

	var finding *online.Finding
	if in.FindingID != "" {
		f, ok, err := e.store.OnlineFinding(in.FindingID)
		if err != nil {
			return nil, err
		}
		if !ok || !info.AllowsAnyTenant([]string{f.Tenant}) {
			return nil, notFound("finding")
		}
		if in.Tenant != "" && in.Tenant != f.Tenant || in.Kind != "" && in.Kind != f.Kind {
			return nil, &statusError{400, "tenant and kind must match the finding"}
		}
		if !online.Active(f.State) {
			return nil, &statusError{409, "the finding is " + f.State + " and takes no more corrections"}
		}
		finding = f
		in.Tenant, in.Kind = f.Tenant, f.Kind
	}
	if in.Kind == "" {
		return nil, &statusError{400, "kind is required"}
	}
	if in.Tenant == "" {
		if t, ok := singleTenant(info); ok {
			in.Tenant = t
		} else {
			return nil, &statusError{400, "tenant is required: name it, or pass a finding"}
		}
	}
	if !info.AllowsAnyTenant([]string{in.Tenant}) {
		return nil, &statusError{403, "this role cannot register corrections for tenant " + in.Tenant}
	}

	now := e.now()
	c := &online.Correction{
		ID: newRandomID("corr"), Tenant: in.Tenant, Kind: in.Kind, Version: in.Version, State: online.CorrectionPending,
		CreatedAt:      now.UTC(),
		CorrectionData: online.CorrectionData{What: in.What, By: in.By},
	}
	if finding != nil {
		c.FindingID, c.Metric = finding.ID, finding.Metric
		c.Verification.Before = online.VWindow{Mean: finding.Window.Mean, N: finding.Window.N}
	}
	if c.Verification.Before.N == 0 {
		// No finding to read the "before" from: the detector's baseline span before now.
		base, err := e.store.OnlineSamplesFor(c.Tenant, c.Kind, c.Metric, "", now.Add(-e.conf().Detector.Baseline.D()).UnixMilli(), now.UnixMilli()+1)
		if err != nil {
			return nil, err
		}
		c.Verification.Before = online.MeanOf(base)
	}
	c.Verification.WindowEnd = now.Add(e.conf().Findings.VerifyWithin.D()).UTC()

	e.fmu.Lock()
	// Counted under fmu with the insert, so two callers cannot both take the last slot.
	if n, err := e.store.CountOnlineCorrections(c.Tenant, c.Kind, online.CorrectionPending); err != nil {
		e.fmu.Unlock()
		return nil, err
	} else if n >= online.MaxPendingPerSeries {
		e.fmu.Unlock()
		return nil, &statusError{http.StatusTooManyRequests, fmt.Sprintf("%s/%s already has %d corrections waiting for verification; wait for them to be decided", c.Tenant, c.Kind, online.MaxPendingPerSeries)}
	}
	if err := e.store.UpsertOnlineCorrection(c); err != nil {
		e.fmu.Unlock()
		return nil, err
	}
	if finding != nil {
		if f, ok, err := e.store.OnlineFinding(finding.ID); err == nil && ok {
			if next, ok := online.NextFindingState(f.State, online.TriggerCorrect); ok {
				f.State = next
				f.History = append(f.History, c.Note())
				f.UpdatedAt = now.UTC()
				if err := e.store.UpsertOnlineFinding(f); err != nil {
					logx.Errorf("online: storing finding: %v", err)
				}
			}
		}
	}
	e.fmu.Unlock()

	// A correction is a change to the agent, so its next sessions are scored first, like a new version's.
	e.sampler.StartBurst(c.Tenant, c.Kind)
	logx.Infof("online: correction %s registered for %s/%s (finding %s, version %q)", c.ID, c.Tenant, c.Kind, c.FindingID, c.Version)
	return c, nil
}

// singleTenant returns the tenant a role is limited to when that is exactly one concrete tenant key. A grant such
// as "<sender>/*" names many, so nothing is implied.
func singleTenant(info RoleInfo) (string, bool) {
	if len(info.Tenants) != 1 {
		return "", false
	}
	t := info.Tenants[0]
	if t == "" || strings.HasSuffix(t, "*") {
		return "", false
	}
	return t, true
}

// verifyCorrections decides every pending correction with the scores since it was registered.
func (e *OnlineEngine) verifyCorrections(ctx context.Context, now time.Time) {
	// Every pending correction, not the newest page: a flood from one tenant must not starve the others. The
	// per-(tenant, kind) quota keeps the set small.
	pending, err := e.store.OnlineCorrections(online.CorrectionPending, "", 0)
	if err != nil {
		logx.Errorf("online: reading corrections: %v", err)
		return
	}
	// Oldest first, so a finding with two corrections hears about them in the order they were made.
	sort.Slice(pending, func(i, j int) bool { return pending[i].CreatedAt.Before(pending[j].CreatedAt) })
	for _, c := range pending {
		e.verifyOne(ctx, now, c)
	}
}

func (e *OnlineEngine) verifyOne(ctx context.Context, now time.Time, c *online.Correction) {
	alertResolved := false
	if c.FindingID != "" {
		if f, ok, err := e.store.OnlineFinding(c.FindingID); err == nil && ok && f.AlertID != "" {
			if a, ok, err := e.store.OnlineAlert(f.AlertID); err == nil && ok && a.Status == "resolved" {
				alertResolved = true
			}
		}
	}
	// The scores of the correction's series since it was made, and only at its version when it names one; an empty
	// metric (a correction with no finding) pools every metric of the kind.
	after, err := e.store.OnlineSamplesFor(c.Tenant, c.Kind, c.Metric, c.Version, c.CreatedAt.UnixMilli(), now.UnixMilli()+1)
	if err != nil {
		logx.Errorf("online: verifying %s: %v", c.ID, err)
		return
	}
	state, v := online.Verify(online.VerifyInput{
		Before: c.Verification.Before, After: after, AlertResolved: alertResolved, Created: c.CreatedAt, Now: now,
	}, e.conf().Detector, e.conf().Findings)

	e.fmu.Lock()
	fresh, ok, err := e.store.OnlineCorrection(c.ID)
	if err != nil || !ok || fresh.State != online.CorrectionPending {
		e.fmu.Unlock()
		return
	}
	if state == online.CorrectionPending && fresh.Verification.After == v.After && fresh.Verification.Reason == v.Reason {
		e.fmu.Unlock()
		return
	}
	fresh.Verification, fresh.State = v, state
	if state != online.CorrectionPending {
		t := now.UTC()
		fresh.VerifiedAt = &t
	}
	err = e.store.UpsertOnlineCorrection(fresh)
	e.fmu.Unlock()
	if err != nil {
		logx.Errorf("online: storing correction: %v", err)
		return
	}
	if state == online.CorrectionPending {
		return
	}
	logx.Infof("online: correction %s %s: %.2f -> %.2f (n=%d)", fresh.ID, state, v.Before.Mean, v.After.Mean, v.After.N)
	if fresh.FindingID != "" {
		note := fresh.Note()
		trigger := online.TriggerVerified
		if state == online.CorrectionFailed {
			trigger = online.TriggerFailed
		}
		e.moveFinding(ctx, now, fresh.FindingID, trigger, &note)
	}
	if err := e.notifier.Correction(ctx, online.CorrectionPayloadOf(state, fresh, e.conf())); err != nil {
		logx.Warnf("online: notifying correction %s: %v", fresh.ID, err)
	}
}

// validFindingState and validCorrectionState are the filters list calls accept; "" and "all" mean every state.
func validFindingState(s string) bool {
	switch s {
	case "", "all", online.FindingOpen, online.FindingCorrected, online.FindingResolved, online.FindingStale:
		return true
	}
	return false
}

func validCorrectionState(s string) bool {
	switch s {
	case "", "all", online.CorrectionPending, online.CorrectionVerified, online.CorrectionFailed:
		return true
	}
	return false
}

func allStates(s string) string {
	if s == "all" {
		return ""
	}
	return s
}

// Findings lists the findings of the tenants info may see, last updated first.
func (e *OnlineEngine) Findings(info RoleInfo, kind, state string, since time.Time) ([]online.Finding, error) {
	if e == nil {
		return nil, errOnlineUnavailable
	}
	if info.Scoped() {
		return nil, errAgentScopedOnline
	}
	if !validFindingState(state) {
		return nil, &statusError{400, "state is open, corrected, resolved, stale or all"}
	}
	var sinceMs int64
	if !since.IsZero() {
		sinceMs = since.UnixMilli()
	}
	rows, err := e.store.OnlineFindings(allStates(state), kind, sinceMs, maxListed)
	if err != nil {
		return nil, err
	}
	out := []online.Finding{}
	for _, f := range filterByTenant(rows, info, func(f *online.Finding) string { return f.Tenant }) {
		out = append(out, *f)
	}
	return out, nil
}

// Finding returns one finding, for a caller who may see its tenant.
func (e *OnlineEngine) Finding(info RoleInfo, id string) (*online.Finding, error) {
	if e == nil {
		return nil, errOnlineUnavailable
	}
	if info.Scoped() {
		return nil, errAgentScopedOnline
	}
	f, ok, err := e.store.OnlineFinding(id)
	if err != nil {
		return nil, err
	}
	if !ok || !info.AllowsAnyTenant([]string{f.Tenant}) {
		return nil, notFound("finding")
	}
	return f, nil
}

// Corrections lists the corrections of the tenants info may see, newest first.
func (e *OnlineEngine) Corrections(info RoleInfo, kind, state string) ([]online.Correction, error) {
	if e == nil {
		return nil, errOnlineUnavailable
	}
	if info.Scoped() {
		return nil, errAgentScopedOnline
	}
	if !validCorrectionState(state) {
		return nil, &statusError{400, "state is pending, verified, failed or all"}
	}
	rows, err := e.store.OnlineCorrections(allStates(state), kind, maxListed)
	if err != nil {
		return nil, err
	}
	out := []online.Correction{}
	for _, c := range filterByTenant(rows, info, func(c *online.Correction) string { return c.Tenant }) {
		out = append(out, *c)
	}
	return out, nil
}
