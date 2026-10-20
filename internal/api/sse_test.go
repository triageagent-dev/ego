package api

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestUIUpdatesStream_LiveEvents starts a real HTTP server (via httptest),
// connects to /stream/ui-updates the same way the UI's EventSource does,
// then POSTs an OTLP/JSON trace containing a user turn, a tool call/result,
// and an agent response, and asserts the expected SSE event sequence
// arrives: session_started, user_input, tool_call, tool_result,
// agent_response, token_update (no per-span span_received, see Ingest).
func TestUIUpdatesStream_LiveEvents(t *testing.T) {
	hub := newSSEHub()
	store := NewSessionStore(hub)

	mux := http.NewServeMux()
	mux.HandleFunc("/stream/ui-updates", uiUpdatesHandler(hub))
	server := httptest.NewServer(mux)
	defer server.Close()

	resp, err := http.Get(server.URL + "/stream/ui-updates")
	if err != nil {
		t.Fatalf("connecting to SSE stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", resp.Header.Get("Content-Type"))
	}

	events := make(chan map[string]any, 32)
	go readSSE(t, resp.Body, events)

	// Give the SSE handler a moment to register before ingesting, so no
	// events are missed (broadcast is fire-and-forget, not buffered per
	// not-yet-connected client).
	time.Sleep(50 * time.Millisecond)

	body := map[string]any{
		"resourceSpans": []any{
			map[string]any{
				"resource": map[string]any{
					"attributes": []any{
						map[string]any{"key": "ego.session_name", "value": map[string]any{"stringValue": "live-sess"}},
					},
				},
				"scopeSpans": []any{
					map[string]any{
						"scope": map[string]any{"name": "gcp.vertex.agent"},
						"spans": []any{
							llmSpanWithMessages(),
							toolSpan(),
						},
					},
				},
			},
		},
	}
	store.Ingest(body)

	wantTypes := []string{"session_started", "user_input", "agent_response", "token_update", "tool_call", "tool_result"}
	gotTypes := make([]string, 0, len(wantTypes))
	deadline := time.After(2 * time.Second)
	for len(gotTypes) < len(wantTypes) {
		select {
		case ev := <-events:
			gotTypes = append(gotTypes, ev["type"].(string))
		case <-deadline:
			t.Fatalf("timed out waiting for events; got %v so far", gotTypes)
		}
	}

	for i, want := range wantTypes {
		if gotTypes[i] != want {
			t.Errorf("event[%d] = %q, want %q (full sequence: %v)", i, gotTypes[i], want, gotTypes)
		}
	}
}

func llmSpanWithMessages() map[string]any {
	return map[string]any{
		"traceId": "aaaa", "spanId": "llm-span", "name": "call_llm",
		"startTimeUnixNano": "1700000000000000000", "endTimeUnixNano": "1700000001000000000",
		"attributes": []any{
			attr("gcp.vertex.agent.llm_request", `{"contents":[{"role":"user","parts":[{"text":"hi there"}]}]}`),
			attr("gcp.vertex.agent.llm_response", `{"content":{"role":"model","parts":[{"text":"hello!"}]},"usage_metadata":{"prompt_token_count":5,"candidates_token_count":3}}`),
		},
	}
}

func toolSpan() map[string]any {
	return map[string]any{
		"traceId": "aaaa", "spanId": "tool-span", "parentSpanId": "llm-span", "name": "execute_tool lookup",
		"startTimeUnixNano": "1700000000500000000", "endTimeUnixNano": "1700000000800000000",
		"attributes": []any{
			attr("gen_ai.tool.name", "lookup"),
			attr("gcp.vertex.agent.tool_call_args", `{"query":"weather"}`),
			attr("gcp.vertex.agent.tool_response", `{"result":"sunny"}`),
		},
	}
}

func attr(key, value string) map[string]any {
	return map[string]any{"key": key, "value": map[string]any{"stringValue": value}}
}

// readSSE parses a text/event-stream body ("data: {...}\n\n" frames) and
// pushes each decoded JSON event onto ch.
func readSSE(t *testing.T, body io.Reader, ch chan<- map[string]any) {
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			t.Logf("failed to decode SSE event %q: %v", line, err)
			continue
		}
		ch <- ev
	}
}
