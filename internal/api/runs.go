package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
)

// runSpecSummaryDTO mirrors ui/src/lib/types.ts's RunSpecSummary. EvalSet/ EvalConfig/Target are free-form (raw
// dicts on the wire.
type runSpecSummaryDTO struct {
	Approach   string         `json:"approach,omitempty"`
	EvalSet    map[string]any `json:"evalSet,omitempty"`
	EvalConfig map[string]any `json:"evalConfig,omitempty"`
	Target     map[string]any `json:"target,omitempty"`
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
	Errors       []string                       `json:"errors,omitempty"`
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
				limit = n
			}
		}
		runs, err := store.ListRuns(q["status"], limit, q.Get("before"))
		if err != nil {
			writeEvaluateError(w, http.StatusInternalServerError, "failed to list runs: "+err.Error())
			return
		}
		info := roleInfoOrOpen(r.Context())
		visible := make([]runDTO, 0, len(runs))
		for _, run := range runs {
			if runVisibleForRole(run, info) {
				visible = append(visible, run)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": visible, "error": nil})
	}
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
		run, ok, err := store.GetRun(r.PathValue("id"))
		if err != nil {
			writeEvaluateError(w, http.StatusInternalServerError, "failed to fetch run: "+err.Error())
			return
		}
		if !ok || !runVisibleForRole(run, roleInfoOrOpen(r.Context())) {
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
		run, ok, err := store.GetRun(r.PathValue("id"))
		if err != nil {
			writeEvaluateError(w, http.StatusInternalServerError, "failed to fetch run: "+err.Error())
			return
		}
		if !ok || !runVisibleForRole(run, roleInfoOrOpen(r.Context())) {
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
	spec.Target = map[string]any{
		"kind":        "traces",
		"trace_count": len(req.traces),
		"trace_files": filenames,
	}
	return spec
}

// persistCompletedRun builds and writes the Run + RunResultRows for one
// finished /api/evaluate (or /api/evaluate/stream) call. Best-effort: a
// storage failure here is logged by the caller but never fails the HTTP
// response the evaluation itself already computed.
func persistCompletedRun(store *SQLiteStore, spec runSpecSummaryDTO, req *evaluateRequest, result runResultDTO) error {
	run, err := store.CreateRun(spec)
	if err != nil {
		return err
	}

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
		Errors:       result.Errors,
	}

	if err := store.InsertRunResults(run.RunID, rows); err != nil {
		return err
	}

	status := "succeeded"
	var errMsg *string
	if len(result.Errors) > 0 && len(result.TraceResults) == 0 {
		status = "failed"
		msg := result.Errors[0]
		errMsg = &msg
	}
	return store.FinishRun(run.RunID, status, summary, errMsg)
}

// Returns nil (not an empty slice) when no trace in this run had a resolvable agent name, so
// RoleInfo.AllowsAnyAgent's "can't tell, so allow" default applies to the whole run rather than hiding it.
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
