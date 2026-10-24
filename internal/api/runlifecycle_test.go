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

// A run belongs to the tenants of the sessions it was started on: the switcher shows it under those and no others,
// and a run that cannot be attributed shows under "all tenants" only.
func TestPickedTenantListsRunsOfThatTenant(t *testing.T) {
	store := newRunStore(t)
	mine, _ := store.CreateRun(runSpecSummaryDTO{Approach: "traces", Tenants: []string{"s/mine"}})
	other, _ := store.CreateRun(runSpecSummaryDTO{Approach: "traces", Tenants: []string{"s/other"}})
	upload, _ := store.CreateRun(runSpecSummaryDTO{Approach: "traces"})
	// An online run is attributed by its own spec even when its session rows are gone.
	online, _ := store.CreateRun(runSpecSummaryDTO{Approach: "online", Target: map[string]any{"tenant": "s/mine"}})

	ids := func(info RoleInfo, pick string) map[string]bool {
		runs, err := store.ListRunsFor(info, pick, nil, 10, "")
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]bool{}
		for _, r := range runs {
			got[r.RunID] = true
		}
		return got
	}
	if got := ids(RoleInfo{}, "s/mine"); len(got) != 2 || !got[mine.RunID] || !got[online.RunID] {
		t.Errorf("picked tenant shows %v, want its two runs", got)
	}
	if got := ids(RoleInfo{}, "s/"); len(got) != 3 || got[upload.RunID] {
		t.Errorf("picked sender shows %v, want the three runs of its tenants", got)
	}
	if got := ids(RoleInfo{}, ""); len(got) != 4 || !got[upload.RunID] || !got[other.RunID] {
		t.Errorf("no pick shows %v, want all four", got)
	}
	if got := ids(RoleInfo{Tenants: []string{"s/mine"}}, ""); len(got) != 2 || !got[mine.RunID] {
		t.Errorf("limited role sees %v, want only its tenant's runs", got)
	}
}

// Today's spend is counted per tenant, so a view of one tenant is not charged with the others' judge calls.
func TestOnlineSpendIsPerTenant(t *testing.T) {
	store := newRunStore(t)
	for i, e := range []onlineEvalRow{
		{SessionID: "a", Tenant: "s/a", JudgeTokens: 500, JevCalls: 2, AtMs: 10},
		{SessionID: "b", Tenant: "s/a", JudgeTokens: 100, JevCalls: 1, AtMs: 20},
		{SessionID: "c", Tenant: "s/b", JudgeTokens: 900, JevCalls: 7, AtMs: 30},
		{SessionID: "d", Tenant: "s/b", JudgeTokens: 5000, JevCalls: 9, AtMs: 1},
	} {
		e.TraceID = "t"
		if err := store.InsertOnlineEval(e, "v", "m", nil); err != nil {
			t.Fatalf("row %d: %v", i, err)
		}
	}
	got, err := store.OnlineSpentByTenantSince(5)
	if err != nil {
		t.Fatal(err)
	}
	if got["s/a"].JudgeTokens != 600 || got["s/a"].JevCalls != 3 || got["s/b"].JudgeTokens != 900 {
		t.Errorf("spend = %+v, want s/a 600 tokens 3 calls and s/b 900 tokens (the older row left out)", got)
	}
}

// An evaluation recorded before runs carried their tenants is attributed from the sessions its trace files name.
func TestEarlierEvaluationsAreAttributedToTheirSessionsTenants(t *testing.T) {
	archive := newRunStore(t)
	store := NewSessionStoreWithArchive(nil, archive, SessionLimits{})
	store.Ingest(labelledTracesBody("mine-1", "aaaa", "s1", "mine"))
	var id string
	for _, sum := range store.List() {
		id = sum.SessionID
	}
	run, _ := archive.CreateRun(runSpecSummaryDTO{Approach: "traces", Target: map[string]any{"trace_files": []any{"trace_" + id + ".jsonl"}}})

	store.backfillRunTenants()

	got, _, _ := archive.GetRun(run.RunID)
	if len(got.Spec.Tenants) != 1 || got.Spec.Tenants[0] != "mine" {
		t.Errorf("tenants = %v, want [mine]", got.Spec.Tenants)
	}
}
