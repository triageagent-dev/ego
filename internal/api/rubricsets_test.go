package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func rubricSetRequest(t *testing.T, store *SQLiteStore, method, path string, body any, info *RoleInfo) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	if info != nil {
		req = req.WithContext(withRoleInfo(req.Context(), *info))
	}
	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/rubricsets", rubricSetsHandler(store))
	mux.HandleFunc("/api/rubricsets/{id}", rubricSetHandler(store))
	mux.ServeHTTP(rec, req)
	return rec
}

func TestRubricSets_SaveListGetDelete(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "rubrics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	rec := rubricSetRequest(t, store, http.MethodPost, "/api/rubricsets", map[string]any{
		"rubricSetId": "grounding", "name": "Grounding", "kind": "final_response",
		"rubrics": []string{"  Each conclusion cites a fact.  ", "", "No claim without a fact."},
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("save = %d %s", rec.Code, rec.Body.String())
	}
	var saved struct{ Data rubricSetDTO }
	_ = json.Unmarshal(rec.Body.Bytes(), &saved)
	if saved.Data.RubricSetID != "grounding" || len(saved.Data.Rubrics) != 2 || saved.Data.Rubrics[0] != "Each conclusion cites a fact." {
		t.Fatalf("saved = %+v", saved.Data)
	}

	// Without an id one is made; a re-save keeps created_at.
	rec = rubricSetRequest(t, store, http.MethodPost, "/api/rubricsets", map[string]any{"name": "Tools", "kind": "tool_use", "rubrics": []string{"Calls only tools the task needs."}}, nil)
	var made struct{ Data rubricSetDTO }
	_ = json.Unmarshal(rec.Body.Bytes(), &made)
	if rec.Code != 200 || !strings.HasPrefix(made.Data.RubricSetID, "rubrics-") {
		t.Fatalf("made = %d %+v", rec.Code, made.Data)
	}
	first := saved.Data.CreatedAt
	rec = rubricSetRequest(t, store, http.MethodPost, "/api/rubricsets", map[string]any{"rubricSetId": "grounding", "name": "Grounding v2", "kind": "final_response", "rubrics": []string{"One."}}, nil)
	var again struct{ Data rubricSetDTO }
	_ = json.Unmarshal(rec.Body.Bytes(), &again)
	if again.Data.CreatedAt != first || again.Data.Name != "Grounding v2" || len(again.Data.Rubrics) != 1 {
		t.Errorf("re-save = %+v, want created_at %s kept", again.Data, first)
	}

	// The list can be narrowed to one kind.
	var list struct{ Data []rubricSetDTO }
	rec = rubricSetRequest(t, store, http.MethodGet, "/api/rubricsets", nil, nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Data) != 2 {
		t.Errorf("list = %d, want 2", len(list.Data))
	}
	rec = rubricSetRequest(t, store, http.MethodGet, "/api/rubricsets?kind=tool_use", nil, nil)
	list.Data = nil
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Data) != 1 || list.Data[0].Kind != "tool_use" {
		t.Errorf("tool_use list = %+v", list.Data)
	}
	if rec = rubricSetRequest(t, store, http.MethodGet, "/api/rubricsets?kind=nope", nil, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown kind = %d, want 400", rec.Code)
	}

	if rec = rubricSetRequest(t, store, http.MethodGet, "/api/rubricsets/grounding", nil, nil); rec.Code != 200 {
		t.Errorf("get = %d", rec.Code)
	}
	if rec = rubricSetRequest(t, store, http.MethodDelete, "/api/rubricsets/grounding", nil, nil); rec.Code != 200 {
		t.Errorf("delete = %d", rec.Code)
	}
	if rec = rubricSetRequest(t, store, http.MethodGet, "/api/rubricsets/grounding", nil, nil); rec.Code != http.StatusNotFound {
		t.Errorf("get after delete = %d, want 404", rec.Code)
	}
	if rec = rubricSetRequest(t, store, http.MethodDelete, "/api/rubricsets/grounding", nil, nil); rec.Code != http.StatusNotFound {
		t.Errorf("second delete = %d, want 404", rec.Code)
	}
}

func TestRubricSets_Refusals(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "rubrics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ok := map[string]any{"name": "N", "kind": "final_response", "rubrics": []string{"r"}}
	with := func(k string, v any) map[string]any {
		m := map[string]any{}
		for kk, vv := range ok {
			m[kk] = vv
		}
		m[k] = v
		return m
	}
	long := strings.Repeat("x", 501)
	many := make([]string, 21)
	for i := range many {
		many[i] = "r"
	}
	for name, body := range map[string]map[string]any{
		"no name":     with("name", " "),
		"bad kind":    with("kind", "other"),
		"no rubrics":  with("rubrics", []string{" ", ""}),
		"too many":    with("rubrics", many),
		"long rubric": with("rubrics", []string{long}),
		"bad id":      with("rubricSetId", "Has Space"),
		"long name":   with("name", strings.Repeat("n", 121)),
	} {
		if rec := rubricSetRequest(t, store, http.MethodPost, "/api/rubricsets", body, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, rec.Code, rec.Body.String())
		}
	}
	viewer := RoleInfo{Role: RoleViewer}
	if rec := rubricSetRequest(t, store, http.MethodPost, "/api/rubricsets", ok, &viewer); rec.Code != http.StatusForbidden {
		t.Errorf("viewer save = %d, want 403", rec.Code)
	}
	if rec := rubricSetRequest(t, store, http.MethodDelete, "/api/rubricsets/x", nil, &viewer); rec.Code != http.StatusForbidden {
		t.Errorf("viewer delete = %d, want 403", rec.Code)
	}
	tenant := RoleInfo{Role: RoleMember, Tenants: []string{"a/b"}}
	if rec := rubricSetRequest(t, store, http.MethodGet, "/api/rubricsets", nil, &tenant); rec.Code != http.StatusForbidden {
		t.Errorf("tenant-limited list = %d, want 403", rec.Code)
	}
	if rec := rubricSetRequest(t, nil, http.MethodGet, "/api/rubricsets", nil, nil); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no archive = %d, want 503", rec.Code)
	}
}

// Saves racing for the last places must not pass the cap together; the ones that lose are refused for lack of room,
// not for a busy archive.
func TestSaveRubricSet_CapHoldsUnderConcurrentSaves(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "rubrics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	save := func(i int) error {
		_, err := store.SaveRubricSet(rubricSetDTO{RubricSetID: fmt.Sprintf("set-%03d", i), Name: "N", Kind: "final_response", Rubrics: []string{"r"}})
		return err
	}
	const room = 5
	for i := 0; i < maxRubricSets-room; i++ {
		if err := save(i); err != nil {
			t.Fatal(err)
		}
	}

	const racers = 40
	var wg sync.WaitGroup
	errs := make([]error, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = save(maxRubricSets + i)
		}(i)
	}
	wg.Wait()

	stored := 0
	for _, e := range errs {
		switch {
		case e == nil:
			stored++
		case e == errTooManyRubricSets:
		default:
			t.Fatalf("save: %v", e)
		}
	}
	var n int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM rubric_sets`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != maxRubricSets || stored != room {
		t.Errorf("%d sets stored (%d of the racers), want the cap %d filled by exactly the %d places left", n, stored, maxRubricSets, room)
	}

	// An existing id is still saved at the cap, and keeps the date it was made.
	before, found, err := store.GetRubricSet("set-000")
	if err != nil || !found {
		t.Fatalf("set-000: found %v, err %v", found, err)
	}
	{
		after, err := store.SaveRubricSet(rubricSetDTO{RubricSetID: "set-000", Name: "Renamed", Kind: "final_response", Rubrics: []string{"r"}})
		if err != nil {
			t.Fatalf("updating a set at the cap: %v", err)
		}
		if after.CreatedAt != before.CreatedAt || after.Name != "Renamed" {
			t.Errorf("update = %+v, want the original creation date %s and the new name", after, before.CreatedAt)
		}
	}
}
