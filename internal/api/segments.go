package api

import (
	"strconv"
	"time"

	"github.com/triageagent-dev/ego/internal/incremental"
	"github.com/triageagent-dev/ego/internal/logx"
	"github.com/triageagent-dev/ego/internal/otlp"
	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

// Threads, segments and turns.
//
// A sender's session id says who is talking, not what one unit of work was: an always-on agent (a pod that wakes
// every few minutes for weeks) sends everything under one id. ego therefore keeps three levels.
//
//   - A thread is the sender's own id (ego.thread, else ego.session_name, else gen_ai.conversation.id, namespaced by
//     sender and tenant label like a session always was). It lasts as long as the sender keeps using it and is never
//     scored or stored as one blob.
//   - A segment is ego's session: a bounded run of whole traces. Everything the rest of the code calls a session is a
//     segment. The first segment of a thread keeps the thread's id, so nothing that exists changes; segment n >= 2 is
//     "<thread>~n".
//   - A turn is one trace (one prompt-to-answer unit). A segment is made of whole traces and never cuts one.
//
// Assignment is by trace id, not by arrival time. The first span seen for a trace picks the segment; every later span
// of that trace goes to the same segment, even once it has closed, because a trace's spans arrive in several batches
// and its root often last.
//
// A segment is closed for good, on the arrival of a new trace of its thread or from the sweep, when
//   - its last span ended more than segIdle ago (a merge window: a trace within it joins; it is not a timer that
//     completes anything),
//   - it holds segMaxSpans spans or more (checked per trace, never inside one), or
//   - its first span is older than segMaxAge.
//
// The 30 s IsComplete state stays what it was: a quiet state the UI shows. Online evaluation is queued once, when the
// segment closes, so a segment is scored after its final trace and only once. How the scoring side keeps that promise:
// onComplete runs only from finish, for ids closeSegmentLocked closed; a closed segment never reopens and never takes
// a new trace, so no later span can start a second "completion"; late spans of its own traces are appended and its
// invocations rebuilt, but they do not queue it again, and OnlineEvalExists (the first score wins) keeps the engine
// from scoring it a second time even if something does enqueue it.

const (
	threadAttr = "ego.thread"

	// segmentSep joins a thread id and a part number into a segment id. A thread whose own name ends in "~n" can
	// collide with that thread's segment n; senders that care name their threads with ego.thread.
	segmentSep = "~"

	DefaultSegmentIdle     = 30 * time.Minute
	DefaultSegmentMaxSpans = 2000
	DefaultSegmentMaxAge   = 6 * time.Hour

	// hardMaxSpansPerSegment only stops a single runaway trace: segments close on segMaxSpans between traces, long
	// before this.
	hardMaxSpansPerSegment = 50000

	// threadStateKeep is how long a thread with no open segment keeps its in-memory state; it is read back from the
	// archive when the thread sends again.
	threadStateKeep = time.Hour
	// threadPruneEvery rate-limits the pass over the thread states.
	threadPruneEvery = time.Minute
)

// segmentID is the id of part n of a thread.
func segmentID(thread string, part int) string {
	if part <= 1 {
		return thread
	}
	return thread + segmentSep + strconv.Itoa(part)
}

// threadState is the in-memory routing of one thread: which segment takes new traces and which segment holds each
// trace seen so far. It is rebuilt from the archive's index when the thread is not in memory.
type threadState struct {
	// parts is the highest part number in use; open is the id of the segment that takes new traces ("" when none).
	parts int
	open  string
	// sender owns the thread; known is false until the first segment exists.
	sender  string
	known   bool
	traces  map[string]traceRef
	touched time.Time
}

type traceRef struct {
	seg string
	at  time.Time
}

// closings collects what happens to segments while s.mu is held, so the hooks and broadcasts run after it is
// released.
type closings struct {
	completions []completion
	closed      []string
}

// completion is a session_complete broadcast prepared under the lock.
type completion struct {
	id      string
	ev      sessionCompleteEvent
	agents  []string
	tenants []string
}

func usTime(us int64) time.Time { return time.UnixMicro(us) }

// threadKey is the id of the session's thread; part its 1-based number. Sessions built without them (tests, the
// archive written before segments) are the first part of their own thread.
func (s *Session) threadKey() string {
	if s.Thread != "" {
		return s.Thread
	}
	return s.ID
}

func (s *Session) partNo() int {
	if s.Part > 0 {
		return s.Part
	}
	return 1
}

// noteSpan widens the segment's span window.
func (s *Session) noteSpan(sp *tracepkg.Span) {
	if sp.StartTime > 0 && (s.firstUs == 0 || sp.StartTime < s.firstUs) {
		s.firstUs = sp.StartTime
	}
	if end := sp.StartTime + sp.Duration; end > s.lastUs {
		s.lastUs = end
	}
}

// recomputeBounds derives the span window from the spans (after a restore).
func (s *Session) recomputeBounds() {
	s.firstUs, s.lastUs = 0, 0
	for _, sp := range s.Spans {
		s.noteSpan(sp)
	}
}

// activity is the latest sign of life: the end of the last span or the last arrival, whichever is later. The arrival
// keeps a replayed trace of old timestamps from looking idle while it is still being sent.
func (s *Session) activity() time.Time {
	t := s.lastArrival
	if s.lastUs > 0 {
		if end := usTime(s.lastUs); end.After(t) {
			t = end
		}
	}
	return t
}

// traceRefTime is the moment a new trace is judged at: when its last span ended, never later than now. A trace
// without times is judged at now.
func traceRefTime(tr *tracepkg.Trace, now time.Time) time.Time {
	var end int64
	for _, sp := range tr.AllSpans {
		if e := sp.StartTime + sp.Duration; e > end {
			end = e
		}
	}
	if end <= 0 {
		return now
	}
	if t := usTime(end); t.Before(now) {
		return t
	}
	return now
}

// segmentDueAt reports whether a new trace seen at ref ends the open segment.
func (s *SessionStore) segmentDueAt(seg *Session, ref time.Time) bool {
	if seg.lastUs > 0 && ref.Sub(usTime(seg.lastUs)) > s.segIdle {
		return true
	}
	if len(seg.Spans) >= s.segMaxSpans {
		return true
	}
	return seg.firstUs > 0 && ref.Sub(usTime(seg.firstUs)) > s.segMaxAge
}

// segmentSweepDue is the same decision for a thread that sent nothing new. The size and age caps wait for the
// quiet 30 s state so a trace still arriving is not cut off from scoring.
func (s *SessionStore) segmentSweepDue(seg *Session, now time.Time) bool {
	if now.Sub(seg.activity()) > s.segIdle {
		return true
	}
	if !seg.IsComplete {
		return false
	}
	return len(seg.Spans) >= s.segMaxSpans || (seg.firstUs > 0 && now.Sub(usTime(seg.firstUs)) > s.segMaxAge)
}

// nextSweep is how long until the open segment can be due.
func (s *SessionStore) nextSweep(seg *Session, now time.Time) time.Duration {
	d := s.segIdle - now.Sub(seg.activity())
	if seg.firstUs > 0 {
		if age := s.segMaxAge - now.Sub(usTime(seg.firstUs)); age < d {
			d = age
		}
	}
	if d <= 0 {
		// Due by size or age, but a trace may still be arriving: look again when the quiet state has been reached.
		d = s.idleTimeout
	}
	return d
}

// scheduleSweepLocked (re)starts the thread's sweep timer. Must be called with s.mu held.
func (s *SessionStore) scheduleSweepLocked(key string, d time.Duration) {
	if t, ok := s.sweepTimers[key]; ok {
		t.Stop()
	}
	s.sweepTimers[key] = time.AfterFunc(d, func() { s.sweepThread(key) })
}

// sweepThread closes the thread's open segment when it is due, so a thread that just stopped still gets its last
// segment closed and scored. It reschedules itself while the segment is not due yet.
func (s *SessionStore) sweepThread(key string) {
	var out closings
	s.mu.Lock()
	delete(s.sweepTimers, key)
	ts := s.threadStateLocked(key)
	if ts.open != "" {
		if seg, ok := s.reviveLocked(ts.open); ok && !seg.Closed {
			now := s.now()
			if s.segmentSweepDue(seg, now) {
				s.closeSegmentLocked(seg, &out)
			} else {
				s.scheduleSweepLocked(key, s.nextSweep(seg, now))
			}
		}
	}
	s.mu.Unlock()
	s.finish(out)
}

// closeSegmentLocked closes a segment for good: it completes if it had not, takes no new trace and is queued for
// online evaluation by finish. Must be called with s.mu held.
func (s *SessionStore) closeSegmentLocked(seg *Session, out *closings) {
	if seg.Closed {
		return
	}
	now := s.now().UTC()
	seg.Closed, seg.ClosedAt = true, &now
	if !seg.IsComplete {
		out.completions = append(out.completions, s.completeLocked(seg))
	}
	if t, ok := s.idleTimers[seg.ID]; ok {
		t.Stop()
		delete(s.idleTimers, seg.ID)
	}
	if t, ok := s.completionTimers[seg.ID]; ok {
		t.Stop()
		delete(s.completionTimers, seg.ID)
	}
	key := seg.threadKey()
	if ts := s.threads[key]; ts != nil {
		if ts.open == seg.ID {
			ts.open = ""
			if t, ok := s.sweepTimers[key]; ok {
				t.Stop()
				delete(s.sweepTimers, key)
			}
		}
		ts.touched = now
	}
	s.markChangedLocked(seg)
	out.closed = append(out.closed, seg.ID)
}

// finish runs, with s.mu released, what closing and completing prepared: the session_complete broadcasts and the
// online-evaluation hook for each closed segment.
func (s *SessionStore) finish(out closings) {
	if len(out.completions) == 0 && len(out.closed) == 0 {
		return
	}
	s.mu.Lock()
	onComplete := s.onComplete
	s.mu.Unlock()
	for _, id := range out.closed {
		if onComplete != nil {
			onComplete(id)
		}
	}
	if s.hub != nil {
		for _, c := range out.completions {
			s.hub.broadcastSession(c.ev, c.id, c.agents, c.tenants, false)
		}
	}
}

// threadStateLocked returns the routing state of a thread, reading it from the archive's index when the thread is not
// in memory (after a restart, or once an idle thread's state was dropped). Must be called with s.mu held.
func (s *SessionStore) threadStateLocked(key string) *threadState {
	if ts, ok := s.threads[key]; ok {
		return ts
	}
	now := s.now()
	ts := &threadState{traces: map[string]traceRef{}, touched: now}
	var lastID string
	lastClosed := false
	if s.store != nil {
		rows, err := s.store.ThreadSegments(key)
		if err != nil {
			logx.Warnf("session archive: reading thread %s: %v", key, err)
		}
		for _, r := range rows {
			if r.Part > ts.parts {
				ts.parts, lastID, lastClosed = r.Part, r.ID, r.Closed
			}
			for _, tid := range r.TraceIDs {
				ts.traces[tid] = traceRef{seg: r.ID, at: now}
			}
		}
	}
	if ts.parts == 0 {
		// Not indexed (yet): an archive written before segments, whose index is still being filled in.
		if old, ok := s.reviveLocked(key); ok {
			ts.parts, lastID, lastClosed = old.partNo(), old.ID, old.Closed
			for tid := range old.TraceIDs {
				ts.traces[tid] = traceRef{seg: old.ID, at: now}
			}
		}
	}
	if ts.parts > 0 {
		if last, ok := s.reviveLocked(lastID); ok {
			ts.sender, ts.known = last.Sender, true
		}
		if !lastClosed {
			ts.open = lastID
		}
	}
	s.threads[key] = ts
	return ts
}

// reviveLocked returns a session from memory, or reads it back from the archive into memory, so a late span for an
// evicted session does not start a second one under the same id. Must be called with s.mu held.
func (s *SessionStore) reviveLocked(id string) (*Session, bool) {
	if sess, ok := s.sessions[id]; ok {
		return sess, true
	}
	old, ok := s.lookupLocked(id)
	if !ok {
		return nil, false
	}
	s.sessions[id] = old
	s.order = append(s.order, id)
	s.persistedSeq[id] = 0
	return old, true
}

// placeTraceLocked picks the segment a trace belongs to (opening or closing segments as the rules above say) and
// notes the trace in the thread's routing. ok is false when the thread belongs to another sender, in which case
// nothing is touched. Must be called with s.mu held.
func (s *SessionStore) placeTraceLocked(key string, tr *tracepkg.Trace, evalSetID, sender string, resourceAttrs map[string]any, out *closings) (seg *Session, isNew, ok bool) {
	ts := s.threadStateLocked(key)
	if ts.known && ts.sender != sender {
		return nil, false, false
	}
	now := s.now()

	if ref, seen := ts.traces[tr.TraceID]; seen {
		if found, ok := s.reviveLocked(ref.seg); ok {
			ts.traces[tr.TraceID] = traceRef{seg: ref.seg, at: now}
			ts.touched = now
			s.joinSegmentLocked(found, tr.TraceID, evalSetID)
			return found, false, true
		}
		delete(ts.traces, tr.TraceID) // its segment is gone: a new trace as far as routing goes
	}

	var open *Session
	if ts.open != "" {
		if o, ok := s.reviveLocked(ts.open); ok && !o.Closed {
			open = o
		} else {
			ts.open = ""
		}
	}
	if open != nil && s.segmentDueAt(open, traceRefTime(tr, now)) {
		s.closeSegmentLocked(open, out)
		open = nil
	}
	if open == nil {
		ts.parts++
		id := segmentID(key, ts.parts)
		open = &Session{
			ID:             id,
			Thread:         key,
			Part:           ts.parts,
			Sender:         sender,
			tenantAttr:     s.usageTenantAttr,
			PrimaryTraceID: tr.TraceID,
			EvalSetID:      evalSetID,
			Metadata:       withoutSessionAttrs(resourceAttrs),
			TraceIDs:       map[string]struct{}{},
			StartedAt:      now.UTC(),
			extractor:      incremental.New(),
		}
		s.sessions[id] = open
		s.order = append(s.order, id)
		s.markChangedLocked(open)
		ts.open, isNew = id, true
	}
	ts.sender, ts.known = open.Sender, true
	ts.traces[tr.TraceID] = traceRef{seg: open.ID, at: now}
	ts.touched = now
	s.joinSegmentLocked(open, tr.TraceID, evalSetID)
	return open, isNew, true
}

// joinSegmentLocked adds a trace to a segment. A segment that is merely quiet reopens, as a session always did; a
// closed one keeps its closed state and only takes the spans of traces it already holds.
func (s *SessionStore) joinSegmentLocked(seg *Session, traceID, evalSetID string) {
	if evalSetID != "" && seg.EvalSetID != evalSetID {
		seg.EvalSetID = evalSetID
		s.markChangedLocked(seg)
	}
	if seg.IsComplete && !seg.Closed {
		seg.IsComplete = false
		seg.CompletedAt = nil
		s.markChangedLocked(seg)
	}
	seg.TraceIDs[traceID] = struct{}{}
}

// completeLocked marks a session complete and prepares its session_complete broadcast. Must be called with s.mu held;
// the caller publishes the returned completion after releasing it.
func (s *SessionStore) completeLocked(session *Session) completion {
	now := s.now().UTC()
	session.IsComplete = true
	session.CompletedAt = &now
	s.markChangedLocked(session)

	// Batch-convert the session's full span set into real invocations (tool calls, user/agent text, model info) now
	// that it's done. Built from otlp.BuildTrace's merged parent/child tree, same as the Trace() method.
	session.Invocations = extractInvocationsForUI(otlp.BuildTrace(session.ID, session.Spans))

	if t, ok := s.idleTimers[session.ID]; ok {
		t.Stop()
		delete(s.idleTimers, session.ID)
	}
	if t, ok := s.completionTimers[session.ID]; ok {
		t.Stop()
		delete(s.completionTimers, session.ID)
	}

	completedAt := now.Format(time.RFC3339)
	rootName, llmCalls, errs := spanOutline(session.Spans)
	c := completion{
		id: session.ID,
		ev: sessionCompleteEvent{
			Type:         "session_complete",
			SessionID:    session.ID,
			Invocations:  session.Invocations,
			CompletedAt:  &completedAt,
			RootSpanName: rootName,
			LLMCalls:     llmCalls,
			Errors:       errs,
			Turns:        claudeCodeTurns(session.Spans),
			DurationMs:   spanWindowMs(session.Spans),
		},
	}
	if s.hub != nil && s.hub.hasScoped() {
		c.agents, _ = spanIdentities(session.Spans)
		c.tenants = sessionTenants(session)
	}
	return c
}

// pruneThreadsLocked bounds the routing state: trace entries older than the session retention go, and so does a
// thread with no open segment that has been quiet for threadStateKeep, which the archive can rebuild. Without an
// archive nothing is dropped, as the sessions themselves stay in memory too. Must be called with s.mu held.
func (s *SessionStore) pruneThreadsLocked() {
	now := s.now()
	if s.store == nil || now.Sub(s.lastThreadPrune) < threadPruneEvery {
		return
	}
	s.lastThreadPrune = now
	for key, ts := range s.threads {
		if s.sessionRetention > 0 {
			for tid, ref := range ts.traces {
				if now.Sub(ref.at) > s.sessionRetention {
					delete(ts.traces, tid)
				}
			}
		}
		if ts.open == "" && now.Sub(ts.touched) > threadStateKeep {
			delete(s.threads, key)
		}
	}
}
