package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// llmSpanBody is one OTLP/HTTP JSON export with an LLM span
// (OpenInference token counts, a tenant and gen_ai.agent.name) under an AGENT parent
// that repeats no counts.
func llmSpanBody(traceID, spanID string, startMs int64, prompt, completion int, statusCode int) map[string]any {
	ns := func(ms int64) string { return strconvI(ms * 1_000_000) }
	attr := func(k string, v map[string]any) map[string]any { return map[string]any{"key": k, "value": v} }
	str := func(s string) map[string]any { return map[string]any{"stringValue": s} }
	num := func(n int) map[string]any { return map[string]any{"intValue": strconvI(int64(n))} }
	return map[string]any{"resourceSpans": []any{map[string]any{
		"resource": map[string]any{"attributes": []any{attr("service.name", str("checkout-agent"))}},
		"scopeSpans": []any{map[string]any{"scope": map[string]any{}, "spans": []any{
			map[string]any{
				"traceId": traceID, "spanId": "agent-" + spanID, "name": "agent.reflection",
				"startTimeUnixNano": ns(startMs), "endTimeUnixNano": ns(startMs + 3000),
				"attributes": []any{attr("openinference.span.kind", str("AGENT"))},
			},
			map[string]any{
				"traceId": traceID, "spanId": spanID, "parentSpanId": "agent-" + spanID, "name": "llm.chat.completions",
				"startTimeUnixNano": ns(startMs + 10), "endTimeUnixNano": ns(startMs + 2010),
				"status": map[string]any{"code": float64(statusCode)},
				"attributes": []any{
					attr("openinference.span.kind", str("LLM")),
					attr("llm.model_name", str("gemini-2.5-flash")),
					attr("llm.token_count.prompt", num(prompt)),
					attr("llm.token_count.completion", num(completion)),
					attr("tenant.id", str("shop")),
					attr("gen_ai.agent.name", str("reflection")),
				},
			},
		}}},
	}}}
}

func strconvI(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestUsageLedger_IngestFlushAndQuery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.db")
	archive, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	store := NewSessionStoreWithArchive(nil, archive, SessionLimits{UsageTenantAttr: "tenant.id"})
	now := time.Now().Add(-time.Hour).UnixMilli()

	store.Ingest(llmSpanBody("aaaaaaaaaaaa0001", "llm1", now, 1000, 100, 1))
	store.Ingest(llmSpanBody("aaaaaaaaaaaa0002", "llm2", now+60_000, 500, 50, 2))
	// The same span again (a retried export) must not count twice.
	store.Ingest(llmSpanBody("aaaaaaaaaaaa0001", "llm1", now, 1000, 100, 1))
	if err := store.flushPersist(); err != nil {
		t.Fatal(err)
	}

	cells, err := archive.UsageSeries(time.Now().Add(-24*time.Hour), time.Now(), int64(24*time.Hour/time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	var calls, errs, in, out int64
	for _, c := range cells {
		if c.Kind != "reflection" || c.Tenant != "shop" || c.Model != "gemini-2.5-flash" || c.Service != "checkout-agent" {
			t.Errorf("unexpected cell dimensions: %+v", c)
		}
		calls += c.Calls
		errs += c.Errors
		in += c.InputTokens
		out += c.OutputTokens
	}
	if calls != 2 || in != 1500 || out != 150 || errs != 1 {
		t.Errorf("got calls=%d in=%d out=%d errors=%d, want 2/1500/150/1 (AGENT parent not counted, duplicate ignored)", calls, in, out, errs)
	}

	top, err := archive.TopUsageCalls(time.Now().Add(-24*time.Hour), time.Now(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 2 || top[0].SpanID != "llm1" || top[0].SessionID != "otlp-aaaaaaaaaaaa" {
		t.Errorf("top calls = %+v, want llm1 first in session otlp-aaaaaaaaaaaa", top)
	}
	store.Close()

	// A restart backfills from the archive without duplicating rows.
	archive2, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	store2 := NewSessionStoreWithArchive(nil, archive2, SessionLimits{})
	if err := store2.LoadPersisted(); err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	top, _ = archive2.TopUsageCalls(time.Now().Add(-24*time.Hour), time.Now(), 10)
	if len(top) != 2 {
		t.Errorf("after restart got %d rows, want 2", len(top))
	}
}

func TestUsageLedger_BackfillsArchiveWithoutLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	archive, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	store := NewSessionStore(nil) // no archive: spans are not ledgered on ingest
	store.Ingest(llmSpanBody("bbbbbbbbbbbb0001", "llm1", time.Now().Add(-time.Hour).UnixMilli(), 10, 5, 1))
	if err := archive.SaveAll(store.sessions); err != nil {
		t.Fatal(err)
	}

	restored := NewSessionStoreWithArchive(nil, archive, SessionLimits{})
	if err := restored.LoadPersisted(); err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	top, _ := archive.TopUsageCalls(time.Now().Add(-24*time.Hour), time.Now(), 10)
	if len(top) != 1 || top[0].InputTokens != 10 {
		t.Errorf("backfill rows = %+v, want one row with 10 input tokens", top)
	}
}

func TestUsageHandler_WindowAndStorage(t *testing.T) {
	rec := httptest.NewRecorder()
	usageHandler(nil)(rec, httptest.NewRequest(http.MethodGet, "/api/usage", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no archive: status %d, want 503", rec.Code)
	}

	archive, err := NewSQLiteStore(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	usageHandler(archive)(rec, httptest.NewRequest(http.MethodGet, "/api/usage?hours=168", nil))
	var body struct {
		Data struct {
			From, To, BucketMs int64
			Buckets            []usageBucketDTO
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Data.BucketMs != int64(24*time.Hour/time.Millisecond) {
		t.Errorf("7-day window bucket = %d ms, want one day", body.Data.BucketMs)
	}
	if body.Data.To-body.Data.From != int64(168*time.Hour/time.Millisecond) {
		t.Errorf("window = %d ms, want 168h", body.Data.To-body.Data.From)
	}
	if body.Data.Buckets == nil {
		t.Error("buckets must be [] not null when empty")
	}
}

// A session's summary names its root span and counts its LLM calls, so the
// UI can label and hide traces that made none.
func TestSummarize_RootSpanAndLLMCalls(t *testing.T) {
	store := NewSessionStore(nil)
	store.Ingest(llmSpanBody("cccccccccccc0001", "llm1", time.Now().UnixMilli(), 10, 5, 1))
	got := store.List()
	if len(got) != 1 {
		t.Fatalf("got %d sessions, want 1", len(got))
	}
	if got[0].RootSpanName != "agent.reflection" || got[0].LLMCalls != 1 {
		t.Errorf("root=%q llmCalls=%d, want agent.reflection / 1", got[0].RootSpanName, got[0].LLMCalls)
	}
}

// A span without gen_ai.agent.name takes its trace root's name, even when
// the root ends (and is exported) after the LLM spans, as a voice session's
// root does.
func TestUsageLedger_KindFallsBackToTraceRoot(t *testing.T) {
	archive, err := NewSQLiteStore(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	store := NewSessionStoreWithArchive(nil, archive, SessionLimits{})
	defer store.Close()
	start := time.Now().Add(-time.Hour).UnixMilli()
	ns := func(ms int64) string { return strconvI(ms * 1_000_000) }
	attr := func(k string, v map[string]any) map[string]any { return map[string]any{"key": k, "value": v} }
	export := func(span map[string]any) map[string]any {
		return map[string]any{"resourceSpans": []any{map[string]any{
			"resource":   map[string]any{"attributes": []any{attr("service.name", map[string]any{"stringValue": "voice"})}},
			"scopeSpans": []any{map[string]any{"scope": map[string]any{}, "spans": []any{span}}},
		}}}
	}
	const traceID = "bbbbbbbbbbbb0001"

	store.Ingest(export(map[string]any{
		"traceId": traceID, "spanId": "llm1", "parentSpanId": "root1", "name": "llm.gemini_realtime",
		"startTimeUnixNano": ns(start + 10), "endTimeUnixNano": ns(start + 20),
		"attributes": []any{
			attr("gen_ai.system", map[string]any{"stringValue": "vertex_ai"}),
			attr("gen_ai.request.model", map[string]any{"stringValue": "gemini-live"}),
			attr("gen_ai.usage.input_tokens", map[string]any{"intValue": "900"}),
			attr("gen_ai.usage.output_tokens", map[string]any{"intValue": "90"}),
		},
	}))
	if err := store.flushPersist(); err != nil {
		t.Fatal(err)
	}
	store.Ingest(export(map[string]any{
		"traceId": traceID, "spanId": "root1", "name": "voice.session",
		"startTimeUnixNano": ns(start), "endTimeUnixNano": ns(start + 60_000),
	}))
	if err := store.flushPersist(); err != nil {
		t.Fatal(err)
	}

	cells, err := archive.UsageSeries(time.Now().Add(-24*time.Hour), time.Now(), int64(24*time.Hour/time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if len(cells) != 1 || cells[0].Kind != "voice.session" || cells[0].Tenant != "" {
		t.Errorf("cells = %+v, want one cell of kind voice.session and no tenant", cells)
	}
}

// Many turns of one session rank as one row, ahead of a single larger call.
func TestUsageLedger_TopSessionsGroupsTurns(t *testing.T) {
	archive, err := NewSQLiteStore(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	start := time.Now().Add(-time.Hour).UnixMilli()
	var rows []usageRow
	for i := 0; i < 10; i++ {
		rows = append(rows, usageRow{SpanID: "turn" + strconvI(int64(i)), TraceID: "t1", SessionID: "voice", StartMs: start + int64(i),
			Kind: "voice.session", Model: "live", InputTokens: int64(1000 * (i + 1)), OutputTokens: 10})
	}
	rows = append(rows, usageRow{SpanID: "big", TraceID: "t2", SessionID: "rca", StartMs: start, Kind: "rca", Model: "flash",
		InputTokens: 20000, OutputTokens: 500})
	if err := archive.InsertUsage(rows); err != nil {
		t.Fatal(err)
	}
	got, err := archive.TopUsageSessions(time.Now().Add(-24*time.Hour), time.Now(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(got), got)
	}
	v := got[0]
	if v.SessionID != "voice" || v.Calls != 10 || v.InputTokens != 55000 || v.PeakTokens != 10010 || v.Kinds != "voice.session" {
		t.Errorf("first row = %+v, want the voice session: 10 calls, 55000 in, peak 10010", v)
	}
	if got[1].SessionID != "rca" || got[1].Calls != 1 {
		t.Errorf("second row = %+v, want the single rca call", got[1])
	}
}
