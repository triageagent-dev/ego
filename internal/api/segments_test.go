package api

import (
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"
)

// segSpan is one span of segBody. StartMs is milliseconds since the epoch.
type segSpan struct {
	trace, span, parent string
	startMs, durMs      int64
	tokens              int // > 0 makes it an LLM span with that many prompt tokens
}

// segBody is an OTLP export whose resource names a thread and/or a session, holding the given spans.
func segBody(thread, session string, spans ...segSpan) map[string]any {
	attr := func(k, v string) map[string]any {
		return map[string]any{"key": k, "value": map[string]any{"stringValue": v}}
	}
	res := []any{attr("service.name", "always-on-agent")}
	if thread != "" {
		res = append(res, attr(threadAttr, thread))
	}
	if session != "" {
		res = append(res, attr(sessionNameAttr, session))
	}
	var out []any
	for _, sp := range spans {
		m := map[string]any{
			"traceId": sp.trace, "spanId": sp.span, "name": "step",
			"startTimeUnixNano": strconvI(sp.startMs * 1_000_000),
			"endTimeUnixNano":   strconvI((sp.startMs + sp.durMs) * 1_000_000),
		}
		if sp.parent != "" {
			m["parentSpanId"] = sp.parent
		}
		if sp.tokens > 0 {
			m["attributes"] = []any{
				map[string]any{"key": "openinference.span.kind", "value": map[string]any{"stringValue": "LLM"}},
				map[string]any{"key": "llm.model_name", "value": map[string]any{"stringValue": "gemini-2.5-flash"}},
				map[string]any{"key": "llm.token_count.prompt", "value": map[string]any{"intValue": strconvI(int64(sp.tokens))}},
				map[string]any{"key": "llm.token_count.completion", "value": map[string]any{"intValue": "1"}},
			}
		}
		out = append(out, m)
	}
	return map[string]any{"resourceSpans": []any{map[string]any{
		"resource":   map[string]any{"attributes": res},
		"scopeSpans": []any{map[string]any{"scope": map[string]any{}, "spans": out}},
	}}}
}

// segStore is a store whose timers never fire on their own (the tests drive the sweep), recording what it hands to
// the online-evaluation hook.
type segStore struct {
	*SessionStore
	mu     sync.Mutex
	queued []string
}

func newSegStore(t *testing.T) *segStore {
	t.Helper()
	s := &segStore{SessionStore: NewSessionStore(nil)}
	s.idleTimeout = time.Hour
	s.completionGrace = time.Hour
	s.SetOnComplete(func(id string) {
		s.mu.Lock()
		s.queued = append(s.queued, id)
		s.mu.Unlock()
	})
	return s
}

func (s *segStore) queuedIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.queued...)
}

func (s *segStore) byID(t *testing.T, id string) SessionSummary {
	return summaryOf(t, s.SessionStore, id)
}

func (s *segStore) ids() []string { return idsOf(s.SessionStore) }

func summaryOf(t *testing.T, s *SessionStore, id string) SessionSummary {
	t.Helper()
	sum, ok := s.Summary(id)
	if !ok {
		t.Fatalf("session %q not found; have %v", id, idsOf(s))
	}
	return sum
}

func idsOf(s *SessionStore) []string {
	var ids []string
	for _, sum := range s.List() {
		ids = append(ids, sum.SessionID)
	}
	sort.Strings(ids)
	return ids
}

const minute = int64(60_000)

func TestSegments_Assignment(t *testing.T) {
	// Each case sends traces to one thread in order; a trace is (id, start minute, spans).
	type trace struct {
		id    string
		at    int64 // start, in minutes
		spans int
	}
	cases := []struct {
		name     string
		idle     time.Duration
		maxSpans int
		maxAge   time.Duration
		traces   []trace
		// want maps segment id to its trace ids, in the order the segments were opened.
		want  map[string][]string
		order []string
	}{
		{
			name:   "first trace opens the first segment under the thread id",
			traces: []trace{{"t1", 0, 1}},
			want:   map[string][]string{"agent": {"t1"}},
			order:  []string{"agent"},
		},
		{
			name:   "a trace within the idle window joins",
			traces: []trace{{"t1", 0, 1}, {"t2", 20, 1}, {"t3", 45, 1}},
			want:   map[string][]string{"agent": {"t1", "t2", "t3"}},
			order:  []string{"agent"},
		},
		{
			name:   "a trace after the idle window opens part 2 and the first keeps its id",
			traces: []trace{{"t1", 0, 1}, {"t2", 40, 1}, {"t3", 50, 1}},
			want:   map[string][]string{"agent": {"t1"}, "agent~2": {"t2", "t3"}},
			order:  []string{"agent", "agent~2"},
		},
		{
			name:     "the span cap closes between traces, never inside one",
			maxSpans: 4,
			traces:   []trace{{"t1", 0, 3}, {"t2", 1, 3}, {"t3", 2, 1}},
			// t2 joins although it takes the segment to 6 spans: 3 < 4 when it arrived.
			want:  map[string][]string{"agent": {"t1", "t2"}, "agent~2": {"t3"}},
			order: []string{"agent", "agent~2"},
		},
		{
			name:   "the age cap closes a segment that keeps getting traces",
			maxAge: time.Hour,
			traces: []trace{{"t1", 0, 1}, {"t2", 25, 1}, {"t3", 50, 1}, {"t4", 75, 1}, {"t5", 100, 1}, {"t6", 125, 1}, {"t7", 150, 1}},
			// t4 ends past 1 h from the first span (0), t7 past 1 h from part 2's first span (75).
			want:  map[string][]string{"agent": {"t1", "t2", "t3"}, "agent~2": {"t4", "t5", "t6"}, "agent~3": {"t7"}},
			order: []string{"agent", "agent~2", "agent~3"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newSegStore(t)
			if tc.idle > 0 {
				s.segIdle = tc.idle
			}
			if tc.maxSpans > 0 {
				s.segMaxSpans = tc.maxSpans
			}
			if tc.maxAge > 0 {
				s.segMaxAge = tc.maxAge
			}
			base := time.Now().Add(-48*time.Hour).UnixMilli() / minute * minute
			for _, tr := range tc.traces {
				var spans []segSpan
				for i := 0; i < tr.spans; i++ {
					parent := ""
					if i > 0 {
						parent = fmt.Sprintf("%s-0", tr.id)
					}
					spans = append(spans, segSpan{trace: tr.id, span: fmt.Sprintf("%s-%d", tr.id, i), parent: parent, startMs: base + tr.at*minute, durMs: 1000})
				}
				s.Ingest(segBody("agent", "", spans...))
			}
			if got := s.ids(); len(got) != len(tc.order) {
				t.Fatalf("segments = %v, want %v", got, tc.order)
			}
			for i, id := range tc.order {
				sum := s.byID(t, id)
				if sum.Thread != "agent" || sum.Part != i+1 {
					t.Errorf("%s: thread/part = %q/%d, want agent/%d", id, sum.Thread, sum.Part, i+1)
				}
				sess, _ := s.sessionLocked(id)
				var got []string
				for tid := range sess.TraceIDs {
					got = append(got, tid)
				}
				sort.Strings(got)
				if fmt.Sprint(got) != fmt.Sprint(tc.want[id]) {
					t.Errorf("%s holds traces %v, want %v", id, got, tc.want[id])
				}
				// Only the last segment is still open.
				if wantClosed := i < len(tc.order)-1; sum.Closed != wantClosed {
					t.Errorf("%s closed = %v, want %v", id, sum.Closed, wantClosed)
				}
			}
			if got := s.queuedIDs(); len(got) != len(tc.order)-1 {
				t.Errorf("queued for scoring = %v, want one per closed segment (%d)", got, len(tc.order)-1)
			}
		})
	}
}

func (s *SessionStore) sessionLocked(id string) (*Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lookupLocked(id)
}

// A trace's later spans go to the segment its first span chose, also when that segment has closed since, and they
// neither reopen it nor queue it for scoring again.
func TestSegments_LateSpansOfAClosedSegmentsTrace(t *testing.T) {
	s := newSegStore(t)
	base := time.Now().Add(-48*time.Hour).UnixMilli() / minute * minute

	s.Ingest(segBody("agent", "", segSpan{trace: "t1", span: "a", parent: "r", startMs: base, durMs: 1000}))
	s.Ingest(segBody("agent", "", segSpan{trace: "t2", span: "b", startMs: base + 90*minute, durMs: 1000}))
	if got := s.queuedIDs(); fmt.Sprint(got) != "[agent]" {
		t.Fatalf("queued = %v, want [agent] once part 2 opened", got)
	}
	// The root of t1 arrives last, long after part 1 closed.
	s.Ingest(segBody("agent", "", segSpan{trace: "t1", span: "r", startMs: base - 1000, durMs: 5000}))

	first, part2 := s.byID(t, "agent"), s.byID(t, "agent~2")
	if first.SpanCount != 2 || part2.SpanCount != 1 {
		t.Errorf("span counts = %d/%d, want 2/1: the late root belongs to part 1", first.SpanCount, part2.SpanCount)
	}
	if !first.Closed || !first.IsComplete {
		t.Errorf("part 1 closed/complete = %v/%v, want both: a closed segment never reopens", first.Closed, first.IsComplete)
	}
	if got := s.queuedIDs(); fmt.Sprint(got) != "[agent]" {
		t.Errorf("queued = %v after a late span, want [agent] still once", got)
	}
}

// A trace nobody has seen opens the next segment when the thread's last one is closed, however old its timestamps.
func TestSegments_NewTraceWithOldTimestampsAfterCloseOpensNextSegment(t *testing.T) {
	s := newSegStore(t)
	base := time.Now().Add(-48*time.Hour).UnixMilli() / minute * minute
	s.Ingest(segBody("agent", "", segSpan{trace: "t1", span: "a", startMs: base, durMs: 1000}))
	s.Ingest(segBody("agent", "", segSpan{trace: "t2", span: "b", startMs: base + 90*minute, durMs: 1000}))
	// t3 happened between t1 and t2 as far as its clock says, but nobody has seen it.
	s.Ingest(segBody("agent", "", segSpan{trace: "t3", span: "c", startMs: base + 10*minute, durMs: 1000}))
	if s.byID(t, "agent").SpanCount != 1 {
		t.Errorf("part 1 took a trace it had not seen: %+v", s.byID(t, "agent"))
	}
	if got := s.byID(t, "agent~2").SpanCount; got != 2 {
		t.Errorf("part 2 holds %d spans, want 2 (t2 and t3)", got)
	}
}

func TestSegments_ThreadAttributeOverridesTheSessionID(t *testing.T) {
	s := newSegStore(t)
	base := time.Now().Add(-time.Hour).UnixMilli()
	s.Ingest(segBody("pod-a", "run-1", segSpan{trace: "t1", span: "a", startMs: base, durMs: 10}))
	s.Ingest(segBody("pod-a", "run-2", segSpan{trace: "t2", span: "b", startMs: base + 1000, durMs: 10}))
	s.Ingest(segBody("", "run-2", segSpan{trace: "t3", span: "c", startMs: base + 2000, durMs: 10}))
	if got := fmt.Sprint(s.ids()); got != "[pod-a run-2]" {
		t.Fatalf("sessions = %s, want [pod-a run-2]: the rotated session id keeps one thread", got)
	}
	if got := s.byID(t, "pod-a").SpanCount; got != 2 {
		t.Errorf("pod-a holds %d spans, want 2", got)
	}
	if _, ok := s.byID(t, "pod-a").Metadata[threadAttr]; ok {
		t.Error("the thread attribute is a session attribute, not metadata")
	}
}

func TestSegments_ThreadIsNamespacedBySender(t *testing.T) {
	s := newSegStore(t)
	base := time.Now().Add(-time.Hour).UnixMilli()
	s.IngestFor("alice", segBody("pod", "", segSpan{trace: "t1", span: "a", startMs: base, durMs: 10}))
	s.IngestFor("bob", segBody("pod", "", segSpan{trace: "t2", span: "b", startMs: base, durMs: 10}))
	if got := fmt.Sprint(s.ids()); got != "[alice:pod bob:pod]" {
		t.Errorf("sessions = %s, want one thread per sender", got)
	}
	if sum := s.byID(t, "bob:pod"); sum.Thread != "bob:pod" || sum.Part != 1 {
		t.Errorf("thread/part = %q/%d", sum.Thread, sum.Part)
	}
}

// Completing is the quiet state: it must not queue the segment for scoring; the sweep of an idle thread closes it,
// and only then is it queued.
func TestSegments_OnlineHookRunsAtCloseNotAtComplete(t *testing.T) {
	s := newSegStore(t)
	s.idleTimeout = 5 * time.Millisecond
	s.completionGrace = 5 * time.Millisecond
	s.segIdle = 150 * time.Millisecond

	now := time.Now().UnixMilli()
	s.Ingest(segBody("agent", "", segSpan{trace: "t1", span: "root", startMs: now, durMs: 10}))
	waitForCondition(t, time.Second, func() bool { return s.byID(t, "agent").IsComplete })
	if got := s.queuedIDs(); len(got) != 0 {
		t.Fatalf("queued = %v at the 30 s complete, want none", got)
	}
	waitForCondition(t, 2*time.Second, func() bool { return len(s.queuedIDs()) > 0 })
	time.Sleep(50 * time.Millisecond)
	if got := fmt.Sprint(s.queuedIDs()); got != "[agent]" {
		t.Errorf("queued = %s, want [agent] exactly once", got)
	}
	if sum := s.byID(t, "agent"); !sum.Closed {
		t.Error("the swept segment must be closed")
	}
	// The next trace starts part 2.
	s.Ingest(segBody("agent", "", segSpan{trace: "t2", span: "b", startMs: time.Now().UnixMilli(), durMs: 10}))
	if sum := s.byID(t, "agent~2"); sum.Closed {
		t.Error("part 2 must be open")
	}
}

// A segment still receiving traces is not swept closed by the span or age caps before its last trace is quiet.
func TestSegments_SweepWaitsForTheQuietStateBeforeSizeOrAge(t *testing.T) {
	s := newSegStore(t)
	s.segMaxSpans = 1
	now := time.Now().UnixMilli()
	s.Ingest(segBody("agent", "", segSpan{trace: "t1", span: "a", startMs: now, durMs: 10}))
	s.sweepThread("agent")
	if s.byID(t, "agent").Closed {
		t.Fatal("swept closed while its trace may still be arriving")
	}
	s.completeSession("agent")
	s.sweepThread("agent")
	if !s.byID(t, "agent").Closed {
		t.Fatal("a quiet segment past the span cap must close in the sweep")
	}
}

func TestSegments_ArchiveRestoresPartsAndRoutesLateSpans(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seg.db")
	archive, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	store := NewSessionStoreWithArchive(nil, archive, SessionLimits{UsageTenantAttr: "tenant.id"})
	store.idleTimeout, store.completionGrace = time.Hour, time.Hour
	base := time.Now().Add(-48*time.Hour).UnixMilli() / minute * minute
	store.Ingest(segBody("agent", "", segSpan{trace: "t1", span: "a", parent: "r1", startMs: base, durMs: 1000, tokens: 10}))
	store.Ingest(segBody("agent", "", segSpan{trace: "t2", span: "b", startMs: base + 90*minute, durMs: 1000, tokens: 20}))
	if err := store.flushPersist(); err != nil {
		t.Fatal(err)
	}
	store.Close()

	archive2, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	store2 := NewSessionStoreWithArchive(nil, archive2, SessionLimits{UsageTenantAttr: "tenant.id"})
	store2.idleTimeout, store2.completionGrace = time.Hour, time.Hour
	if err := store2.LoadPersisted(); err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	if got := idsOf(store2); fmt.Sprint(got) != "[agent agent~2]" {
		t.Fatalf("restored %v, want [agent agent~2]", got)
	}
	// The thread's routing is not in memory after the restart; the archive's index rebuilds it.
	store2.mu.Lock()
	store2.threads = map[string]*threadState{}
	store2.mu.Unlock()
	store2.Ingest(segBody("agent", "", segSpan{trace: "t1", span: "r1", startMs: base - 500, durMs: 3000}))
	store2.Ingest(segBody("agent", "", segSpan{trace: "t3", span: "c", startMs: base + 91*minute, durMs: 1000}))
	if p1 := summaryOf(t, store2, "agent"); p1.SpanCount != 2 || !p1.Closed {
		t.Errorf("part 1 = %d spans closed=%v, want 2 spans, closed: the late root of t1 belongs to it", p1.SpanCount, p1.Closed)
	}
	p2 := summaryOf(t, store2, "agent~2")
	if p2.SpanCount != 2 || p2.Closed || p2.Part != 2 {
		t.Errorf("part 2 = %+v, want the open segment with t2 and t3", p2)
	}
	if _, ok := store2.Summary("agent~3"); ok {
		t.Error("a third part was opened although part 2 was open")
	}
}

// Thread and part are stored columns of the index and the ledger.
func TestSegments_StoredThreadColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cols.db")
	archive, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	store := NewSessionStoreWithArchive(nil, archive, SessionLimits{UsageTenantAttr: "tenant.id"})
	defer store.Close()
	store.idleTimeout, store.completionGrace = time.Hour, time.Hour
	base := time.Now().Add(-5*time.Hour).UnixMilli() / minute * minute
	store.Ingest(segBody("pod", "", segSpan{trace: "t1", span: "a", startMs: base, durMs: 1000, tokens: 10}))
	store.Ingest(segBody("pod", "", segSpan{trace: "t2", span: "b", startMs: base + 60*minute, durMs: 1000, tokens: 20}))
	if err := store.flushPersist(); err != nil {
		t.Fatal(err)
	}
	segs, err := archive.ThreadSegments("pod", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 2 || segs[0].ID != "pod" || segs[1].ID != "pod~2" || segs[1].Part != 2 || !segs[0].Closed || segs[1].Closed {
		t.Fatalf("index rows = %+v", segs)
	}
	if fmt.Sprint(segs[1].TraceIDs) != "[t2]" {
		t.Errorf("part 2 trace ids = %v", segs[1].TraceIDs)
	}
	rows, err := archive.db.Query(`SELECT session_id, thread FROM llm_calls ORDER BY session_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var id, thread string
		if err := rows.Scan(&id, &thread); err != nil {
			t.Fatal(err)
		}
		got = append(got, id+"@"+thread)
	}
	if fmt.Sprint(got) != "[pod@pod pod~2@pod]" {
		t.Errorf("ledger rows (session@thread) = %v, want [pod@pod pod~2@pod]", got)
	}
	// The snapshot blob carries them too.
	snap, ok, err := archive.Load("pod~2")
	if err != nil || !ok || snap.Thread != "pod" || snap.Part != 2 {
		t.Errorf("snapshot = %+v ok=%v err=%v", snap, ok, err)
	}
}

// An archive written before segments gets thread = session id and part 1 on every index and ledger row.
func TestSegments_MigratesAnArchiveWrittenBeforeThreads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	archive, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	// Recreate the old shapes of the two tables.
	for _, stmt := range []string{
		`DROP TABLE session_index`, `DROP TABLE llm_calls`,
		`CREATE TABLE session_index (session_id TEXT PRIMARY KEY, started_ms INTEGER NOT NULL, complete INTEGER NOT NULL,
			llm_calls INTEGER NOT NULL, tenants TEXT NOT NULL, agents TEXT NOT NULL, search TEXT NOT NULL)`,
		`CREATE TABLE llm_calls (span_id TEXT PRIMARY KEY, trace_id TEXT NOT NULL, session_id TEXT NOT NULL, start_ms INTEGER NOT NULL,
			duration_ms INTEGER NOT NULL, service TEXT NOT NULL, tenant TEXT NOT NULL, kind TEXT NOT NULL, model TEXT NOT NULL,
			name TEXT NOT NULL, input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL, is_error INTEGER NOT NULL)`,
		`INSERT INTO session_index VALUES ('old-done', 1, 1, 0, '', '', 'old-done'), ('old-open', 2, 0, 0, '', '', 'old-open')`,
		`INSERT INTO llm_calls VALUES ('s1', 't1', 'old-done', 1, 1, '', '', '', '', '', 5, 5, 0)`,
	} {
		if _, err := archive.db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	archive.Close()

	archive, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	segs, err := archive.ThreadSegments("old-done", false)
	if err != nil || len(segs) != 1 || segs[0].ID != "old-done" || segs[0].Part != 1 || !segs[0].Closed {
		t.Errorf("old-done = %+v err=%v, want part 1 of its own thread, closed (it had completed)", segs, err)
	}
	if segs, _ := archive.ThreadSegments("old-open", false); len(segs) != 1 || segs[0].Closed {
		t.Errorf("old-open = %+v, want one open session", segs)
	}
	if segs, _ := archive.ThreadSegments("old-done", true); len(segs) != 0 {
		t.Errorf("old-done as a declared thread = %+v, want none", segs)
	}
	var thread string
	if err := archive.db.QueryRow(`SELECT thread FROM llm_calls WHERE span_id = 's1'`).Scan(&thread); err != nil || thread != "old-done" {
		t.Errorf("ledger thread = %q err=%v, want old-done", thread, err)
	}
	// None of them is a segment of a declared thread, so none is swept.
	open, err := archive.OpenThreads()
	if err != nil || len(open) != 0 {
		t.Errorf("open threads = %v err=%v, want none", open, err)
	}
}

// A sender that starts declaring its thread under a name it already used as a plain session: the session is adopted
// as part 1 (its id stays) and the new trace is kept, not dropped. An open one keeps collecting; a complete one stays
// closed and the new trace opens part 2.
func TestSegments_AdoptsAPlainSessionWhenTheSenderDeclaresItsThread(t *testing.T) {
	s := newSegStore(t)
	base := time.Now().Add(-2 * time.Hour).UnixMilli()
	s.Ingest(segBody("", "pod-a", segSpan{trace: "t1", span: "a", startMs: base, durMs: 10}))
	if s.byID(t, "pod-a").Segmented {
		t.Fatal("a session without the thread attribute is not a segment")
	}
	s.Ingest(segBody("pod-a", "", segSpan{trace: "t2", span: "b", startMs: base + minute, durMs: 10}))
	if got := fmt.Sprint(s.ids()); got != "[pod-a]" {
		t.Fatalf("sessions = %s, want [pod-a]: the open session is adopted", got)
	}
	if sum := s.byID(t, "pod-a"); !sum.Segmented || sum.Thread != "pod-a" || sum.Part != 1 || sum.SpanCount != 2 {
		t.Fatalf("adopted = segmented %v thread %q part %d spans %d", sum.Segmented, sum.Thread, sum.Part, sum.SpanCount)
	}

	// A complete plain session (one archived before segments existed, for instance) stays closed when adopted.
	c := newSegStore(t)
	c.Ingest(segBody("", "pod-b", segSpan{trace: "t1", span: "a", startMs: base, durMs: 10}))
	c.completeSession("pod-b")
	c.Ingest(segBody("pod-b", "", segSpan{trace: "t2", span: "b", startMs: base + minute, durMs: 10}))
	if got := fmt.Sprint(c.ids()); got != "[pod-b pod-b~2]" {
		t.Fatalf("sessions = %s, want [pod-b pod-b~2]", got)
	}
	if sum := c.byID(t, "pod-b"); !sum.Segmented || sum.Part != 1 || !sum.Closed {
		t.Errorf("part 1 = segmented %v part %d closed %v, want an adopted, closed part 1", sum.Segmented, sum.Part, sum.Closed)
	}
	if sum := c.byID(t, "pod-b~2"); sum.Thread != "pod-b" || sum.Part != 2 || sum.SpanCount != 1 {
		t.Errorf("part 2 = thread %q part %d spans %d", sum.Thread, sum.Part, sum.SpanCount)
	}
	if got := fmt.Sprint(c.queuedIDs()); got != "[pod-b]" {
		t.Errorf("queued = %s, want only the plain completion of pod-b", got)
	}
}
