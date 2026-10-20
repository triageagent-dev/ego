package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/triageagent-dev/ego/internal/otlp"
)

// TestEvaluateHandler_PersistsRunHistory exercises the whole Runs
// write path end to end: POST /api/evaluate against a real SQLiteStore
// must leave a Run + RunResultRow behind that GET /api/runs, GET
// /api/runs/{id} and GET /api/runs/{id}/results can all read back with the
// exact camelCase shape ui/src/lib/types.ts's Run/RunResultRow expect -
// this is the feature that was reported as "Runs is empty".
func TestEvaluateHandler_PersistsRunHistory(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	tr := buildTrajectoryTrace()
	content, err := otlp.EncodeTraceJSONL(tr)
	if err != nil {
		t.Fatalf("EncodeTraceJSONL: %v", err)
	}
	evalSet := `{
		"eval_set_id": "set-1",
		"eval_cases": [{
			"eval_id": "case_1",
			"conversation": [{
				"invocation_id": "inv-1",
				"user_content": {"role": "user", "parts": [{"text": "what time is it in tokyo"}]},
				"final_response": {"role": "model", "parts": [{"text": "it is 10am in tokyo"}]},
				"intermediate_data": {
					"tool_uses": [{"name": "get_time", "args": {"tz": "Asia/Tokyo"}}],
					"tool_responses": [],
					"intermediate_responses": []
				}
			}]
		}]
	}`
	config := `{"evaluators":[{"type":"builtin","name":"tool_trajectory_avg_score","threshold":0.5}]}`
	req := multipartEvaluateRequest(t, content, []byte(evalSet), config)
	rec := httptest.NewRecorder()

	evaluateHandler(store)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	runs, err := store.ListRuns(nil, 100, "")
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 persisted run, got %d", len(runs))
	}
	run := runs[0]
	if run.Status != "succeeded" {
		t.Errorf("Status = %q, want succeeded", run.Status)
	}
	if run.Summary == nil {
		t.Fatal("Summary is nil, want a populated summary")
	}
	if run.Summary.TraceCount != 1 {
		t.Errorf("Summary.TraceCount = %d, want 1", run.Summary.TraceCount)
	}
	if run.Summary.ResultCounts.Passed != 1 {
		t.Errorf("Summary.ResultCounts.Passed = %d, want 1 (%+v)", run.Summary.ResultCounts.Passed, run.Summary)
	}
	if run.Spec.Approach != "traces" {
		t.Errorf("Spec.Approach = %q, want traces", run.Spec.Approach)
	}

	// GET /api/runs must round-trip through the HTTP handler with the same
	// non-null array guarantee established for /api/convert and
	// /api/evaluate's warnings fields.
	listRec := httptest.NewRecorder()
	listRunsHandler(store)(listRec, httptest.NewRequest(http.MethodGet, "/api/runs", nil))
	var listBody struct {
		Data []runDTO `json:"data"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listBody); err != nil {
		t.Fatalf("unmarshal /api/runs response: %v", err)
	}
	if len(listBody.Data) != 1 {
		t.Fatalf("GET /api/runs returned %d runs, want 1", len(listBody.Data))
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/runs/"+run.RunID, nil)
	getReq.SetPathValue("id", run.RunID)
	getRec := httptest.NewRecorder()
	getRunHandler(store)(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET /api/runs/{id} status = %d, body = %s", getRec.Code, getRec.Body.String())
	}

	resultsReq := httptest.NewRequest(http.MethodGet, "/api/runs/"+run.RunID+"/results", nil)
	resultsReq.SetPathValue("id", run.RunID)
	resultsRec := httptest.NewRecorder()
	getRunResultsHandler(store)(resultsRec, resultsReq)
	if resultsRec.Code != http.StatusOK {
		t.Fatalf("GET /api/runs/{id}/results status = %d, body = %s", resultsRec.Code, resultsRec.Body.String())
	}
	var resultsBody struct {
		Data []runResultRowDTO `json:"data"`
	}
	if err := json.Unmarshal(resultsRec.Body.Bytes(), &resultsBody); err != nil {
		t.Fatalf("unmarshal results response: %v", err)
	}
	if len(resultsBody.Data) != 1 {
		t.Fatalf("expected 1 result row, got %d", len(resultsBody.Data))
	}
	row := resultsBody.Data[0]
	if row.EvaluatorName != "tool_trajectory_avg_score" {
		t.Errorf("EvaluatorName = %q, want tool_trajectory_avg_score", row.EvaluatorName)
	}
	if row.Status != "passed" {
		t.Errorf("Status = %q, want passed", row.Status)
	}
	if row.PerInvocationScores == nil {
		t.Error("PerInvocationScores is nil, want a non-null array (even if empty) to match the non-optional TS type")
	}
}

func TestListRunsHandler_NoStore(t *testing.T) {
	rec := httptest.NewRecorder()
	listRunsHandler(nil)(rec, httptest.NewRequest(http.MethodGet, "/api/runs", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestGetRunHandler_NotFound(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/runs/does-not-exist", nil)
	req.SetPathValue("id", "does-not-exist")
	rec := httptest.NewRecorder()
	getRunHandler(store)(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
