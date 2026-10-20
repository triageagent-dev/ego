package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/triageagent-dev/ego/internal/otlp"
	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

// samplesDir resolves samples/ relative to this test file, so `go test
// ./...` works from any working directory. Mirrors internal/adk/
// converter_test.go's helper of the same name (each package keeps its own
// copy rather than sharing a test-only import across package boundaries).
func samplesDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not resolve test file path")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "samples")
}

// structuredContent decodes a CallToolResult produced by
// mcp.NewToolResultStructuredOnly back into v, the way a real MCP client
// would read the tool's structured JSON content.
func structuredContent(t *testing.T, result *mcp.CallToolResult, v any) {
	t.Helper()
	if result.IsError {
		var msg strings.Builder
		for _, c := range result.Content {
			if tc, ok := c.(mcp.TextContent); ok {
				msg.WriteString(tc.Text)
			}
		}
		t.Fatalf("tool returned an error result: %s", msg.String())
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshaling structured content: %v", err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("decoding structured content into %T: %v", v, err)
	}
}

func TestListMetricsHandler_ReturnsSnakeCaseFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/metrics" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"name":"tool_trajectory_avg_score","category":"trajectory","requiresEvalSet":true,"requiresLLM":false,"requiresGCP":false,"requiresRubrics":false,"description":"d","working":true}],"error":null}`))
	}))
	defer srv.Close()

	backend := &mcpBackend{baseURL: srv.URL, client: srv.Client()}
	result, err := listMetricsHandler(backend)(t.Context(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("listMetricsHandler: %v", err)
	}

	var out []metricInfoMCP
	structuredContent(t, result, &out)
	if len(out) != 1 || out[0].Name != "tool_trajectory_avg_score" || !out[0].RequiresEvalSet || !out[0].Working {
		t.Fatalf("unexpected metrics list: %+v", out)
	}
}

func TestListMetricsHandler_ServerDown(t *testing.T) {
	backend := &mcpBackend{baseURL: "http://127.0.0.1:1", client: http.DefaultClient}
	result, err := listMetricsHandler(backend)(t.Context(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("handler itself should not error, got: %v", err)
	}
	if !result.IsError {
		t.Fatal("want an error result when the server is unreachable")
	}
}

func TestListSessionsHandler_SortsMostRecentFirstAndAppliesLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"sessionId":"old","isComplete":true,"spanCount":3,"startedAt":"2024-01-01T00:00:00Z"},
			{"sessionId":"new","isComplete":false,"spanCount":5,"startedAt":"2024-06-01T00:00:00Z"}
		],"error":null}`))
	}))
	defer srv.Close()

	backend := &mcpBackend{baseURL: srv.URL, client: srv.Client()}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"limit": float64(1)}
	result, err := listSessionsHandler(backend)(t.Context(), req)
	if err != nil {
		t.Fatalf("listSessionsHandler: %v", err)
	}

	var out []sessionSummaryMCP
	structuredContent(t, result, &out)
	if len(out) != 1 || out[0].SessionID != "new" {
		t.Fatalf("want most-recent session first, limited to 1: %+v", out)
	}
}

func TestSummarizeSessionHandler_ParsesTraceContentIntoInvocations(t *testing.T) {
	dir := samplesDir(t)
	traces, err := tracepkg.LoadJaegerJSON(filepath.Join(dir, "helm.json"))
	if err != nil {
		t.Fatalf("loading helm.json: %v", err)
	}
	if len(traces) != 1 {
		t.Fatalf("got %d traces, want 1", len(traces))
	}
	jsonl, err := otlp.EncodeTraceJSONL(traces[0])
	if err != nil {
		t.Fatalf("encoding trace as JSONL: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/streaming/get-trace" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		var body struct {
			SessionID string `json:"session_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decoding request body: %v", err)
		}
		if body.SessionID != "sess-1" {
			t.Fatalf("session_id = %q, want sess-1", body.SessionID)
		}
		resp := map[string]any{
			"data": map[string]any{
				"sessionId":    "sess-1",
				"traceContent": string(jsonl),
				"numSpans":     len(traces[0].AllSpans),
			},
			"error": nil,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	backend := &mcpBackend{baseURL: srv.URL, client: srv.Client()}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"session_id": "sess-1"}
	result, err := summarizeSessionHandler(backend)(t.Context(), req)
	if err != nil {
		t.Fatalf("summarizeSessionHandler: %v", err)
	}

	var out summarizeSessionResultMCP
	structuredContent(t, result, &out)
	if out.SessionID != "sess-1" {
		t.Errorf("session_id = %q, want sess-1", out.SessionID)
	}
	if out.NumSpans != len(traces[0].AllSpans) {
		t.Errorf("num_spans = %d, want %d", out.NumSpans, len(traces[0].AllSpans))
	}
	if out.NumInvocations == 0 || len(out.Invocations) != out.NumInvocations {
		t.Fatalf("want at least one invocation, got num_invocations=%d len(invocations)=%d", out.NumInvocations, len(out.Invocations))
	}
	if out.Invocations[0].User == "" {
		t.Error("first invocation's user text is empty, want the helm sample's actual prompt")
	}
}

func TestListRunsHandler_FiltersByStatusAndAppliesLimit(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/runs" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"runId":"run-1","status":"succeeded","spec":{"approach":"traces"},"createdAt":"2024-01-01T00:00:00Z",
			 "summary":{"trace_count":2,"result_counts":{"passed":2,"failed":0,"errored":0,"skipped":0}}}
		],"error":null}`))
	}))
	defer srv.Close()

	backend := &mcpBackend{baseURL: srv.URL, client: srv.Client()}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"limit": float64(5), "status": []any{"succeeded"}}
	result, err := listRunsHandler(backend)(t.Context(), req)
	if err != nil {
		t.Fatalf("listRunsHandler: %v", err)
	}

	if !strings.Contains(gotQuery, "limit=5") || !strings.Contains(gotQuery, "status=succeeded") {
		t.Fatalf("query = %q, want limit=5 and status=succeeded", gotQuery)
	}

	var out []runListItemMCP
	structuredContent(t, result, &out)
	if len(out) != 1 || out[0].RunID != "run-1" || out[0].Approach != "traces" {
		t.Fatalf("unexpected runs list: %+v", out)
	}
	if out[0].Summary == nil || out[0].Summary.TraceCount != 2 || out[0].Summary.ResultCounts.Passed != 2 {
		t.Fatalf("unexpected run summary: %+v", out[0].Summary)
	}
}

func TestListRunsHandler_StorageUnavailableReportsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"data":null,"error":"Run history storage is not configured (start with --session-db to enable /api/runs)"}`))
	}))
	defer srv.Close()

	backend := &mcpBackend{baseURL: srv.URL, client: srv.Client()}
	result, err := listRunsHandler(backend)(t.Context(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("handler itself should not error, got: %v", err)
	}
	if !result.IsError {
		t.Fatal("want an error result when run history storage is unavailable")
	}
}

func TestGetRunResultsHandler_CombinesRunSummaryAndResultRows(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/runs/run-1":
			_, _ = w.Write([]byte(`{"data":{"runId":"run-1","status":"succeeded","spec":{"approach":"traces"},
				"createdAt":"2024-01-01T00:00:00Z",
				"summary":{"trace_count":1,"result_counts":{"passed":1,"failed":0,"errored":0,"skipped":0}}},"error":null}`))
		case "/api/runs/run-1/results":
			_, _ = w.Write([]byte(`{"data":[{"resultId":"res-1","evalSetItemId":"item-1","evalSetItemName":"case 1",
				"evaluatorName":"tool_trajectory_avg_score","evaluatorType":"local","status":"passed","score":1.0,
				"perInvocationScores":[1.0],"createdAt":"2024-01-01T00:00:01Z"}],"error":null}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	backend := &mcpBackend{baseURL: srv.URL, client: srv.Client()}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"run_id": "run-1"}
	result, err := getRunResultsHandler(backend)(t.Context(), req)
	if err != nil {
		t.Fatalf("getRunResultsHandler: %v", err)
	}

	var out getRunResultsResultMCP
	structuredContent(t, result, &out)
	if out.RunID != "run-1" || out.Approach != "traces" {
		t.Fatalf("unexpected run: %+v", out)
	}
	if out.Summary == nil || out.Summary.TraceCount != 1 {
		t.Fatalf("unexpected summary: %+v", out.Summary)
	}
	if len(out.Results) != 1 || out.Results[0].ResultID != "res-1" || out.Results[0].EvaluatorName != "tool_trajectory_avg_score" {
		t.Fatalf("unexpected results: %+v", out.Results)
	}
	if out.Results[0].Score == nil || *out.Results[0].Score != 1.0 {
		t.Fatalf("unexpected score: %+v", out.Results[0].Score)
	}
}

func TestEvaluateTracesHandler_OfflineHelmQuickstart(t *testing.T) {
	dir := samplesDir(t)
	traceFile := filepath.Join(dir, "helm.json")
	evalSetFile := filepath.Join(dir, "eval_set_helm.json")

	result, err := runEvaluateTraces(
		t.Context(),
		[]string{traceFile},
		[]string{"tool_trajectory_avg_score"},
		"", evalSetFile, "", 0.5, nil, "",
	)
	if err != nil {
		t.Fatalf("runEvaluateTraces: %v", err)
	}
	if !result.Passed {
		t.Errorf("passed = false, want true (%+v)", result)
	}
	if len(result.Traces) != 1 {
		t.Fatalf("got %d traces, want 1", len(result.Traces))
	}
	tr := result.Traces[0]
	if len(tr.Metrics) != 1 {
		t.Fatalf("got %d metric results, want 1", len(tr.Metrics))
	}
	m := tr.Metrics[0]
	if m.Metric != "tool_trajectory_avg_score" || m.Status != "PASSED" || m.Score == nil || *m.Score != 1.0 {
		t.Errorf("unexpected metric result: %+v", m)
	}
}

func TestEvaluateTracesHandler_MetricErrorFailsAndReportsErrorStatus(t *testing.T) {
	dir := samplesDir(t)
	traceFile := filepath.Join(dir, "helm.json")

	// tool_trajectory_avg_score with no eval set errors per-metric (it
	// needs expected invocations to compare against) rather than failing
	// to load the trace file at all - this must flip "passed" to false
	// and report status "ERROR", not silently read as passing.
	result, err := runEvaluateTraces(
		t.Context(),
		[]string{traceFile},
		[]string{"tool_trajectory_avg_score"},
		"", "", "", 0.5, nil, "",
	)
	if err != nil {
		t.Fatalf("runEvaluateTraces: %v", err)
	}
	if result.Passed {
		t.Error("passed = true, want false when a metric errors")
	}
	if len(result.Traces) != 1 || len(result.Traces[0].Metrics) != 1 {
		t.Fatalf("unexpected result shape: %+v", result)
	}
	m := result.Traces[0].Metrics[0]
	if m.Status != "ERROR" || m.Error == "" || m.Score != nil {
		t.Errorf("unexpected metric result: %+v", m)
	}
}

func TestEvaluateTracesHandler_MissingFileReportsErrorNotPanic(t *testing.T) {
	result, err := runEvaluateTraces(
		t.Context(),
		[]string{"/nonexistent/trace.json"},
		[]string{"tool_trajectory_avg_score"},
		"", "", "", 0.5, nil, "",
	)
	if err != nil {
		t.Fatalf("runEvaluateTraces: %v", err)
	}
	if !result.Passed {
		t.Error("passed = false, want true (no metric ran, so none failed)")
	}
	if len(result.Errors) != 1 {
		t.Fatalf("got %d top-level errors, want 1: %+v", len(result.Errors), result.Errors)
	}
}

func TestListRoleBindingsHandler_ReturnsSnakeCaseFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/admin/roles" || r.Method != http.MethodGet {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"rb-1","subjectType":"user","subject":"octocat","role":"admin","createdAt":"2024-01-01T00:00:00Z","updatedAt":"2024-01-01T00:00:00Z"}],"error":null}`))
	}))
	defer srv.Close()

	backend := &mcpBackend{baseURL: srv.URL, client: srv.Client()}
	result, err := listRoleBindingsHandler(backend)(t.Context(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("listRoleBindingsHandler: %v", err)
	}
	var out []roleBindingMCP
	structuredContent(t, result, &out)
	if len(out) != 1 || out[0].ID != "rb-1" || out[0].SubjectType != "user" || out[0].Subject != "octocat" || out[0].Role != "admin" {
		t.Fatalf("unexpected role bindings: %+v", out)
	}
}

func TestListRoleBindingsHandler_AdminRequiredReportsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"data":null,"error":"admin role required"}`))
	}))
	defer srv.Close()

	backend := &mcpBackend{baseURL: srv.URL, client: srv.Client()}
	result, err := listRoleBindingsHandler(backend)(t.Context(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("handler itself should not error, got: %v", err)
	}
	if !result.IsError {
		t.Fatal("want an error result when caller is not an admin")
	}
}

func TestSetRoleBindingHandler_CreatesAndUpdates(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/admin/roles" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":"rb-2","subjectType":"team","subject":"acme/sre","role":"viewer","agents":["billing-agent"],"createdAt":"2024-01-01T00:00:00Z","updatedAt":"2024-01-01T00:00:00Z"},"error":null}`))
	}))
	defer srv.Close()

	backend := &mcpBackend{baseURL: srv.URL, client: srv.Client()}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"subject_type": "team",
		"subject":      "acme/sre",
		"role":         "viewer",
		"agents":       []any{"billing-agent"},
	}
	result, err := setRoleBindingHandler(backend)(t.Context(), req)
	if err != nil {
		t.Fatalf("setRoleBindingHandler: %v", err)
	}
	var out roleBindingMCP
	structuredContent(t, result, &out)
	if out.ID != "rb-2" || out.SubjectType != "team" || out.Subject != "acme/sre" || out.Role != "viewer" || len(out.Agents) != 1 {
		t.Fatalf("unexpected role binding: %+v", out)
	}
	if gotBody["subjectType"] != "team" || gotBody["subject"] != "acme/sre" || gotBody["role"] != "viewer" {
		t.Fatalf("unexpected request body sent: %+v", gotBody)
	}
	if _, hasID := gotBody["id"]; hasID {
		t.Fatalf("id should be omitted from the request body when not creating an update, got: %+v", gotBody)
	}
}

func TestDeleteRoleBindingHandler_Succeeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/admin/roles/rb-1" || r.Method != http.MethodDelete {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"deleted":true},"error":null}`))
	}))
	defer srv.Close()

	backend := &mcpBackend{baseURL: srv.URL, client: srv.Client()}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"id": "rb-1"}
	result, err := deleteRoleBindingHandler(backend)(t.Context(), req)
	if err != nil {
		t.Fatalf("deleteRoleBindingHandler: %v", err)
	}
	var out map[string]bool
	structuredContent(t, result, &out)
	if !out["deleted"] {
		t.Fatalf("unexpected delete response: %+v", out)
	}
}

func TestCheckSessionSecret(t *testing.T) {
	if err := checkSessionSecret(""); err != nil {
		t.Errorf("empty secret (auth off) = %v, want nil", err)
	}
	if err := checkSessionSecret("short"); err == nil {
		t.Error("short secret accepted")
	}
	if err := checkSessionSecret(strings.Repeat("k", minSecretLen)); err != nil {
		t.Errorf("long enough secret = %v", err)
	}
}
