package api

import (
	"net/http"
	"net/http/httptest"
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
