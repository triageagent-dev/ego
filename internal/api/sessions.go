package api

import (
	"cmp"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/triageagent-dev/ego/internal/adk"
	"github.com/triageagent-dev/ego/internal/incremental"
	"github.com/triageagent-dev/ego/internal/logx"
	"github.com/triageagent-dev/ego/internal/otlp"
	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

const (
	sessionNameAttr     = "ego.session_name"
	evalSetIDAttr       = "ego.eval_set_id"
	genAIConversationID = "gen_ai.conversation.id"

	// maxSpansPerSession is the cap of a session that is not a segment of a declared thread; a segment is bounded by
	// the segment rules instead (segments.go).
	maxSpansPerSession = 10000

	defaultIdleTimeout     = 30 * time.Second
	defaultCompletionGrace = 3 * time.Second
)

// Session is one live OTLP-ingested session: the aggregated spans of every trace grouped under one session
// identity.
//
// A session whose sender declared a thread (the resource attribute ego.thread) is a segment of that thread
// (segments.go): ID is the thread's id for the first part and "<thread>~n" for part n. Every other session is its own
// thread, part 1: Thread is its ID and it never gets a "~n". Thread and Part are stored, so the thread table is an
// indexed query rather than a parse of ids.
type Session struct {
	ID string
	// tenantKey is the tenant of the session's first trace for a named sender. It is archived with the session; a row
	// archived before that gets it from its spans when it is restored (sessionHeader).
	tenantKey string
	// Segmented is true for a segment of a declared thread. Only those are split, kept in the thread routing, swept
	// and scored at close.
	Segmented bool
	// Thread is the sender's own id for the work this session is a part of, Part its 1-based number in that thread.
	Thread string
	Part   int
	// Closed is true once the segment is final: it takes no new trace and is scored. A merely complete (quiet)
	// segment can still reopen. A session that is not a segment is "closed" while it is complete, so the data model
	// is the same for both; a late span reopens it as it always did.
	Closed         bool
	ClosedAt       *time.Time
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
	// Sender is the sender that owns the session: the one whose token sent it. Spans from any other sender are
	// refused. Empty for sessions from the shared token or an open receiver. The session's tenants are read from the
	// spans' tenant label (tenantAttr) and keyed with the sender, see tenantKey.
	Sender     string
	tenantAttr string
	// firstUs and lastUs are the earliest span start and latest span end (microseconds), lastArrival the wall-clock
	// time the last span came in; segments close on them.
	firstUs, lastUs int64
	lastArrival     time.Time

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
	SessionID string `json:"sessionId"`
	// Thread and Part place the session in its thread: the sender's own id and the 1-based number of this segment.
	// Closed is true once the segment is final and scored.
	Thread       string          `json:"thread,omitempty"`
	Part         int             `json:"part,omitempty"`
	Closed       bool            `json:"closed,omitempty"`
	Segmented    bool            `json:"-"`
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
	// Turns counts the prompts of a Claude Code session (its claude_code.interaction spans). Its spans carry no
	// message text for the invocation count to be made of, so this is its turn count; 0 (omitted) for any other session.
	Turns int `json:"turns,omitempty"`
	// Agents and Models are the distinct gen_ai.agent.name and model values on the session's spans, in the
	// order first seen, so the list can say who ran and on what before the session completes.
	Agents []string `json:"agents,omitempty"`
	// Tenants are the session's tenant keys ("<sender>/<label>"), one per distinct label on its spans.
	Tenants []string `json:"tenants,omitempty"`
	Models  []string `json:"models,omitempty"`
	// Service is the first service.name seen on the session's spans.
	Service string `json:"service,omitempty"`
	// Scored is true when online evaluation scored the session (set by Find and the ?id= lookup, not by
	// summarize): the session card then shows judge scores a person can label.
	Scored bool `json:"scored,omitempty"`
	// TotalTokens sums the input and output tokens of the session's LLM spans: the same spans, and so the same
	// total, as its rows in the usage ledger.
	TotalTokens int64 `json:"totalTokens,omitempty"`
	// InputTokens and OutputTokens are the two parts of TotalTokens; the card starts its live counters from them,
	// so a session that was already running when the page opened is not counted from zero.
	InputTokens  int64 `json:"inputTokens,omitempty"`
	OutputTokens int64 `json:"outputTokens,omitempty"`
	// CacheReadTokens and CacheWriteTokens are the parts of InputTokens served from and written to the prompt cache,
	// which the cost estimate prices apart.
	CacheReadTokens  int64 `json:"cacheReadTokens,omitempty"`
	CacheWriteTokens int64 `json:"cacheWriteTokens,omitempty"`
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
	service, inTokens, outTokens := spanServiceAndTokens(s.Spans)
	cacheRead, cacheWrite := spanCacheTokens(s.Spans)
	return SessionSummary{
		CacheReadTokens:  cacheRead,
		CacheWriteTokens: cacheWrite,
		Service:          service,
		TotalTokens:      inTokens + outTokens,
		InputTokens:      inTokens,
		OutputTokens:     outTokens,
		Agents:           agents,
		Tenants:          tenants,
		Models:           models,
		RootSpanName:     rootName,
		LLMCalls:         llmCalls,
		Errors:           errs,
		Turns:            claudeCodeTurns(s.Spans),
		DurationMs:       spanWindowMs(s.Spans),
		SessionID:        s.ID,
		Thread:           s.threadKey(),
		Part:             s.partNo(),
		Closed:           s.Closed,
		Segmented:        s.Segmented,
		TraceID:          s.PrimaryTraceID,
		EvalSetID:        evalSetID,
		SpanCount:        len(s.Spans),
		IsComplete:       s.IsComplete,
		StartedAt:        s.StartedAt.Format(time.RFC3339),
		CompletedAt:      completedAt,
		Metadata:         metadata,
		Invocations:      invocations,
	}
}

const maxIdentities = 6

// spanServiceAndTokens returns the first service.name on spans and the input and output tokens their LLM spans
// reported.
func spanServiceAndTokens(spans []*tracepkg.Span) (service string, input, output int64) {
	for _, sp := range spans {
		if service == "" {
			service = sp.TagString(adk.OtelServiceName)
		}
		if adk.IsLLMSpan(sp) {
			in, out, _ := adk.ExtractTokenUsageFromAttrs(sp.Tags)
			input += in
			output += out
		}
	}
	return service, input, output
}

// spanCacheTokens sums the prompt-cache parts of the input tokens of the LLM spans.
func spanCacheTokens(spans []*tracepkg.Span) (read, write int64) {
	for _, sp := range spans {
		if adk.IsLLMSpan(sp) {
			read += tagInt(sp, "gen_ai.usage.cache_read.input_tokens")
			write += cacheWriteTokens(sp)
		}
	}
	return read, write
}

// spanIdentities returns the distinct agent names (gen_ai.agent.name) and model names on spans, in the order
// first seen, at most maxIdentities of each.
// traceTenantName is the display name a sender gave its tenant on the trace's spans: the first non-empty
// TenantNameAttr.
func traceTenantName(spans []*tracepkg.Span) string {
	for _, sp := range spans {
		if v := strings.TrimSpace(sp.TagString(TenantNameAttr)); v != "" {
			return v
		}
	}
	return ""
}

// traceTenantLabel is the tenant label of a trace: the first non-empty value of attr on its spans.
func traceTenantLabel(spans []*tracepkg.Span, attr string) string {
	if attr == "" {
		attr = DefaultTenantAttr
	}
	for _, sp := range spans {
		if v := cleanLabel(sp.TagString(attr)); v != "" {
			return v
		}
	}
	return ""
}

// sessionTenants lists a session's tenant keys: the distinct labels on its spans, each keyed with the owning
// sender. Spans without a label (an HTTP client span inside a labelled call, say) belong to the tenant of their
// siblings and add nothing; a named sender's session with no label at all belongs to that sender's default tenant.
func sessionTenants(s *Session) []string {
	attr := s.tenantAttr
	if attr == "" {
		attr = DefaultTenantAttr
	}
	var out []string
	seen := map[string]bool{}
	for _, sp := range s.Spans {
		label := cleanLabel(sp.TagString(attr))
		if label == "" {
			continue
		}
		k := tenantKey(s.Sender, label)
		if k != "" && !seen[k] && len(out) < maxIdentities {
			seen[k] = true
			out = append(out, k)
		}
	}
	if len(out) == 0 && len(s.Spans) > 0 {
		if k := tenantKey(s.Sender, ""); k != "" {
			out = append(out, k)
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
		add(&agents, seenAgent, cleanIdentity(sp.TagString(adk.GenAIAgentName)))
		model := sp.TagString(adk.GenAIRequestModel)
		if model == "" {
			model = sp.TagString("gen_ai.response.model")
		}
		add(&models, seenModel, cleanIdentity(model))
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

// claudeCodeTurns counts the prompts of a Claude Code session: one claude_code.interaction span per prompt.
func claudeCodeTurns(spans []*tracepkg.Span) int {
	n := 0
	for _, sp := range spans {
		if sp.OperationName == "claude_code.interaction" {
			n++
		}
	}
	return n
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
	Turns        int             `json:"turns,omitempty"`
	DurationMs   int64           `json:"durationMs"`
}

// SessionStore holds every session ingested since process start, in memory,
// optionally archived to a SQLiteStore so sessions survive a restart.
type SessionStore struct {
	// egoReads caches the answers about an agent's own turns (REST and MCP alike) and limits how many are built at once.
	egoReads *readCache

	mu               sync.Mutex
	indexing         sync.WaitGroup // the session-index backfill LoadPersisted starts; Close waits for it, up to indexingCloseWait
	sessions         map[string]*Session
	order            []string
	hub              *sseHub
	store            *SQLiteStore
	idleTimeout      time.Duration
	completionGrace  time.Duration
	idleTimers       map[string]*time.Timer
	completionTimers map[string]*time.Timer

	// Segments (segments.go): threads routes new traces and late spans to a segment, sweepTimers close a thread's
	// open segment once it is due, and the three limits are the close rules.
	threads         map[string]*threadState
	sweepTimers     map[string]*time.Timer
	lastThreadPrune time.Time
	segIdle         time.Duration
	segMaxSpans     int
	segMaxAge       time.Duration
	// now is the clock of the segment rules; tests replace it.
	now func() time.Time

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

	// onComplete, when set, is called with a segment's ID once it is closed for good (not when it merely goes quiet),
	// outside s.mu (online evaluation's hook). It must not block.
	onComplete  func(sessionID string)
	quotaLogged map[string]time.Time
	// persistMu serialises a flush of the archive with a purge of tenants.
	persistMu sync.Mutex
	// tenantNamer receives the display name a named sender gives its tenant (TenantNameAttr).
	tenantNamer func(key, name string)
}

// Limits per named sender, in memory: open sessions and distinct tenants. Variables so a test can lower them.
var (
	maxOpenSessionsPerSender = 5000
	maxTenantsPerSender      = 1000
)

// overSenderQuotaLocked reports that a trace of sender, for tenant key want and session id name, would open a session
// or a tenant past the sender's limits, and says so in the log at most once a minute per sender. Must be called with
// s.mu held.
func (s *SessionStore) overSenderQuotaLocked(sender, want, name string) bool {
	if _, exists := s.sessions[name]; exists {
		return false
	}
	open, tenants := 0, map[string]bool{}
	for _, sess := range s.sessions {
		if sess.Sender != sender {
			continue
		}
		if !sess.IsComplete {
			open++
		}
		if sess.tenantKey != "" {
			tenants[sess.tenantKey] = true
		}
	}
	reason := ""
	switch {
	case open >= maxOpenSessionsPerSender:
		reason = "open sessions"
	case !tenants[want] && len(tenants) >= maxTenantsPerSender:
		reason = "tenants"
	default:
		return false
	}
	now := s.now()
	if s.quotaLogged == nil {
		s.quotaLogged = map[string]time.Time{}
	}
	if last, ok := s.quotaLogged[sender]; !ok || now.Sub(last) > time.Minute {
		s.quotaLogged[sender] = now
		logx.Warnf("ingest: sender %q is over its limit of %s: new ones are refused", sender, reason)
	}
	return true
}

// SetTenantNamer registers the hook that keeps a sender's own name for its tenant. Call before serving.
func (s *SessionStore) SetTenantNamer(fn func(key, name string)) { s.tenantNamer = fn }

// SetOnComplete registers the segment-closed hook. Call before serving.
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
	// SegmentIdle, SegmentMaxSpans and SegmentMaxAge are the rules that close a segment of a thread (segments.go);
	// zero takes the default.
	SegmentIdle     time.Duration
	SegmentMaxSpans int
	SegmentMaxAge   time.Duration
}

func NewSessionStore(hub *sseHub) *SessionStore {
	return &SessionStore{
		egoReads:         newReadCache(),
		sessions:         map[string]*Session{},
		hub:              hub,
		idleTimeout:      defaultIdleTimeout,
		completionGrace:  defaultCompletionGrace,
		idleTimers:       map[string]*time.Timer{},
		persistedSeq:     map[string]uint64{},
		completionTimers: map[string]*time.Timer{},
		threads:          map[string]*threadState{},
		sweepTimers:      map[string]*time.Timer{},
		segIdle:          DefaultSegmentIdle,
		segMaxSpans:      DefaultSegmentMaxSpans,
		segMaxAge:        DefaultSegmentMaxAge,
		now:              time.Now,
		usageTenantAttr:  DefaultTenantAttr,
	}
}

// markChangedLocked bumps the store-wide change counter and stamps it onto
// session, marking it (and only it) dirty for the next flushPersist. Must
// be called with s.mu held, at every point a Session's persisted fields
// (Spans/HasRootSpan/IsComplete/CompletedAt/EvalSetID/Invocations) change.
// changes is a counter that moves whenever any session changes; an answer computed at one value is still right at
// the same value.
func (s *SessionStore) changes() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.changeSeq
}

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
	if limits.SegmentIdle > 0 {
		s.segIdle = limits.SegmentIdle
	}
	if limits.SegmentMaxSpans > 0 {
		s.segMaxSpans = limits.SegmentMaxSpans
	}
	if limits.SegmentMaxAge > 0 {
		s.segMaxAge = limits.SegmentMaxAge
	}
	return s
}

// migrationBatch is how many ledger rows a startup pass over the archive writes at a time.
const migrationBatch = 5000

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
	// Each pass below streams the archive and writes in batches, so memory stays flat on a large archive and a pass
	// that is cut short (a restart) keeps what it has done: every pass marks its rows and runs again only for the
	// rows still unmarked.
	if empty {
		var batch []usageRow
		var werr error
		flush := func() {
			if werr == nil && len(batch) > 0 {
				werr = s.store.InsertUsage(batch)
			}
			batch = batch[:0]
		}
		sessions := 0
		if err := s.store.ForEachSnapshot(func(snap sessionSnapshot) {
			for _, span := range snap.Spans {
				if row, ok := usageRowFromSpan(span, snap.SessionID, s.usageTenantAttr, snap.Sender, traceTenantLabel(snap.Spans, s.usageTenantAttr)); ok {
					row.Thread = snapThread(snap)
					batch = append(batch, row)
				}
			}
			if sessions++; len(batch) >= migrationBatch {
				flush()
				logx.Infof("session archive: usage backfill, %d sessions read", sessions)
			}
		}); err != nil {
			return fmt.Errorf("reading session archive for usage backfill: %w", err)
		}
		if flush(); werr != nil {
			return fmt.Errorf("backfilling usage ledger: %w", werr)
		}
	}
	// Split the input of rows written before the ledger kept the cache parts, from the archived spans.
	if unknown, err := s.store.UsageCacheUnknown(); err != nil {
		return fmt.Errorf("checking usage ledger cache split: %w", err)
	} else if unknown {
		splits := map[string]cacheSplit{}
		var werr error
		flush := func() {
			if werr == nil && len(splits) > 0 {
				werr = s.store.FillUsageCache(splits)
			}
			clear(splits)
		}
		sessions := 0
		if err := s.store.ForEachSnapshot(func(snap sessionSnapshot) {
			for _, span := range snap.Spans {
				if row, ok := usageRowFromSpan(span, snap.SessionID, s.usageTenantAttr, snap.Sender, ""); ok {
					splits[row.SpanID] = cacheSplit{row.CacheReadTokens, row.CacheWriteTokens}
				}
			}
			if sessions++; len(splits) >= migrationBatch {
				flush()
				logx.Infof("session archive: usage cache split, %d sessions read", sessions)
			}
		}); err != nil {
			return fmt.Errorf("reading session archive for the usage cache split: %w", err)
		}
		if flush(); werr != nil {
			return fmt.Errorf("filling usage cache split: %w", werr)
		}
		// Rows whose session left the archive have nothing to read: their input is priced as it always was.
		if err := s.store.FinishUsageCache(); err != nil {
			return fmt.Errorf("finishing usage cache split: %w", err)
		}
	}
	// Name kindless rows after their trace's root span, for rows written
	// before roots were used as the fallback or whose root came later.
	if kindless, err := s.store.UsageKindless(); err != nil {
		return fmt.Errorf("checking usage ledger kinds: %w", err)
	} else if kindless {
		roots := map[string]string{}
		var werr error
		flush := func() {
			if werr == nil && len(roots) > 0 {
				werr = s.store.FillUsageKinds(roots)
			}
			clear(roots)
		}
		if err := s.store.ForEachSnapshot(func(snap sessionSnapshot) {
			for _, span := range snap.Spans {
				if span.ParentSpanID == "" {
					roots[span.TraceID] = span.OperationName
				}
			}
			if len(roots) >= migrationBatch {
				flush()
			}
		}); err != nil {
			return fmt.Errorf("reading session archive for usage kinds: %w", err)
		}
		if flush(); werr != nil {
			return fmt.Errorf("filling usage kinds: %w", werr)
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
	openThreads := map[string]bool{}
	for _, snap := range snapshots {
		id := snap.SessionID
		sess := restoreSession(snap, s.usageTenantAttr)
		s.sessions[id] = sess
		s.order = append(s.order, id)
		if !snap.IsComplete {
			s.resetIdleTimerLocked(id)
		}
		if sess.Segmented && !sess.Closed {
			openThreads[sess.threadKey()] = true
		}
	}
	// A segment that was still open when the process stopped is closed by its thread's sweep once it is due, also
	// when it is older than the sessions restored into memory.
	if more, err := s.store.OpenThreads(); err != nil {
		logx.Warnf("session archive: listing open threads: %v", err)
	} else {
		for _, th := range more {
			openThreads[th] = true
		}
	}
	for th := range openThreads {
		s.scheduleSweepLocked(th, s.idleTimeout)
	}
	if len(snapshots) > 0 {
		logx.Infof("restored %d session(s) from archive", len(snapshots))
	}
	// An archive written before the session list was paged has no index rows yet; add them without holding up start.
	s.indexing.Add(1)
	go func() {
		defer s.indexing.Done()
		if n, err := s.store.BackfillSessionIndex(s.usageTenantAttr); err != nil {
			logx.Warnf("session archive: indexing for the session list: %v", err)
		} else if n > 0 {
			logx.Infof("session archive: indexed %d archived session(s) for the session list", n)
		}
		s.backfillRunTenants()
	}()
	return nil
}

// restoreBudgetBytes bounds the archived JSON read back into memory at start,
// newest sessions first, whatever maxMemory allows.
const restoreBudgetBytes = 16 << 20

// restoreSession rebuilds a Session from its archived snapshot, with a
// fresh extractor (see sessionSnapshot) and, for a completed session, its
// invocations recomputed from the spans.
func restoreSession(snap sessionSnapshot, tenantAttr string) *Session {
	sess := sessionHeader(snap, tenantAttr)
	if snap.IsComplete {
		// Persisted snapshots predate this converter (or were archived
		// before invocations existed) - recompute rather than leaving
		// completed sessions permanently without detail after restart.
		sess.Invocations = extractInvocationsForUI(otlp.BuildTrace(snap.SessionID, snap.Spans))
	}
	return sess
}

// snapThread is the thread an archived session belongs to: the stored one, or its own id for a session archived
// before segments existed.
func snapThread(snap sessionSnapshot) string {
	if snap.Thread != "" {
		return snap.Thread
	}
	return snap.SessionID
}

// sessionHeader rebuilds a Session from its snapshot without its invocations. A session archived before segments
// existed, or from a sender that declared no thread, is the first part of its own thread, closed while it is
// complete; a later span under its id reopens it.
func sessionHeader(snap sessionSnapshot, tenantAttr string) *Session {
	traceIDs := make(map[string]struct{}, len(snap.TraceIDs))
	for _, tid := range snap.TraceIDs {
		traceIDs[tid] = struct{}{}
	}
	tenant := snap.TenantKey
	if tenant == "" && snap.Sender != "" {
		tenant = tenantKey(snap.Sender, traceTenantLabel(snap.Spans, tenantAttr))
	}
	sess := &Session{
		ID:             snap.SessionID,
		tenantKey:      tenant,
		Segmented:      snap.Segmented,
		Thread:         snapThread(snap),
		Part:           snap.Part,
		Closed:         snap.Closed,
		ClosedAt:       snap.ClosedAt,
		Sender:         snap.Sender,
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
	if !snap.Segmented {
		// Not a segment: complete means closed, and a late span reopens it. A row split by an earlier version keeps
		// the thread and part it was stored with.
		if sess.Part == 0 {
			sess.Part = 1
		}
		sess.Closed, sess.ClosedAt = snap.IsComplete, nil
		if snap.IsComplete {
			sess.ClosedAt = snap.CompletedAt
		}
	}
	sess.recomputeBounds()
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
		logx.Warnf("session archive: loading %s: %v", sessionID, err)
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
					logx.Errorf("session archive: periodic snapshot failed: %v", err)
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
	// A purge holds the same lock, so a snapshot taken before it cannot be written back after it.
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
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
	s.pruneThreadsLocked()
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
		logx.Errorf("session archive: prune failed: %v", err)
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
		logx.Errorf("session archive: compact failed: %v", err)
	}
	logx.Infof("session archive: pruned %d session(s) older than %s and compacted", n, s.sessionRetention)
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
			logx.Errorf("usage ledger: prune failed: %v", err)
		} else if n > 0 {
			logx.Infof("usage ledger: pruned %d row(s) older than %s", n, s.usageRetention)
		}
	}
	return nil
}

// indexingCloseWait bounds how long Close waits for the session-index backfill before it flushes and closes anyway.
const indexingCloseWait = 5 * time.Second

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
	for _, t := range s.sweepTimers {
		t.Stop()
	}
	s.mu.Unlock()

	if s.store == nil {
		return nil
	}
	// A backfill that finishes soon is waited for; on a large archive it can outlast the pod's grace period, and
	// the final snapshot must not be lost to it.
	indexed := make(chan struct{})
	go func() {
		s.indexing.Wait()
		close(indexed)
	}()
	select {
	case <-indexed:
	case <-time.After(indexingCloseWait):
		logx.Warnf("session archive: indexing for the session list still running after %s; closing without it", indexingCloseWait)
	}
	if err := s.flushPersist(); err != nil {
		logx.Errorf("session archive: final snapshot failed: %v", err)
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

// IngestFor stores an export sent by sender ("" for the shared token or an open receiver). A named sender's sessions
// are called "<sender>:<tenant>:<name>" ("<sender>:<name>" without a label), so neither two senders nor two tenants
// meet in one session, and spans for a session owned by another sender are dropped. The tenant label on the spans is the sender's own and is kept as sent: it only ever names a
// tenant inside the sender's namespace.
func (s *SessionStore) IngestFor(sender string, body map[string]any) int {
	return s.IngestBound(sender, "", body)
}

// IngestBound is IngestFor for a token bound to one tenant of its sender: every span of the export belongs to that
// tenant, whatever label it carries, so the token cannot write into another tenant.
func (s *SessionStore) IngestBound(sender, tenant string, body map[string]any) int {
	// Decoding a large export is the expensive part and touches no store state, so it happens before the lock is
	// taken: a big body must not stall every reader of the store while it is parsed.
	type parsedResource struct {
		resourceAttrs                          map[string]any
		sessionName, conversationID, evalSetID string
		thread                                 string
		traces                                 []*tracepkg.Trace
	}
	var parsed []parsedResource
	for _, rs := range asSlice(body["resourceSpans"]) {
		resource := asMap(asMap(rs)["resource"])
		p := parsedResource{resourceAttrs: otlp.DecodeAttributes(asSlice(resource["attributes"]))}
		p.sessionName, _ = p.resourceAttrs[sessionNameAttr].(string)
		p.conversationID, _ = p.resourceAttrs[genAIConversationID].(string)
		p.evalSetID, _ = p.resourceAttrs[evalSetIDAttr].(string)
		p.thread, _ = p.resourceAttrs[threadAttr].(string)
		p.traces = otlp.ParseExportRequest(map[string]any{"resourceSpans": []any{rs}})
		if sender != "" && tenant != "" {
			attr := cmp.Or(s.usageTenantAttr, DefaultTenantAttr)
			for _, tr := range p.traces {
				for _, sp := range tr.AllSpans {
					sp.Tags[attr] = tenant
				}
			}
		}
		parsed = append(parsed, p)
	}

	// The names a named sender gives its tenants; an anonymous push (no sender) cannot name anyone's tenant.
	var tenantNames map[string]string
	if s.tenantNamer != nil && sender != "" {
		for _, p := range parsed {
			for _, tr := range p.traces {
				if n := traceTenantName(tr.AllSpans); n != "" {
					if tenantNames == nil {
						tenantNames = map[string]string{}
					}
					tenantNames[tenantKey(sender, traceTenantLabel(tr.AllSpans, s.usageTenantAttr))] = n
				}
			}
		}
	}

	var out closings
	s.mu.Lock()
	ingested := 0
	for _, p := range parsed {
		resourceAttrs, sessionName, conversationID, evalSetID := p.resourceAttrs, p.sessionName, p.conversationID, p.evalSetID

		for _, tr := range p.traces {
			// A sender that declares a thread (ego.thread) is an always-on agent: its name is the thread, whatever the
			// session id says, and its sessions are segments of it. Every other sender's session is its own.
			declared := p.thread != ""
			name := p.thread
			if name == "" {
				name = sessionName
			}
			if name == "" {
				name = conversationID
			}
			if name == "" {
				name = traceConversationID(tr)
			}
			if name == "" {
				name = fmt.Sprintf("otlp-%s", shortID(tr.TraceID))
			}
			if sender != "" {
				// Sender and tenant label are part of the identity: the same session name from two senders, or for
				// two tenants of one sender, is two sessions.
				if label := traceTenantLabel(tr.AllSpans, s.usageTenantAttr); label != "" {
					name = sender + ":" + label + ":" + name
				} else {
					name = sender + ":" + name
				}
			}

			// One sender token must not exhaust the pod for every tenant: a new session or tenant past its sender's
			// limits is refused, the sessions and tenants it already has keep working.
			if sender != "" && s.overSenderQuotaLocked(sender, tenantKey(sender, traceTenantLabel(tr.AllSpans, s.usageTenantAttr)), name) {
				continue
			}
			var session *Session
			var isNew, owned bool
			if declared {
				session, isNew, owned = s.placeTraceLocked(name, tr, evalSetID, sender, resourceAttrs, &out)
			} else {
				session, isNew, owned = s.placePlainLocked(name, tr.TraceID, evalSetID, sender, resourceAttrs)
			}
			if !owned {
				logx.Warnf("ingest: dropped spans for session %q: it belongs to another sender", name)
				continue
			}
			// A session belongs to one tenant of its sender, the one its first trace named: a trace of another tenant
			// that lands on the same id is refused, as another sender's is.
			if sender != "" {
				want := tenantKey(sender, traceTenantLabel(tr.AllSpans, s.usageTenantAttr))
				if isNew {
					session.tenantKey = want
				} else if session.tenantKey != "" && session.tenantKey != want {
					logx.Warnf("ingest: dropped spans for session %q: it belongs to another tenant", name)
					continue
				}
			}
			arrived := s.now()

			// session_started must reach clients before any update event for this session, so the UI has a
			// session to attach those events to (LiveStreamingView.tsx's user_input/etc. handlers no-op on an
			// unknown sessionId). It is sent after this batch's spans are in, un.
			var updates []sessionScopedUpdate

			for _, span := range tr.AllSpans {
				// Segments close on size between traces (segmentDueAt), so theirs only stops one runaway trace; any
				// other session is capped, and the spans past it are dropped.
				if len(session.Spans) >= maxSpansPerSession && !session.Segmented ||
					len(session.Spans) >= hardMaxSpansPerSegment {
					break
				}
				session.Spans = append(session.Spans, span)
				session.noteSpan(span)
				session.lastArrival = arrived
				s.markChangedLocked(session)
				ingested++
				if s.store != nil {
					if row, ok := usageRowFromSpan(span, session.ID, s.usageTenantAttr, session.Sender, traceTenantLabel(tr.AllSpans, s.usageTenantAttr)); ok {
						row.Thread = session.threadKey()
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

				// Both timers ultimately call completeSession. A closed segment is final: it is already complete and
				// stays so.
				if span.ParentSpanID == "" {
					session.HasRootSpan = true
				}
				if !session.sealed() {
					s.resetIdleTimerLocked(session.ID)
					if span.ParentSpanID == "" {
						s.scheduleCompletionLocked(session.ID)
					}
				}
			}
			if session.sealed() {
				// A late span of a closed segment's own trace: its invocations are rebuilt, the segment is neither
				// reopened nor queued for scoring again.
				session.Invocations = extractInvocationsForUI(otlp.BuildTrace(session.ID, session.Spans))
			} else if session.Segmented {
				s.scheduleSweepLocked(name, s.segIdle)
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
	s.mu.Unlock()
	s.finish(out)
	for key, name := range tenantNames {
		s.tenantNamer(key, name)
	}
	return ingested
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
// idle/completion timer callback, so it acquires s.mu itself rather than assuming it's held. For a segment of a
// declared thread, complete is only the quiet state the UI shows: online evaluation hears about it when it is closed
// (closeSegmentLocked). Any other session is final when it completes, and is queued for scoring here; a late span
// reopens it, and OnlineEvalExists keeps it from being scored twice.
func (s *SessionStore) completeSession(sessionID string) {
	s.mu.Lock()
	session, ok := s.sessions[sessionID]
	if !ok || session.IsComplete {
		s.mu.Unlock()
		return
	}
	c := s.completeLocked(session)
	out := closings{completions: []completion{c}}
	if !session.Segmented {
		out.closed = []string{sessionID}
	}
	s.mu.Unlock()
	s.finish(out)
}

type sessionScopedUpdate struct {
	SessionID string `json:"sessionId"`
	incremental.Update
}

// List returns every session's summary, oldest first.
// TenantCounts is how many sessions each tenant key has: the whole archive when there is one, else what is in
// memory.
func (s *SessionStore) TenantCounts() map[string]int {
	if s.store != nil {
		if counts, err := s.store.TenantCounts(); err == nil {
			return counts
		} else {
			logx.Warnf("session archive: counting tenants: %v", err)
		}
	}
	counts := map[string]int{}
	for _, sum := range s.List() {
		for _, k := range sum.Tenants {
			counts[k]++
		}
	}
	return counts
}

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

// SenderOf returns the sender that owns a session ("" for the shared token, an open receiver or an unknown id).
func (s *SessionStore) SenderOf(sessionID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if session, ok := s.lookupLocked(sessionID); ok {
		return session.Sender
	}
	return ""
}
