package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// countingDecisionServer answers every question and counts the requests.
func countingDecisionServer(t *testing.T, calls *atomic.Int64) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			Questions map[string]any `json:"questions"`
		}
		_ = json.Unmarshal(raw, &req)
		ans := map[string]any{}
		for k := range req.Questions {
			ans[k] = map[string]any{"type": "noul", "noul": 0.9}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"answers": ans})
	}))
	t.Cleanup(srv.Close)
	for _, k := range []string{"EGO_DECISION_API_KEY", "JEV_API_KEY", "EGO_DECISION_TOKEN_FILE", "EGO_DECISION_MAX_STATE_BYTES", "EGO_DECISION_PROTOCOL", "EGO_DECISION_MODEL", "JEV_MODEL"} {
		t.Setenv(k, "")
	}
	t.Setenv("EGO_DECISION_URL", srv.URL)
}

// cutSpan marks the model span of an onlineSessionBody or onlineTurnBody as holding part of its input.
func cutSpan(body map[string]any) {
	rs := body["resourceSpans"].([]any)[0].(map[string]any)
	span := rs["scopeSpans"].([]any)[0].(map[string]any)["spans"].([]any)[1].(map[string]any)
	span["attributes"] = append(span["attributes"].([]any), map[string]any{"key": "ego.input.truncated", "value": map[string]any{"boolValue": true}})
}

// The daily decision-call cap counts the requests made. A cut session skips jev_grounded while the profile's other
// question is still asked; with jev_grounded listed first that used to count as no call at all, and the zero estimate
// that followed skipped the cap check.
func TestDecisionCallCapHoldsWhenAMetricIsSkipped(t *testing.T) {
	var calls atomic.Int64
	countingDecisionServer(t, &calls)
	e, sessions, _, _ := newTestEngine(t, `{
	 "budget": {"jevCallsPerDay": 3},
	 "sampling": {"perKindPerHour": 1000, "boostPerKindPerHour": 1000},
	 "profiles": [{"kind": "rca", "evaluators": [{"name": "jev_grounded"}, {"name": "jev_task_resolved"}]}]
	}`)
	now := time.Date(2026, 10, 4, 12, 30, 0, 0, time.UTC)
	e.now = func() time.Time { return now }
	for i := 0; i < 25; i++ {
		id := fmt.Sprintf("%012x0000", 0xaaaa0000+i)
		body := onlineSessionBody(id, now.UnixMilli(), "rca", "question "+strings.Repeat("q", i+1), "an answer")
		cutSpan(body)
		sessions.Ingest(body)
		e.process(context.Background(), "otlp-"+shortID(id))
	}
	if made, metered := calls.Load(), e.budget.Status(now).Spent.JevCalls; made != 3 || metered != made {
		t.Errorf("cap 3: %d calls made, %d metered", made, metered)
	}
}

// With some turns cut and some whole the questions go in two passes: both are counted.
func TestDecisionCallsOfBothPassesAreCounted(t *testing.T) {
	var calls atomic.Int64
	countingDecisionServer(t, &calls)
	e, sessions, _, _ := newTestEngine(t, `{"budget":{"jevCallsPerDay":100},"profiles":[{"kind":"rca","maxInvocations":3,"evaluators":[{"name":"jev_task_resolved"},{"name":"jev_grounded"}]}]}`)
	now := time.Date(2026, 10, 4, 12, 30, 0, 0, time.UTC)
	e.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("%012x0000", 0xcccc0000+i)
		body := onlineTurnBody("chat-split", id, now.UnixMilli()+int64(i*5000), "rca", "question "+strings.Repeat("q", i+1), "an answer", false)
		if i == 0 {
			cutSpan(body)
		}
		sessions.Ingest(body)
	}
	e.process(context.Background(), "chat-split")
	if made, metered := calls.Load(), e.budget.Status(now).Spent.JevCalls; made <= 3 || metered != made {
		t.Errorf("%d calls made, %d metered", made, metered)
	}
}
