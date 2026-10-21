package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestWSUpdatesHandler_LiveEvents mirrors TestUIUpdatesStream_LiveEvents
// (the SSE test) but drives a real WebSocket client against a real HTTP
// server, proving the WS transport delivers the identical event sequence
// the SSE transport does, from the same hub.
func TestWSUpdatesHandler_LiveEvents(t *testing.T) {
	hub := newSSEHub()
	store := NewSessionStore(hub)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/ui-updates", wsUpdatesHandler(hub))
	server := httptest.NewServer(mux)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/ui-updates"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dialing WS: %v", err)
	}
	defer conn.Close()

	events := make(chan map[string]any, 32)
	go func() {
		for {
			var msg map[string]any
			if err := conn.ReadJSON(&msg); err != nil {
				close(events)
				return
			}
			events <- msg
		}
	}()

	// Give the WS handler a moment to register before ingesting, same as
	// the SSE test - broadcast is fire-and-forget, not buffered for a
	// not-yet-connected client.
	time.Sleep(50 * time.Millisecond)

	body := map[string]any{
		"resourceSpans": []any{
			map[string]any{
				"resource": map[string]any{
					"attributes": []any{
						map[string]any{"key": "ego.session_name", "value": map[string]any{"stringValue": "ws-live-sess"}},
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
		case ev, ok := <-events:
			if !ok {
				t.Fatalf("WS connection closed early; got %v so far", gotTypes)
			}
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

// TestWSUpdatesHandler_SharesHubWithSSE proves a single OTLP ingest
// reaches both an SSE client and a WS client connected at the same time,
// confirming WS was added alongside SSE, not swapped in for it.
func TestWSUpdatesHandler_SharesHubWithSSE(t *testing.T) {
	hub := newSSEHub()
	store := NewSessionStore(hub)

	mux := http.NewServeMux()
	mux.HandleFunc("/stream/ui-updates", uiUpdatesHandler(hub))
	mux.HandleFunc("/ws/ui-updates", wsUpdatesHandler(hub))
	server := httptest.NewServer(mux)
	defer server.Close()

	// SSE client.
	sseResp, err := http.Get(server.URL + "/stream/ui-updates")
	if err != nil {
		t.Fatalf("connecting SSE: %v", err)
	}
	defer sseResp.Body.Close()
	sseEvents := make(chan map[string]any, 32)
	go readSSE(t, sseResp.Body, sseEvents)

	// WS client.
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/ui-updates"
	wsConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dialing WS: %v", err)
	}
	defer wsConn.Close()
	wsEvents := make(chan map[string]any, 32)
	go func() {
		for {
			var msg map[string]any
			if err := wsConn.ReadJSON(&msg); err != nil {
				return
			}
			wsEvents <- msg
		}
	}()

	time.Sleep(50 * time.Millisecond)

	store.Ingest(sampleTracesBody("shared-hub-sess", "aaaa", "span-a"))

	deadline := time.After(2 * time.Second)
	var sawSSE, sawWS bool
	for !sawSSE || !sawWS {
		select {
		case ev := <-sseEvents:
			if ev["type"] == "session_started" {
				sawSSE = true
			}
		case ev := <-wsEvents:
			if ev["type"] == "session_started" {
				sawWS = true
			}
		case <-deadline:
			t.Fatalf("timed out: sawSSE=%v sawWS=%v", sawSSE, sawWS)
		}
	}
}

func TestWSUpdatesHandler_SendsHeartbeats(t *testing.T) {
	old := wsPingInterval
	wsPingInterval = 30 * time.Millisecond
	defer func() { wsPingInterval = old }()

	hub := newSSEHub()
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/ui-updates", wsUpdatesHandler(hub))
	server := httptest.NewServer(mux)
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws/ui-updates", nil)
	if err != nil {
		t.Fatalf("dialing WS: %v", err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	var msg map[string]any
	if err := conn.ReadJSON(&msg); err != nil {
		t.Fatalf("no heartbeat on an idle feed: %v", err)
	}
	if msg["type"] != "heartbeat" {
		t.Fatalf("first message on an idle feed = %v, want a heartbeat", msg)
	}
}
