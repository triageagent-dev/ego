package api

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/triageagent-dev/ego/internal/mcptools"
	"github.com/triageagent-dev/ego/internal/online"
)

// A sender that declares no thread (no ego.thread) gets none of the segment rules: its session is its own thread,
// part 1, and behaves as sessions always did.

// plainStore records what the store hands to the online-evaluation hook.
func plainStore(t *testing.T) (*SessionStore, func() []string) {
	t.Helper()
	s := NewSessionStore(nil)
	s.idleTimeout, s.completionGrace = 10*time.Millisecond, 10*time.Millisecond
	var mu sync.Mutex
	var queued []string
	s.SetOnComplete(func(id string) {
		mu.Lock()
		queued = append(queued, id)
		mu.Unlock()
	})
	return s, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), queued...)
	}
}

func TestPlainSession_CompletesAndIsQueuedAtCompletion(t *testing.T) {
	s, queued := plainStore(t)
	now := time.Now().UnixMilli()
	s.Ingest(segBody("", "plain", segSpan{trace: "t1", span: "root", startMs: now, durMs: 10}))
	waitForCondition(t, time.Second, func() bool { return len(queued()) == 1 })
	sum := summaryOf(t, s, "plain")
	if !sum.IsComplete || !sum.Closed || sum.Thread != "plain" || sum.Part != 1 {
		t.Errorf("summary = %+v, want complete, closed (complete), its own thread, part 1", sum)
	}
	if got := fmt.Sprint(queued()); got != "[plain]" {
		t.Errorf("queued = %s at completion, want [plain]", got)
	}
}

func TestPlainSession_KeepsItsIDAfterALongGap(t *testing.T) {
	s, _ := plainStore(t)
	base := time.Now().Add(-12 * time.Hour).UnixMilli()
	s.Ingest(segBody("", "plain", segSpan{trace: "t1", span: "a", startMs: base, durMs: 10}))
	waitForCondition(t, time.Second, func() bool { return summaryOf(t, s, "plain").IsComplete })
	s.Ingest(segBody("", "plain", segSpan{trace: "t2", span: "b", startMs: base + 10*60*minute, durMs: 10}))
	if got := fmt.Sprint(idsOf(s)); got != "[plain]" {
		t.Fatalf("sessions = %s, want one: a plain session is never split", got)
	}
	sum := summaryOf(t, s, "plain")
	if sum.SpanCount != 2 || sum.Part != 1 {
		t.Errorf("summary = %+v, want both spans in part 1", sum)
	}
	// Nothing is kept for a plain session: no routing, no sweep.
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.threads) != 0 || len(s.sweepTimers) != 0 {
		t.Errorf("threads=%d sweeps=%d, want none for a plain session", len(s.threads), len(s.sweepTimers))
	}
}

func TestPlainSession_ReopensOnALateSpanAndIsScoredOnce(t *testing.T) {
	e, sessions, archive, fake := newTestEngine(t, testOnlineConfig)
	sessions.idleTimeout, sessions.completionGrace = time.Hour, 10*time.Millisecond
	sessions.SetOnComplete(e.Enqueue)
	now := time.Date(2026, 10, 4, 12, 30, 0, 0, time.UTC)
	e.now = func() time.Time { return now }
	ctx := context.Background()

	sessions.Ingest(onlineSessionBody("aaaaaaaaaaaa0001", now.UnixMilli(), "rca", "why is checkout down?", "The payment service is out of memory."))
	waitForCondition(t, time.Second, func() bool { return len(e.queue) == 1 })
	e.process(ctx, <-e.queue)
	if fake.calls == 0 {
		t.Fatal("the session was not scored at completion")
	}
	calls := fake.calls

	// A late span of the same trace reopens it; completing again queues it, and the engine has seen it.
	sessions.Ingest(sampleTracesBodyWithParent("otlp-aaaaaaaaaaaa", "aaaaaaaaaaaa0001", "late", "agent-aaaaaaaaaaaa0001"))
	if sum := summaryOf(t, sessions, "otlp-aaaaaaaaaaaa"); sum.IsComplete || sum.Closed {
		t.Fatalf("a late span must reopen the session: %+v", sum)
	}
	sessions.completeSession("otlp-aaaaaaaaaaaa")
	if len(e.queue) != 1 {
		t.Fatalf("queued %d after the second completion, want 1 (the hook runs at every completion, as before)", len(e.queue))
	}
	e.process(ctx, <-e.queue)
	if fake.calls != calls {
		t.Errorf("judge called %d more time(s): a session is scored once ever", fake.calls-calls)
	}
	if st := e.status(); st.Counts[online.SkipAlreadySeen] != 1 {
		t.Errorf("counts = %v, want one skip as already seen", st.Counts)
	}
	if rows, _ := archive.OnlineSamples(0); len(rows) != 1 {
		t.Errorf("%d scores stored, want 1", len(rows))
	}
}

func TestPlainSession_IsCappedAtTenThousandSpans(t *testing.T) {
	s, _ := plainStore(t)
	s.idleTimeout, s.completionGrace = time.Hour, time.Hour
	now := time.Now().UnixMilli()
	var spans []segSpan
	for i := 0; i < maxSpansPerSession+5; i++ {
		spans = append(spans, segSpan{trace: "t1", span: fmt.Sprintf("s%d", i), parent: "s0", startMs: now, durMs: 1})
	}
	if n := s.Ingest(segBody("", "plain", spans...)); n != maxSpansPerSession {
		t.Errorf("ingested %d spans, want the cap %d", n, maxSpansPerSession)
	}
	if got := summaryOf(t, s, "plain").SpanCount; got != maxSpansPerSession {
		t.Errorf("span count = %d, want %d", got, maxSpansPerSession)
	}
}

// A plain session restored from the archive reopens on a late span, as before; it does not open a part 2.
func TestPlainSession_RestoredFromTheArchiveReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plain.db")
	archive, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	store := NewSessionStoreWithArchive(nil, archive, SessionLimits{})
	store.idleTimeout, store.completionGrace = time.Hour, time.Hour
	base := time.Now().Add(-48*time.Hour).UnixMilli() / minute * minute
	store.Ingest(segBody("", "plain", segSpan{trace: "t1", span: "a", startMs: base, durMs: 10}))
	store.completeSession("plain")
	if err := store.flushPersist(); err != nil {
		t.Fatal(err)
	}
	store.Close()

	archive2, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	store2 := NewSessionStoreWithArchive(nil, archive2, SessionLimits{})
	store2.idleTimeout, store2.completionGrace = time.Hour, time.Hour
	if err := store2.LoadPersisted(); err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	if sum := summaryOf(t, store2, "plain"); !sum.IsComplete || !sum.Closed || sum.Part != 1 {
		t.Fatalf("restored = %+v, want complete and closed", sum)
	}
	store2.Ingest(segBody("", "plain", segSpan{trace: "t2", span: "b", startMs: base + 5*60*minute, durMs: 10}))
	if got := fmt.Sprint(idsOf(store2)); got != "[plain]" {
		t.Fatalf("sessions = %s, want the one plain session", got)
	}
	if sum := summaryOf(t, store2, "plain"); sum.IsComplete || sum.Closed || sum.SpanCount != 2 {
		t.Errorf("after the late span = %+v, want reopened with 2 spans", sum)
	}
	// Nothing of it is a thread to sweep.
	if open, _ := archive2.OpenThreads(); len(open) != 0 {
		t.Errorf("open threads = %v, want none", open)
	}
}

// The threads list holds declared threads only, from the archive and from memory.
func TestThreads_ExcludePlainSessions(t *testing.T) {
	f := newThreadFixture(t) // pod and job are declared threads
	f.store.Ingest(segBody("", "plain-one", segSpan{trace: "p1", span: "p", startMs: f.base, durMs: 10, tokens: 3}))
	f.store.completeSession("plain-one")
	if err := f.store.flushPersist(); err != nil {
		t.Fatal(err)
	}
	rows, err := f.store.Threads(mcptools.SessionFilter{}, 10, threadAdmin)
	if err != nil || len(rows) != 2 {
		t.Fatalf("threads = %+v err=%v, want the two declared ones", rows, err)
	}
	for _, r := range rows {
		if r.Thread == "plain-one" {
			t.Errorf("a plain session is listed as a thread: %+v", r)
		}
	}

	s := newSegStore(t)
	s.Ingest(segBody("", "plain-two", segSpan{trace: "p2", span: "p", startMs: time.Now().UnixMilli(), durMs: 10}))
	s.Ingest(segBody("decl", "", segSpan{trace: "d1", span: "d", startMs: time.Now().UnixMilli(), durMs: 10}))
	mem, _ := s.Threads(mcptools.SessionFilter{}, 10, threadAdmin)
	if len(mem) != 1 || mem[0].Thread != "decl" {
		t.Errorf("in-memory threads = %+v, want only decl", mem)
	}
}

// A name is a declared thread or a plain session, not both.
func TestSegments_ANameIsNotBothAThreadAndAPlainSession(t *testing.T) {
	s := newSegStore(t)
	now := time.Now().UnixMilli()
	s.Ingest(segBody("", "same", segSpan{trace: "t1", span: "a", startMs: now, durMs: 10}))
	if n := s.Ingest(segBody("same", "", segSpan{trace: "t2", span: "b", startMs: now, durMs: 10})); n != 0 {
		t.Errorf("a declared thread took the name of a plain session (%d spans)", n)
	}
	if sum := summaryOf(t, s.SessionStore, "same"); sum.SpanCount != 1 {
		t.Errorf("plain session = %+v, want untouched", sum)
	}
	s.Ingest(segBody("other", "", segSpan{trace: "t3", span: "c", startMs: now, durMs: 10}))
	if n := s.Ingest(segBody("", "other", segSpan{trace: "t4", span: "d", startMs: now, durMs: 10})); n != 0 {
		t.Errorf("a plain session took the name of a declared thread (%d spans)", n)
	}
}
