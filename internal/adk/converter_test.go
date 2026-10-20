package adk_test

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/triageagent-dev/ego/internal/adk"
	"github.com/triageagent-dev/ego/internal/eval"
	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

// samplesDir resolves samples/ relative to this test file, so `go test ./...`
// works from any working directory.
func samplesDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not resolve test file path")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "samples")
}

func TestQuickstart_HelmTraceMatchesGolden(t *testing.T) {
	dir := samplesDir(t)

	traces, err := tracepkg.LoadJaegerJSON(filepath.Join(dir, "helm.json"))
	if err != nil {
		t.Fatalf("loading helm.json: %v", err)
	}
	if len(traces) != 1 {
		t.Fatalf("got %d traces, want 1", len(traces))
	}

	evalSet, err := eval.LoadEvalSet(filepath.Join(dir, "eval_set_helm.json"))
	if err != nil {
		t.Fatalf("loading eval_set_helm.json: %v", err)
	}

	conv := adk.ConvertADKTrace(traces[0])
	if len(conv.Warnings) != 0 {
		t.Fatalf("unexpected conversion warnings: %v", conv.Warnings)
	}
	if len(conv.Invocations) != 1 {
		t.Fatalf("got %d invocations, want 1", len(conv.Invocations))
	}

	result := eval.RunMetric("tool_trajectory_avg_score", conv.Invocations, eval.FindExpectedInvocations(conv.Invocations, evalSet), eval.MatchExact, 0.5)
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if result.Score != 1.0 {
		t.Errorf("score = %v, want 1.0", result.Score)
	}
	if result.Status != eval.StatusPassed {
		t.Errorf("status = %v, want PASSED", result.Status)
	}
}

func TestQuickstart_K8sTraceMismatch(t *testing.T) {
	dir := samplesDir(t)

	traces, err := tracepkg.LoadJaegerJSON(filepath.Join(dir, "k8s.json"))
	if err != nil {
		t.Skipf("samples/k8s.json not present: %v", err)
	}

	evalSet, err := eval.LoadEvalSet(filepath.Join(dir, "eval_set_helm.json"))
	if err != nil {
		t.Fatalf("loading eval_set_helm.json: %v", err)
	}

	conv := adk.ConvertADKTrace(traces[0])
	expected := eval.FindExpectedInvocations(conv.Invocations, evalSet)
	result := eval.RunMetric("tool_trajectory_avg_score", conv.Invocations, expected, eval.MatchExact, 0.5)

	if result.Score != 0.0 {
		t.Errorf("score = %v, want 0.0 (no matching tool call)", result.Score)
	}
	if result.Status != eval.StatusFailed {
		t.Errorf("status = %v, want FAILED", result.Status)
	}
}
