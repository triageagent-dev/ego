package api

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/triageagent-dev/ego/internal/adk"
	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

func evidenceBatch(traceID string, docs string) map[string]any {
	attr := func(k, v string) map[string]any {
		return map[string]any{"key": k, "value": map[string]any{"stringValue": v}}
	}
	span := func(id, parent, name string, start string, attrs ...map[string]any) map[string]any {
		as := make([]any, len(attrs))
		for i, a := range attrs {
			as[i] = a
		}
		return map[string]any{"traceId": traceID, "spanId": id, "parentSpanId": parent, "name": name,
			"startTimeUnixNano": start, "endTimeUnixNano": start, "attributes": as}
	}
	return map[string]any{"resourceSpans": []any{map[string]any{"scopeSpans": []any{map[string]any{"spans": []any{
		span("a000000000000001", "", "llm.chat.completions", "1000",
			attr("openinference.span.kind", "LLM"), attr("gen_ai.request.model", "m"), attr("gen_ai.usage.input_tokens", "52590"),
			attr("gen_ai.input.messages", `[{"role":"user","content":"Situation: `+strings.Repeat("x", 3000)+`"}]`),
			attr("gen_ai.output.messages", `[{"role":"assistant","content":"done"}]`)),
		span("a000000000000002", "a000000000000001", "retrieval context", "1001",
			attr("openinference.span.kind", "RETRIEVER"), attr(adk.EgoEvidenceDocuments, docs)),
	}}}}}}
}

// Documents leave the live session at ingest, come back for whoever reads the trace, are stored once however often
// they repeat, and a session that loses them says its input is cut.
func TestEvidence_KeptBesideTheSessionAndStoredOnce(t *testing.T) {
	archive, err := NewSQLiteStore(filepath.Join(t.TempDir(), "evidence.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	store := NewSessionStoreWithArchive(nil, archive, SessionLimits{})
	ids := func() []string {
		store.mu.Lock()
		defer store.mu.Unlock()
		var out []string
		for id := range store.sessions {
			out = append(out, id)
		}
		sort.Strings(out)
		return out
	}
	liveSpans := func(id string) []*tracepkg.Span {
		store.mu.Lock()
		defer store.mu.Unlock()
		return append([]*tracepkg.Span(nil), store.sessions[id].Spans...)
	}
	text := "memory cell: " + strings.Repeat("the image tag is missing. ", 3000)
	docs := `[{"id":"cell-1","content":"` + text + `"}]`
	for _, id := range []string{"a0000000000000000000000000000000", "b0000000000000000000000000000000"} {
		if n := store.Ingest(evidenceBatch(id, docs)); n != 2 {
			t.Fatalf("ingested %d spans, want 2", n)
		}
	}
	sessions := ids()
	if len(sessions) != 2 {
		t.Fatalf("sessions = %v, want 2", sessions)
	}
	for _, span := range liveSpans(sessions[0]) {
		if _, ok := span.Tags[adk.EgoEvidenceDocuments]; ok {
			t.Fatal("the live session holds the documents")
		}
	}
	judged := func(id string) adk.Invocation {
		tr, ok := store.Trace(id)
		if !ok {
			t.Fatalf("no trace for %s", id)
		}
		got := adk.ConvertTrace(tr).Invocations
		if len(got) != 1 {
			t.Fatalf("invocations = %d", len(got))
		}
		return got[0]
	}
	inv := judged(sessions[0])
	if inv.InputCut || len(inv.Context) != 1 || inv.Context[0].ID != "cell-1" || inv.Context[0].Text != text {
		t.Fatalf("InputCut = %v, context = %d docs; want the whole input with its document", inv.InputCut, len(inv.Context))
	}
	for _, span := range liveSpans(sessions[0]) {
		if _, ok := span.Tags[adk.EgoEvidenceDocuments]; ok {
			t.Fatal("reading the trace put the documents into the live session")
		}
	}
	var blobs, refs int
	if err := archive.db.QueryRow(`SELECT (SELECT COUNT(*) FROM evidence_blobs), (SELECT COUNT(*) FROM evidence_refs)`).Scan(&blobs, &refs); err != nil {
		t.Fatal(err)
	}
	if blobs != 1 || refs != 2 {
		t.Errorf("blobs = %d, refs = %d; want one text referred to twice", blobs, refs)
	}

	store.DropEvidence(sessions[0])
	if inv := judged(sessions[0]); !inv.InputCut || len(inv.Context) != 0 {
		t.Errorf("after the drop InputCut = %v with %d docs; want cut with none", inv.InputCut, len(inv.Context))
	}
	if err := archive.PruneEvidence(time.Now()); err != nil {
		t.Fatal(err)
	}
	if inv := judged(sessions[1]); inv.InputCut || len(inv.Context) != 1 {
		t.Errorf("the other session lost its document to the prune: cut = %v, docs = %d", inv.InputCut, len(inv.Context))
	}
}
