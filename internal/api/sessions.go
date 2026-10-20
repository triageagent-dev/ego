package api

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/triageagent-dev/ego/internal/adk"
	"github.com/triageagent-dev/ego/internal/incremental"
	"github.com/triageagent-dev/ego/internal/otlp"
	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

const (
	sessionNameAttr     = "ego.session_name"
	evalSetIDAttr       = "ego.eval_set_id"
	genAIConversationID = "gen_ai.conversation.id"

	maxSpansPerSession = 10000

	defaultIdleTimeout     = 30 * time.Second
	defaultCompletionGrace = 3 * time.Second
)

// Session is one live OTLP-ingested session: the aggregated spans of every trace grouped under one session
// identity.
type Session struct {
	ID             string
	PrimaryTraceID string
	EvalSetID      string
	Metadata       map[string]any
	TraceIDs       map[string]struct{}
	Spans          []*tracepkg.Span
	StartedAt      time.Time
	HasRootSpan    bool
	IsComplete     bool
	CompletedAt    *time.Time
	Invocations    []InvocationDTO
	extractor      *incremental.Extractor
	// Tenant is set when the sender was authenticated as a tenant: the session belongs to it alone, and spans from
	// any other sender are refused. Empty for sessions from the shared token or an open receiver, whose tenants (if
	// any) are read from the spans' tenant attribute (tenantAttr).
	Tenant     string
	tenantAttr string

	// changeSeq is this session's own stamp from SessionStore.changeSeq as
	// of its last mutation - only ever set with SessionStore.mu held. A
	// zero-value session (freshly restored from the archive, not yet
	// mutated again) is never dirty until something changes it again;
	// compared against SessionStore.persistedSeq[id] to let flushPersist
	// skip re-marshaling/re-saving a session that hasn't changed since it
	// was last durably written, even when other sessions in the same
	// SessionStore have.
	changeSeq uint64
}

// SessionSummary is the JSON shape returned by GET /api/streaming/sessions and the session_started SSE event's
// "session" field. Invocations mirrors SessionInfo.invocations: nil unless the session is complete and has at
// least one real invocation (populated by completeSession's batch GenAI-semconv conversion).
type SessionSummary struct {
	SessionID    string          `json:"sessionId"`
	TraceID      string          `json:"traceId"`
	EvalSetID    *string         `json:"evalSetId"`
	SpanCount    int             `json:"spanCount"`
	IsComplete   bool            `json:"isComplete"`
	StartedAt    string          `json:"startedAt"`
	CompletedAt  *string         `json:"completedAt"`
	Metadata     map[string]any  `json:"metadata"`
	Invocations  []InvocationDTO `json:"invocations,omitempty"`
	RootSpanName string          `json:"rootSpanName,omitempty"`
	LLMCalls     int             `json:"llmCalls"`
	// Errors counts spans whose status is ERROR (otel.status_code).
	Errors int `json:"errors"`
	// Agents and Models are the distinct gen_ai.agent.name and model values on the session's spans, in the
	// order first seen, so the list can say who ran and on what before the session completes.
	Agents []string `json:"agents,omitempty"`
	// Tenants are the session's tenants: the sender's tenant, or the distinct tenant attribute values on its spans.
	Tenants []string `json:"tenants,omitempty"`
	Models  []string `json:"models,omitempty"`
	// DurationMs is the session's span window: earliest span start to
	// latest span end, in milliseconds. Not CompletedAt - StartedAt, which
	// is ingest wall-clock and includes the completion grace.
	DurationMs int64 `json:"durationMs"`
}

func summarize(s *Session) SessionSummary {
	var evalSetID *string
	if s.EvalSetID != "" {
		evalSetID = &s.EvalSetID
	}
	metadata := s.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	var completedAt *string
	if s.CompletedAt != nil {
		formatted := s.CompletedAt.Format(time.RFC3339)
		completedAt = &formatted
	}
	var invocations []InvocationDTO
	if s.IsComplete && len(s.Invocations) > 0 {
		invocations = s.Invocations
	}
	rootName, llmCalls, errs := spanOutline(s.Spans)
	agents, models := spanIdentities(s.Spans)
	tenants := sessionTenants(s)
	return SessionSummary{
		Agents:       agents,
		Tenants:      tenants,
		Models:       models,
		RootSpanName: rootName,
		LLMCalls:     llmCalls,
		Errors:       errs,
		DurationMs:   spanWindowMs(s.Spans),
		SessionID:    s.ID,
		TraceID:      s.PrimaryTraceID,
		EvalSetID:    evalSetID,
		SpanCount:    len(s.Spans),
		IsComplete:   s.IsComplete,
		StartedAt:    s.StartedAt.Format(time.RFC3339),
		CompletedAt:  completedAt,
		Metadata:     metadata,
		Invocations:  invocations,
	}
}

const maxIdentities = 6

// spanIdentities returns the distinct agent names (gen_ai.agent.name) and model names on spans, in the order
// first seen, at most maxIdentities of each.
// tenantAttrOrDefault is the span attribute tenants are carried in.
func (s *SessionStore) tenantAttrOrDefault() string {
	if s.usageTenantAttr == "" {
		return DefaultTenantAttr
	}
	return s.usageTenantAttr
}

// sessionTenants lists a session's tenants: its owner when it has one, otherwise the distinct values of the tenant
// attribute on its spans, in the order first seen.
func sessionTenants(s *Session) []string {
	if s.Tenant != "" {
		return []string{s.Tenant}
	}
	attr := s.tenantAttr
	if attr == "" {
		attr = DefaultTenantAttr
	}
	var out []string
	seen := map[string]bool{}
	for _, sp := range s.Spans {
		if v := sp.TagString(attr); v != "" && !seen[v] && len(out) < maxIdentities {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func spanIdentities(spans []*tracepkg.Span) (agents, models []string) {
	seenAgent, seenModel := map[string]bool{}, map[string]bool{}
	add := func(list *[]string, seen map[string]bool, v string) {
		if v == "" || v == "unknown" || seen[v] || len(*list) >= maxIdentities {
			return
		}
		seen[v] = true
		*list = append(*list, v)
	}
	for _, sp := range spans {
		add(&agents, seenAgent, sp.TagString(adk.GenAIAgentName))
		model := sp.TagString(adk.GenAIRequestModel)
		if model == "" {
			model = sp.TagString("gen_ai.response.model")
		}
		add(&models, seenModel, model)
	}
	return agents, models
}

// spanOutline returns the earliest root span's name, the number of LLM
// call spans and the number of spans with status ERROR in spans.
// traceRootName returns the name of traceID's root span among spans, or ""
// when it has not arrived yet.
func traceRootName(spans []*tracepkg.Span, traceID string) string {
	for _, sp := range spans {
		if sp.TraceID == traceID && sp.ParentSpanID == "" {
			return sp.OperationName
		}
	}
	return ""
}

// spanWindowMs returns the time from the earliest span start to the latest
// span end in spans, in milliseconds; 0 when there are no spans.
func spanWindowMs(spans []*tracepkg.Span) int64 {
	var start, end int64
	for i, sp := range spans {
		if i == 0 || sp.StartTime < start {
			start = sp.StartTime
		}
		if e := sp.StartTime + sp.Duration; i == 0 || e > end {
			end = e
		}
	}
	return (end - start) / 1000
}

func spanOutline(spans []*tracepkg.Span) (rootName string, llmCalls, errs int) {
	var rootStart int64
	for _, sp := range spans {
		if adk.IsLLMSpan(sp) {
			llmCalls++
		}
		if sp.TagString("otel.status_code") == "ERROR" {
			errs++
		}
		if sp.ParentSpanID == "" && (rootName == "" || sp.StartTime < rootStart) {
			rootName, rootStart = sp.OperationName, sp.StartTime
		}
	}
	return rootName, llmCalls, errs
}

// sessionStartedEvent. WSSpanReceivedEvent is not sent; see Ingest.
type sessionStartedEvent struct {
	Type    string         `json:"type"`
	Session SessionSummary `json:"session"`
}

// sessionCompleteEvent. Invocations is the batch-converted GenAI-semconv/ADK invocation list (see
// extractInvocationsForUI): non-nil only when the conversion produced at least one invocation, matching
// LiveStreamingView.tsx's `data.invocations?.length` check (an empty/absent list leaves whatever
// internal/incremental already broadcast live untouched).
type sessionCompleteEvent struct {
	Type         string          `json:"type"`
	SessionID    string          `json:"sessionId"`
	Invocations  []InvocationDTO `json:"invocations"`
	CompletedAt  *string         `json:"completedAt"`
	RootSpanName string          `json:"rootSpanName,omitempty"`
	LLMCalls     int             `json:"llmCalls"`
	Errors       int             `json:"errors"`
	DurationMs   int64           `json:"durationMs"`
}

// SessionStore holds every session ingested since process start, in memory,
// optionally archived to a SQLiteStore so sessions survive a restart.
type SessionStore struct {
	mu               sync.Mutex
	sessions         map[string]*Session
	order            []string
	hub              *sseHub
	store            *SQLiteStore
	idleTimeout      time.Duration
	completionGrace  time.Duration
	idleTimers       map[string]*time.Timer
	completionTimers map[string]*time.Timer

	// changeSeq/lastPersistedSeq let flushPersist skip its snapshot+DB
	// round trip entirely when nothing has changed since the last
	// successful persist (e.g. an idle deployment with no active OTLP
	// traffic, ticking every persistInterval for no reason otherwise).
	// persistedSeq additionally lets it skip re-marshaling/re-saving
	// individual sessions that haven't changed even when *some* other
	// session has (e.g. one active session amid many long-completed
	// ones that will never mutate again). All three are only ever
	// touched with mu held. changeSeq is bumped by every mutation to a
	// Session's persisted fields (span ingestion, completion, reopening),
	// and that new value is stamped onto the mutated Session's own
	// changeSeq; lastPersistedSeq/persistedSeq only ever advance to a
	// changeSeq value flushPersist has confirmed was durably written -
	// never on a failed write, so a failed persist is retried next tick
	// exactly as before this optimization, not silently dropped.
	changeSeq        uint64
	lastPersistedSeq uint64
	persistedSeq     map[string]uint64

	// pendingUsage holds ledger rows (usagestore.go) for LLM spans ingested
	// since the last flushPersist, which writes them alongside the session
	// snapshots. Only collected when an archive is configured. Touched with
	// mu held.
	pendingUsage []usageRow
	// pendingRoots maps a trace ID to its root span's name, for
	// FillUsageKinds at the next flush. Touched with mu held.
	pendingRoots map[string]string
	// usageRetention bounds the ledger; lastUsagePrune rate-limits the
	// prune to once a day.
	usageRetention time.Duration
	// usageTenantAttr is SessionLimits.UsageTenantAttr.
	usageTenantAttr string
	lastUsagePrune  time.Time

	// maxMemory caps the completed sessions held in memory (0 = no cap); active sessions always stay. Older
	// completed sessions are evicted once durably archived and read back on demand (lookupLocked).
	// sessionRetention deletes archived sessions not updated for that long (0 = keep forever), once a day, then
	// compacts the file.
	maxMemory        int
	sessionRetention time.Duration
	lastSessionPrune time.Time

	// onComplete, when set, is called with a session's ID after it
	// completes, outside s.mu (online evaluation's hook). It must not
	// block.
	onComplete func(sessionID string)
}

// SetOnComplete registers the session-complete hook. Call before serving.
func (s *SessionStore) SetOnComplete(fn func(sessionID string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onComplete = fn
}

// Defaults for SessionLimits: the newest 200 completed sessions in memory,
// archived sessions kept 30 days.
const (
	DefaultSessionMemory    = 200
	DefaultSessionRetention = 30 * 24 * time.Hour
)

// SessionLimits bounds the session store: how many completed sessions stay
// in memory and how long archived sessions are kept.
type SessionLimits struct {
	Memory    int
	Retention time.Duration
	// UsageTenantAttr is the span attribute the usage ledger reads the
	// tenant from; empty records no tenant.
	UsageTenantAttr string
	// EvalDailyTokens caps the judge tokens interactive evaluations may use per UTC day; 0 means unlimited.
	EvalDailyTokens int64
}

func NewSessionStore(hub *sseHub) *SessionStore {
	return &SessionStore{
		sessions:         map[string]*Session{},
		hub:              hub,
		idleTimeout:      defaultIdleTimeout,
		completionGrace:  defaultCompletionGrace,
		idleTimers:       map[string]*time.Timer{},
		persistedSeq:     map[string]uint64{},
		completionTimers: map[string]*time.Timer{},
		usageTenantAttr:  DefaultTenantAttr,
	}
}

// markChangedLocked bumps the store-wide change counter and stamps it onto
// session, marking it (and only it) dirty for the next flushPersist. Must
// be called with s.mu held, at every point a Session's persisted fields
// (Spans/HasRootSpan/IsComplete/CompletedAt/EvalSetID/Invocations) change.
func (s *SessionStore) markChangedLocked(session *Session) {
	s.changeSeq++
	session.changeSeq = s.changeSeq
}

// NewSessionStoreWithArchive is NewSessionStore plus a SQLiteStore to persist to.
func NewSessionStoreWithArchive(hub *sseHub, store *SQLiteStore, limits SessionLimits) *SessionStore {
	s := NewSessionStore(hub)
	s.store = store
	s.usageRetention = DefaultUsageRetention
	s.maxMemory = limits.Memory
	s.sessionRetention = limits.Retention
	if limits.UsageTenantAttr != "" {
		s.usageTenantAttr = limits.UsageTenantAttr
	}
	return s
}

// LoadPersisted restores every archived session, if a store is configured.
// Each restored session gets a fresh incremental.Extractor - see
// sessionSnapshot's doc comment for why that's fine. Call once at startup,
// before serving traffic.
//
// A restored, not-yet-complete session gets a fresh idle timer here so it still completes once idleTimeout
// passes with no new span, matching what would have happened had the process never restarted.
func (s *SessionStore) LoadPersisted() error {
	if s.store == nil {
		return nil
	}

	// Backfill the usage ledger from archived spans, so token history
	// starts with the archive rather than with the first deploy that has
	// the ledger. Only while the ledger is empty: once it has rows, every
	// new LLM span is ledgered as it arrives, and a full pass over the
	// archive on every start is wasted work. Streams one session at a time.
	empty, err := s.store.UsageEmpty()
	if err != nil {
		return fmt.Errorf("checking usage ledger: %w", err)
	}
	if empty {
		var backfill []usageRow
		if err := s.store.ForEachSnapshot(func(snap sessionSnapshot) {
			for _, span := range snap.Spans {
				if row, ok := usageRowFromSpan(span, snap.SessionID, s.usageTenantAttr); ok {
					backfill = append(backfill, row)
				}
			}
		}); err != nil {
			return fmt.Errorf("reading session archive for usage backfill: %w", err)
		}
		if err := s.store.InsertUsage(backfill); err != nil {
			return fmt.Errorf("backfilling usage ledger: %w", err)
		}
	}
	// Name kindless rows after their trace's root span, for rows written
	// before roots were used as the fallback or whose root came later.
	if kindless, err := s.store.UsageKindless(); err != nil {
		return fmt.Errorf("checking usage ledger kinds: %w", err)
	} else if kindless {
		roots := map[string]string{}
		if err := s.store.ForEachSnapshot(func(snap sessionSnapshot) {
			for _, span := range snap.Spans {
				if span.ParentSpanID == "" {
					roots[span.TraceID] = span.OperationName
				}
			}
		}); err != nil {
			return fmt.Errorf("reading session archive for usage kinds: %w", err)
		}
		if err := s.store.FillUsageKinds(roots); err != nil {
			return fmt.Errorf("filling usage kinds: %w", err)
		}
	}

	// Only the newest sessions come back into memory; the rest stay in the
	// archive and are read on demand. A few extra beyond maxMemory leave
	// room for still-active sessions among the newest.
	limit := 0
	if s.maxMemory > 0 {
		limit = s.maxMemory + 50
	}
	snapshots, err := s.store.LoadRecent(limit, restoreBudgetBytes)
	if err != nil {
		return fmt.Errorf("loading session archive: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, snap := range snapshots {
		id := snap.SessionID
		s.sessions[id] = restoreSession(snap, s.usageTenantAttr)
		s.order = append(s.order, id)
		if !snap.IsComplete {
			s.resetIdleTimerLocked(id)
		}
	}
	if len(snapshots) > 0 {
		log.Printf("restored %d session(s) from archive", len(snapshots))
	}
	return nil
}

// restoreBudgetBytes bounds the archived JSON read back into memory at start,
// newest sessions first, whatever maxMemory allows.
const restoreBudgetBytes = 16 << 20

// restoreSession rebuilds a Session from its archived snapshot, with a
// fresh extractor (see sessionSnapshot) and, for a completed session, its
// invocations recomputed from the spans.
func restoreSession(snap sessionSnapshot, tenantAttr string) *Session {
	traceIDs := make(map[string]struct{}, len(snap.TraceIDs))
	for _, tid := range snap.TraceIDs {
		traceIDs[tid] = struct{}{}
	}
	sess := &Session{
		ID:             snap.SessionID,
		Tenant:         snap.Tenant,
		tenantAttr:     tenantAttr,
		PrimaryTraceID: snap.TraceID,
		EvalSetID:      snap.EvalSetID,
		Metadata:       snap.Metadata,
		TraceIDs:       traceIDs,
		Spans:          snap.Spans,
		StartedAt:      snap.StartedAt,
		HasRootSpan:    snap.HasRootSpan,
		IsComplete:     snap.IsComplete,
		CompletedAt:    snap.CompletedAt,
		extractor:      incremental.New(),
	}
	if snap.IsComplete {
		// Persisted snapshots predate this converter (or were archived
		// before invocations existed) - recompute rather than leaving
		// completed sessions permanently without detail after restart.
		sess.Invocations = extractInvocationsForUI(otlp.BuildTrace(snap.SessionID, snap.Spans))
	}
	return sess
}

// lookupLocked returns a session from memory or, when it was evicted, read
// back from the archive (not re-added to memory). Must be called with s.mu
// held.
func (s *SessionStore) lookupLocked(sessionID string) (*Session, bool) {
	if sess, ok := s.sessions[sessionID]; ok {
		return sess, true
	}
	if s.store == nil {
		return nil, false
	}
	snap, ok, err := s.store.Load(sessionID)
	if err != nil {
		log.Printf("session archive: loading %s: %v", sessionID, err)
		return nil, false
	}
	if !ok {
		return nil, false
	}
	return restoreSession(snap, s.usageTenantAttr), true
}

// StartPersistence begins a background loop snapshotting every session to
// the archive every interval, until stop is closed. A no-op if no store is
// configured.
func (s *SessionStore) StartPersistence(interval time.Duration, stop <-chan struct{}) {
	if s.store == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := s.flushPersist(); err != nil {
					log.Printf("session archive: periodic snapshot failed: %v", err)
				}
			case <-stop:
				return
			}
		}
	}()
}

// flushPersist snapshots and saves only the sessions that changed since
// the last snapshot this function itself confirmed was durably saved -
// both as a whole (the changeSeq/lastPersistedSeq fast path skips the
// entire cycle when nothing anywhere changed, e.g. an idle deployment
// ticking every persistInterval for no reason) and individually (a
// session whose own changeSeq stamp is no newer than what persistedSeq[id]
// already reflects is left out of the batch, so one active session amid
// many long-completed ones that will never mutate again doesn't cause
// every one of them to be re-marshaled and rewritten every tick). See
// changeSeq's doc comment on SessionStore. A failed SaveAll leaves
// lastPersistedSeq/persistedSeq untouched, so the same sessions are
// retried next tick, exactly as before this optimization existed (when
// every tick unconditionally retried everything).
func (s *SessionStore) flushPersist() error {
	if err := s.flushUsage(); err != nil {
		return err
	}

	s.mu.Lock()
	seq := s.changeSeq
	if seq == s.lastPersistedSeq {
		s.mu.Unlock()
		return nil
	}
	snapshot := make(map[string]*Session)
	captured := make(map[string]uint64, len(s.sessions))
	for id, sess := range s.sessions {
		if sess.changeSeq > s.persistedSeq[id] {
			snapshot[id] = sess
			captured[id] = sess.changeSeq
		}
	}
	s.mu.Unlock()

	if len(snapshot) > 0 {
		if err := s.store.SaveAll(snapshot); err != nil {
			return err
		}
	}

	s.mu.Lock()
	for id, v := range captured {
		if v > s.persistedSeq[id] {
			s.persistedSeq[id] = v
		}
	}
	if seq > s.lastPersistedSeq {
		s.lastPersistedSeq = seq
	}
	s.evictLocked()
	s.mu.Unlock()
	s.pruneSessions()
	return nil
}

// evictLocked drops the oldest completed sessions from memory while more
// than maxMemory are held. Only sessions already durably archived (their
// changeSeq persisted) with no pending timer go; they stay readable via
// lookupLocked. Must be called with s.mu held.
func (s *SessionStore) evictLocked() {
	if s.maxMemory <= 0 || s.store == nil {
		return
	}
	var done []*Session
	for _, sess := range s.sessions {
		if sess.IsComplete {
			done = append(done, sess)
		}
	}
	excess := len(done) - s.maxMemory
	if excess <= 0 {
		return
	}
	sort.Slice(done, func(i, j int) bool { return sessionAge(done[i]).Before(sessionAge(done[j])) })
	evicted := map[string]bool{}
	for _, sess := range done {
		if len(evicted) == excess {
			break
		}
		id := sess.ID
		if sess.changeSeq > s.persistedSeq[id] || s.idleTimers[id] != nil || s.completionTimers[id] != nil {
			continue
		}
		evicted[id] = true
		delete(s.sessions, id)
		delete(s.persistedSeq, id)
	}
	if len(evicted) == 0 {
		return
	}
	order := s.order[:0]
	for _, id := range s.order {
		if !evicted[id] {
			order = append(order, id)
		}
	}
	s.order = order
}

func sessionAge(sess *Session) time.Time {
	if sess.CompletedAt != nil {
		return *sess.CompletedAt
	}
	return sess.StartedAt
}

// pruneSessions deletes archived sessions older than sessionRetention, at
// most once a day, drops them from memory too, and compacts the file when
// anything was deleted.
func (s *SessionStore) pruneSessions() {
	s.mu.Lock()
	due := s.store != nil && s.sessionRetention > 0 && time.Since(s.lastSessionPrune) >= 24*time.Hour
	if due {
		s.lastSessionPrune = time.Now()
	}
	s.mu.Unlock()
	if !due {
		return
	}
	cutoff := time.Now().Add(-s.sessionRetention)
	n, err := s.store.PruneSessions(cutoff)
	if err != nil {
		log.Printf("session archive: prune failed: %v", err)
		return
	}
	if n == 0 {
		return
	}
	s.mu.Lock()
	order := s.order[:0]
	for _, id := range s.order {
		sess := s.sessions[id]
		if sess.IsComplete && sessionAge(sess).Before(cutoff) && sess.changeSeq <= s.persistedSeq[id] {
			delete(s.sessions, id)
			delete(s.persistedSeq, id)
			continue
		}
		order = append(order, id)
	}
	s.order = order
	s.mu.Unlock()
	if err := s.store.Compact(); err != nil {
		log.Printf("session archive: compact failed: %v", err)
	}
	log.Printf("session archive: pruned %d session(s) older than %s and compacted", n, s.sessionRetention)
}

// requeueRootsLocked puts trace roots back for the next flush after a
// failed write. Caller holds mu.
func (s *SessionStore) requeueRootsLocked(roots map[string]string) {
	if len(roots) == 0 {
		return
	}
	if s.pendingRoots == nil {
		s.pendingRoots = map[string]string{}
	}
	for id, name := range roots {
		if _, ok := s.pendingRoots[id]; !ok {
			s.pendingRoots[id] = name
		}
	}
}

// flushUsage writes buffered usage rows and, at most once a day, prunes
// rows older than usageRetention. Rows go back into the buffer when the
// write fails, so the next tick retries them.
func (s *SessionStore) flushUsage() error {
	s.mu.Lock()
	rows := s.pendingUsage
	s.pendingUsage = nil
	roots := s.pendingRoots
	s.pendingRoots = nil
	prune := s.usageRetention > 0 && time.Since(s.lastUsagePrune) >= 24*time.Hour
	if prune {
		s.lastUsagePrune = time.Now()
	}
	s.mu.Unlock()

	if err := s.store.InsertUsage(rows); err != nil {
		s.mu.Lock()
		s.pendingUsage = append(rows, s.pendingUsage...)
		s.requeueRootsLocked(roots)
		s.mu.Unlock()
		return fmt.Errorf("writing usage ledger: %w", err)
	}
	if err := s.store.FillUsageKinds(roots); err != nil {
		s.mu.Lock()
		s.requeueRootsLocked(roots)
		s.mu.Unlock()
		return fmt.Errorf("filling usage kinds: %w", err)
	}
	if prune {
		if n, err := s.store.PruneUsage(time.Now().Add(-s.usageRetention)); err != nil {
			log.Printf("usage ledger: prune failed: %v", err)
		} else if n > 0 {
			log.Printf("usage ledger: pruned %d row(s) older than %s", n, s.usageRetention)
		}
	}
	return nil
}

// Close flushes a final snapshot and closes the archive, if configured.
// Also stops every pending idle/completion timer so no goroutine fires
// after the store is done.
func (s *SessionStore) Close() error {
	s.mu.Lock()
	for _, t := range s.idleTimers {
		t.Stop()
	}
	for _, t := range s.completionTimers {
		t.Stop()
	}
	s.mu.Unlock()

	if s.store == nil {
		return nil
	}
	if err := s.flushPersist(); err != nil {
		log.Printf("session archive: final snapshot failed: %v", err)
	}
	return s.store.Close()
}

// Ingest decodes one OTLP export body (resourceSpans) and merges its spans into the.session_name resource
// attribute, falling back to gen_ai.conversation.id, falling back to a per-trace-ID synthetic name. Each span
// is also run through that session's incremental extractor, and every resulting update - plus the span itself,
// plus a session_started event on first sight of a session - is broadcast to connected /stream/ui-updates
// clients.
// Ingest stores an export from an unauthenticated or shared-token sender (no tenant of its own).
func (s *SessionStore) Ingest(body map[string]any) int {
	return s.IngestFor("", body)
}

// IngestFor stores an export sent as tenant ("" for none). A tenant sender's spans get the tenant stamped on them,
// overwriting whatever tenant attribute they carried, its sessions are named "<tenant>:<name>" so two tenants
// cannot meet in one session, and spans for a session owned by a different tenant are dropped.
func (s *SessionStore) IngestFor(tenant string, body map[string]any) int {
	// Decoding a large export is the expensive part and touches no store state, so it happens before the lock is
	// taken: a big body must not stall every reader of the store while it is parsed.
	type parsedResource struct {
		resourceAttrs                          map[string]any
		sessionName, conversationID, evalSetID string
		traces                                 []*tracepkg.Trace
	}
	var parsed []parsedResource
	for _, rs := range asSlice(body["resourceSpans"]) {
		resource := asMap(asMap(rs)["resource"])
		p := parsedResource{resourceAttrs: otlp.DecodeAttributes(asSlice(resource["attributes"]))}
		p.sessionName, _ = p.resourceAttrs[sessionNameAttr].(string)
		p.conversationID, _ = p.resourceAttrs[genAIConversationID].(string)
		p.evalSetID, _ = p.resourceAttrs[evalSetIDAttr].(string)
		p.traces = otlp.ParseExportRequest(map[string]any{"resourceSpans": []any{rs}})
		if tenant != "" {
			for _, tr := range p.traces {
				for _, sp := range tr.AllSpans {
					if sp.Tags == nil {
						sp.Tags = map[string]any{}
					}
					sp.Tags[s.tenantAttrOrDefault()] = tenant
				}
			}
		}
		parsed = append(parsed, p)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	ingested := 0
	for _, p := range parsed {
		resourceAttrs, sessionName, conversationID, evalSetID := p.resourceAttrs, p.sessionName, p.conversationID, p.evalSetID

		for _, tr := range p.traces {
			name := sessionName
			if name == "" {
				name = conversationID
			}
			if name == "" {
				name = traceConversationID(tr)
			}
			if name == "" {
				name = fmt.Sprintf("otlp-%s", shortID(tr.TraceID))
			}
			if tenant != "" {
				name = tenant + ":" + name
			}

			session, isNew := s.getOrCreateSession(name, tr.TraceID, evalSetID, tenant, resourceAttrs)
			if session.Tenant != tenant {
				log.Printf("ingest: dropped spans for session %q: it belongs to another sender", name)
				continue
			}

			// session_started must reach clients before any update event for this session, so the UI has a
			// session to attach those events to (LiveStreamingView.tsx's user_input/etc. handlers no-op on an
			// unknown sessionId). It is sent after this batch's spans are in, un.
			var updates []sessionScopedUpdate

			for _, span := range tr.AllSpans {
				if len(session.Spans) >= maxSpansPerSession {
					break
				}
				session.Spans = append(session.Spans, span)
				s.markChangedLocked(session)
				ingested++
				if s.store != nil {
					if row, ok := usageRowFromSpan(span, session.ID, s.usageTenantAttr); ok {
						if row.Kind == "" {
							row.Kind = traceRootName(session.Spans, span.TraceID)
						}
						s.pendingUsage = append(s.pendingUsage, row)
					}
					if span.ParentSpanID == "" {
						if s.pendingRoots == nil {
							s.pendingRoots = map[string]string{}
						}
						s.pendingRoots[span.TraceID] = span.OperationName
					}
				}

				// Diverges from process_traces, which also broadcasts every
				// span as span_received: a traced server exports hundreds of
				// spans per batch, which overflowed each client's buffer and
				// re-rendered the whole session list once per span. The UI
				// reads spans on demand (session-spans) instead.
				if s.hub != nil {
					for _, u := range session.extractor.ProcessSpan(span) {
						updates = append(updates, sessionScopedUpdate{SessionID: session.ID, Update: u})
					}
				}

				// Both timers ultimately call completeSession.
				s.resetIdleTimerLocked(session.ID)
				if span.ParentSpanID == "" {
					session.HasRootSpan = true
					s.scheduleCompletionLocked(session.ID)
				}
			}

			if s.hub != nil {
				// Agents are read once per batch: a scoped client is only sent what its agents allow.
				var agents, tenants []string
				if s.hub.hasScoped() {
					agents, _ = spanIdentities(session.Spans)
					tenants = sessionTenants(session)
				}
				if isNew {
					s.hub.broadcastSession(sessionStartedEvent{Type: "session_started", Session: summarize(session)}, session.ID, agents, tenants, true)
				}
				for _, u := range updates {
					s.hub.broadcastSession(u, session.ID, agents, tenants, false)
				}
			}
		}
	}
	return ingested
}

func (s *SessionStore) getOrCreateSession(name, traceID, evalSetID, tenant string, resourceAttrs map[string]any) (*Session, bool) {
	session := s.sessions[name]
	if session == nil {
		// A span for a session evicted from memory: bring it back rather
		// than starting a second session under the same name.
		if old, ok := s.lookupLocked(name); ok {
			session = old
			s.sessions[name] = session
			s.order = append(s.order, name)
			s.persistedSeq[name] = 0
		}
	}
	isNew := false
	if session == nil {
		session = &Session{
			ID:             name,
			Tenant:         tenant,
			tenantAttr:     s.usageTenantAttr,
			PrimaryTraceID: traceID,
			EvalSetID:      evalSetID,
			Metadata:       withoutSessionAttrs(resourceAttrs),
			TraceIDs:       map[string]struct{}{},
			StartedAt:      time.Now().UTC(),
			extractor:      incremental.New(),
		}
		s.sessions[name] = session
		s.order = append(s.order, name)
		s.markChangedLocked(session)
		isNew = true
	} else {
		if evalSetID != "" {
			session.EvalSetID = evalSetID
			s.markChangedLocked(session)
		}
		// Reopen rather than leaving the session stuck complete.
		if session.IsComplete {
			session.IsComplete = false
			session.CompletedAt = nil
			s.markChangedLocked(session)
		}
	}
	session.TraceIDs[traceID] = struct{}{}
	return session, isNew
}

// resetIdleTimerLocked (re)starts the idle-completion timer for a session. Must be called with s.mu held; the
// timer's own callback (completeSession) acquires s.mu itself when it eventually fires.
func (s *SessionStore) resetIdleTimerLocked(sessionID string) {
	if t, ok := s.idleTimers[sessionID]; ok {
		t.Stop()
	}
	s.idleTimers[sessionID] = time.AfterFunc(s.idleTimeout, func() {
		s.completeSession(sessionID)
	})
}

// scheduleCompletionLocked (re)starts the post-root-span grace timer for a session. Must be called with s.mu
// held.
func (s *SessionStore) scheduleCompletionLocked(sessionID string) {
	if t, ok := s.completionTimers[sessionID]; ok {
		t.Stop()
	}
	s.completionTimers[sessionID] = time.AfterFunc(s.completionGrace, func() {
		s.completeSession(sessionID)
	})
}

// completeSession marks a session complete and broadcasts session_complete, idempotently. Runs as an
// idle/completion timer callback, so it acquires s.mu itself rather than assuming it's held.
func (s *SessionStore) completeSession(sessionID string) {
	s.mu.Lock()
	session, ok := s.sessions[sessionID]
	if !ok || session.IsComplete {
		s.mu.Unlock()
		return
	}

	now := time.Now().UTC()
	session.IsComplete = true
	session.CompletedAt = &now
	s.markChangedLocked(session)

	// Batch-convert the session's full span set into real invocations (tool calls, user/agent text, model info)
	// now that it's done -. Built from otlp.BuildTrace's merged parent/child tree, same as the Trace() method.
	trace := otlp.BuildTrace(sessionID, session.Spans)
	session.Invocations = extractInvocationsForUI(trace)

	if t, ok := s.idleTimers[sessionID]; ok {
		t.Stop()
		delete(s.idleTimers, sessionID)
	}
	if t, ok := s.completionTimers[sessionID]; ok {
		t.Stop()
		delete(s.completionTimers, sessionID)
	}

	completedAt := now.Format(time.RFC3339)
	invocations := session.Invocations
	rootName, llmCalls, errs := spanOutline(session.Spans)
	durationMs := spanWindowMs(session.Spans)
	var agents, tenants []string
	if s.hub != nil && s.hub.hasScoped() {
		agents, _ = spanIdentities(session.Spans)
		tenants = sessionTenants(session)
	}
	hub := s.hub
	onComplete := s.onComplete
	s.mu.Unlock()

	if onComplete != nil {
		onComplete(sessionID)
	}

	if hub != nil {
		hub.broadcastSession(sessionCompleteEvent{
			Type:         "session_complete",
			SessionID:    sessionID,
			Invocations:  invocations,
			CompletedAt:  &completedAt,
			RootSpanName: rootName,
			LLMCalls:     llmCalls,
			Errors:       errs,
			DurationMs:   durationMs,
		}, sessionID, agents, tenants, false)
	}
}

type sessionScopedUpdate struct {
	SessionID string `json:"sessionId"`
	incremental.Update
}

// List returns every session's summary, oldest first.
func (s *SessionStore) List() []SessionSummary {
	s.mu.Lock()
	defer s.mu.Unlock()

	summaries := make([]SessionSummary, 0, len(s.order))
	for _, id := range s.order {
		summaries = append(summaries, summarize(s.sessions[id]))
	}
	return summaries
}

// Summary returns one session's summary by ID, reading it back from the archive when it was evicted from
// memory, so a link to an old session still opens.
func (s *SessionStore) Summary(sessionID string) (SessionSummary, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.lookupLocked(sessionID)
	if !ok {
		return SessionSummary{}, false
	}
	return summarize(session), true
}

// Trace rebuilds the merged parent/child span tree across every span in
// the named session, regardless of which OTLP trace ID it originally
// arrived under (mirrors how a session can span multiple traces).
func (s *SessionStore) Trace(sessionID string) (*tracepkg.Trace, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.lookupLocked(sessionID)
	if !ok {
		return nil, false
	}
	return otlp.BuildTrace(sessionID, session.Spans), true
}

// SpanRows returns a session's spans depth-first, children by start time
// (BuildTrace already sorts them), for GET /api/streaming/session-spans.
// The walk runs under s.mu: BuildTrace rewires Children on the shared span
// pointers, so walking after unlocking could race with a span arriving.
func (s *SessionStore) SpanRows(sessionID string) ([]sessionSpanDTO, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.lookupLocked(sessionID)
	if !ok {
		return nil, false
	}
	tr := otlp.BuildTrace(sessionID, session.Spans)
	out := make([]sessionSpanDTO, 0, len(tr.AllSpans))
	var walk func(sp *tracepkg.Span, depth int)
	walk = func(sp *tracepkg.Span, depth int) {
		out = append(out, sessionSpanDTO{
			SpanID:        sp.SpanID,
			ParentSpanID:  sp.ParentSpanID,
			OperationName: sp.OperationName,
			StartTime:     sp.StartTime,
			Duration:      sp.Duration,
			Depth:         depth,
			Tags:          sp.Tags,
		})
		for _, c := range sp.Children {
			walk(c, depth+1)
		}
	}
	for _, root := range tr.RootSpans {
		walk(root, 0)
	}
	return out, true
}

// withoutSessionAttrs returns resourceAttrs minus any ego.*-prefixed key.
func withoutSessionAttrs(resourceAttrs map[string]any) map[string]any {
	out := make(map[string]any, len(resourceAttrs))
	for k, v := range resourceAttrs {
		if !strings.HasPrefix(k, "ego.") {
			out[k] = v
		}
	}
	return out
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func traceConversationID(tr *tracepkg.Trace) string {
	for _, span := range tr.AllSpans {
		if v := span.TagString(genAIConversationID); v != "" {
			return v
		}
	}
	return ""
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}
