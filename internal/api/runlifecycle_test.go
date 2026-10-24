package api

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/triageagent-dev/ego/internal/otlp"
)

func runOnce(t *testing.T, ctx context.Context, store *SQLiteStore, config string) runDTO {
	t.Helper()
	content, err := otlp.EncodeTraceJSONL(buildTrajectoryTrace())
	if err != nil {
		t.Fatalf("EncodeTraceJSONL: %v", err)
	}
	req := multipartEvaluateRequest(t, content, nil, config).WithContext(ctx)
	evaluateHandler(store, nil)(httptest.NewRecorder(), req)
	runs, err := store.ListRuns(nil, 10, "")
	if err != nil || len(runs) != 1 {
		t.Fatalf("ListRuns = %v, %v; want one run", runs, err)
	}
	return runs[0]
}

func newRunStore(t *testing.T) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// A run whose evaluation was stopped is on the list as cancelled, not missing.
func TestStoppedEvaluationIsRecordedAsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	run := runOnce(t, ctx, newRunStore(t), `{"evaluators":[{"type":"builtin","name":"tool_trajectory_avg_score"}]}`)
	if run.Status != "cancelled" || run.Error == nil {
		t.Fatalf("run = %s / %v, want cancelled with a reason", run.Status, run.Error)
	}
}

// A run in which nothing could be scored says why on the run itself.
func TestRunThatCouldNotScoreAnythingIsFailedWithTheReason(t *testing.T) {
	run := runOnce(t, context.Background(), newRunStore(t), `{"evaluators":[{"type":"builtin","name":"final_response_match_v2"}]}`)
	if run.Status != "failed" || run.Error == nil || !strings.Contains(*run.Error, "final_response_match_v2") {
		t.Fatalf("run = %s / %v, want failed naming the metric", run.Status, run.Error)
	}
}

// A manual evaluation still running when the server stopped is closed at the next start; an online run is left open.
func TestInterruptedManualRunsAreClosedAtStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	manual, _ := store.CreateRun(runSpecSummaryDTO{Approach: "traces"})
	online, _ := store.CreateRun(runSpecSummaryDTO{Approach: "online"})
	store.Close()

	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if got, _, _ := store.GetRun(manual.RunID); got.Status != "failed" {
		t.Errorf("manual run status = %q, want failed", got.Status)
	}
	if got, _, _ := store.GetRun(online.RunID); got.Status != "running" {
		t.Errorf("online run status = %q, want running", got.Status)
	}
}
