package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/triageagent-dev/ego/internal/adk"
	"github.com/triageagent-dev/ego/internal/decision"
	"github.com/triageagent-dev/ego/internal/judge"
	"github.com/triageagent-dev/ego/internal/logx"
	"github.com/triageagent-dev/ego/internal/mcptools"
	"github.com/triageagent-dev/ego/internal/online"
)

// Labels, the judge-agreement gate and anchor re-scoring (docs/rsi.md, section 4). The decisions live in
// internal/online/labels.go; this file reads the stores, re-scores with the judge, and answers the endpoints.

const (
	// recalibrateKey remembers when the last complete anchor pass ran.
	recalibrateKey = "labels_recalibrated_ms"
	// anchorPrefix + metric + "/" + session id keys the time an anchor was last re-scored (unix ms).
	anchorPrefix = "anchor/"
	// driftPrefix + metric keys the last pass's drift, as JSON.
	driftPrefix = "drift/"
	// maxLabelBody bounds a POST: a label is a few short fields and a note.
	maxLabelBody = 16 << 10
)

// labelsMetricDTO is one metric's entry in `labels.perMetric` of GET /api/online/status.
type labelsMetricDTO struct {
	Agree    int `json:"agree"`
	Disagree int `json:"disagree"`
	// Agreement is agree / (agree + disagree) over the last 7 days; null when the window holds no labels.
	Agreement *float64 `json:"agreement"`
	// Gated is true when the metric's judge is too far from the people who checked it to drive findings.
	Gated bool `json:"gated"`
	// Drift is the mean absolute difference between the last anchor pass's new scores and the labelled scores.
	Drift   *float64   `json:"drift,omitempty"`
	DriftN  int        `json:"driftN,omitempty"`
	DriftAt *time.Time `json:"driftAt,omitempty"`
}

// labelsStatusDTO is the `labels` part of GET /api/online/status.
type labelsStatusDTO struct {
	PerMetric map[string]labelsMetricDTO `json:"perMetric"`
	// Skipped counts anchor passes cut short because the judge budget was spent (since the process started).
	Skipped struct {
		Budget int64 `json:"budget"`
	} `json:"skipped"`
}

// recentLabels reads the labels inside the agreement window.
func (e *OnlineEngine) recentLabels(now time.Time) ([]*online.Label, error) {
	return e.store.OnlineLabels("", "", now.Add(-online.AgreementWindow).UnixMilli(), 0)
}

func labelValues(ls []*online.Label) []online.Label {
	out := make([]online.Label, len(ls))
	for i, l := range ls {
		out[i] = *l
	}
	return out
}

// uncalibrated reports whether a metric's judge is gated: enough recent labels and agreement below the floor. The
// gate looks at every tenant's labels, because it judges the metric's judge, not one tenant's sessions.
func (e *OnlineEngine) uncalibrated(metric string, now time.Time) bool {
	ls, err := e.store.OnlineLabels(metric, "", now.Add(-online.AgreementWindow).UnixMilli(), 0)
	if err != nil {
		logx.Warnf("online: reading labels for the agreement gate: %v", err)
		return false
	}
	return online.AgreementByMetric(labelValues(ls))[metric].Gated(e.cfg.Findings.MinAgreement)
}

func (e *OnlineEngine) storedDrift(metric string) *online.Drift {
	v, err := e.store.OnlineState(driftPrefix + metric)
	if err != nil || v == "" {
		return nil
	}
	var d online.Drift
	if json.Unmarshal([]byte(v), &d) != nil {
		return nil
	}
	return &d
}

// labelsStatus is the agreement per metric. A tenant-limited caller counts only the labels of its tenants; the
// gate flag stays the engine's (it is about the judge), and drift, which is not attributable to a tenant, is not
// shown to such a caller.
func (e *OnlineEngine) labelsStatus(info RoleInfo) labelsStatusDTO {
	st := labelsStatusDTO{PerMetric: map[string]labelsMetricDTO{}}
	st.Skipped.Budget = e.lSkippedBudget.Load()
	now := e.now()
	ls, err := e.recentLabels(now)
	if err != nil {
		logx.Warnf("online: reading labels: %v", err)
		return st
	}
	all := online.AgreementByMetric(labelValues(ls))
	shown := all
	if info.TenantScoped() {
		shown = online.AgreementByMetric(labelValues(filterByTenant(ls, info, func(l *online.Label) string { return l.Tenant })))
	}
	for metric, a := range shown {
		dto := labelsMetricDTO{Agree: a.Agree, Disagree: a.Disagree, Gated: all[metric].Gated(e.cfg.Findings.MinAgreement)}
		if rate, ok := a.Rate(); ok {
			dto.Agreement = &rate
		}
		st.PerMetric[metric] = dto
	}
	if info.TenantScoped() {
		return st
	}
	// A metric whose labels all aged out of the window still reports its last drift.
	drifts, err := e.store.OnlineStatePrefix(driftPrefix)
	if err != nil {
		return st
	}
	for key := range drifts {
		metric := strings.TrimPrefix(key, driftPrefix)
		d := e.storedDrift(metric)
		if d == nil {
			continue
		}
		dto := st.PerMetric[metric]
		dto.Drift, dto.DriftN, dto.DriftAt = &d.MeanAbsDiff, d.N, &d.At
		st.PerMetric[metric] = dto
	}
	return st
}

// digestAgreement is the agreement lines of the daily digest.
func (e *OnlineEngine) digestAgreement(now time.Time) []online.AgreementRow {
	ls, err := e.recentLabels(now)
	if err != nil {
		return nil
	}
	var out []online.AgreementRow
	for metric, a := range online.AgreementByMetric(labelValues(ls)) {
		out = append(out, online.AgreementRow{Metric: metric, Agreement: a, Gated: a.Gated(e.cfg.Findings.MinAgreement)})
	}
	return out
}

// LabelSession stores a verdict on the online score of one metric of one session. `by` is the caller's identity
// when the auth layer knows one; it is never taken from the request. The session must have an online score for the
// metric (404 otherwise, also for a tenant the caller may not see, so a refusal reveals nothing).
func (e *OnlineEngine) LabelSession(info RoleInfo, by string, in mcptools.LabelInput) (*online.Label, error) {
	if e == nil {
		return nil, errOnlineUnavailable
	}
	if info.Scoped() {
		return nil, errAgentScopedOnline
	}
	if info.Role != RoleAdmin && info.Role != RoleMember {
		return nil, &statusError{http.StatusForbidden, "this role cannot label sessions"}
	}
	in.SessionID, in.Metric, in.Note = strings.TrimSpace(in.SessionID), strings.TrimSpace(in.Metric), strings.TrimSpace(in.Note)
	switch {
	case in.SessionID == "" || in.Metric == "":
		return nil, &statusError{http.StatusBadRequest, "sessionId and metric are required"}
	case !online.ValidVerdict(in.Verdict):
		return nil, &statusError{http.StatusBadRequest, "verdict is agree or disagree"}
	case in.Score != nil && (*in.Score < 0 || *in.Score > 1 || *in.Score != *in.Score):
		return nil, &statusError{http.StatusBadRequest, "score is between 0 and 1"}
	case len([]rune(in.Note)) > online.MaxLabelNoteChars:
		return nil, &statusError{http.StatusBadRequest, "note is at most " + strconv.Itoa(online.MaxLabelNoteChars) + " characters"}
	}
	rec, ok, err := e.store.OnlineEval(in.SessionID)
	if err != nil {
		return nil, err
	}
	if !ok || !info.AllowsAnyTenant([]string{rec.Tenant}) {
		return nil, notFound("scored session")
	}
	scores, err := e.store.OnlineScoresOf(in.SessionID)
	if err != nil {
		return nil, err
	}
	var judgeScore *float64
	for _, sc := range scores {
		if sc.Metric == in.Metric && sc.Score != nil {
			judgeScore = sc.Score
		}
	}
	if judgeScore == nil {
		return nil, notFound("online score for that metric on the session")
	}
	l := &online.Label{
		ID: newRandomID("label"), SessionID: in.SessionID, Tenant: rec.Tenant, Kind: rec.Kind, Metric: in.Metric,
		JudgeScore: *judgeScore, Verdict: in.Verdict, Score: in.Score, Note: in.Note,
		By: clipRunes(strings.TrimSpace(by), online.MaxLabelByChars), CreatedAt: e.now().UTC(),
	}
	if err := e.store.InsertOnlineLabel(l); err != nil {
		return nil, err
	}
	return l, nil
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// Labels lists the labels of the tenants info may see, newest first.
func (e *OnlineEngine) Labels(info RoleInfo, metric, sessionID string, since time.Time) ([]online.Label, error) {
	if e == nil {
		return nil, errOnlineUnavailable
	}
	if info.Scoped() {
		return nil, errAgentScopedOnline
	}
	var sinceMs int64
	if !since.IsZero() {
		sinceMs = since.UnixMilli()
	}
	rows, err := e.store.OnlineLabels(metric, sessionID, sinceMs, maxListed)
	if err != nil {
		return nil, err
	}
	out := []online.Label{}
	for _, l := range filterByTenant(rows, info, func(l *online.Label) string { return l.Tenant }) {
		out = append(out, *l)
	}
	return out, nil
}

// callerName is who the request authenticated as, "" in the no-login mode.
func callerName(ctx context.Context) string {
	if id, ok := identityFromContext(ctx); ok {
		return id.Username
	}
	return ""
}

// onlineLabelsHandler implements GET /api/online/labels?metric=&session=&tenant=&since= and POST (a write: a viewer
// gets 403, a tenant-limited member may label its own tenants' sessions).
func onlineLabelsHandler(e *OnlineEngine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if e == nil {
				writeOnlineUnavailable(w)
				return
			}
			q := r.URL.Query()
			since, err := mcptools.ParseSince(q.Get("since"), e.now())
			if err != nil {
				writeEvaluateError(w, http.StatusBadRequest, err.Error())
				return
			}
			out, err := e.Labels(narrowedInfo(r), q.Get("metric"), q.Get("session"), since)
			if err != nil {
				writeStatusError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": out, "error": nil})
		case http.MethodPost:
			if e == nil {
				writeOnlineUnavailable(w)
				return
			}
			var in mcptools.LabelInput
			dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLabelBody))
			if err := dec.Decode(&in); err != nil {
				var tooBig *http.MaxBytesError
				switch {
				case errors.As(err, &tooBig):
					writeEvaluateError(w, http.StatusRequestEntityTooLarge, "request body too large")
				case errors.Is(err, io.EOF):
					writeEvaluateError(w, http.StatusBadRequest, "request body required")
				default:
					writeEvaluateError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
				}
				return
			}
			out, err := e.LabelSession(roleInfoOrOpen(r.Context()), callerName(r.Context()), in)
			if err != nil {
				writeStatusError(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, map[string]any{"data": out, "error": nil})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// --- Anchor re-scoring ---

// recalJudge is the judge for anchor passes: its own models and token counter, because the scoring worker's models
// report to a counter that process() resets and the two run on different goroutines.
func (e *OnlineEngine) getRecalJudge() func(string) (judge.Model, error) {
	return func(name string) (judge.Model, error) {
		if name == "" {
			name = judge.DefaultModel
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		if m, ok := e.rjudges[name]; ok {
			return m, nil
		}
		m, err := e.newJudge(context.Background(), name, func(n int64) { e.recalTokens.Add(n) })
		if err != nil {
			return nil, err
		}
		e.rjudges[name] = m
		return m, nil
	}
}

// anchorResult is what re-scoring one labelled session found.
type anchorResult int

const (
	anchorScored  anchorResult = iota // new scores are in the map
	anchorSkipped                     // nothing to re-score with (no trace, profile gone); do not retry at once
	anchorBudget                      // the judge budget is spent
)

// rescoreSession scores a session again with the profile that scored it, against the same budget, and returns the
// new score of each metric that produced one.
func (e *OnlineEngine) rescoreSession(ctx context.Context, now time.Time, sessionID string) (map[string]float64, anchorResult) {
	rec, ok, err := e.store.OnlineEval(sessionID)
	if err != nil || !ok {
		return nil, anchorSkipped
	}
	var p online.Profile
	found := false
	for _, cand := range e.cfg.Profiles {
		if cand.Key() == rec.Profile {
			p, found = cand, true
		}
	}
	tr, haveTrace := e.sessions.Trace(sessionID)
	if !found || !haveTrace {
		return nil, anchorSkipped
	}
	invocations := adk.ConvertTrace(tr).Invocations
	if len(invocations) == 0 {
		return nil, anchorSkipped
	}
	if len(invocations) > p.MaxInvocations {
		invocations = invocations[len(invocations)-p.MaxInvocations:]
	}
	reserved, ok := e.budget.Reserve(p, now)
	if !ok {
		return nil, anchorBudget
	}
	evalCtx, cancel := context.WithTimeout(ctx, onlineEvalTimeout)
	defer cancel()
	before := e.recalTokens.Load()
	result := evaluateInvocations(evalCtx, traceResultDTO{
		TraceID: rec.TraceID, NumInvocations: len(invocations), MetricResults: []metricResultDTO{},
		ConversionWarnings: []string{}, AgentName: rec.Service,
	}, invocations, profileEvaluators(p), nil, e.getRecalJudge(), noVertexEval, e.getJev, nil)

	actual := online.Cost{JudgeTokens: e.recalTokens.Load() - before}
	scores := map[string]float64{}
	for _, m := range result.MetricResults {
		m = unscoredAsError(m)
		if m.Error == nil && m.Score != nil {
			scores[m.MetricName] = *m.Score
		}
		if isJevMetric(m.MetricName) && m.Error == nil {
			actual.JevCalls = int64(len(invocations))
		}
	}
	e.budget.Settle(p, reserved, &actual, now)
	e.persistSpend(now, actual.JudgeTokens)
	return scores, anchorScored
}

// recalibrate re-scores the labelled sessions (anchors) of each metric, oldest re-score first, and records how far
// the new scores are from what people said. Nothing acts on the drift; it is reported. A pass runs every
// findings.recalibrateEvery; one cut short by the budget is retried on the next tick.
func (e *OnlineEngine) recalibrate(ctx context.Context, now time.Time) {
	if v, err := e.store.OnlineState(recalibrateKey); err == nil && v != "" {
		if ms, perr := strconv.ParseInt(v, 10, 64); perr == nil && now.Sub(time.UnixMilli(ms)) < e.cfg.Findings.RecalibrateEvery.D() {
			return
		}
	}
	all, err := e.store.OnlineLabels("", "", 0, 0)
	if err != nil {
		logx.Errorf("online: reading labels: %v", err)
		return
	}
	byMetric := map[string][]online.Label{}
	for _, l := range all {
		byMetric[l.Metric] = append(byMetric[l.Metric], *l)
	}
	metrics := make([]string, 0, len(byMetric))
	for m := range byMetric {
		metrics = append(metrics, m)
	}
	sort.Strings(metrics)

	picks := map[string][]online.Label{}
	var order []string
	queued := map[string]bool{}
	for _, m := range metrics {
		rescored := map[string]time.Time{}
		state, err := e.store.OnlineStatePrefix(anchorPrefix + m + "/")
		if err != nil {
			logx.Errorf("online: reading anchor times: %v", err)
			return
		}
		for key, v := range state {
			if ms, perr := strconv.ParseInt(v, 10, 64); perr == nil {
				rescored[online.AnchorKey(strings.TrimPrefix(key, anchorPrefix+m+"/"), m)] = time.UnixMilli(ms)
			}
		}
		picks[m] = online.PickAnchors(byMetric[m], rescored, online.AnchorsPerMetric)
		for _, l := range picks[m] {
			if !queued[l.SessionID] {
				queued[l.SessionID] = true
				order = append(order, l.SessionID)
			}
		}
	}

	newScores := map[string]map[string]float64{}
	attempted := map[string]bool{}
	budgetHit := false
	for _, id := range order {
		scores, res := e.rescoreSession(ctx, now, id)
		if res == anchorBudget {
			budgetHit = true
			break
		}
		attempted[id] = true
		if res == anchorScored {
			newScores[id] = scores
		}
		if ctx.Err() != nil {
			return
		}
	}

	for _, m := range metrics {
		var fresh, ref []float64
		for _, l := range picks[m] {
			if !attempted[l.SessionID] {
				continue
			}
			if err := e.store.SetOnlineState(anchorPrefix+m+"/"+l.SessionID, strconv.FormatInt(now.UnixMilli(), 10)); err != nil {
				logx.Warnf("online: saving anchor time: %v", err)
			}
			if s, ok := newScores[l.SessionID][m]; ok {
				fresh, ref = append(fresh, s), append(ref, l.Reference())
			}
		}
		if len(fresh) == 0 {
			continue
		}
		d := online.Drift{MeanAbsDiff: online.MeanAbsDiff(fresh, ref), N: len(fresh), At: now.UTC()}
		b, _ := json.Marshal(d)
		if err := e.store.SetOnlineState(driftPrefix+m, string(b)); err != nil {
			logx.Warnf("online: saving drift: %v", err)
		}
		logx.Infof("online: judge drift on %s: %.3f over %d anchor(s)", m, d.MeanAbsDiff, d.N)
	}

	if budgetHit {
		e.lSkippedBudget.Add(1)
		return
	}
	if err := e.store.SetOnlineState(recalibrateKey, strconv.FormatInt(now.UnixMilli(), 10)); err != nil {
		logx.Warnf("online: saving recalibration time: %v", err)
	}
}

// isJevMetric reports whether a metric is scored by the Jev decision model, which is metered in calls, not tokens.
func isJevMetric(name string) bool {
	return decision.IsMetric(name) || strings.HasPrefix(name, "jev:")
}
