package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func jobsRequest(t *testing.T, e *OnlineEngine, method, body string, info RoleInfo) (int, onlineJobsDTO) {
	t.Helper()
	req := httptest.NewRequest(method, "/api/online/jobs", strings.NewReader(body))
	req = req.WithContext(withRoleInfo(req.Context(), info))
	rec := httptest.NewRecorder()
	onlineJobsHandler(e)(rec, req)
	var out struct {
		Data onlineJobsDTO `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out.Data
}

func TestOnlineJobs_SaveReloadAndReset(t *testing.T) {
	e, sessions, archive, fake := newTestEngine(t, testOnlineConfig)
	now := time.Date(2026, 10, 4, 12, 30, 0, 0, time.UTC)
	e.now = func() time.Time { return now }
	admin := RoleInfo{Role: RoleAdmin, IsAdmin: true}

	// A chat session has no job yet: it is counted as unscored, not scored.
	sessions.Ingest(onlineSessionBody("aaaaaaaaaaaa0001", now.UnixMilli(), "chat", "why is checkout down?", "The payment service is out of memory."))
	e.process(context.Background(), "otlp-aaaaaaaaaaaa")
	if code, got := jobsRequest(t, e, http.MethodGet, "", RoleInfo{Role: RoleViewer}); code != 200 || got.Source != "file" ||
		len(got.Unscored) != 1 || got.Unscored[0].Kind != "chat" || got.Unscored[0].Sessions != 1 {
		t.Fatalf("get = %d %+v", code, got)
	}

	body := `{"perKindPerHour":3,"profiles":[{"kind":"c*","perHour":2,"evaluators":[{"name":"rubric_based_final_response_quality_v1","rubrics":["The answer is short"]}]}]}`
	if code, _ := jobsRequest(t, e, http.MethodPut, body, RoleInfo{Role: RoleMember}); code != http.StatusForbidden {
		t.Errorf("a member saving jobs = %d, want 403", code)
	}
	if code, got := jobsRequest(t, e, http.MethodPut, body, admin); code != 200 || got.Source != "stored" || got.PerKindPerHour != 3 || len(got.Profiles) != 1 {
		t.Fatalf("put = %d %+v", code, got)
	}
	if code, _ := jobsRequest(t, e, http.MethodPut, `{"profiles":[{"kind":"chat"}]}`, admin); code != http.StatusBadRequest {
		t.Errorf("a job with no evaluators = %d, want 400", code)
	}

	// The saved job scores the next chat session without a restart.
	sessions.Ingest(onlineSessionBody("bbbbbbbbbbbb0002", now.UnixMilli(), "chat", "is the cart service up?", "Yes, it is running."))
	e.process(context.Background(), "otlp-bbbbbbbbbbbb")
	if fake.calls == 0 {
		t.Fatal("the saved job did not score the chat session")
	}
	rows, _ := archive.OnlineSamples(0)
	if len(rows) != 1 || rows[0].Key.Kind != "chat" {
		t.Fatalf("samples = %+v", rows)
	}

	// A restart reads the saved jobs back.
	e2 := NewOnlineEngine(e.base, archive, sessions, "tenant.id")
	e2.loadJobs()
	if got := e2.jobs(); got.Source != "stored" || got.Profiles[0].Kind != "c*" || got.Profiles[0].PerHour != 2 {
		t.Errorf("after restart: %+v", got)
	}

	if code, got := jobsRequest(t, e, http.MethodDelete, "", admin); code != 200 || got.Source != "file" || got.Profiles[0].Kind != "rca" {
		t.Errorf("reset = %d %+v", code, got)
	}
	e3 := NewOnlineEngine(e.base, archive, sessions, "tenant.id")
	e3.loadJobs()
	if e3.jobs().Source != "file" {
		t.Error("a reset must survive a restart")
	}
}

func TestOnlineJobs_ScopedRoleIsRefused(t *testing.T) {
	e, _, _, _ := newTestEngine(t, testOnlineConfig)
	if code, _ := jobsRequest(t, e, http.MethodGet, "", RoleInfo{Role: RoleViewer, Tenants: []string{"shop"}}); code != http.StatusForbidden {
		t.Errorf("tenant-scoped read = %d, want 403", code)
	}
}
