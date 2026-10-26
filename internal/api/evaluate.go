package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"time"

	"github.com/triageagent-dev/ego/internal/adk"
	"github.com/triageagent-dev/ego/internal/decision"
	"github.com/triageagent-dev/ego/internal/eval"
	"github.com/triageagent-dev/ego/internal/judge"
	"github.com/triageagent-dev/ego/internal/loader"
	"github.com/triageagent-dev/ego/internal/logx"
	tracepkg "github.com/triageagent-dev/ego/internal/trace"
	"github.com/triageagent-dev/ego/internal/vertexeval"
)

const maxUploadFileSize = 10 << 20 // 10MB

// evaluatorConfigDTO is one entry in POST /api/evaluate's "config" form field, matching ui/src/lib/types.ts's
// EvalConfig.evaluators.
type evaluatorConfigDTO struct {
	Type                string   `json:"type"`
	Name                string   `json:"name"`
	JudgeModel          string   `json:"judgeModel"`
	Threshold           *float64 `json:"threshold"`
	TrajectoryMatchType string   `json:"trajectoryMatchType"`
	// Rubrics is required by rubric_based_final_response_quality_v1 and rubric_based_tool_use_quality_v1 (a
	// plain list of rubric-text strings; see judge.RubricsFromStrings).
	Rubrics []string `json:"rubrics,omitempty"`
	// Questions is the jev_custom evaluator's question set (see decision.ParseCustom).
	Questions json.RawMessage `json:"questions,omitempty"`
	// NumSamples is the judge samples per invocation; 0 means judge.DefaultNumSamples.
	NumSamples int `json:"numSamples,omitempty"`
}

// evalConfigDTO is POST /api/evaluate's "config" form field, matching
// ui/src/lib/types.ts's EvalConfig.
type evalConfigDTO struct {
	Evaluators []evaluatorConfigDTO `json:"evaluators"`
}

// metricResultDTO mirrors ui/src/lib/types.ts's MetricResult (CamelModel
// field names) built from an internal/eval.Result.
type metricResultDTO struct {
	MetricName          string         `json:"metricName"`
	Score               *float64       `json:"score"`
	EvalStatus          string         `json:"evalStatus"`
	PerInvocationScores []*float64     `json:"perInvocationScores"`
	Error               *string        `json:"error"`
	Details             map[string]any `json:"details,omitempty"`
	// EvaluatorKind and JudgeModel explain *how* the score was produced -
	// "algorithm" (deterministic Go code, e.g. tool_trajectory_avg_score),
	// "llm_judge" (a live judge-model call via internal/judge, with
	// JudgeModel naming the actual model used), or "vertex_judge" (Vertex
	// AI's Managed Eval Service :evaluateInstances RPC). Surfaced so the
	// UI can show users which results are deterministic vs. LLM-judged,
	// answering "why/how was this score produced" (see
	// MetricsComparisonSection.tsx).
	EvaluatorKind string `json:"evaluatorKind"`
	JudgeModel    string `json:"judgeModel,omitempty"`
}

func toMetricResultDTO(r eval.Result, kind, judgeModel string) metricResultDTO {
	dto := metricResultDTO{
		MetricName:          r.MetricName,
		PerInvocationScores: make([]*float64, len(r.PerInvocationScores)),
		Details:             r.Details,
		EvaluatorKind:       kind,
		JudgeModel:          judgeModel,
	}
	for i, s := range r.PerInvocationScores {
		s := s
		dto.PerInvocationScores[i] = &s
	}
	if r.Error != "" {
		errCopy := r.Error
		dto.Error = &errCopy
		dto.EvalStatus = "ERROR"
		return dto
	}
	score := r.Score
	dto.Score = &score
	dto.EvalStatus = string(r.Status)
	return dto
}

// traceResultDTO mirrors ui/src/lib/types.ts's TraceResult. Performance metrics aren't populated yet - see
// README.md.
type traceResultDTO struct {
	TraceID            string            `json:"traceId"`
	NumInvocations     int               `json:"numInvocations"`
	MetricResults      []metricResultDTO `json:"metricResults"`
	ConversionWarnings []string          `json:"conversionWarnings"`
	AgentName          string            `json:"agentName,omitempty"`
	// Model is the model the trace called (the first LLM span's), "" when it is not known.
	Model string `json:"model,omitempty"`
}

// runResultDTO mirrors ui/src/lib/types.ts's RunResult.
type runResultDTO struct {
	TraceResults []traceResultDTO `json:"traceResults"`
	Errors       []string         `json:"errors"`
}

// judgeMetrics are the metric names routed to internal/judge instead of
// eval.RunMetric, since they need a live judge-model call. Mirrors
// cmd/ego/main.go's judgeMetrics map.
var judgeMetrics = map[string]bool{
	"final_response_match_v2":                true,
	"hallucinations_v1":                      true,
	"rubric_based_final_response_quality_v1": true,
	"rubric_based_tool_use_quality_v1":       true,
}

// vertexEvalMetrics are the metric names routed to internal/vertexeval
// (Vertex AI's Managed Eval Service :evaluateInstances RPC) instead of a
// direct judge-model call - architecturally distinct from judgeMetrics,
// see internal/vertexeval's package doc. Mirrors
// cmd/ego/main.go's vertexEvalMetrics map.
var vertexEvalMetrics = map[string]bool{
	"safety_v1":                        true,
	"multi_turn_task_success_v1":       true,
	"multi_turn_trajectory_quality_v1": true,
	"multi_turn_tool_use_quality_v1":   true,
}

// defaultThreshold is the pass threshold of an evaluator that sets none.
const defaultThreshold = 0.5

// vertexEvalServiceLabel names the "model" for vertex_judge results: the
// Vertex Managed Eval Service doesn't accept a caller-chosen model name
// (unlike judgeMetrics), it's a fixed managed backend.
const vertexEvalServiceLabel = "Vertex AI Managed Eval Service"

// metricKind classifies how a builtin metric's score is produced, so the
// UI can tell users whether a result is deterministic ("algorithm") or
// came from a live LLM-as-judge call ("llm_judge") or Vertex AI's Managed
// Eval Service ("vertex_judge").
func metricKind(name string) string {
	switch {
	case judgeMetrics[name]:
		return "llm_judge"
	case vertexEvalMetrics[name]:
		return "vertex_judge"
	case decision.IsMetric(name):
		return "llm_judge"
	default:
		return "algorithm"
	}
}

type evaluateRequest struct {
	cfg        evalConfigDTO
	traces     []*tracepkg.Trace
	evalSet    *adk.EvalSet
	loadErrors []string
	// traceFilenames maps each trace's TraceID to its source upload
	// filename, used only for Runs's evalSetItemName (see runs.go);
	// evaluation itself never needs it.
	traceFilenames map[string]string
	// tenants are the tenant keys of the stored sessions the traces come from; none for uploaded files.
	tenants []string
}

// parseEvaluateRequest reads and validates the "trace_files"/"config"/ "eval_set_file" multipart form fields
// shared by /api/evaluate and /api/evaluate/stream. Returns an HTTP status + message for the caller to report
// (via a plain JSON error or an SSE error event) when the request itself is malformed; per-file load failures
// are instead accumulated into loadErrors.
func parseEvaluateRequest(r *http.Request, sessions *SessionStore, archive *SQLiteStore) (*evaluateRequest, int, string) {
	if isJSONBody(r) {
		return parseEvaluateByID(r, sessions, archive)
	}
	limitUpload(nil, r)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return nil, http.StatusBadRequest, "invalid multipart form: " + err.Error()
	}

	configRaw := r.FormValue("config")
	if configRaw == "" {
		return nil, http.StatusBadRequest, "config is required"
	}
	var cfg evalConfigDTO
	if err := json.Unmarshal([]byte(configRaw), &cfg); err != nil {
		return nil, http.StatusBadRequest, "invalid config JSON: " + err.Error()
	}

	traceFileHeaders := r.MultipartForm.File["trace_files"]
	if len(traceFileHeaders) == 0 {
		return nil, http.StatusBadRequest, "No valid trace files provided"
	}
	if msg := validateEvalConfig(cfg, len(traceFileHeaders)); msg != "" {
		return nil, http.StatusBadRequest, msg
	}

	var traces []*tracepkg.Trace
	var loadErrors []string
	traceFilenames := map[string]string{}
	for _, fh := range traceFileHeaders {
		content, err := readMultipartFile(fh, maxUploadFileSize)
		if err != nil {
			loadErrors = append(loadErrors, fmt.Sprintf("Failed to read trace file %q: %s", fh.Filename, err))
			continue
		}
		parsed, err := loader.ParseFile(fh.Filename, content, "")
		if err != nil {
			loadErrors = append(loadErrors, fmt.Sprintf("Failed to load trace file %q: %s", fh.Filename, err))
			continue
		}
		for _, t := range parsed {
			traceFilenames[t.TraceID] = fh.Filename
		}
		traces = append(traces, parsed...)
	}

	var evalSet *adk.EvalSet
	if evalSetHeaders := r.MultipartForm.File["eval_set_file"]; len(evalSetHeaders) > 0 {
		content, err := readMultipartFile(evalSetHeaders[0], maxUploadFileSize)
		if err != nil {
			loadErrors = append(loadErrors, fmt.Sprintf("Failed to read eval set file: %s", err))
		} else {
			var set adk.EvalSet
			if err := json.Unmarshal(content, &set); err != nil {
				loadErrors = append(loadErrors, fmt.Sprintf("Failed to parse eval set file: %s", err))
			} else {
				evalSet = &set
			}
		}
	}

	req := &evaluateRequest{cfg: cfg, traces: traces, evalSet: evalSet, loadErrors: loadErrors, traceFilenames: traceFilenames}
	// The traces to score, when the reader picked some of those in the files: a JSON list of trace ids.
	if raw := r.FormValue("trace_ids"); raw != "" {
		var ids []string
		if err := json.Unmarshal([]byte(raw), &ids); err != nil {
			return nil, http.StatusBadRequest, "invalid trace_ids JSON: " + err.Error()
		}
		keepTraces(req, ids)
	}
	return req, 0, ""
}

// newJudgeModelGetter returns a memoized per-model-name judge.Model
// factory, shared by evaluateHandler and evaluateStreamHandler so each
// distinct judgeModel named across a request's evaluators is only
// constructed (and thus authenticated) once.
func newJudgeModelGetter(ctx context.Context, user string) func(string) (judge.Model, error) {
	judgeClients := map[string]judge.Model{}
	return func(modelName string) (judge.Model, error) {
		if modelName == "" {
			modelName = judge.DefaultModel
		}
		if m, ok := judgeClients[modelName]; ok {
			return m, nil
		}
		m, err := judge.NewGenAIModel(ctx, "", modelName)
		if err != nil {
			return nil, fmt.Errorf("setting up judge model %s: %w", modelName, err)
		}
		m.WithUsageHook(func(tokens int64) { interactiveEval.addFor(user, tokens) })
		judgeClients[modelName] = m
		return m, nil
	}
}

// newVertexEvalClientGetter returns a memoized vertexeval.Client factory
// (there's only ever one: the Vertex Managed Eval Service client has no
// per-evaluator model-name variance, unlike judge.Model), shared by
// evaluateHandler and evaluateStreamHandler so it's only constructed
// (and thus authenticated) once per request even if several evaluators
// need it.
func newVertexEvalClientGetter(ctx context.Context) func() (*vertexeval.Client, error) {
	var client *vertexeval.Client
	var err error
	var built bool
	return func() (*vertexeval.Client, error) {
		if !built {
			client, err = vertexeval.NewClient(ctx)
			built = true
		}
		return client, err
	}
}

// runJudgeMetric dispatches one judgeMetrics-listed metric name to its
// internal/judge implementation.
func runJudgeMetric(ctx context.Context, name string, model judge.Model, actual, expected []adk.Invocation, rubricTexts []string, numSamples int, threshold float64) eval.Result {
	switch name {
	case "final_response_match_v2":
		if expected == nil {
			return eval.Result{MetricName: name, Error: fmt.Sprintf(
				"Metric '%s' requires expected invocations (golden eval set), but none were provided or matched.", name)}
		}
		r, err := judge.FinalResponseMatchV2(ctx, model, actual, expected, numSamples, threshold)
		if err != nil {
			return eval.Result{MetricName: name, Error: err.Error()}
		}
		return r
	case "hallucinations_v1":
		r, err := judge.HallucinationsV1(ctx, model, actual, threshold)
		if err != nil {
			return eval.Result{MetricName: name, Error: err.Error()}
		}
		return r
	case "rubric_based_final_response_quality_v1", "rubric_based_tool_use_quality_v1":
		rubrics := judge.RubricsFromStrings(rubricTexts)
		if len(rubrics) == 0 {
			return eval.Result{MetricName: name, Error: "Rubrics are required for this metric; supply evaluator.rubrics (a list of rubric-text strings)."}
		}
		var r eval.Result
		var err error
		if name == "rubric_based_final_response_quality_v1" {
			r, err = judge.RubricBasedFinalResponseQualityV1(ctx, model, actual, rubrics, numSamples, threshold)
		} else {
			r, err = judge.RubricBasedToolUseQualityV1(ctx, model, actual, rubrics, numSamples, threshold)
		}
		if err != nil {
			return eval.Result{MetricName: name, Error: err.Error()}
		}
		return r
	default:
		return eval.Result{MetricName: name, Error: fmt.Sprintf("unknown judge metric %q", name)}
	}
}

// runVertexEvalMetric dispatches one vertexEvalMetrics-listed metric name
// to its internal/vertexeval implementation.
func runVertexEvalMetric(ctx context.Context, name string, client *vertexeval.Client, actual, expected []adk.Invocation, threshold float64) eval.Result {
	var r eval.Result
	var err error
	switch name {
	case "safety_v1":
		r, err = vertexeval.SafetyV1(ctx, client, actual, expected, threshold)
	case "multi_turn_task_success_v1":
		r, err = vertexeval.MultiTurnTaskSuccessV1(ctx, client, actual, threshold)
	case "multi_turn_trajectory_quality_v1":
		r, err = vertexeval.MultiTurnTrajectoryQualityV1(ctx, client, actual, threshold)
	case "multi_turn_tool_use_quality_v1":
		r, err = vertexeval.MultiTurnToolUseQualityV1(ctx, client, actual, threshold)
	default:
		return eval.Result{MetricName: name, Error: fmt.Sprintf("unknown vertex-eval metric %q", name)}
	}
	if err != nil {
		return eval.Result{MetricName: name, Error: err.Error()}
	}
	return r
}

// evaluateHandler implements POST /api/evaluate: scores every uploaded trace file (Jaeger or OTLP JSON/JSONL,
// auto-detected) against an optional golden eval set, using the evaluator list in the "config" form field.
func evaluateHandler(store *SQLiteStore, sessions *SessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !roleInfoOrOpen(r.Context()).CanWrite() {
			writeEvaluateError(w, http.StatusForbidden, "this role cannot run evaluations")
			return
		}

		req, status, msg := parseEvaluateRequest(r, sessions, store)
		if r.MultipartForm != nil {
			defer r.MultipartForm.RemoveAll()
		}
		if msg != "" {
			writeEvaluateError(w, status, msg)
			return
		}

		if len(req.traces) == 0 {
			writeJSON(w, http.StatusOK, map[string]any{
				"data":  runResultDTO{Errors: append(req.loadErrors, "No traces loaded.")},
				"error": nil,
			})
			return
		}

		user, admin := callerOf(r)
		release, status, msg := interactiveEval.admit(time.Now(), user, admin)
		if release == nil {
			writeEvaluateError(w, status, msg)
			return
		}
		defer release()

		ctx := r.Context()
		getJudgeModel := newJudgeModelGetter(ctx, user)
		getVertexEvalClient := newVertexEvalClientGetter(ctx)

		run := beginRun(store, runSpecFromRequest(req))
		result := runResultDTO{Errors: req.loadErrors}
		for _, tr := range req.traces {
			if ctx.Err() != nil {
				break
			}
			result.TraceResults = append(result.TraceResults, evaluateOneTrace(ctx, tr, req.cfg.Evaluators, req.evalSet, getJudgeModel, getVertexEvalClient, newDecisionClientGetter(), nil))
		}
		finishRun(store, run, req, result, ctx.Err() != nil)

		writeJSON(w, http.StatusOK, map[string]any{"data": result, "error": nil})
	}
}

// evaluateStreamHandler implements POST /api/evaluate/stream: identical scoring to evaluateHandler, but reports
// progress over Server-Sent Events as each trace/metric completes instead of a single response at the end. The
// UI (ui/src/context/TraceProvider.tsx) always calls this endpoint, not POST /api/evaluate directly.
//
// Un. This keeps the connection flushing during a slow judge-model call (30-40s+) so an intermediate proxy
// buffering an idle streaming response doesn't silently swallow the eventual "done" event.
func evaluateStreamHandler(store *SQLiteStore, sessions *SessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !roleInfoOrOpen(r.Context()).CanWrite() {
			writeEvaluateError(w, http.StatusForbidden, "this role cannot run evaluations")
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		req, _, msg := parseEvaluateRequest(r, sessions, store)
		if r.MultipartForm != nil {
			defer r.MultipartForm.RemoveAll()
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)

		if msg != "" {
			writeSSEData(w, map[string]any{"error": msg})
			flusher.Flush()
			return
		}

		user, admin := callerOf(r)
		release, _, admitMsg := interactiveEval.admit(time.Now(), user, admin)
		if release == nil {
			writeSSEData(w, map[string]any{"error": admitMsg})
			flusher.Flush()
			return
		}

		ctx := r.Context()
		events := make(chan map[string]any, 16)

		go func() {
			defer close(events)
			defer release()
			// A client that has gone away must not leave this goroutine blocked on a full channel, holding the
			// uploaded traces: sends give up when the request context ends.
			send := func(ev map[string]any) {
				select {
				case events <- ev:
				case <-ctx.Done():
				}
			}

			if len(req.traces) == 0 {
				send(map[string]any{"error": "No traces loaded."})
				return
			}

			getJudgeModel := newJudgeModelGetter(ctx, user)
			getVertexEvalClient := newVertexEvalClientGetter(ctx)
			getDecisionClient := newDecisionClientGetter()
			total := len(req.traces)
			send(map[string]any{"message": fmt.Sprintf("Evaluating %d trace(s)...", total)})

			run := beginRun(store, runSpecFromRequest(req))
			result := runResultDTO{Errors: req.loadErrors}
			for idx, tr := range req.traces {
				if ctx.Err() != nil {
					break
				}
				send(map[string]any{"message": fmt.Sprintf("Trace %d/%d: %s", idx+1, total, tr.TraceID)})

				traceResult := evaluateOneTrace(ctx, tr, req.cfg.Evaluators, req.evalSet, getJudgeModel, getVertexEvalClient, getDecisionClient, func(partial traceResultDTO) {
					send(map[string]any{"traceProgress": map[string]any{
						"traceId":       partial.TraceID,
						"partialResult": partial,
					}})
				})
				result.TraceResults = append(result.TraceResults, traceResult)
			}

			// A stop (or a client that went away) ends the request context: the run keeps what was scored so far.
			finishRun(store, run, req, result, ctx.Err() != nil)

			send(map[string]any{"message": "Evaluation complete"})
			send(map[string]any{"done": true, "result": result})
		}()

		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case ev, open := <-events:
				if !open {
					return
				}
				writeSSEData(w, ev)
				flusher.Flush()
				if ev["done"] != nil || ev["error"] != nil {
					return
				}
			case <-ticker.C:
				fmt.Fprint(w, ": ping\n\n")
				flusher.Flush()
			case <-ctx.Done():
				return
			}
		}
	}
}

func writeSSEData(w http.ResponseWriter, payload map[string]any) {
	data, err := json.Marshal(payload)
	if err != nil {
		data = []byte(fmt.Sprintf(`{"error":%q}`, "failed to encode event: "+err.Error()))
	}
	fmt.Fprintf(w, "data: %s\n\n", data)
}

// evaluateOneTrace scores a single trace against evaluators, optionally invoking onMetric after each metric
// result is appended (streaming progress) -.
func evaluateOneTrace(
	ctx context.Context,
	tr *tracepkg.Trace,
	evaluators []evaluatorConfigDTO,
	evalSet *adk.EvalSet,
	getJudgeModel func(string) (judge.Model, error),
	getVertexEvalClient func() (*vertexeval.Client, error),
	getDecisionClient func() (*decision.Client, error),
	onMetric func(traceResultDTO),
) traceResultDTO {
	conv := adk.ConvertTrace(tr)
	meta := adk.ExtractTraceMetadata(tr, conv.Invocations)
	traceResult := traceResultDTO{
		TraceID:        tr.TraceID,
		NumInvocations: len(conv.Invocations),
		// append onto a non-nil empty slice (rather than assigning conv.Warnings
		// directly) so a nil conv.Warnings still marshals as JSON [] not null -
		// the UI reads conversionWarnings.length without optional chaining.
		ConversionWarnings: append([]string{}, conv.Warnings...),
		MetricResults:      []metricResultDTO{},
		AgentName:          meta.AgentName,
		Model:              meta.Model,
	}

	if len(conv.Invocations) == 0 {
		traceResult.MetricResults = []metricResultDTO{toMetricResultDTO(eval.Result{
			MetricName: "(all)",
			Error:      "No invocations extracted from trace.",
		}, "", "")}
		return traceResult
	}

	return evaluateInvocations(ctx, traceResult, conv.Invocations, evaluators, evalSet, getJudgeModel, getVertexEvalClient, getDecisionClient, onMetric)
}

// evaluateInvocations scores already-converted invocations into
// traceResult; evaluateOneTrace's second half, split out so online
// evaluation can score a trimmed invocation list.
func evaluateInvocations(
	ctx context.Context,
	traceResult traceResultDTO,
	invocations []adk.Invocation,
	evaluators []evaluatorConfigDTO,
	evalSet *adk.EvalSet,
	getJudgeModel func(string) (judge.Model, error),
	getVertexEvalClient func() (*vertexeval.Client, error),
	getDecisionClient func() (*decision.Client, error),
	onMetric func(traceResultDTO),
) traceResultDTO {
	expected := eval.FindExpectedInvocations(invocations, evalSet)
	decisionResults, decisionModel := runDecisionEvaluators(ctx, evaluators, invocations, expected, getDecisionClient)

	for i, evaluator := range evaluators {
		if ctx.Err() != nil {
			break
		}
		if results, ok := decisionResults[i]; ok {
			for _, r := range results {
				traceResult.MetricResults = append(traceResult.MetricResults, toMetricResultDTO(r, "llm_judge", decisionModel))
				notifyMetric(traceResult, onMetric)
			}
			continue
		}
		if evaluator.Type != "" && evaluator.Type != "builtin" {
			traceResult.MetricResults = append(traceResult.MetricResults, toMetricResultDTO(eval.Result{
				MetricName: evaluator.Name,
				Error:      fmt.Sprintf("evaluator type %q is not supported (only builtin evaluators are supported)", evaluator.Type),
			}, "", ""))
			notifyMetric(traceResult, onMetric)
			continue
		}

		threshold := defaultThreshold
		if evaluator.Threshold != nil {
			threshold = *evaluator.Threshold
		}

		kind := metricKind(evaluator.Name)
		judgeModelUsed := ""

		var result eval.Result
		switch {
		case judgeMetrics[evaluator.Name]:
			modelName := evaluator.JudgeModel
			if modelName == "" {
				modelName = judge.DefaultModel
			}
			judgeModelUsed = modelName
			model, err := getJudgeModel(evaluator.JudgeModel)
			if err != nil {
				result = eval.Result{MetricName: evaluator.Name, Error: err.Error()}
			} else {
				numSamples := judge.DefaultNumSamples
				if evaluator.NumSamples > 0 {
					numSamples = evaluator.NumSamples
				}
				result = runJudgeMetric(ctx, evaluator.Name, model, invocations, expected, evaluator.Rubrics, numSamples, threshold)
			}
		case vertexEvalMetrics[evaluator.Name]:
			judgeModelUsed = vertexEvalServiceLabel
			client, err := getVertexEvalClient()
			if err != nil {
				result = eval.Result{MetricName: evaluator.Name, Error: err.Error()}
			} else {
				result = runVertexEvalMetric(ctx, evaluator.Name, client, invocations, expected, threshold)
			}
		default:
			matchType, err := eval.ParseMatchType(evaluator.TrajectoryMatchType)
			if err != nil {
				result = eval.Result{MetricName: evaluator.Name, Error: err.Error()}
			} else {
				result = eval.RunMetric(evaluator.Name, invocations, expected, matchType, threshold)
			}
		}
		if result.Error != "" {
			logx.Warnf("evaluation: %s failed on trace %s: %s", evaluator.Name, traceResult.TraceID, result.Error)
		}
		traceResult.MetricResults = append(traceResult.MetricResults, toMetricResultDTO(result, kind, judgeModelUsed))
		notifyMetric(traceResult, onMetric)
	}

	return traceResult
}

// notifyMetric calls onMetric (if non-nil) with a snapshot of tr whose
// MetricResults slice is copied. Without this, since onMetric hands the
// value to a buffered channel consumed by a separate goroutine, later
// append() calls in evaluateOneTrace's loop could reuse the same backing
// array and mutate an already-queued-but-not-yet-marshaled event.
func notifyMetric(tr traceResultDTO, onMetric func(traceResultDTO)) {
	if onMetric == nil {
		return
	}
	snapshot := tr
	snapshot.MetricResults = append([]metricResultDTO(nil), tr.MetricResults...)
	onMetric(snapshot)
}

// readMultipartFile reads an uploaded file's content, rejecting it once maxSize bytes have been read (.
func readMultipartFile(fh *multipart.FileHeader, maxSize int64) ([]byte, error) {
	f, err := fh.Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()

	limited := io.LimitReader(f, maxSize+1)
	content, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > maxSize {
		return nil, fmt.Errorf("exceeds %dMB limit", maxSize/(1<<20))
	}
	return content, nil
}

func writeEvaluateError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"data": nil, "error": message})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// callerOf names who made a request, for the per-user share of the evaluation budget: the signed-in user or token
// label, else the remote address, and whether the caller is an admin.
func callerOf(r *http.Request) (user string, admin bool) {
	if id, ok := identityFromContext(r.Context()); ok && id.Username != "" {
		user = id.Username
	} else {
		user = sourceOf(r.RemoteAddr)
	}
	return user, roleInfoOrOpen(r.Context()).IsAdmin
}
