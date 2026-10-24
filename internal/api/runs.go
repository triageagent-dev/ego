package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"

	"github.com/triageagent-dev/ego/internal/logx"
)

// runSpecSummaryDTO mirrors ui/src/lib/types.ts's RunSpecSummary. EvalSet/ EvalConfig/Target are free-form (raw
// dicts on the wire.
type runSpecSummaryDTO struct {
	Approach   string         `json:"approach,omitempty"`
	EvalSet    map[string]any `json:"evalSet,omitempty"`
	EvalConfig map[string]any `json:"evalConfig,omitempty"`
	Target     map[string]any `json:"target,omitempty"`
	// Tenants are the tenants of the sessions an evaluation was started on.
	Tenants []string `json:"tenants,omitempty"`
}

// resultCountsDTO mirrors ui/src/lib/types.ts's ResultCounts.
type resultCountsDTO struct {
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Errored int `json:"errored"`
	Skipped int `json:"skipped"`
}

// perMetricSummaryDTO mirrors ui/src/lib/types.ts's PerMetricSummary.
type perMetricSummaryDTO struct {
	resultCountsDTO
	AvgScore *float64 `json:"avg_score"`
}

// runSummaryDTO mirrors ui/src/lib/types.ts's RunSummary.
type runSummaryDTO struct {
	TraceCount   int                            `json:"trace_count"`
	ResultCounts resultCountsDTO                `json:"result_counts"`
	PerMetric    map[string]perMetricSummaryDTO `json:"per_metric,omitempty"`
	Agents       []string                       `json:"agents,omitempty"`
	// Models are the distinct models the run's traces called, as the Models column of the runs table shows them.
	Models []string `json:"models,omitempty"`
	// DurationMs is the time an online run spent scoring its sessions, added up. An hourly run stays open for the
	// whole hour, so its start and finish say nothing about the work; 0 for a run that did not record it.
	DurationMs int64    `json:"duration_ms,omitempty"`
	Errors     []string `json:"errors,omitempty"`
}

// runDTO mirrors ui/src/lib/types.ts's Run.
type runDTO struct {
	RunID      string            `json:"runId"`
	Status     string            `json:"status"`
	Spec       runSpecSummaryDTO `json:"spec"`
	Error      *string           `json:"error,omitempty"`
	Summary    *runSummaryDTO    `json:"summary,omitempty"`
	CreatedAt  string            `json:"createdAt"`
	StartedAt  *string           `json:"startedAt,omitempty"`
	FinishedAt *string           `json:"finishedAt,omitempty"`
}

// runResultRowDTO mirrors ui/src/lib/types.ts's RunResultRow.
type runResultRowDTO struct {
	ResultID            string         `json:"resultId"`
	RunID               string         `json:"runId"`
	EvalSetItemID       string         `json:"evalSetItemId"`
	EvalSetItemName     string         `json:"evalSetItemName"`
	EvaluatorName       string         `json:"evaluatorName"`
	EvaluatorType       string         `json:"evaluatorType"`
	Status              string         `json:"status"`
	Score               *float64       `json:"score"`
	PerInvocationScores []*float64     `json:"perInvocationScores"`
	TraceID             *string        `json:"traceId,omitempty"`
	Details             map[string]any `json:"details,omitempty"`
	ErrorText           *string        `json:"errorText,omitempty"`
	CreatedAt           string         `json:"createdAt"`
}

// runStatusFromMetric maps a metricResultDTO's evalStatus ("PASSED"/
// "FAILED"/"ERROR") to the lowercase ResultStatus2 the UI expects. There's
// no "skipped" case yet - every non-PASSED/FAILED outcome here already
// carries an Error (unsupported evaluator type, missing judge model, ...).
func runStatusFromMetric(m metricResultDTO) string {
	if m.EvalStatus == "ERROR" {
		return "errored"
	}
	if m.EvalStatus == "PASSED" {
		return "passed"
	}
	return "failed"
}

// writeStorageUnavailable matches the UI's StorageUnavailableError path
// (ui/src/api/client.ts checks specifically for HTTP 503) for every /api/
// runs* route when no --session-db was configured, instead of silently
// pretending an empty list is the same thing as "no persistence enabled".
func writeStorageUnavailable(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"data":  nil,
		"error": "Run history storage is not configured (start with --session-db to enable /api/runs)",
	})
}

// listRunsHandler implements GET /api/runs.
func listRunsHandler(store *SQLiteStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeStorageUnavailable(w)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		q := r.URL.Query()
		limit := 100
		if raw := q.Get("limit"); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n > 0 {
				limit = clampLimit(n, limit)
			}
		}
		visible, err := store.ListRunsFor(roleInfoOrOpen(r.Context()), q.Get("tenant"), q["status"], limit, q.Get("before"))
		if err != nil {
			writeEvaluateError(w, http.StatusInternalServerError, "failed to list runs: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": visible, "error": nil})
	}
}

// tenantRunScan is how many recent runs are examined to find the online runs of a tenant-limited role: the
// newest runs mostly belong to other tenants, so the page size cannot bound the scan.
const tenantRunScan = 2000

// runTenants are the tenants a run belongs to: the sessions an evaluation was started on, else the tenant an online
// run was made for (from its own spec, so it survives the sessions' rows being purged), else what online_evals
// records. None for a run that cannot be attributed, such as one over uploaded files.
func runTenants(run runDTO, online string) []string {
	if len(run.Spec.Tenants) > 0 {
		return run.Spec.Tenants
	}
	if t, _ := run.Spec.Target["tenant"].(string); t != "" {
		return []string{t}
	}
	if online != "" {
		return []string{online}
	}
	return nil
}

// ListRunsFor lists the runs info may see, newest first, at most limit. pick is the tenant chosen in the switcher:
// only runs of that tenant are listed, so a run that cannot be attributed to a tenant shows under "all tenants" only.
func (s *SQLiteStore) ListRunsFor(info RoleInfo, pick string, statuses []string, limit int, before string) ([]runDTO, error) {
	picked := info
	picked.Tenants = narrowTenants(info.Tenants, pick)
	fetch := limit
	var tenants map[string]string
	if info.TenantScoped() || picked.TenantScoped() {
		fetch = tenantRunScan
		var err error
		if tenants, err = s.OnlineRunTenants(); err != nil {
			return nil, err
		}
	}
	runs, err := s.ListRuns(statuses, fetch, before)
	if err != nil {
		return nil, err
	}
	visible := make([]runDTO, 0, min(len(runs), limit))
	for _, run := range runs {
		if len(visible) == limit {
			break
		}
		of := runTenants(run, tenants[run.RunID])
		if !runVisibleToRole(run, info, of) || !picked.AllowsAnyTenant(of) {
			continue
		}
		visible = append(visible, run)
	}
	return visible, nil
}

// GetRunFor returns a run if info may see it; ok is false for a missing run and a run out of scope alike.
func (s *SQLiteStore) GetRunFor(info RoleInfo, runID string) (run runDTO, ok bool, err error) {
	run, ok, err = s.GetRun(runID)
	if err != nil || !ok {
		return runDTO{}, false, err
	}
	online := ""
	if info.TenantScoped() {
		if online, _, err = s.OnlineRunTenant(runID); err != nil {
			return runDTO{}, false, err
		}
	}
	if !runVisibleToRole(run, info, runTenants(run, online)) {
		return runDTO{}, false, nil
	}
	return run, true, nil
}

// getRunHandler implements GET /api/runs/{id}.
func getRunHandler(store *SQLiteStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeStorageUnavailable(w)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		run, ok, err := store.GetRunFor(roleInfoOrOpen(r.Context()), r.PathValue("id"))
		if err != nil {
			writeEvaluateError(w, http.StatusInternalServerError, "failed to fetch run: "+err.Error())
			return
		}
		if !ok {
			writeEvaluateError(w, http.StatusNotFound, "run not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": run, "error": nil})
	}
}

// getRunResultsHandler implements GET /api/runs/{id}/results.
func getRunResultsHandler(store *SQLiteStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeStorageUnavailable(w)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// Fetched only to apply the same per-agent role scoping
		// getRunHandler does - a restricted viewer must not be able to
		// read a run's result rows just because they know its ID, even
		// without ever seeing it via GET /api/runs or GET /api/runs/{id}.
		_, ok, err := store.GetRunFor(roleInfoOrOpen(r.Context()), r.PathValue("id"))
		if err != nil {
			writeEvaluateError(w, http.StatusInternalServerError, "failed to fetch run: "+err.Error())
			return
		}
		if !ok {
			writeEvaluateError(w, http.StatusNotFound, "run not found")
			return
		}
		rows, err := store.GetRunResults(r.PathValue("id"))
		if err != nil {
			writeEvaluateError(w, http.StatusInternalServerError, "failed to fetch run results: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": rows, "error": nil})
	}
}

// runSpecFromRequest builds the Run's spec summary from a parsed evaluate request: evalConfig/evalSet
// round-trip through JSON to a free-form map (.
func runSpecFromRequest(req *evaluateRequest) runSpecSummaryDTO {
	spec := runSpecSummaryDTO{Approach: "traces"}

	if b, err := json.Marshal(req.cfg); err == nil {
		var m map[string]any
		if json.Unmarshal(b, &m) == nil {
			spec.EvalConfig = m
		}
	}
	if req.evalSet != nil {
		if b, err := json.Marshal(req.evalSet); err == nil {
			var m map[string]any
			if json.Unmarshal(b, &m) == nil {
				spec.EvalSet = m
			}
		}
	}

	filenames := make([]string, 0, len(req.traceFilenames))
	seen := map[string]struct{}{}
	for _, name := range req.traceFilenames {
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		filenames = append(filenames, name)
	}
	spec.Tenants = req.tenants
	spec.Target = map[string]any{
		"kind":        "traces",
		"trace_count": len(req.traces),
		"trace_files": filenames,
	}
	return spec
}

// beginRun records that an evaluation has started, so the Runs list shows it while it works instead of only after
// it ends. Best-effort: nil when there is no run history or it cannot be written, and finishRun then does nothing.
func beginRun(store *SQLiteStore, spec runSpecSummaryDTO) *runDTO {
	if store == nil {
		return nil
	}
	run, err := store.CreateRun(spec)
	if err != nil {
		logx.Errorf("run history: failed to record the run: %v", err)
		return nil
	}
	return &run
}

// finishRun writes the RunResultRows of an evaluation begun with beginRun and closes its run: cancelled when the
// caller stopped it (the results so far are kept), failed when nothing could be scored, else succeeded. Best-effort,
// like beginRun: a storage failure is logged and never fails the response the evaluation already computed.
func finishRun(store *SQLiteStore, run *runDTO, req *evaluateRequest, result runResultDTO, stopped bool) {
	if run == nil {
		return
	}
	if err := writeRun(store, run, req, result, stopped); err != nil {
		logx.Errorf("run history: failed to persist run %s: %v", run.RunID, err)
	}
}

func writeRun(store *SQLiteStore, run *runDTO, req *evaluateRequest, result runResultDTO, stopped bool) error {
	rows := make([]runResultRowDTO, 0)
	perMetric := map[string]perMetricSummaryDTO{}
	counts := resultCountsDTO{}

	for _, tr := range result.TraceResults {
		itemName := req.traceFilenames[tr.TraceID]
		if itemName == "" {
			itemName = tr.TraceID
		}
		for _, mr := range tr.MetricResults {
			status := runStatusFromMetric(mr)
			switch status {
			case "passed":
				counts.Passed++
			case "failed":
				counts.Failed++
			case "errored":
				counts.Errored++
			}

			pm := perMetric[mr.MetricName]
			switch status {
			case "passed":
				pm.Passed++
			case "failed":
				pm.Failed++
			case "errored":
				pm.Errored++
			}
			if mr.Score != nil {
				if pm.AvgScore == nil {
					v := *mr.Score
					pm.AvgScore = &v
				} else {
					// Running mean over however many scored results this
					// metric has seen so far this run (Passed+Failed
					// already incremented above, so it doubles as count).
					n := float64(pm.Passed + pm.Failed)
					if n < 1 {
						n = 1
					}
					*pm.AvgScore += (*mr.Score - *pm.AvgScore) / n
				}
			}
			perMetric[mr.MetricName] = pm

			row := runResultRowDTO{
				ResultID:            newRandomID("res"),
				RunID:               run.RunID,
				EvalSetItemID:       tr.TraceID,
				EvalSetItemName:     itemName,
				EvaluatorName:       mr.MetricName,
				EvaluatorType:       "builtin",
				Status:              status,
				Score:               mr.Score,
				PerInvocationScores: mr.PerInvocationScores,
				TraceID:             &tr.TraceID,
				Details:             mr.Details,
				ErrorText:           mr.Error,
				CreatedAt:           nowRFC3339(),
			}
			rows = append(rows, row)
		}
	}

	summary := &runSummaryDTO{
		TraceCount:   len(result.TraceResults),
		ResultCounts: counts,
		PerMetric:    perMetric,
		Agents:       distinctAgentNames(result.TraceResults),
		Models:       distinctModels(result.TraceResults),
		Errors:       result.Errors,
	}

	if err := store.InsertRunResults(run.RunID, rows); err != nil {
		return err
	}

	status := "succeeded"
	var errMsg *string
	switch {
	case stopped:
		status = "cancelled"
		msg := "stopped before it finished"
		errMsg = &msg
	case len(result.Errors) > 0 && len(result.TraceResults) == 0:
		status = "failed"
		msg := result.Errors[0]
		errMsg = &msg
	case counts.Errored > 0 && counts.Passed+counts.Failed == 0:
		// Nothing was scored: say why on the run, not only on its rows.
		status = "failed"
		for _, row := range rows {
			if row.ErrorText != nil {
				msg := row.EvaluatorName + ": " + *row.ErrorText
				errMsg = &msg
				break
			}
		}
	}
	return store.FinishRun(run.RunID, status, summary, errMsg)
}

// distinctModels lists the models the traces of a run called, sorted; nil when none could be read.
func distinctModels(results []traceResultDTO) []string {
	seen := map[string]bool{}
	for _, r := range results {
		if r.Model != "" {
			seen[r.Model] = true
		}
	}
	return sortedKeys(seen)
}

// sortedKeys is the keys of a set in order, nil when it is empty.
func sortedKeys(set map[string]bool) []string {
	var out []string
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Returns nil (not an empty slice) when no trace in this run had a resolvable agent name; a scoped role
// then does not see the run, as it cannot be attributed to one of its agents.
func distinctAgentNames(results []traceResultDTO) []string {
	seen := map[string]bool{}
	for _, r := range results {
		if r.AgentName != "" {
			seen[r.AgentName] = true
		}
	}
	if len(seen) == 0 {
		return nil
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
