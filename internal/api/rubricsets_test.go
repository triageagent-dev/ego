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
		_, err := store.SaveRubricSet(rubricSetDTO{RubricSetID: fmt.Sprintf("set-%03d", i), Name: "N", Kind: "final_response", Rubrics: []string{"r"}}, "", true)
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
		after, err := store.SaveRubricSet(rubricSetDTO{RubricSetID: "set-000", Name: "Renamed", Kind: "final_response", Rubrics: []string{"r"}}, "", true)
		if err != nil {
			t.Fatalf("updating a set at the cap: %v", err)
		}
		if after.CreatedAt != before.CreatedAt || after.Name != "Renamed" {
			t.Errorf("update = %+v, want the original creation date %s and the new name", after, before.CreatedAt)
		}
	}
}

// A set has an author: another member cannot overwrite or delete it, an admin can, and a set saved before authors
// were recorded is taken by whoever changes it next.
func TestRubricSets_OnlyTheAuthorOrAnAdminChangesASet(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "rubrics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	set := func(name string) rubricSetDTO {
		return rubricSetDTO{RubricSetID: "grounding", Name: name, Kind: "final_response", Rubrics: []string{"r"}}
	}

	saved, err := store.SaveRubricSet(set("Alice's"), "alice", false)
	if err != nil || saved.CreatedBy != "alice" {
		t.Fatalf("first save: %+v %v", saved, err)
	}
	if _, err := store.SaveRubricSet(set("Bob's"), "bob", false); err != errNotRubricSetOwner {
		t.Errorf("another member's save = %v, want the not-the-author refusal", err)
	}
	if got, _, _ := store.GetRubricSet("grounding"); got.Name != "Alice's" {
		t.Errorf("the set was overwritten: %+v", got)
	}
	if again, err := store.SaveRubricSet(set("Alice's, again"), "alice", false); err != nil || again.CreatedBy != "alice" || again.CreatedAt != saved.CreatedAt {
		t.Errorf("the author's re-save: %+v %v", again, err)
	}
	if byAdmin, err := store.SaveRubricSet(set("By an admin"), "carol", true); err != nil || byAdmin.CreatedBy != "alice" {
		t.Errorf("an admin's save: %+v %v; want it saved and the author kept", byAdmin, err)
	}
	if ok, err := store.DeleteRubricSet("grounding", "bob", false); ok || err != errNotRubricSetOwner {
		t.Errorf("another member's delete = %v, %v; want the refusal", ok, err)
	}
	if ok, err := store.DeleteRubricSet("grounding", "alice", false); !ok || err != nil {
		t.Errorf("the author's delete = %v, %v", ok, err)
	}
	if ok, err := store.DeleteRubricSet("grounding", "alice", false); ok || err != nil {
		t.Errorf("deleting a set that is gone = %v, %v; want not found, no error", ok, err)
	}

	// A set from before authors were recorded.
	if _, err := store.db.Exec(`INSERT INTO rubric_sets (rubric_set_id, name, kind, rubrics_json, created_at, updated_at) VALUES ('old', 'Old', 'final_response', '["r"]', 'x', 'x')`); err != nil {
		t.Fatal(err)
	}
	if taken, err := store.SaveRubricSet(rubricSetDTO{RubricSetID: "old", Name: "Old", Kind: "final_response", Rubrics: []string{"r"}}, "bob", false); err != nil || taken.CreatedBy != "bob" {
		t.Errorf("an old set changed by bob: %+v %v; want bob to take it", taken, err)
	}
	if _, err := store.SaveRubricSet(rubricSetDTO{RubricSetID: "old", Name: "Old", Kind: "final_response", Rubrics: []string{"r"}}, "alice", false); err != errNotRubricSetOwner {
		t.Errorf("alice changing bob's set = %v, want the refusal", err)
	}
}

func TestRubricSets_HandlerAnswers403ToAnotherMember(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "rubrics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	as := func(user string, role Role, method, path string, body any) int {
		var rdr *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = bytes.NewReader(b)
		} else {
			rdr = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, path, rdr)
		ctx := withRoleInfo(req.Context(), RoleInfo{Role: role, IsAdmin: role == RoleAdmin})
		req = req.WithContext(withIdentity(ctx, identity{Username: user}))
		rec := httptest.NewRecorder()
		mux := http.NewServeMux()
		mux.HandleFunc("/api/rubricsets", rubricSetsHandler(store))
		mux.HandleFunc("/api/rubricsets/{id}", rubricSetHandler(store))
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	body := map[string]any{"rubricSetId": "mine", "name": "N", "kind": "final_response", "rubrics": []string{"r"}}
	if code := as("alice", RoleMember, http.MethodPost, "/api/rubricsets", body); code != http.StatusOK {
		t.Fatalf("alice's save = %d", code)
	}
	if code := as("bob", RoleMember, http.MethodPost, "/api/rubricsets", body); code != http.StatusForbidden {
		t.Errorf("bob overwriting alice's set = %d, want 403", code)
	}
	if code := as("bob", RoleMember, http.MethodDelete, "/api/rubricsets/mine", nil); code != http.StatusForbidden {
		t.Errorf("bob deleting alice's set = %d, want 403", code)
	}
	if code := as("root", RoleAdmin, http.MethodDelete, "/api/rubricsets/mine", nil); code != http.StatusOK {
		t.Errorf("an admin deleting it = %d, want 200", code)
	}
}
