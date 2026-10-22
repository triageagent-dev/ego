package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/triageagent-dev/ego/internal/mcptools"
)

// threadFixture is an archive with the thread "pod" in three segments (the first two closed, the last open), one
// short-lived thread "job" from another sender, and a memory limit of one session so the older segments live in the
// archive only.
type threadFixture struct {
	store   *SessionStore
	archive *SQLiteStore
	base    int64 // ms
}

func newThreadFixture(t *testing.T) *threadFixture {
	t.Helper()
	archive, err := NewSQLiteStore(filepath.Join(t.TempDir(), "threads.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { archive.Close() })
	store := NewSessionStoreWithArchive(nil, archive, SessionLimits{Memory: 1, UsageTenantAttr: "tenant.id"})
	store.idleTimeout, store.completionGrace = time.Hour, time.Hour
	base := time.Now().Add(-10*time.Hour).UnixMilli() / minute * minute
	// Three segments of "pod", 100 and 200 minutes apart, 10 + 20 + 30 prompt tokens (each call adds one more).
	for i, tokens := range []int{10, 20, 30} {
		store.IngestFor("core", segBody("pod", "", segSpan{
			trace: fmt.Sprintf("pod-t%d", i), span: fmt.Sprintf("pod-s%d", i), startMs: base + int64(i)*100*minute, durMs: 1000, tokens: tokens}))
	}
	store.IngestFor("rc", segBody("job", "", segSpan{trace: "job-t0", span: "job-s0", startMs: base + 5*minute, durMs: 1000, tokens: 7}))
	for _, sum := range store.List() {
		store.completeSession(sum.SessionID)
	}
	if err := store.flushPersist(); err != nil {
		t.Fatal(err)
	}
	return &threadFixture{store: store, archive: archive, base: base}
}

var threadAdmin = RoleInfo{Role: RoleAdmin, IsAdmin: true}

func TestThreads_TotalsAndLastScore(t *testing.T) {
	f := newThreadFixture(t)
	// Part 1 was scored 0.5 and 1.0 earlier, part 2 later with 0.25: the thread's last score is part 2's.
	for _, s := range []struct {
		session, metric string
		at              int64
		score           float64
	}{{"core:pod", "m1", 100, 0.5}, {"core:pod", "m2", 100, 1.0}, {"core:pod~2", "m1", 200, 0.25}} {
		if _, err := f.archive.db.Exec(`INSERT INTO online_scores (session_id, at_ms, tenant, kind, version, model, metric, score, status)
			VALUES (?, ?, '', '', '', '', ?, ?, 'ok')`, s.session, s.at, s.metric, s.score); err != nil {
			t.Fatal(err)
		}
	}

	rows, err := f.store.Threads(mcptools.SessionFilter{}, 10, threadAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("threads = %+v, want 2", rows)
	}
	pod := rows[0]
	if pod.Thread != "core:pod" || pod.Segments != 3 || !pod.Open {
		t.Errorf("pod = %+v, want 3 segments, the newest open", pod)
	}
	if pod.TotalTokens != 63 { // (10+1) + (20+1) + (30+1)
		t.Errorf("pod tokens = %d, want 63", pod.TotalTokens)
	}
	if pod.LastScore == nil || *pod.LastScore != 0.25 {
		t.Errorf("pod last score = %v, want 0.25", pod.LastScore)
	}
	first, _ := time.Parse(time.RFC3339, pod.FirstSeen)
	last, _ := time.Parse(time.RFC3339, pod.LastSeen)
	if got := last.Sub(first); got < 200*time.Minute || got > 201*time.Minute {
		t.Errorf("first/last seen %s .. %s span %s, want about 200 minutes", pod.FirstSeen, pod.LastSeen, got)
	}
	if job := rows[1]; job.Thread != "rc:job" || job.Segments != 1 || job.TotalTokens != 8 || job.LastScore != nil {
		t.Errorf("job = %+v", job)
	}

	// A filter narrows the threads, a limit the count.
	if rows, _ := f.store.Threads(mcptools.SessionFilter{Query: "job"}, 10, threadAdmin); len(rows) != 1 || rows[0].Thread != "rc:job" {
		t.Errorf("query job = %+v", rows)
	}
	if rows, _ := f.store.Threads(mcptools.SessionFilter{}, 1, threadAdmin); len(rows) != 1 || rows[0].Thread != "core:pod" {
		t.Errorf("limit 1 = %+v, want the thread seen last", rows)
	}
	if rows, _ := f.store.Threads(mcptools.SessionFilter{Since: time.Now().Add(-time.Minute)}, 10, threadAdmin); len(rows) != 0 {
		t.Errorf("since a minute ago = %+v, want none", rows)
	}
}

func TestThreads_RoleLimits(t *testing.T) {
	f := newThreadFixture(t)
	core := RoleInfo{Role: RoleMember, Tenants: []string{"core/*"}}
	rows, err := f.store.Threads(mcptools.SessionFilter{}, 10, core)
	if err != nil || len(rows) != 1 || rows[0].Thread != "core:pod" || rows[0].Segments != 3 {
		t.Fatalf("core sees %+v err=%v, want only its thread with 3 segments", rows, err)
	}
	if rows[0].TotalTokens != 63 {
		t.Errorf("core tokens = %d, want 63", rows[0].TotalTokens)
	}
	none := RoleInfo{Role: RoleMember, Tenants: []string{}}
	if rows, _ := f.store.Threads(mcptools.SessionFilter{}, 10, none); len(rows) != 0 {
		t.Errorf("a role without tenants sees %+v", rows)
	}
	// An agent-scoped role has no usage view: rows come without tokens.
	scoped := RoleInfo{Role: RoleMember, Agents: []string{"nobody"}}
	if rows, _ := f.store.Threads(mcptools.SessionFilter{}, 10, scoped); len(rows) != 0 {
		t.Errorf("agent-scoped role sees %+v, want none: no segment ran that agent", rows)
	}
}

// Searching a thread's id finds every part, also the ones that left memory; the thread filter does the same.
func TestThreads_SearchAndFilterFindAllParts(t *testing.T) {
	f := newThreadFixture(t)
	all := func(SessionSummary) bool { return true }
	if got := len(f.store.List()); got != 1 { // every session is complete; the memory limit keeps one
		t.Fatalf("%d sessions in memory, want the older ones evicted", got)
	}
	ids := func(sums []SessionSummary) []string {
		var out []string
		for _, s := range sums {
			out = append(out, s.SessionID)
		}
		slices.Sort(out)
		return out
	}
	want := "[core:pod core:pod~2 core:pod~3]"
	if got := fmt.Sprint(ids(f.store.Find(mcptools.SessionFilter{Query: "core:pod"}, all))); got != want {
		t.Errorf("search for the thread id = %s, want %s", got, want)
	}
	if got := fmt.Sprint(ids(f.store.Find(mcptools.SessionFilter{Thread: "core:pod"}, all))); got != want {
		t.Errorf("thread filter = %s, want %s", got, want)
	}
	parts := map[string]int{}
	for _, s := range f.store.Find(mcptools.SessionFilter{Thread: "core:pod"}, all) {
		parts[s.SessionID] = s.Part
		if s.Thread != "core:pod" {
			t.Errorf("%s reports thread %q", s.SessionID, s.Thread)
		}
	}
	if parts["core:pod"] != 1 || parts["core:pod~2"] != 2 || parts["core:pod~3"] != 3 {
		t.Errorf("parts = %v", parts)
	}
	// The paged list filters by thread in the index.
	page, err := f.store.Page(SessionPageRequest{Filter: mcptools.SessionFilter{Thread: "core:pod"}, Limit: 10, Unrestricted: true}, all)
	if err != nil || fmt.Sprint(ids(page.Items)) != want || page.Total != 3 {
		t.Errorf("page = %v total %d err=%v, want %s", ids(page.Items), page.Total, err, want)
	}
}

func TestThreads_WithoutAnArchive(t *testing.T) {
	s := newSegStore(t)
	base := time.Now().Add(-5 * time.Hour).UnixMilli()
	s.Ingest(segBody("pod", "", segSpan{trace: "a", span: "a", startMs: base, durMs: 10, tokens: 4}))
	s.Ingest(segBody("pod", "", segSpan{trace: "b", span: "b", startMs: base + 90*minute, durMs: 10, tokens: 6}))
	rows, err := s.Threads(mcptools.SessionFilter{}, 10, threadAdmin)
	if err != nil || len(rows) != 1 || rows[0].Segments != 2 || rows[0].TotalTokens != 12 || !rows[0].Open {
		t.Errorf("threads = %+v err=%v, want pod with 2 segments and 12 tokens", rows, err)
	}
}

// list_threads through the in-process backend and the HTTP endpoint answer the same rows.
func TestThreads_ToolAndEndpoint(t *testing.T) {
	f := newThreadFixture(t)
	backend := newMCPBackend(f.store, f.archive, nil, nil)
	h := mcpRoute("", nil, nil, "", nil, nil, backend)

	code, res := callTool(t, h, "", "list_threads", map[string]any{"limit": 5})
	if code != http.StatusOK || res.IsError {
		t.Fatalf("list_threads = %d %+v", code, res)
	}
	var items []mcptools.ThreadListItem
	if err := json.Unmarshal(res.StructuredContent, &items); err != nil || len(items) != 2 {
		t.Fatalf("items = %s err=%v", res.StructuredContent, err)
	}
	if it := items[0]; it.Thread != "core:pod" || it.Segments != 3 || it.TotalTokens != 63 || !it.Open || it.Tenant == "" {
		t.Errorf("first item = %+v", it)
	}
	if _, res := callTool(t, h, "", "list_threads", map[string]any{"limit": 0}); !res.IsError {
		t.Error("limit 0 must be refused")
	}

	// list_sessions with `thread` returns the parts, each saying which one it is.
	_, res = callTool(t, h, "", "list_sessions", map[string]any{"thread": "core:pod", "limit": 10})
	var sessions []mcptools.SessionListItem
	if err := json.Unmarshal(res.StructuredContent, &sessions); err != nil || len(sessions) != 3 {
		t.Fatalf("sessions = %s err=%v", res.StructuredContent, err)
	}
	for _, s := range sessions {
		if s.Thread != "core:pod" || s.Part < 1 {
			t.Errorf("session %+v lacks its thread and part", s)
		}
	}

	// The REST endpoint, and the stdio client of it, agree with the backend.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/threads", threadsHandler(f.store))
	mux.HandleFunc("/api/streaming/sessions", sessionsHandler(f.store))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	rest := mcptools.NewREST(srv.URL, "", srv.Client())
	got, err := rest.Threads(context.Background(), mcptools.SessionFilter{}, 5)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := backend.Threads(context.Background(), mcptools.SessionFilter{}, 5)
	jg, _ := json.Marshal(got)
	jw, _ := json.Marshal(want)
	if string(jg) != string(jw) {
		t.Errorf("REST threads differ:\n rest       %s\n in-process %s", jg, jw)
	}
	restSessions, err := rest.Sessions(context.Background(), mcptools.SessionFilter{Thread: "core:pod"})
	if err != nil || len(restSessions) != 3 {
		t.Errorf("REST sessions of the thread = %+v err=%v", restSessions, err)
	}
	if !strings.Contains(string(jg), `"thread":"core:pod"`) {
		t.Errorf("rows = %s", jg)
	}
	resp, err := srv.Client().Get(srv.URL + "/api/threads?limit=0")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("limit=0 = %d, want 400", resp.StatusCode)
	}
}
