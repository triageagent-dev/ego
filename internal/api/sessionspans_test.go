package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// TestSessionSpansHandler: GET /api/streaming/session-spans returns every
// span of the session depth-first with its depth and attributes, and 404s
// an unknown session.
func TestSessionSpansHandler(t *testing.T) {
	store := NewSessionStore(nil)
	store.Ingest(sampleGenAISpanTracesBody("sess-1", "aaaa", "root-b", "user-b", "asst-b", "hello there", "hi, how can I help"))
	sessionID := store.List()[0].SessionID

	rec := httptest.NewRecorder()
	sessionSpansHandler(store)(rec, httptest.NewRequest("GET", "/api/streaming/session-spans?session_id="+sessionID, nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data struct {
			Spans []sessionSpanDTO `json:"spans"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	spans := resp.Data.Spans
	if len(spans) == 0 {
		t.Fatal("no spans returned")
	}
	if spans[0].Depth != 0 || spans[0].ParentSpanID != "" {
		t.Errorf("first span = %+v, want the root at depth 0", spans[0])
	}
	byID := map[string]sessionSpanDTO{}
	for _, s := range spans {
		byID[s.SpanID] = s
		if s.Tags == nil {
			t.Errorf("span %s has nil tags", s.SpanID)
		}
	}
	for _, s := range spans {
		if p, ok := byID[s.ParentSpanID]; ok && s.Depth != p.Depth+1 {
			t.Errorf("span %s depth %d, parent depth %d", s.SpanID, s.Depth, p.Depth)
		}
	}

	rec = httptest.NewRecorder()
	sessionSpansHandler(store)(rec, httptest.NewRequest("GET", "/api/streaming/session-spans?session_id=nope", nil))
	if rec.Code != 404 {
		t.Errorf("unknown session status = %d, want 404", rec.Code)
	}
}
