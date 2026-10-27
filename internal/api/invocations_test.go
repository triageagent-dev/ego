package api

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

// sampleGenAISpanTracesBody builds an OTLP/JSON traces body reproducing
// triage-core's real span shape: a "voice.session" root span (no gen_ai
// attrs itself) with two child spans, "llm.gemini_realtime" carrying
// gen_ai.request.model + input.value (the user utterance) and a second
// carrying gen_ai.request.model + output.value (the assistant reply) - so
// completeSession's batch converter has something real to convert.
func sampleGenAISpanTracesBody(sessionName, traceID, rootSpanID, userSpanID, assistantSpanID, inputValue, outputValue string) map[string]any {
	llmAttrs := func(value, key string) []any {
		return []any{
			map[string]any{"key": "gen_ai.request.model", "value": map[string]any{"stringValue": "gemini-live"}},
			map[string]any{"key": key, "value": map[string]any{"stringValue": value}},
		}
	}
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
								"traceId": traceID, "spanId": rootSpanID, "name": "voice.session",
								"startTimeUnixNano": "1000000000", "endTimeUnixNano": "1010000000",
							},
							map[string]any{
								"traceId": traceID, "spanId": userSpanID, "parentSpanId": rootSpanID, "name": "llm.gemini_realtime",
								"startTimeUnixNano": "1001000000", "endTimeUnixNano": "1002000000",
								"attributes": llmAttrs(inputValue, "input.value"),
							},
							map[string]any{
								"traceId": traceID, "spanId": assistantSpanID, "parentSpanId": rootSpanID, "name": "llm.gemini_realtime",
								"startTimeUnixNano": "1003000000", "endTimeUnixNano": "1004000000",
								"attributes": llmAttrs(outputValue, "output.value"),
							},
						},
					},
				},
			},
		},
	}
}

// TestSessionStore_CompleteSessionPopulatesInvocations reproduces "i can
// not see tools calling" / "detailed information not survive browser
// reload": a completed session's summary must carry real, batch-converted
// invocations (user/agent text at minimum), not the previously-hardcoded
// empty list.
func TestSessionStore_CompleteSessionPopulatesInvocations(t *testing.T) {
	hub := newSSEHub()
	ch := hub.register()
	defer hub.unregister(ch)

	store := NewSessionStore(hub)
	store.completionGrace = 10 * time.Millisecond

	store.Ingest(sampleGenAISpanTracesBody("sess-1", "aaaa", "root-a", "user-a", "asst-a", "what time is it", "it is noon"))

	waitForCondition(t, time.Second, func() bool {
		return store.List()[0].IsComplete
	})

	summary := store.List()[0]
	if len(summary.Invocations) != 1 {
		t.Fatalf("expected 1 invocation on completed session, got %d: %+v", len(summary.Invocations), summary.Invocations)
	}
	inv := summary.Invocations[0]
	if inv.UserText != "what time is it" {
		t.Errorf("UserText = %q, want %q", inv.UserText, "what time is it")
	}
	if inv.AgentText != "it is noon" {
		t.Errorf("AgentText = %q, want %q", inv.AgentText, "it is noon")
	}

	deadline := time.After(time.Second)
	for {
		select {
		case ev := <-ch:
			if sc, ok := ev.(sessionCompleteEvent); ok {
				if len(sc.Invocations) != 1 {
					t.Errorf("session_complete event invocations = %+v, want 1 entry", sc.Invocations)
				}
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for session_complete SSE event")
		}
	}
}

// TestCreateEvalSetHandler reproduces "Failed to prepare evaluation: Failed
// to create eval set from golden session": POST /api/streaming/create-eval-set
// on a completed session must return a real ADK EvalSet, not 404.
func TestCreateEvalSetHandler(t *testing.T) {
	store := NewSessionStore(nil)
	store.completionGrace = 10 * time.Millisecond

	store.Ingest(sampleGenAISpanTracesBody("sess-1", "aaaa", "root-b", "user-b", "asst-b", "hello there", "hi, how can I help"))
	waitForCondition(t, time.Second, func() bool {
		return store.List()[0].IsComplete
	})

	sessionID := store.List()[0].SessionID
	reqBody, _ := json.Marshal(createEvalSetRequest{SessionID: sessionID, EvalSetID: "my-eval-set"})

	req := httptest.NewRequest("POST", "/api/streaming/create-eval-set", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()
	createEvalSetHandler(store)(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Data struct {
			EvalSet        json.RawMessage `json:"evalSet"`
			NumInvocations int             `json:"numInvocations"`
		} `json:"data"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v (body=%s)", err, rec.Body.String())
	}
	if resp.Error != nil {
		t.Errorf("error = %v, want nil", resp.Error)
	}
	if resp.Data.NumInvocations != 1 {
		t.Errorf("numInvocations = %d, want 1", resp.Data.NumInvocations)
	}

	var evalSet map[string]any
	if err := json.Unmarshal(resp.Data.EvalSet, &evalSet); err != nil {
		t.Fatalf("decoding evalSet: %v", err)
	}
	if evalSet["eval_set_id"] != "my-eval-set" {
		t.Errorf("eval_set_id = %v, want my-eval-set", evalSet["eval_set_id"])
	}
	cases, _ := evalSet["eval_cases"].([]any)
	if len(cases) != 1 {
		t.Fatalf("eval_cases = %+v, want 1 entry", cases)
	}
}

// TestCreateEvalSetHandler_UnknownSession confirms a bad session_id 404s
// rather than 500ing or falling through to the SPA static-file handler
// (which previously masked this endpoint's total absence).
func TestCreateEvalSetHandler_UnknownSession(t *testing.T) {
	store := NewSessionStore(nil)
	reqBody, _ := json.Marshal(createEvalSetRequest{SessionID: "does-not-exist", EvalSetID: "x"})
	req := httptest.NewRequest("POST", "/api/streaming/create-eval-set", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()
	createEvalSetHandler(store)(rec, req)

	if rec.Code != 404 {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// A session accumulates turns over time, so a turn reports when it started: the earliest of its spans, in
// Unix milliseconds.
func TestExtractModelInfoFromSpans_TurnStartedAt(t *testing.T) {
	const startMicros = 1_760_000_000_000_000
	spans := []*tracepkg.Span{
		{SpanID: "b", StartTime: startMicros + 5_000_000, Duration: 1_000_000},
		{SpanID: "a", StartTime: startMicros, Duration: 2_000_000},
	}
	info := extractModelInfoFromSpans(spans)
	if info == nil {
		t.Fatal("info is nil")
	}
	if want := int64(startMicros / 1000); info.TurnStartedAt != want {
		t.Errorf("TurnStartedAt = %d, want %d", info.TurnStartedAt, want)
	}
	if info.TurnLatencyMs != 6000 {
		t.Errorf("TurnLatencyMs = %v, want 6000", info.TurnLatencyMs)
	}
}

// A turn says how much of its input the prompt cache served, summed over its model calls.
func TestExtractModelInfoFromSpans_CacheTokens(t *testing.T) {
	call := func(read, write int64) *tracepkg.Span {
		return &tracepkg.Span{OperationName: "chat", StartTime: 1, Duration: 1, Tags: map[string]any{
			"gen_ai.request.model": "m", "gen_ai.usage.input_tokens": int64(1000), "gen_ai.usage.output_tokens": int64(10),
			"gen_ai.usage.cache_read.input_tokens": read, "gen_ai.usage.cache_creation.input_tokens": write,
		}}
	}
	info := extractModelInfoFromSpans([]*tracepkg.Span{call(800, 0), call(900, 50)})
	if info == nil || info.CacheReadTokens != 1700 || info.CacheWriteTokens != 50 {
		t.Fatalf("cache = %+v", info)
	}
}
