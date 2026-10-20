package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

// sampleTracesBodyWithParent is sampleTracesBody plus an explicit
// parentSpanId, for tests that need a non-root span (so only the idle
// timer, not the completion-grace timer, drives completion).
func sampleTracesBodyWithParent(sessionName, traceID, spanID, parentSpanID string) map[string]any {
	body := sampleTracesBody(sessionName, traceID, spanID)
	spans := body["resourceSpans"].([]any)[0].(map[string]any)["scopeSpans"].([]any)[0].(map[string]any)["spans"].([]any)
	spans[0].(map[string]any)["parentSpanId"] = parentSpanID
	return body
}

// waitForCondition polls cond every 5ms until it's true or timeout elapses,
// failing the test on timeout. Avoids flaking on exact timer-fire timing
// while keeping these tests fast (default idle/completion durations are
// overridden to a few tens of milliseconds).
func waitForCondition(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !cond() {
		t.Fatal("condition not met before timeout")
	}
}

// TestSessionStore_RootSpanCompletesSessionAfterGracePeriod reproduces the
// "duration never stops" bug: a session must eventually flip IsComplete
// (and get a CompletedAt) once its root span's grace period elapses, or
// the UI's useDuration hook (SessionMetadata.tsx) ticks forever.
func TestSessionStore_RootSpanCompletesSessionAfterGracePeriod(t *testing.T) {
	hub := newSSEHub()
	ch := hub.register()
	defer hub.unregister(ch)

	store := NewSessionStore(hub)
	store.completionGrace = 10 * time.Millisecond

	// sampleTracesBody's span has no parentSpanId, i.e. it's a root span.
	store.Ingest(sampleTracesBody("sess-1", "aaaa", "span-a"))

	sessions := store.List()
	if sessions[0].IsComplete {
		t.Fatal("session must not be complete immediately after ingest (grace period not yet elapsed)")
	}

	waitForCondition(t, time.Second, func() bool {
		return store.List()[0].IsComplete
	})

	summary := store.List()[0]
	if summary.CompletedAt == nil {
		t.Error("CompletedAt must be set once IsComplete is true")
	}

	// A session_complete event must reach SSE clients so the UI flips
	// status from 'active' to 'complete' (LiveStreamingView.tsx).
	deadline := time.After(time.Second)
	for {
		select {
		case ev := <-ch:
			if sc, ok := ev.(sessionCompleteEvent); ok {
				if sc.SessionID != "sess-1" || sc.Type != "session_complete" {
					t.Errorf("session_complete event = %+v, want sessionId=sess-1", sc)
				}
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for session_complete SSE event")
		}
	}
}

func TestSessionStore_IdleTimeoutCompletesSessionWithoutRootSpan(t *testing.T) {
	store := NewSessionStore(nil)
	store.idleTimeout = 10 * time.Millisecond
	store.completionGrace = time.Hour // must not be what completes this session

	store.Ingest(sampleTracesBodyWithParent("sess-1", "aaaa", "span-a", "parent-span"))

	if store.List()[0].IsComplete {
		t.Fatal("session must not be complete immediately after ingest")
	}

	waitForCondition(t, time.Second, func() bool {
		return store.List()[0].IsComplete
	})
}

// Rather than leaving the session stuck complete, it must reopen.
func TestSessionStore_ReopensCompletedSessionOnLateSpan(t *testing.T) {
	store := NewSessionStore(nil)
	store.completionGrace = 10 * time.Millisecond

	store.Ingest(sampleTracesBody("sess-1", "aaaa", "span-a"))
	waitForCondition(t, time.Second, func() bool {
		return store.List()[0].IsComplete
	})

	// A second, non-root span for the same session arrives late.
	store.Ingest(sampleTracesBodyWithParent("sess-1", "aaaa", "span-b", "span-a"))

	summary := store.List()[0]
	if summary.IsComplete {
		t.Error("session must reopen (IsComplete=false) once a late span for an already-registered trace arrives")
	}
	if summary.CompletedAt != nil {
		t.Error("CompletedAt must be cleared on reopen")
	}
	if summary.SpanCount != 2 {
		t.Errorf("span count = %d, want 2", summary.SpanCount)
	}
}

func sampleTracesBody(sessionName, traceID, spanID string) map[string]any {
	return map[string]any{
		"resourceSpans": []any{
			map[string]any{
				"resource": map[string]any{
					"attributes": []any{
						map[string]any{"key": sessionNameAttr, "value": map[string]any{"stringValue": sessionName}},
						map[string]any{"key": "service.name", "value": map[string]any{"stringValue": "test-agent"}},
					},
				},
				"scopeSpans": []any{
					map[string]any{
						"scope": map[string]any{"name": "test-scope"},
						"spans": []any{
							map[string]any{
								"traceId": traceID, "spanId": spanID, "name": "invoke_agent",
								"startTimeUnixNano": "1000000000", "endTimeUnixNano": "1002000000",
							},
						},
					},
				},
			},
		},
	}
}

func TestSessionStore_IngestAndList(t *testing.T) {
	store := NewSessionStore(nil)

	n := store.Ingest(sampleTracesBody("sess-1", "aaaa", "span-a"))
	if n != 1 {
		t.Fatalf("ingested %d spans, want 1", n)
	}

	sessions := store.List()
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(sessions))
	}
	if sessions[0].SessionID != "sess-1" {
		t.Errorf("session ID = %q, want %q", sessions[0].SessionID, "sess-1")
	}
	if sessions[0].SpanCount != 1 {
		t.Errorf("span count = %d, want 1", sessions[0].SpanCount)
	}
}

// A session whose only resource attribute is ego.session_name must report empty metadata, not that attribute
// echoed back.
func TestSessionStore_MetadataExcludesSessionAttrs(t *testing.T) {
	store := NewSessionStore(nil)
	store.Ingest(sampleTracesBody("sess-1", "aaaa", "span-a"))

	sessions := store.List()
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(sessions))
	}
	metadata := sessions[0].Metadata
	if _, present := metadata[sessionNameAttr]; present {
		t.Errorf("metadata = %v, must not include %q", metadata, sessionNameAttr)
	}
	if metadata["service.name"] != "test-agent" {
		t.Errorf("metadata[service.name] = %v, want %q (other attrs must survive)", metadata["service.name"], "test-agent")
	}
}

func TestSessionStore_MultipleTracesShareSession(t *testing.T) {
	store := NewSessionStore(nil)

	store.Ingest(sampleTracesBody("sess-1", "aaaa", "span-a"))
	store.Ingest(sampleTracesBody("sess-1", "bbbb", "span-b"))

	sessions := store.List()
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1 (same session_name should merge)", len(sessions))
	}
	if sessions[0].SpanCount != 2 {
		t.Errorf("span count = %d, want 2", sessions[0].SpanCount)
	}
	if len(store.sessions["sess-1"].TraceIDs) != 2 {
		t.Errorf("trace IDs = %v, want 2 distinct trace IDs", store.sessions["sess-1"].TraceIDs)
	}
}

func TestSessionStore_FallsBackToSyntheticNameWithoutSessionName(t *testing.T) {
	store := NewSessionStore(nil)
	body := map[string]any{
		"resourceSpans": []any{
			map[string]any{
				"resource": map[string]any{},
				"scopeSpans": []any{
					map[string]any{
						"scope": map[string]any{},
						"spans": []any{
							map[string]any{
								"traceId": "cccccccccccc1234", "spanId": "span-c", "name": "invoke_agent",
								"startTimeUnixNano": "1000", "endTimeUnixNano": "2000",
							},
						},
					},
				},
			},
		},
	}

	store.Ingest(body)
	sessions := store.List()
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(sessions))
	}
	want := "otlp-cccccccccccc"
	if sessions[0].SessionID != want {
		t.Errorf("session ID = %q, want %q (first 12 chars of trace ID)", sessions[0].SessionID, want)
	}
}

// A server's batched export carries many traces in one resourceSpans; each
// trace must route by its own gen_ai.conversation.id, and a trace without
// one must not be pulled into another trace's conversation.
func TestSessionStore_ConversationIDRoutesPerTrace(t *testing.T) {
	store := NewSessionStore(nil)
	span := func(traceID, spanID, conv string) map[string]any {
		s := map[string]any{
			"traceId": traceID, "spanId": spanID, "name": "llm.chat.completions",
			"startTimeUnixNano": "1000", "endTimeUnixNano": "2000",
		}
		if conv != "" {
			s["attributes"] = []any{map[string]any{"key": "gen_ai.conversation.id", "value": map[string]any{"stringValue": conv}}}
		}
		return s
	}
	body := map[string]any{
		"resourceSpans": []any{
			map[string]any{
				"resource": map[string]any{},
				"scopeSpans": []any{
					map[string]any{
						"scope": map[string]any{},
						"spans": []any{
							span("aaaaaaaaaaaa0001", "a1", "concern-a"),
							span("bbbbbbbbbbbb0002", "b1", "concern-b"),
							span("dddddddddddd0003", "d1", ""),
						},
					},
				},
			},
		},
	}

	store.Ingest(body)
	got := map[string]int{}
	for _, s := range store.List() {
		got[s.SessionID] = s.SpanCount
	}
	want := map[string]int{"concern-a": 1, "concern-b": 1, "otlp-dddddddddddd": 1}
	if len(got) != len(want) {
		t.Fatalf("sessions = %v, want %v", got, want)
	}
	for id, n := range want {
		if got[id] != n {
			t.Errorf("session %q has %d span(s), want %d (all: %v)", id, got[id], n, got)
		}
	}
}

func TestSessionStore_Trace(t *testing.T) {
	store := NewSessionStore(nil)
	store.Ingest(sampleTracesBody("sess-1", "aaaa", "span-a"))

	tr, ok := store.Trace("sess-1")
	if !ok {
		t.Fatal("expected session sess-1 to exist")
	}
	if len(tr.AllSpans) != 1 || tr.AllSpans[0].SpanID != "span-a" {
		t.Errorf("spans = %v, want [span-a]", tr.AllSpans)
	}

	if _, ok := store.Trace("nonexistent"); ok {
		t.Error("expected nonexistent session to be absent")
	}
}

// TestSpanWindowMs: a session's duration is its span window (earliest
// start to latest end), not ingest wall-clock - a 464ms call reads 464ms,
// not the 3s completion grace.
func TestSpanWindowMs(t *testing.T) {
	if got := spanWindowMs(nil); got != 0 {
		t.Fatalf("no spans: got %d, want 0", got)
	}
	spans := []*tracepkg.Span{
		{StartTime: 1_000_000, Duration: 464_000}, // root: 1.000s-1.464s
		{StartTime: 1_010_000, Duration: 300_000}, // child inside it
		{StartTime: 900_000, Duration: 50_000},    // earlier span
		{StartTime: 1_400_000, Duration: 100_000}, // ends last, 1.500s
	}
	if got := spanWindowMs(spans); got != 600 {
		t.Fatalf("got %d ms, want 600", got)
	}
}

func TestSessionsHandler_ByID(t *testing.T) {
	store := NewSessionStore(nil)
	store.Ingest(sampleTracesBody("sess-by-id", "aaaa", "span-a"))
	store.Ingest(sampleTracesBody("sess-other", "bbbb", "span-b"))

	get := func(url string) []SessionSummary {
		rec := httptest.NewRecorder()
		sessionsHandler(store)(rec, httptest.NewRequest("GET", url, nil))
		var body struct {
			Data []SessionSummary `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return body.Data
	}

	if got := get("/api/streaming/sessions?id=sess-by-id"); len(got) != 1 || got[0].SessionID != "sess-by-id" {
		t.Errorf("by id = %+v, want only sess-by-id", got)
	}
	if got := get("/api/streaming/sessions?id=nope"); len(got) != 0 {
		t.Errorf("unknown id = %+v, want empty", got)
	}
	if got := get("/api/streaming/sessions"); len(got) != 2 {
		t.Errorf("no id = %d sessions, want 2", len(got))
	}
}
