package main

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
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
