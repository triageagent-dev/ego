package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/triageagent-dev/ego/internal/adk"
)

func sampleEvalSetJSON(id string) []byte {
	b, _ := json.Marshal(adk.EvalSet{
		EvalSetID:   id,
		Name:        "My Eval Set",
		Description: "built by the EvalSet Builder",
		EvalCases: []adk.EvalCase{{
			EvalID: "case_1",
			Conversation: []adk.Invocation{{
				InvocationID: "inv-1",
				UserContent:  &adk.Content{Role: "user", Parts: []adk.Part{{Text: "hi"}}},
			}},
		}},
	})
	return b
}

// TestSaveEvalSetHandler_ThenListAndGet exercises the whole "Save EvalSet"
// path end to end: POST /api/evalsets must leave a row behind that GET
// /api/evalsets (list) and GET /api/evalsets/{id} (full document) can both
// read back - this is the feature that made the EvalSet Builder's "Save"
// button only ever download a file, with nothing to show "in the list".
func TestSaveEvalSetHandler_ThenListAndGet(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "evalsets.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	body := sampleEvalSetJSON("set-1")
	req := httptest.NewRequest(http.MethodPost, "/api/evalsets", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	evalSetsHandler(store)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("save status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var saveResp struct {
		Data  evalSetSummaryDTO `json:"data"`
		Error *string           `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &saveResp); err != nil {
		t.Fatalf("unmarshal save response: %v", err)
	}
	if saveResp.Error != nil {
		t.Fatalf("save error: %s", *saveResp.Error)
	}
	if saveResp.Data.EvalSetID != "set-1" || saveResp.Data.NumCases != 1 || saveResp.Data.Name != "My Eval Set" {
		t.Errorf("unexpected save summary: %+v", saveResp.Data)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/evalsets", nil)
	listRec := httptest.NewRecorder()
	evalSetsHandler(store)(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", listRec.Code, listRec.Body.String())
	}
	var listResp struct {
		Data  []evalSetSummaryDTO `json:"data"`
		Error *string             `json:"error"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("unmarshal list response: %v", err)
	}
	if len(listResp.Data) != 1 || listResp.Data[0].EvalSetID != "set-1" {
		t.Fatalf("expected 1 listed eval set with id set-1, got %+v", listResp.Data)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/evalsets/set-1", nil)
	getReq.SetPathValue("id", "set-1")
	getRec := httptest.NewRecorder()
	getEvalSetHandler(store)(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get status = %d, body = %s", getRec.Code, getRec.Body.String())
	}
	var getResp struct {
		Data  adk.EvalSet `json:"data"`
		Error *string     `json:"error"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("unmarshal get response: %v", err)
	}
	if len(getResp.Data.EvalCases) != 1 || getResp.Data.EvalCases[0].EvalID != "case_1" {
		t.Fatalf("expected full eval set with 1 case round-tripped, got %+v", getResp.Data)
	}
}

// TestSaveEvalSetHandler_UpsertsByID confirms re-saving the same
// eval_set_id updates the row in place rather than erroring or duplicating
// it in the list.
func TestSaveEvalSetHandler_UpsertsByID(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "evalsets.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	post := func(body []byte) evalSetSummaryDTO {
		req := httptest.NewRequest(http.MethodPost, "/api/evalsets", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		evalSetsHandler(store)(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("save status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var resp struct {
			Data evalSetSummaryDTO `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return resp.Data
	}

	first := post(sampleEvalSetJSON("set-1"))

	updated, _ := json.Marshal(adk.EvalSet{
		EvalSetID: "set-1",
		Name:      "Renamed",
		EvalCases: []adk.EvalCase{
			{EvalID: "case_1", Conversation: []adk.Invocation{{InvocationID: "inv-1"}}},
			{EvalID: "case_2", Conversation: []adk.Invocation{{InvocationID: "inv-2"}}},
		},
	})
	second := post(updated)

	if second.EvalSetID != first.EvalSetID {
		t.Errorf("EvalSetID changed across upsert: %q vs %q", first.EvalSetID, second.EvalSetID)
	}
	if second.Name != "Renamed" || second.NumCases != 2 {
		t.Errorf("upsert did not apply new content: %+v", second)
	}
	if second.CreatedAt != first.CreatedAt {
		t.Errorf("CreatedAt changed on upsert: %q vs %q, want preserved", second.CreatedAt, first.CreatedAt)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/evalsets", nil)
	listRec := httptest.NewRecorder()
	evalSetsHandler(store)(listRec, listReq)
	var listResp struct {
		Data []evalSetSummaryDTO `json:"data"`
	}
	json.Unmarshal(listRec.Body.Bytes(), &listResp)
	if len(listResp.Data) != 1 {
		t.Fatalf("expected upsert to keep exactly 1 row, got %d", len(listResp.Data))
	}
}

// TestSaveEvalSetHandler_RejectsEmpty confirms a missing eval_set_id or an
// eval set with zero cases is rejected with 400, not silently persisted.
func TestSaveEvalSetHandler_RejectsEmpty(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "evalsets.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	for name, body := range map[string][]byte{
		"missing eval_set_id": []byte(`{"eval_cases":[{"eval_id":"c1","conversation":[]}]}`),
		"no eval cases":       []byte(`{"eval_set_id":"set-1","eval_cases":[]}`),
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/evalsets", bytes.NewReader(body))
			rec := httptest.NewRecorder()
			evalSetsHandler(store)(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestEvalSetsHandlers_StorageUnavailable confirms every eval-sets route
// returns 503 (not a 500 or a misleading empty list) when no --session-db
// was configured, matching /api/runs' existing contract.
func TestEvalSetsHandlers_StorageUnavailable(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/evalsets", nil)
	rec := httptest.NewRecorder()
	evalSetsHandler(nil)(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("list: status = %d, want 503", rec.Code)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/evalsets/set-1", nil)
	getRec := httptest.NewRecorder()
	getEvalSetHandler(nil)(getRec, getReq)
	if getRec.Code != http.StatusServiceUnavailable {
		t.Errorf("get: status = %d, want 503", getRec.Code)
	}
}
