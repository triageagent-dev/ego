package api

import (
	"path/filepath"
	"testing"
	"time"

	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

// The ledger keeps how much of a call's input came from the prompt cache, so the cost can be priced per part; a row
// written before that is filled in from its archived span, once.
func TestUsageLedgerKeepsTheCacheSplit(t *testing.T) {
	span := &tracepkg.Span{SpanID: "s1", TraceID: "t1", OperationName: "claude_code.llm_request", StartTime: 1_000_000, Duration: 2000,
		Tags: map[string]any{
			"gen_ai.request.model": "m", "gen_ai.usage.input_tokens": int64(1000), "gen_ai.usage.output_tokens": int64(10),
			"gen_ai.usage.cache_read.input_tokens": int64(800), "gen_ai.usage.cache_creation.input_tokens": int64(150),
		}}
	row, ok := usageRowFromSpan(span, "sess", "", "bot", "")
	if !ok || row.CacheReadTokens != 800 || row.CacheWriteTokens != 150 {
		t.Fatalf("row = %+v ok=%v, want 800 read and 150 written", row, ok)
	}

	archive, err := NewSQLiteStore(filepath.Join(t.TempDir(), "u.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if err := archive.InsertUsage([]usageRow{row}); err != nil {
		t.Fatal(err)
	}
	// A row from before the split has NULL parts.
	if _, err := archive.db.Exec(`INSERT INTO llm_calls (span_id, trace_id, session_id, start_ms, duration_ms, service, tenant, kind, model, name, input_tokens, output_tokens, is_error, thread)
		VALUES ('old', 't0', 'sess', 1, 1, '', '', '', 'm', '', 500, 5, 0, 'sess')`); err != nil {
		t.Fatal(err)
	}
	if unknown, err := archive.UsageCacheUnknown(); err != nil || !unknown {
		t.Fatalf("unknown = %v err=%v, want the old row to be unknown", unknown, err)
	}
	if err := archive.FillUsageCache(map[string]cacheSplit{"old": {400, 0}}); err != nil {
		t.Fatal(err)
	}
	if unknown, _ := archive.UsageCacheUnknown(); unknown {
		t.Error("a row is still unknown after the fill")
	}
	cells, err := archive.UsageSeries(time.UnixMilli(0), time.UnixMilli(10_000), 10_000, nil)
	if err != nil {
		t.Fatal(err)
	}
	var in, read, write int64
	for _, c := range cells {
		in, read, write = in+c.InputTokens, read+c.CacheReadTokens, write+c.CacheWriteTokens
	}
	if in != 1500 || read != 1200 || write != 150 {
		t.Errorf("cells = %+v, want 1200 read, 150 written of 1500 input", cells)
	}
}
