package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/triageagent-dev/ego/internal/adk"
	"github.com/triageagent-dev/ego/internal/decision"
	"github.com/triageagent-dev/ego/internal/eval"
	"github.com/triageagent-dev/ego/internal/judge"
	"github.com/triageagent-dev/ego/internal/logx"
	"github.com/triageagent-dev/ego/internal/online"
	"github.com/triageagent-dev/ego/internal/vertexeval"
)

// Completed sessions are sampled, scored with their kind's profile, stored, folded into an hourly Runs
// run per (tenant, kind), and tested for regressions that are sent to the configured sinks. See
// docs/online-evaluation.md.

const (
	onlineQueueSize   = 256
	onlineRetention   = 90 * 24 * time.Hour
	onlineEvalTimeout = 5 * time.Minute
	onlineDigestKey   = "digest_day"
)

// OnlineEngine runs the loop. Create with NewOnlineEngine and Start.
type OnlineEngine struct {
	// cfgp is the live configuration; the jobs a person edits replace it (SetJobs), so read it through conf().
	cfgp atomic.Pointer[online.Config]
	// base is the configuration as the file wrote it; the jobs a person saved are laid over it.
	base       *online.Config
	jobsStored atomic.Bool
	// unscored counts the sessions of each kind that arrived with no job to score them, since the process started.
	unscored   map[string]int64
	store      *SQLiteStore
	sessions   *SessionStore
	tenantAttr string
	sampler    *online.Sampler
	budget     *online.BudgetMeter
	notifier   *online.Notifier
	queue      chan string
	now        func() time.Time

	// judgeTokens accumulates the current evaluation's judge tokens; the
	// worker is single, so one counter is enough.
	judgeTokens atomic.Int64

	mu     sync.Mutex
	counts map[string]int64
	alerts map[online.SeriesKey]*online.Alert
	runs   map[string]*onlineHourRun
	judges map[string]judge.Model
	// rjudges and recalTokens serve the anchor passes of onlinelabels.go: models of their own, so the scoring
	// worker's token counter is not shared across goroutines.
	rjudges        map[string]judge.Model
	recalTokens    atomic.Int64
	lSkippedBudget atomic.Int64
	jev            *decision.Client
	jevErr         error
	jevBuilt       bool
	// The findings pass (onlinefindings.go). fmu serialises read-modify-write of finding and correction rows between
	// the detector goroutine and the correction endpoint; it is never held across a model call or a notification.
	fmu              sync.Mutex
	fjudge           judge.Model
	findingsTokens   atomic.Int64 // the findings judge's usage hook adds here
	fWritten         atomic.Int64
	fSkippedBudget   atomic.Int64
	fSkippedEvidence atomic.Int64
	// fSkippedUncalibrated counts findings not written because the metric's judge is gated by the label agreement.
	fSkippedUncalibrated atomic.Int64
	// sweeping is set while a findings sweep runs, scheduled or manual.
	sweeping     atomic.Bool
	fTokensTotal atomic.Int64
	fSpendDay    string
	fSpendToday  int64
	// newJudge builds a judge model; tests replace it.
	newJudge func(ctx context.Context, model string, onUsage func(int64)) (judge.Model, error)
	lastTick time.Time
}

// conf is the configuration in force now.
func (e *OnlineEngine) conf() *online.Config { return e.cfgp.Load() }

type onlineHourRun struct {
	runID string
	hour  time.Time
}

// NewOnlineEngine wires the engine. store is required.
func NewOnlineEngine(cfg *online.Config, store *SQLiteStore, sessions *SessionStore, tenantAttr string) *OnlineEngine {
	e := &OnlineEngine{
		store:      store,
		sessions:   sessions,
		tenantAttr: tenantAttr,
		sampler:    online.NewSampler(cfg.Sampling),
		budget:     online.NewBudgetMeter(cfg.Budget),
		notifier:   &online.Notifier{Sinks: cfg.Sinks},
		queue:      make(chan string, onlineQueueSize),
		now:        time.Now,
		counts:     map[string]int64{},
		alerts:     map[online.SeriesKey]*online.Alert{},
		runs:       map[string]*onlineHourRun{},
		judges:     map[string]judge.Model{},
		rjudges:    map[string]judge.Model{},
	}
	e.cfgp.Store(cfg)
	e.base = cfg
	e.unscored = map[string]int64{}
	e.newJudge = func(ctx context.Context, model string, onUsage func(int64)) (judge.Model, error) {
		m, err := judge.NewGenAIModel(ctx, "", model)
		if err != nil {
			return nil, err
		}
		return m.WithUsageHook(onUsage), nil
	}
	return e
}

// Enqueue is the session-complete hook. It never blocks.
func (e *OnlineEngine) Enqueue(sessionID string) {
	select {
	case e.queue <- sessionID:
	default:
		e.count(online.SkipQueueFull)
	}
}

func (e *OnlineEngine) count(key string) {
	e.mu.Lock()
	e.counts[key]++
	e.mu.Unlock()
}

// Start restores state and runs the worker and the detector until stop.
func (e *OnlineEngine) Start(stop <-chan struct{}) {
	e.loadJobs()
	now := e.now()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	if spent, err := e.store.OnlineSpentSince(day.UnixMilli()); err != nil {
		logx.Warnf("online: restoring today's spend: %v", err)
	} else {
		spent.JudgeTokens += e.restoreFindingsSpend(now)
		e.budget.Restore(spent, now)
	}
	if open, err := e.store.OnlineAlerts("open", 1000); err != nil {
		logx.Warnf("online: loading open alerts: %v", err)
	} else {
		for _, a := range open {
			e.alerts[a.Key] = a
		}
	}
	e.finishStaleRuns(true)
	if os.Getenv("JEV_API_KEY") == "" {
		for _, p := range e.conf().Profiles {
			if p.UsesJev() {
				logx.Warnf("online: profile %s has jev_* evaluators but JEV_API_KEY is not set; they will report errors", p.Key())
			}
		}
	}
	logx.Infof("online evaluation: %d profile(s), %d sink(s), judge cap %d tokens/day, jev cap %d calls/day",
		len(e.conf().Profiles), len(e.conf().Sinks), e.conf().Budget.JudgeTokensPerDay, e.conf().Budget.JevCallsPerDay)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-stop
		cancel()
	}()
	go func() {
		for {
			select {
			case id := <-e.queue:
				e.process(ctx, id)
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		t := time.NewTicker(e.conf().Detector.Every.D())
		defer t.Stop()
		for {
			select {
			case <-t.C:
				e.Tick(ctx)
			case <-ctx.Done():
				return
			}
		}
	}()
}

// process samples and maybe scores one segment that has been closed for good (SessionStore.closeSegmentLocked is
// the only caller of the hook that queues it). The segment is scored once, after its final trace: the queue sees it
// once per close, a closed segment never reopens, and OnlineEvalExists below keeps a repeat from scoring it again.
// Spans of its own traces that arrive after the score do not change it.
func (e *OnlineEngine) process(ctx context.Context, sessionID string) {
	if done, err := e.store.OnlineEvalExists(sessionID); err != nil {
		logx.Errorf("online: %v", err)
		return
	} else if done {
		e.count(online.SkipAlreadySeen)
		return
	}
	tr, ok := e.sessions.Trace(sessionID)
	if !ok {
		return
	}
	f := online.FeaturesOf(sessionID, tr.AllSpans, e.tenantAttr)
	if f.Kind == "" {
		e.count(online.SkipNoKind)
		return
	}
	p, ok := e.conf().Match(f.Kind, f.Service, f.Tenant)
	if !ok {
		e.count(online.SkipNoProfile)
		e.countUnscored(f.Kind)
		return
	}
	// Before the sampler sees it, so a greeting neither uses up the kind's allowance nor shifts its baselines.
	if f.NotWorthJudging(e.conf().Sampling.Trivial) {
		e.count(online.SkipTrivial)
		return
	}
	// Profiles match on the label the sender wrote; scores, alerts and series are keyed with the sender too, so two
	// senders that both send a tenant called "acme" stay apart.
	f.Tenant = tenantKey(e.sessions.SenderOf(sessionID), f.Tenant)
	now := e.now()
	d := e.sampler.DecideAt(f, p.PerHour, now)
	if !d.Sample {
		e.count(d.Skip)
		return
	}
	reserved, ok := e.budget.Reserve(p, now)
	if !ok {
		e.count(online.SkipBudget)
		return
	}

	invocations := adk.ConvertTrace(tr).Invocations
	if len(invocations) == 0 {
		e.budget.Settle(p, reserved, nil, now)
		e.count(online.SkipNoInvocs)
		return
	}
	if len(invocations) > p.MaxInvocations {
		invocations = invocations[len(invocations)-p.MaxInvocations:]
	}

	evalCtx, cancel := context.WithTimeout(ctx, onlineEvalTimeout)
	defer cancel()
	// The part of the thread before this one, as context for the judge (segment n >= 2 only).
	getJudge := e.getJudge(evalCtx)
	if earlier, ok := e.sessions.EarlierPart(sessionID); ok {
		inner := getJudge
		getJudge = func(model string) (judge.Model, error) {
			m, err := inner(model)
			if err != nil {
				return nil, err
			}
			return judge.WithEarlierPart(m, earlier), nil
		}
	}
	e.judgeTokens.Store(0)
	evaluators := profileEvaluators(p)
	scoringStart := time.Now()
	result := evaluateInvocations(evalCtx, traceResultDTO{
		TraceID:            f.TraceID,
		NumInvocations:     len(invocations),
		MetricResults:      []metricResultDTO{},
		ConversionWarnings: []string{},
		AgentName:          f.Service,
		Model:              f.Model,
	}, invocations, evaluators, nil, getJudge, noVertexEval, e.getJev, nil)
	scoringMs := time.Since(scoringStart).Milliseconds()
	for i := range result.MetricResults {
		result.MetricResults[i] = unscoredAsError(result.MetricResults[i])
	}

	actual := online.Cost{JudgeTokens: e.judgeTokens.Load()}
	for _, m := range result.MetricResults {
		if decision.IsMetric(m.MetricName) || strings.HasPrefix(m.MetricName, "jev:") {
			if m.Error == nil {
				actual.JevCalls = int64(len(invocations))
			}
			break
		}
	}
	e.budget.Settle(p, reserved, &actual, now)

	lane := d.Lane
	for _, r := range d.Reasons {
		e.count("sampled_" + r)
	}
	runID := e.recordRun(f, p, result, now, scoringMs)
	scores := make([]onlineScoreRow, 0, len(result.MetricResults))
	for _, m := range result.MetricResults {
		scores = append(scores, onlineScoreRow{Metric: m.MetricName, Score: m.Score, Status: runStatusFromMetric(m)})
	}
	if err := e.store.InsertOnlineEval(onlineEvalRow{
		SessionID:   sessionID,
		TraceID:     f.TraceID,
		AtMs:        now.UnixMilli(),
		Tenant:      f.Tenant,
		Kind:        f.Kind,
		Service:     f.Service,
		Profile:     p.Key(),
		Lane:        lane,
		Reasons:     strings.Join(d.Reasons, ","),
		JudgeTokens: actual.JudgeTokens,
		JevCalls:    actual.JevCalls,
		RunID:       runID,
	}, f.Version, f.Model, scores); err != nil {
		logx.Errorf("online: storing scores for %s: %v", sessionID, err)
	}
}

func profileEvaluators(p online.Profile) []evaluatorConfigDTO {
	out := make([]evaluatorConfigDTO, 0, len(p.Evaluators))
	for _, ev := range p.Evaluators {
		out = append(out, evaluatorConfigDTO{
			Type:                ev.Type,
			Name:                ev.Name,
			JudgeModel:          ev.JudgeModel,
			Threshold:           ev.Threshold,
			TrajectoryMatchType: ev.TrajectoryMatchType,
			Rubrics:             ev.Rubrics,
			Questions:           ev.Questions,
			NumSamples:          ev.NumSamples,
		})
	}
	return out
}

// resolvedEvaluators is the profile's evaluators with what scoring settled on in place of what the profile left empty
// (the type, the threshold, the judge model, the trajectory match type), so a run shows the configuration it ran with.
// The model of a jev metric is known only from its results.
func resolvedEvaluators(p online.Profile, results []metricResultDTO) []evaluatorConfigDTO {
	out := profileEvaluators(p)
	for i := range out {
		ev := &out[i]
		if ev.Type == "" {
			ev.Type = "builtin"
		}
		if ev.Threshold == nil {
			t := defaultThreshold
			ev.Threshold = &t
		}
		switch {
		case decision.IsMetric(ev.Name):
			for _, r := range results {
				if r.JudgeModel != "" && decision.IsMetric(r.MetricName) {
					ev.JudgeModel = r.JudgeModel
					break
				}
			}
		case judgeMetrics[ev.Name]:
			if ev.JudgeModel == "" {
				ev.JudgeModel = judge.DefaultModel
			}
		case vertexEvalMetrics[ev.Name]:
			ev.JudgeModel = vertexEvalServiceLabel
		default:
			if ev.TrajectoryMatchType == "" && strings.Contains(ev.Name, "trajectory") {
				ev.TrajectoryMatchType = string(eval.MatchExact)
			}
		}
	}
	return out
}

func noVertexEval() (*vertexeval.Client, error) {
	return nil, errors.New("Vertex managed-eval metrics are not available to online evaluation")
}

// getJudge returns a judge getter whose models report tokens to the
// engine's counter. Models are cached across evaluations.
func (e *OnlineEngine) getJudge(ctx context.Context) func(string) (judge.Model, error) {
	return func(name string) (judge.Model, error) {
		if name == "" {
			name = judge.DefaultModel
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		if m, ok := e.judges[name]; ok {
			return m, nil
		}
		m, err := e.newJudge(context.Background(), name, func(n int64) { e.judgeTokens.Add(n) })
		if err != nil {
			return nil, fmt.Errorf("setting up judge model %s: %w", name, err)
		}
		e.judges[name] = m
		return m, nil
	}
}

func (e *OnlineEngine) getJev() (*decision.Client, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.jevBuilt {
		e.jev, e.jevErr = decision.NewClient("", "")
		e.jevBuilt = true
	}
	return e.jev, e.jevErr
}

// recordRun appends a session's results to its (tenant, kind) run for the
// current UTC hour, opening it (and finishing the previous hour's) as
// needed. It returns the run ID, "" on a storage error.
func (e *OnlineEngine) recordRun(f online.Features, p online.Profile, result traceResultDTO, now time.Time, scoringMs int64) string {
	hour := now.UTC().Truncate(time.Hour)
	key := f.Tenant + "\x00" + f.Kind
	e.mu.Lock()
	hr := e.runs[key]
	e.mu.Unlock()
	if hr != nil && !hr.hour.Equal(hour) {
		e.finishRun(hr.runID)
		hr = nil
	}
	if hr == nil {
		label := "online · " + f.Kind
		if f.Tenant != "" {
			label = "online · " + f.Tenant + " · " + f.Kind
		}
		cfg := map[string]any{"evaluators": resolvedEvaluators(p, result.MetricResults)}
		run, err := e.store.CreateRun(runSpecSummaryDTO{
			Approach:   "online",
			EvalSet:    map[string]any{"name": label},
			EvalConfig: cfg,
			Target: map[string]any{
				"kind": "online", "tenant": f.Tenant, "callKind": f.Kind, "profile": p.Key(),
				"hour": hour.Format(time.RFC3339),
			},
		})
		if err != nil {
			logx.Errorf("online: creating run: %v", err)
			return ""
		}
		hr = &onlineHourRun{runID: run.RunID, hour: hour}
		e.mu.Lock()
		e.runs[key] = hr
		e.mu.Unlock()
	}
	created := nowRFC3339()
	rows := make([]runResultRowDTO, 0, len(result.MetricResults))
	traceID := result.TraceID
	for _, m := range result.MetricResults {
		rows = append(rows, runResultRowDTO{
			ResultID:            newRandomID("res"),
			RunID:               hr.runID,
			EvalSetItemID:       f.SessionID,
			EvalSetItemName:     f.SessionID,
			EvaluatorName:       m.MetricName,
			EvaluatorType:       "builtin",
			Status:              runStatusFromMetric(m),
			Score:               m.Score,
			PerInvocationScores: m.PerInvocationScores,
			TraceID:             &traceID,
			Details:             withSource(m.Details, f.Service, f.Model, scoringMs),
			ErrorText:           m.Error,
			CreatedAt:           created,
		})
	}
	if err := e.store.InsertRunResults(hr.runID, rows); err != nil {
		logx.Errorf("online: writing run results: %v", err)
	}
	return hr.runID
}

// withSource records the session's service and model, and how long scoring it took, in a result's details, so the
// run summary can name its agents (for role scoping) and models and add up its duration.
func withSource(details map[string]any, service, model string, scoringMs int64) map[string]any {
	if service == "" && model == "" && scoringMs == 0 {
		return details
	}
	out := map[string]any{}
	if scoringMs > 0 {
		out["scoringMs"] = scoringMs
	}
	if service != "" {
		out["agent"] = service
	}
	if model != "" {
		out["model"] = model
	}
	for k, v := range details {
		out[k] = v
	}
	return out
}

// finishRun computes a run's summary from its results and closes it.
func (e *OnlineEngine) finishRun(runID string) {
	rows, err := e.store.GetRunResults(runID)
	if err != nil {
		logx.Errorf("online: reading run %s: %v", runID, err)
		return
	}
	summary := summarizeResultRows(rows)
	if err := e.store.FinishRun(runID, "succeeded", &summary, nil); err != nil {
		logx.Errorf("online: finishing run %s: %v", runID, err)
	}
}

// detailMillis reads a millisecond count from a result's details: a number from JSON, or the int64 it was written as.
func detailMillis(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	}
	return 0
}

// summarizeResultRows builds a run summary from stored result rows.
func summarizeResultRows(rows []runResultRowDTO) runSummaryDTO {
	summary := runSummaryDTO{PerMetric: map[string]perMetricSummaryDTO{}}
	sessions := map[string]bool{}
	agents := map[string]bool{}
	models := map[string]bool{}
	scoring := map[string]int64{}
	sums := map[string]float64{}
	scored := map[string]int{}
	for _, r := range rows {
		sessions[r.EvalSetItemID] = true
		if a, _ := r.Details["agent"].(string); a != "" {
			agents[a] = true
		}
		if m, _ := r.Details["model"].(string); m != "" {
			models[m] = true
		}
		if ms := detailMillis(r.Details["scoringMs"]); ms > 0 {
			scoring[r.EvalSetItemID] = ms
		}
		pm := summary.PerMetric[r.EvaluatorName]
		switch r.Status {
		case "passed":
			summary.ResultCounts.Passed++
			pm.Passed++
		case "failed":
			summary.ResultCounts.Failed++
			pm.Failed++
		case "errored":
			summary.ResultCounts.Errored++
			pm.Errored++
		}
		if r.Score != nil && r.Status != "errored" {
			sums[r.EvaluatorName] += *r.Score
			scored[r.EvaluatorName]++
		}
		summary.PerMetric[r.EvaluatorName] = pm
	}
	for name, n := range scored {
		pm := summary.PerMetric[name]
		avg := sums[name] / float64(n)
		pm.AvgScore = &avg
		summary.PerMetric[name] = pm
	}
	summary.TraceCount = len(sessions)
	for a := range agents {
		summary.Agents = append(summary.Agents, a)
	}
	sort.Strings(summary.Agents)
	summary.Models = sortedKeys(models)
	for _, ms := range scoring {
		summary.DurationMs += ms
	}
	return summary
}

// finishStaleRuns closes online runs whose hour is over. At startup (all)
// it also closes runs left open by a previous process.
func (e *OnlineEngine) finishStaleRuns(all bool) {
	hour := e.now().UTC().Truncate(time.Hour)
	if all {
		runs, err := e.store.ListRuns([]string{"running"}, 1000, "")
		if err != nil {
			logx.Errorf("online: listing open runs: %v", err)
			return
		}
		for _, r := range runs {
			if r.Spec.Approach == "online" {
				e.finishRun(r.RunID)
			}
		}
		return
	}
	e.mu.Lock()
	var stale []string
	for key, hr := range e.runs {
		if !hr.hour.Equal(hour) {
			stale = append(stale, hr.runID)
			delete(e.runs, key)
		}
	}
	e.mu.Unlock()
	for _, id := range stale {
		e.finishRun(id)
	}
}

// Tick closes finished hourly runs, runs the regression detector, sends
// alert events, the daily digest, and prunes old rows once a day.
func (e *OnlineEngine) Tick(ctx context.Context) {
	now := e.now()
	e.finishStaleRuns(false)
	e.detect(ctx, now)
	e.findingsMaintain(ctx, now)
	e.recalibrate(ctx, now)
	e.digest(ctx, now)
	if now.Sub(e.lastTick) > 24*time.Hour {
		if !e.lastTick.IsZero() {
			if err := e.store.PruneOnline(now.Add(-onlineRetention)); err != nil {
				logx.Warnf("online: prune: %v", err)
			}
		}
		e.lastTick = now
	}
}

func (e *OnlineEngine) detect(ctx context.Context, now time.Time) {
	cfg := e.conf().Detector
	rows, err := e.store.OnlineSamples(now.Add(-cfg.Baseline.D()).UnixMilli())
	if err != nil {
		logx.Errorf("online: reading samples: %v", err)
		return
	}
	split := now.Add(-cfg.Window.D())
	type sides struct{ base, cur []online.Sample }
	series := map[online.SeriesKey]*sides{}
	for _, r := range rows {
		s := series[r.Key]
		if s == nil {
			s = &sides{}
			series[r.Key] = s
		}
		if r.Sample.At.Before(split) {
			s.base = append(s.base, r.Sample)
		} else {
			s.cur = append(s.cur, r.Sample)
		}
	}
	e.mu.Lock()
	for key := range e.alerts {
		if series[key] == nil {
			series[key] = &sides{}
		}
	}
	e.mu.Unlock()

	keys := make([]online.SeriesKey, 0, len(series))
	for k := range series {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[i].Tenant+keys[i].Kind+keys[i].Metric < keys[j].Tenant+keys[j].Kind+keys[j].Metric
	})
	for _, key := range keys {
		s := series[key]
		r := online.Test(s.base, s.cur, cfg)
		e.mu.Lock()
		prev := e.alerts[key]
		e.mu.Unlock()
		next, event := online.Step(prev, key, r, cfg, now, func() string { return newRandomID("alert") })
		if next == nil {
			continue
		}
		if next.Status == "open" {
			e.mu.Lock()
			e.alerts[key] = next
			e.mu.Unlock()
		} else {
			e.mu.Lock()
			delete(e.alerts, key)
			e.mu.Unlock()
		}
		if err := e.store.UpsertOnlineAlert(next); err != nil {
			logx.Errorf("online: storing alert: %v", err)
		}
		if event != "" {
			logx.Infof("online: alert %s %s %s/%s/%s %.2f -> %.2f (n=%d)", next.ID, event,
				key.Tenant, key.Kind, key.Metric, r.BaseMean, r.CurMean, r.NCur)
			if err := e.notifier.Alert(ctx, online.PayloadOf(event, next, e.conf())); err != nil {
				logx.Warnf("online: notifying alert %s: %v", next.ID, err)
			}
		}
	}
}

func (e *OnlineEngine) digest(ctx context.Context, now time.Time) {
	hour := *e.conf().DigestHourUTC
	if hour < 0 || !e.notifier.HasDigestSink() || now.UTC().Hour() != hour {
		return
	}
	today := now.UTC().Format("2006-01-02")
	if last, err := e.store.OnlineState(onlineDigestKey); err != nil || last == today {
		return
	}
	rows, err := e.store.OnlineSamples(now.Add(-48 * time.Hour).UnixMilli())
	if err != nil {
		logx.Errorf("online: digest samples: %v", err)
		return
	}
	split := now.Add(-24 * time.Hour)
	type acc struct {
		sum, prevSum float64
		n, prevN     int
	}
	by := map[online.SeriesKey]*acc{}
	for _, r := range rows {
		a := by[r.Key]
		if a == nil {
			a = &acc{}
			by[r.Key] = a
		}
		if r.Sample.At.Before(split) {
			a.prevSum += r.Sample.Score
			a.prevN++
		} else {
			a.sum += r.Sample.Score
			a.n++
		}
	}
	var out []online.DigestRow
	for k, a := range by {
		if a.n == 0 {
			continue
		}
		row := online.DigestRow{Key: k, Mean: a.sum / float64(a.n), N: a.n, PrevN: a.prevN}
		if a.prevN > 0 {
			row.Prev = a.prevSum / float64(a.prevN)
		}
		out = append(out, row)
	}
	e.mu.Lock()
	open := len(e.alerts)
	e.mu.Unlock()
	text := online.DigestText(out, e.digestAgreement(now), e.budget.Status(now), open, e.conf().PublicURL)
	if err := e.notifier.Digest(ctx, text); err != nil {
		logx.Errorf("online: digest: %v", err)
		return
	}
	if err := e.store.SetOnlineState(onlineDigestKey, today); err != nil {
		logx.Errorf("online: digest state: %v", err)
	}
}

// onlineStatusDTO is GET /api/online/status.
type onlineStatusDTO struct {
	Profiles []string            `json:"profiles"`
	Sampling online.Sampling     `json:"sampling"`
	Detector online.Detector     `json:"detector"`
	Budget   online.BudgetStatus `json:"budget"`
	Queue    int                 `json:"queue"`
	Counts   map[string]int64    `json:"counts"`
	// SessionsScored24h is how many sessions were scored in the last 24 hours, counted from the store, so a restart
	// does not reset it (Counts does, as it counts since the process started).
	SessionsScored24h int               `json:"sessionsScored24h"`
	OpenAlerts        int               `json:"openAlerts"`
	Sinks             []string          `json:"sinks"`
	Findings          findingsStatusDTO `json:"findings"`
	Labels            labelsStatusDTO   `json:"labels"`
}

// status is the unrestricted view; statusFor limits the labels to what info may see.
func (e *OnlineEngine) status() onlineStatusDTO {
	return e.statusFor(RoleInfo{Role: RoleAdmin, IsAdmin: true})
}

func (e *OnlineEngine) statusFor(info RoleInfo) onlineStatusDTO {
	st := onlineStatusDTO{
		Sampling: e.conf().Sampling,
		Detector: e.conf().Detector,
		Budget:   e.budget.Status(e.now()),
		Queue:    len(e.queue),
		Counts:   map[string]int64{},
		Profiles: []string{},
		Sinks:    []string{},
	}
	for _, p := range e.conf().Profiles {
		st.Profiles = append(st.Profiles, p.Key())
	}
	for _, s := range e.conf().Sinks {
		st.Sinks = append(st.Sinks, s.Type)
	}
	e.mu.Lock()
	for k, v := range e.counts {
		st.Counts[k] = v
	}
	st.OpenAlerts = len(e.alerts)
	e.mu.Unlock()
	if e.store != nil && info.TenantScoped() {
		// The budget is the whole server's; a view of some tenants shows what those spent today.
		now := e.now()
		day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		st.Budget.Reserved = online.Cost{}
		st.Budget.Spent = online.Cost{}
		if spent, err := e.store.OnlineSpentByTenantSince(day.UnixMilli()); err != nil {
			logx.Warnf("online: reading today's spend per tenant: %v", err)
		} else {
			for tenant, c := range spent {
				if info.AllowsAnyTenant([]string{tenant}) {
					st.Budget.Spent.JudgeTokens += c.JudgeTokens
					st.Budget.Spent.JevCalls += c.JevCalls
				}
			}
		}
	}
	if e.store != nil {
		since := e.now().Add(-24 * time.Hour).UnixMilli()
		if counts, err := e.store.OnlineScoredCounts(since); err != nil {
			logx.Warnf("online: counting the sessions scored in 24 hours: %v", err)
		} else {
			for _, c := range filterByTenant(counts, info, func(c onlineScoredCount) string { return c.Tenant }) {
				st.SessionsScored24h += c.Sessions
			}
		}
	}
	st.Findings = e.findingsStatus()
	st.Labels = e.labelsStatus(info)
	return st
}

func writeOnlineUnavailable(w http.ResponseWriter) {
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{
		"data":  nil,
		"error": "Online evaluation is not configured (start with --session-db and --online-config)",
	})
}

// narrowedInfo is the caller's role with the tenant picked in the switcher (?tenant=) applied. A pick narrows an
// unrestricted role too, which then sees only the matching tenants.
func narrowedInfo(r *http.Request) RoleInfo {
	info := roleInfoOrOpen(r.Context())
	info.Tenants = narrowTenants(info.Tenants, r.URL.Query().Get("tenant"))
	return info
}

// filterByTenant keeps the items a tenant-scoped role may see; other roles see all.
func filterByTenant[T any](items []T, info RoleInfo, tenantOf func(T) string) []T {
	if !info.TenantScoped() {
		return items
	}
	out := make([]T, 0, len(items))
	for _, it := range items {
		if info.AllowsAnyTenant([]string{tenantOf(it)}) {
			out = append(out, it)
		}
	}
	return out
}

// onlineStatusHandler implements GET /api/online/status.
func onlineStatusHandler(e *OnlineEngine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if denyScoped(w, r) {
			return
		}
		if e == nil {
			writeOnlineUnavailable(w)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": e.statusFor(narrowedInfo(r)), "error": nil})
	}
}

// onlineSeriesHandler implements GET /api/online/series?hours=N (default
// 168, at most 2160): hourly mean score per (tenant, kind, metric).
func onlineSeriesHandler(e *OnlineEngine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if denyAgentScoped(w, r) {
			return
		}
		if e == nil {
			writeOnlineUnavailable(w)
			return
		}
		hours := 168
		if v, err := strconv.Atoi(r.URL.Query().Get("hours")); err == nil && v > 0 {
			hours = min(v, 2160)
		}
		points, err := e.store.OnlineSeries(e.now().Add(-time.Duration(hours) * time.Hour).UnixMilli())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"data": nil, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": filterByTenant(points, narrowedInfo(r), func(p onlineSeriesPoint) string { return p.Tenant }), "error": nil})
	}
}

// onlineAlertsHandler implements GET /api/online/alerts?state=open|all.
func onlineAlertsHandler(e *OnlineEngine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if denyAgentScoped(w, r) {
			return
		}
		if e == nil {
			writeOnlineUnavailable(w)
			return
		}
		status := "open"
		if r.URL.Query().Get("state") == "all" {
			status = ""
		}
		alerts, err := e.store.OnlineAlerts(status, 200)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"data": nil, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": filterByTenant(alerts, narrowedInfo(r), func(a *online.Alert) string { return a.Key.Tenant }), "error": nil})
	}
}

// unscoredAsError turns a NOT_EVALUATED metric (for a judge, a reply with
// no readable verdict) into an error without a score. Recorded as a 0 it
// would read as the worst quality and feed the detector a false drop.
func unscoredAsError(m metricResultDTO) metricResultDTO {
	if m.EvalStatus != string(eval.StatusNotEvaluated) {
		return m
	}
	msg := "not evaluated: the metric produced no score for this session"
	if strings.HasPrefix(m.MetricName, "rubric_based_") {
		msg = "not evaluated: the judge gave no readable verdict for any rubric"
	}
	m.Score = nil
	m.EvalStatus = "ERROR"
	m.Error = &msg
	return m
}

// onlineFailedHandler implements GET /api/online/failed?hours=24&limit=5: the sessions that failed an online
// evaluation (a score below its threshold), the one scored last first, for the sidebar's flag. A tenant-limited role
// gets the failures of its tenants only.
func onlineFailedHandler(e *OnlineEngine, store *SQLiteStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if denyAgentScoped(w, r) {
			return
		}
		if e == nil || store == nil {
			writeOnlineUnavailable(w)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		hours, limit := 24, 5
		if v, err := strconv.Atoi(r.URL.Query().Get("hours")); err == nil && v > 0 {
			hours = min(v, 24*30)
		}
		if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
			limit = min(v, 50)
		}
		now := e.now()
		// The role is checked after the read, so the read takes more than the page: a role that sees few tenants
		// would otherwise get a page of other people's failures cut down to nothing.
		failed, err := store.FailedOnlineSessions(now.Add(-time.Duration(hours)*time.Hour).UnixMilli(), now.Add(time.Minute).UnixMilli(), 500)
		if err != nil {
			writeEvaluateError(w, http.StatusInternalServerError, "failed to read failed sessions: "+err.Error())
			return
		}
		failed = filterByTenant(failed, narrowedInfo(r), func(f failedOnlineSession) string { return f.Tenant })
		if len(failed) > limit {
			failed = failed[:limit]
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": failed, "error": nil})
	}
}
