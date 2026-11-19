package api

import (
	"path/filepath"
	"testing"
	"time"
)

// nestedLLMSpan is one OTLP span of a model call traced twice: the program's span (llm.chat) and the SDK's span
// (anthropic.messages.create) inside it carry the same usage.
func nestedLLMSpan(traceID, spanID, parent, name string, startMs int64, prompt, completion int) map[string]any {
	ns := func(ms int64) string { return strconvI(ms * 1_000_000) }
	attr := func(k string, v map[string]any) map[string]any { return map[string]any{"key": k, "value": v} }
	str := func(s string) map[string]any { return map[string]any{"stringValue": s} }
	num := func(n int) map[string]any { return map[string]any{"intValue": strconvI(int64(n))} }
	sp := map[string]any{
		"traceId": traceID, "spanId": spanID, "name": name,
		"startTimeUnixNano": ns(startMs), "endTimeUnixNano": ns(startMs + 2000),
		"attributes": []any{
			attr("gen_ai.request.model", str("claude-sonnet")),
			attr("gen_ai.usage.input_tokens", num(prompt)),
			attr("gen_ai.usage.output_tokens", num(completion)),
			attr("tenant.id", str("shop")),
			attr("gen_ai.agent.name", str("chat")),
		},
	}
	if parent != "" {
		sp["parentSpanId"] = parent
	}
	return sp
}

func exportOf(spans ...map[string]any) map[string]any {
	list := make([]any, len(spans))
	for i, s := range spans {
		list[i] = s
	}
	return map[string]any{"resourceSpans": []any{map[string]any{
		"resource":   map[string]any{"attributes": []any{map[string]any{"key": "service.name", "value": map[string]any{"stringValue": "core"}}}},
		"scopeSpans": []any{map[string]any{"scope": map[string]any{}, "spans": list}},
	}}}
}

func ledgerTotals(t *testing.T, archive *SQLiteStore) (calls, in, out int64) {
	t.Helper()
	cells, err := archive.UsageSeries(time.Now().Add(-24*time.Hour), time.Now(), int64(24*time.Hour/time.Millisecond), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cells {
		calls += c.Calls
		in += c.InputTokens
		out += c.OutputTokens
	}
	return calls, in, out
}

// The core's llm.chat span and the anthropic.messages.create span under it both carry the usage: the ledger has the
// one call, whichever of the two arrives first, in one export or in two.
func TestUsageLedger_ACallTracedTwiceIsCountedOnce(t *testing.T) {
	now := time.Now().Add(-time.Hour).UnixMilli()
	for name, exports := range map[string]func(trace string) []map[string]any{
		"one export, outer first": func(tr string) []map[string]any {
			return []map[string]any{exportOf(nestedLLMSpan(tr, "outer", "", "llm.chat", now, 20000, 2400), nestedLLMSpan(tr, "inner", "outer", "anthropic.messages.create", now+5, 20000, 2400))}
		},
		"one export, inner first": func(tr string) []map[string]any {
			return []map[string]any{exportOf(nestedLLMSpan(tr, "inner", "outer", "anthropic.messages.create", now+5, 20000, 2400), nestedLLMSpan(tr, "outer", "", "llm.chat", now, 20000, 2400))}
		},
		"the inner span is exported first, the outer one later": func(tr string) []map[string]any {
			return []map[string]any{exportOf(nestedLLMSpan(tr, "inner", "outer", "anthropic.messages.create", now+5, 20000, 2400)), exportOf(nestedLLMSpan(tr, "outer", "", "llm.chat", now, 20000, 2400))}
		},
		"the outer span is exported first, the inner one later": func(tr string) []map[string]any {
			return []map[string]any{exportOf(nestedLLMSpan(tr, "outer", "", "llm.chat", now, 20000, 2400)), exportOf(nestedLLMSpan(tr, "inner", "outer", "anthropic.messages.create", now+5, 20000, 2400))}
		},
	} {
		t.Run(name, func(t *testing.T) {
			archive, err := NewSQLiteStore(filepath.Join(t.TempDir(), "usage.db"))
			if err != nil {
				t.Fatal(err)
			}
			store := NewSessionStoreWithArchive(nil, archive, SessionLimits{UsageTenantAttr: "tenant.id"})
			defer store.Close()
			for _, body := range exports("bbbbbbbbbbbb0001") {
				store.Ingest(body)
				// Flushed between the exports, so a row already written is taken out again.
				if err := store.flushPersist(); err != nil {
					t.Fatal(err)
				}
			}
			if calls, in, out := ledgerTotals(t, archive); calls != 1 || in != 20000 || out != 2400 {
				t.Errorf("calls=%d in=%d out=%d, want one call of 20000 and 2400 (the pair counted twice would be 40000)", calls, in, out)
			}
		})
	}
}

// Rows an earlier version wrote twice are taken out when the archive loads, once.
func TestUsageLedger_RowsWrittenTwiceAreCleanedOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.db")
	archive, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	store := NewSessionStoreWithArchive(nil, archive, SessionLimits{UsageTenantAttr: "tenant.id"})
	now := time.Now().Add(-time.Hour).UnixMilli()
	store.Ingest(exportOf(nestedLLMSpan("bbbbbbbbbbbb0002", "outer", "", "llm.chat", now, 100, 10), nestedLLMSpan("bbbbbbbbbbbb0002", "inner", "outer", "anthropic.messages.create", now+5, 100, 10)))
	if err := store.flushPersist(); err != nil {
		t.Fatal(err)
	}
	// What the old version wrote: the outer span's row too.
	if err := archive.InsertUsage([]usageRow{{SpanID: "outer", TraceID: "bbbbbbbbbbbb0002", SessionID: "otlp-bbbbbbbbbbbb", StartMs: now, Tenant: "shop", Kind: "chat", Model: "claude-sonnet", InputTokens: 100, OutputTokens: 10}}); err != nil {
		t.Fatal(err)
	}
	if calls, _, _ := ledgerTotals(t, archive); calls != 2 {
		t.Fatalf("setup: %d rows, want the 2 of the old version", calls)
	}
	store.Close()

	archive2, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	store2 := NewSessionStoreWithArchive(nil, archive2, SessionLimits{UsageTenantAttr: "tenant.id"})
	if err := store2.LoadPersisted(); err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	if calls, in, out := ledgerTotals(t, archive2); calls != 1 || in != 100 || out != 10 {
		t.Errorf("after the load: calls=%d in=%d out=%d, want the one inner call", calls, in, out)
	}
}

// Span ids are the sender's choice: a row goes only for the session that ledgered it, so one sender naming another's
// span id removes nothing of theirs.
func TestUsageLedger_ADropIsScopedToItsSession(t *testing.T) {
	archive, err := NewSQLiteStore(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if err := archive.InsertUsage([]usageRow{
		{SpanID: "same", TraceID: "t1", SessionID: "victim:a:s1", Kind: "k"},
		{SpanID: "mine", TraceID: "t2", SessionID: "other:b:s2", Kind: "k"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := archive.DeleteUsageSpans([]usageDrop{{SessionID: "other:b:s2", SpanID: "same"}, {SessionID: "other:b:s2", SpanID: "mine"}}); err != nil {
		t.Fatal(err)
	}
	var left []string
	rows, err := archive.db.Query(`SELECT span_id FROM llm_calls`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		left = append(left, id)
	}
	if len(left) != 1 || left[0] != "same" {
		t.Errorf("rows left = %v, want only the other session's row kept", left)
	}
}
