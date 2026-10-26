package api

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/triageagent-dev/ego/internal/mcptools"
	"github.com/triageagent-dev/ego/internal/online"
)

// This file answers "how is quality doing and why": the get_quality, explain_regression and explain_session tools
// and their REST twins (GET /api/online/quality, /api/online/alerts/{id}/explain, /api/online/sessions/{id}/explain).
// The tools and the endpoints call the same functions, so a role sees the same through both.

// statusError carries the HTTP status a failed lookup maps to; the tools show only its message.
type statusError struct {
	Status int
	Msg    string
}

func (e *statusError) Error() string { return e.Msg }

var (
	errAgentScopedOnline = &statusError{http.StatusForbidden, "this view is not available to an agent-scoped role"}
	errOnlineUnavailable = &statusError{http.StatusServiceUnavailable, "Online evaluation is not configured (start with --session-db and --online-config)"}
)

// notFound is the one answer for a missing record and a record the caller may not see, so a refusal does not
// reveal that the id exists.
func notFound(what string) error { return &statusError{http.StatusNotFound, what + " not found"} }

func writeStatusError(w http.ResponseWriter, err error) {
	var se *statusError
	if errors.As(err, &se) {
		writeEvaluateError(w, se.Status, se.Msg)
		return
	}
	writeEvaluateError(w, http.StatusInternalServerError, err.Error())
}

// Quality reports the open alerts, hourly series, scored sessions and budget of the tenants info may see.
func (e *OnlineEngine) Quality(info RoleInfo, kind string, hours int) (*mcptools.Quality, error) {
	if e == nil {
		return nil, errOnlineUnavailable
	}
	if info.Scoped() {
		return nil, errAgentScopedOnline
	}
	now := e.now()
	since := now.Add(-time.Duration(hours) * time.Hour).UnixMilli()
	wantKind := func(k string) bool { return kind == "" || k == kind }

	points, err := e.store.OnlineSeries(since)
	if err != nil {
		return nil, err
	}
	type seriesKey = online.SeriesKey
	byKey := map[seriesKey]*mcptools.MetricSeries{}
	sums := map[seriesKey]float64{}
	for _, p := range filterByTenant(points, info, func(p onlineSeriesPoint) string { return p.Tenant }) {
		if !wantKind(p.Kind) {
			continue
		}
		k := seriesKey{Tenant: p.Tenant, Kind: p.Kind, Metric: p.Metric}
		s := byKey[k]
		if s == nil {
			s = &mcptools.MetricSeries{Tenant: p.Tenant, Kind: p.Kind, Metric: p.Metric, Points: []mcptools.SeriesPoint{}}
			byKey[k] = s
		}
		s.Points = append(s.Points, mcptools.SeriesPoint{Hour: time.UnixMilli(p.BucketMs).UTC().Format(time.RFC3339), Mean: p.Mean, N: p.N})
		s.N += p.N
		sums[k] += p.Mean * float64(p.N)
	}
	out := &mcptools.Quality{Hours: hours, Kind: kind, OpenAlerts: []online.AlertPayload{}, Series: []mcptools.MetricSeries{}, SessionsScored: []mcptools.ScoredCount{}}
	for k, s := range byKey {
		if s.N > 0 {
			s.Mean = sums[k] / float64(s.N)
		}
		out.Series = append(out.Series, *s)
	}
	sort.Slice(out.Series, func(i, j int) bool {
		a, b := out.Series[i], out.Series[j]
		return a.Tenant+"\x00"+a.Kind+"\x00"+a.Metric < b.Tenant+"\x00"+b.Kind+"\x00"+b.Metric
	})

	alerts, err := e.store.OnlineAlerts("open", 200)
	if err != nil {
		return nil, err
	}
	for _, a := range filterByTenant(alerts, info, func(a *online.Alert) string { return a.Key.Tenant }) {
		if wantKind(a.Key.Kind) {
			out.OpenAlerts = append(out.OpenAlerts, online.PayloadOf(a.Status, a, e.conf()))
		}
	}

	counts, err := e.store.OnlineScoredCounts(since)
	if err != nil {
		return nil, err
	}
	for _, c := range filterByTenant(counts, info, func(c onlineScoredCount) string { return c.Tenant }) {
		if wantKind(c.Kind) {
			out.SessionsScored = append(out.SessionsScored, mcptools.ScoredCount{Tenant: c.Tenant, Kind: c.Kind, Sessions: c.Sessions})
			out.SessionsScoredTotal += c.Sessions
		}
	}

	// The budget is one meter for every tenant: its use says how busy the others are, so a limited caller does
	// not get it.
	if !info.TenantScoped() {
		b := e.budget.Status(now)
		out.Budget = &mcptools.BudgetUse{
			Day:             b.Day,
			JudgeTokensUsed: b.Spent.JudgeTokens, JudgeTokensCap: b.Caps.JudgeTokensPerDay,
			JevCallsUsed: b.Spent.JevCalls, JevCallsCap: b.Caps.JevCallsPerDay,
		}
	}
	return out, nil
}

// sessionEvidence reads what was stored about one scored session, without any access check. ok is false for a
// session that was never scored (or whose rows were pruned).
func (e *OnlineEngine) sessionEvidence(sessionID string) (ev *mcptools.SessionEvidence, ok bool, err error) {
	rec, found, err := e.store.OnlineEval(sessionID)
	if err != nil || !found {
		return nil, false, err
	}
	scores, err := e.store.OnlineScoresOf(sessionID)
	if err != nil {
		return nil, false, err
	}
	var rows []runResultRowDTO
	if rec.RunID != "" {
		if rows, err = e.store.GetRunResultsOfItem(rec.RunID, sessionID); err != nil {
			return nil, false, err
		}
	}
	byMetric := map[string]runResultRowDTO{}
	for _, r := range rows {
		byMetric[r.EvaluatorName] = r
	}
	labels, err := e.store.OnlineLabels("", sessionID, "", 0, 0)
	if err != nil {
		return nil, false, err
	}
	latest := map[string]online.Label{}
	for _, l := range online.Latest(labelValues(labels)) {
		latest[l.Metric] = l
	}

	ev = &mcptools.SessionEvidence{
		SessionID: sessionID, Tenant: rec.Tenant, Kind: rec.Kind, Service: rec.Service,
		ScoredAt: time.UnixMilli(rec.AtMs).UTC().Format(time.RFC3339),
		RunID:    rec.RunID,
		Metrics:  []mcptools.Evidence{},
	}
	for _, sc := range scores {
		if ev.Version == "" {
			ev.Version, ev.Model = sc.Version, sc.Model
		}
		m := mcptools.Evidence{Metric: sc.Metric}
		if row, has := byMetric[sc.Metric]; has {
			m = ExtractEvidence(row, sc.Metric)
		}
		// The score table is the record of the score; the row only explains it.
		m.Score, m.Status = sc.Score, sc.Status
		if l, ok := latest[sc.Metric]; ok {
			m.HumanVerdict, m.HumanScore = l.Verdict, l.Score
		}
		ev.Metrics = append(ev.Metrics, m)
	}
	return ev, true, nil
}

// ExplainSession is the evidence behind one scored session's scores, for a caller who may see its tenant.
func (e *OnlineEngine) ExplainSession(info RoleInfo, sessionID string) (*mcptools.SessionEvidence, error) {
	if e == nil {
		return nil, errOnlineUnavailable
	}
	if info.Scoped() {
		return nil, errAgentScopedOnline
	}
	ev, ok, err := e.sessionEvidence(sessionID)
	if err != nil {
		return nil, err
	}
	if !ok || !info.AllowsAnyTenant([]string{ev.Tenant}) {
		return nil, notFound("scored session")
	}
	return ev, nil
}

// ExplainRegression is an alert with the evidence of its worst sessions, for a caller who may see its tenant.
func (e *OnlineEngine) ExplainRegression(info RoleInfo, alertID string) (*mcptools.RegressionExplanation, error) {
	if e == nil {
		return nil, errOnlineUnavailable
	}
	if info.Scoped() {
		return nil, errAgentScopedOnline
	}
	a, ok, err := e.store.OnlineAlert(alertID)
	if err != nil {
		return nil, err
	}
	if !ok || !info.AllowsAnyTenant([]string{a.Key.Tenant}) {
		return nil, notFound("alert")
	}
	out := &mcptools.RegressionExplanation{Alert: online.PayloadOf(a.Status, a, e.conf()), Suspect: a.Last.Suspect, Sessions: []mcptools.SessionEvidence{}}
	for _, id := range a.Last.Worst {
		ev, found, err := e.sessionEvidence(id)
		if err != nil {
			return nil, err
		}
		if !found {
			out.Missing = append(out.Missing, id)
			continue
		}
		// The alerted metric first: it is the one the drop is about.
		sort.SliceStable(ev.Metrics, func(i, j int) bool {
			return ev.Metrics[i].Metric == a.Key.Metric && ev.Metrics[j].Metric != a.Key.Metric
		})
		out.Sessions = append(out.Sessions, *ev)
	}
	return out, nil
}

// onlineQualityHandler implements GET /api/online/quality?kind=&hours=N (default 24, at most 720).
func onlineQualityHandler(e *OnlineEngine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hours := 24
		if v, err := strconv.Atoi(r.URL.Query().Get("hours")); err == nil && v > 0 {
			hours = min(v, 720)
		}
		out, err := e.Quality(roleInfoOrOpen(r.Context()), r.URL.Query().Get("kind"), hours)
		if err != nil {
			writeStatusError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": out, "error": nil})
	}
}

// onlineExplainAlertHandler implements GET /api/online/alerts/{id}/explain.
func onlineExplainAlertHandler(e *OnlineEngine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out, err := e.ExplainRegression(roleInfoOrOpen(r.Context()), r.PathValue("id"))
		if err != nil {
			writeStatusError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": out, "error": nil})
	}
}

// onlineExplainSessionHandler implements GET /api/online/sessions/{id}/explain.
func onlineExplainSessionHandler(e *OnlineEngine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out, err := e.ExplainSession(roleInfoOrOpen(r.Context()), r.PathValue("id"))
		if err != nil {
			writeStatusError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": out, "error": nil})
	}
}
