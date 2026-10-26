package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func limitByKey(items []limitDTO, key string) *limitDTO {
	for i := range items {
		if items[i].Key == key {
			return &items[i]
		}
	}
	return nil
}

func TestUsageLimits_ReportsTheCapsAndWhatIsUsed(t *testing.T) {
	g := newEvalGuard(1000)
	old := interactiveEval
	interactiveEval = g
	defer func() { interactiveEval = old }()
	g.add(250)
	release, _, _ := g.admit(time.Now(), "", true)
	defer release()

	store := NewSessionStore(nil)
	store.maxMemory = 50
	store.sessionRetention = 30 * 24 * time.Hour
	store.usageRetention = 90 * 24 * time.Hour

	items := usageLimits(store, nil, RoleInfo{}, time.Now())

	if tok := limitByKey(items, "eval.tokens"); tok == nil || tok.Limit != 1000 || tok.Used == nil || *tok.Used != 250 {
		t.Errorf("eval.tokens = %+v, want limit 1000, used 250", tok)
	}
	if run := limitByKey(items, "eval.running"); run == nil || run.Limit != maxConcurrentEvals || run.Used == nil || *run.Used != 1 {
		t.Errorf("eval.running = %+v, want limit %d, used 1", run, maxConcurrentEvals)
	}
	if r := limitByKey(items, "sessions.retention"); r == nil || r.Limit != 30 || r.Unit != "days" {
		t.Errorf("sessions.retention = %+v, want 30 days", r)
	}
	if m := limitByKey(items, "sessions.memory"); m == nil || m.Limit != 50 {
		t.Errorf("sessions.memory = %+v, want 50", m)
	}
	// No online engine: the online budget is not listed rather than listed as zero.
	if limitByKey(items, "online.tokens") != nil {
		t.Error("online.tokens listed without an online engine")
	}
}

func TestUsageLimits_StorageOfTheDatabase(t *testing.T) {
	archive, err := NewSQLiteStore(filepath.Join(t.TempDir(), "size.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	store := NewSessionStoreWithArchive(nil, archive, SessionLimits{})

	items := usageLimits(store, nil, RoleInfo{}, time.Now())
	db := limitByKey(items, "storage.database")
	if db == nil || db.Unit != "bytes" || db.Used == nil || *db.Used <= 0 {
		t.Fatalf("storage.database = %+v, want bytes with a size above zero", db)
	}
	if db.Limit > 0 {
		if vol := limitByKey(items, "storage.volume"); vol == nil || vol.Limit != db.Limit || vol.Used == nil || *vol.Used < *db.Used {
			t.Errorf("storage.volume = %+v, want the same capacity and at least the database's size", vol)
		}
	}

	// The sizes cover every tenant's data: a limited role does not see them.
	if limitByKey(usageLimits(store, nil, RoleInfo{Tenants: []string{"a/b"}}, time.Now()), "storage.database") != nil {
		t.Error("storage.database listed to a tenant-limited role")
	}
	// No archive: nothing to measure, nothing listed.
	if limitByKey(usageLimits(NewSessionStore(nil), nil, RoleInfo{}, time.Now()), "storage.database") != nil {
		t.Error("storage.database listed without an archive")
	}
}

func TestUsageLimitsHandler(t *testing.T) {
	rec := httptest.NewRecorder()
	usageLimitsHandler(NewSessionStore(nil), nil)(rec, httptest.NewRequest(http.MethodGet, "/api/usage/limits", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"eval.tokens"`) {
		t.Fatalf("GET = %d %s, want 200 listing eval.tokens", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	usageLimitsHandler(NewSessionStore(nil), nil)(rec, httptest.NewRequest(http.MethodPost, "/api/usage/limits", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d, want 405", rec.Code)
	}
}

func TestFailedOnlineSessions(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "failed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now()
	f := func(v float64) *float64 { return &v }
	write := func(id, tenant string, ago time.Duration, scores ...onlineScoreRow) {
		t.Helper()
		if err := store.InsertOnlineEval(onlineEvalRow{SessionID: id, TraceID: id, AtMs: now.Add(-ago).UnixMilli(), Tenant: tenant, Kind: "chat"}, "v1", "m", scores); err != nil {
			t.Fatal(err)
		}
	}
	write("old", "acme", 3*time.Hour, onlineScoreRow{Metric: "safety_v1", Score: f(0.4), Status: "failed"})
	write("recent", "acme", 10*time.Minute,
		onlineScoreRow{Metric: "safety_v1", Score: f(0.6), Status: "failed"},
		onlineScoreRow{Metric: "hallucinations_v1", Score: f(0.2), Status: "failed"},
		onlineScoreRow{Metric: "jev_grounded", Score: f(0.9), Status: "passed"})
	write("passes", "acme", 5*time.Minute, onlineScoreRow{Metric: "safety_v1", Score: f(0.95), Status: "passed"})
	write("errored", "acme", 4*time.Minute, onlineScoreRow{Metric: "safety_v1", Status: "errored"})
	write("elsewhere", "other", time.Minute, onlineScoreRow{Metric: "safety_v1", Score: f(0.1), Status: "failed"})

	got, err := store.FailedOnlineSessions(now.Add(-24*time.Hour).UnixMilli(), now.Add(time.Minute).UnixMilli(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].SessionID != "elsewhere" || got[1].SessionID != "recent" || got[2].SessionID != "old" {
		t.Fatalf("failed = %+v, want elsewhere, recent, old (last scored first; a pass and an error are not failures)", got)
	}
	if got[1].Lowest != 0.2 || !strings.Contains(got[1].Metrics, "hallucinations_v1") || !strings.Contains(got[1].Metrics, "safety_v1") {
		t.Errorf("recent = %+v, want lowest 0.2 over its two failed metrics", got[1])
	}

	kept := filterByTenant(got, RoleInfo{Tenants: []string{"acme"}}, func(f failedOnlineSession) string { return f.Tenant })
	if len(kept) != 2 {
		t.Errorf("a role limited to acme keeps %d, want 2", len(kept))
	}
}

func TestFailedUsageSessions(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "failedcalls.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now()
	ms := func(ago time.Duration) int64 { return now.Add(-ago).UnixMilli() }
	if err := store.InsertUsage([]usageRow{
		{SpanID: "a1", SessionID: "old-fail", StartMs: ms(3 * time.Hour), Tenant: "acme", Kind: "chat", IsError: true},
		{SpanID: "b1", SessionID: "recent-fail", StartMs: ms(10 * time.Minute), Tenant: "acme", Kind: "voice", IsError: true},
		{SpanID: "b2", SessionID: "recent-fail", StartMs: ms(9 * time.Minute), Tenant: "acme", Kind: "voice"},
		{SpanID: "c1", SessionID: "fine", StartMs: ms(5 * time.Minute), Tenant: "acme", Kind: "chat"},
		{SpanID: "d1", SessionID: "other-tenant", StartMs: ms(time.Minute), Tenant: "other", Kind: "chat", IsError: true},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.FailedUsageSessions(now.Add(-24*time.Hour), now.Add(time.Minute), 5, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].SessionID != "other-tenant" || got[1].SessionID != "recent-fail" || got[2].SessionID != "old-fail" {
		t.Fatalf("failed sessions = %+v, want other-tenant, recent-fail, old-fail (last failure first, none for 'fine')", got)
	}
	if got[1].Failed != 1 || got[1].Calls != 2 {
		t.Errorf("recent-fail counts = %d failed of %d calls, want 1 of 2", got[1].Failed, got[1].Calls)
	}
	got, err = store.FailedUsageSessions(now.Add(-24*time.Hour), now.Add(time.Minute), 1, []string{"acme"})
	if err != nil || len(got) != 1 || got[0].SessionID != "recent-fail" {
		t.Errorf("limited to acme and 1 = %+v %v, want recent-fail only", got, err)
	}
}
