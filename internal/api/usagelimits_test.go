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
	release, _, _ := g.admit(time.Now())
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

func TestFailedUsageSessions(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "failed.db"))
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
