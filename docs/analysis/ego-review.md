# ego — Comprehensive Architecture, Code & Performance Review

## 1. Executive Summary & Core Purpose

**ego** is a specialized, production-grade observability and recursive self-improvement (RSI) platform for AI agents. Rather than re-executing agents or relying on synthetic benchmarks, ego evaluates live agents by scoring the OpenTelemetry (OTel) traces they emit during real-world execution.

When an agent's quality metrics decline, ego:
1. **Samples** completed sessions (prioritizing errors, outliers, and latency spikes within a daily token budget).
2. **Scores** behavior against deterministic rules (trajectory match, ROUGE) and LLM judges (hallucination detection, rubrics, decision rationales).
3. **Detects** statistically significant regressions using Welch's t-test with automated version/model attribution.
4. **Diagnoses** failure modes with concrete session evidence excerpts.
5. **Serves** findings to the agent over native **Model Context Protocol (MCP)** tools.
6. **Tracks** registered agent self-corrections and statistically **verifies** whether the fix held.

```
+---------------------------------------------------------------------------------------------------+
|                                      The ego RSI Loop Architecture                                 |
+---------------------------------------------------------------------------------------------------+
|  OTel / Live Traces  ──>  Sampling & Budgets  ──>  Deterministic / LLM Judges  ──>  Regression Z-Test  |
|                                                                                               |   |
|                                                                                               v   |
|  Verification  <──  Registered Corrections  <──  MCP Prompts / Tools  <──  Diagnostic Findings   |
+---------------------------------------------------------------------------------------------------+
```

Packaged as a single static Go binary embedding a React/TypeScript UI, ego operates as an OTLP receiver (gRPC `:4317` and HTTP `:4318`), REST API, Web UI, and an MCP server (streamable HTTP `POST /mcp` and stdio).

---

## 2. Architectural Highlights & Strengths

### 2.1. Dual-Transport Unified MCP Architecture
- **Location**: [`internal/mcptools/registry.go`](file:///Users/den/code/triageagent-dev/ego/internal/mcptools/registry.go), [`internal/mcptools/backend.go`](file:///Users/den/code/triageagent-dev/ego/internal/mcptools/backend.go)
- **Design**: Tools are declared exactly once in `internal/mcptools` with explicit behavior hints (`readOnly`, `destructive`, `idempotent`, `openWorld`). Two backends satisfy the interface:
  1. `in-process mcpBackend` ([`internal/api/mcpbackend.go`](file:///Users/den/code/triageagent-dev/ego/internal/api/mcpbackend.go)): Invoked directly by `serve` on `POST /mcp`, using the request's `RoleInfo` context for tenant/agent filtering.
  2. `REST backend` ([`internal/mcptools/rest.go`](file:///Users/den/code/triageagent-dev/ego/internal/mcptools/rest.go)): Used by `./bin/ego mcp` over stdio to call a remote ego instance.
- **RSI Prompt**: The self-correction skill (`skills/ego-rsi/SKILL.md`) is embedded via `//go:embed` and served as the standard MCP prompt `rsi_loop`, allowing agents to load their self-improvement protocol without repo dependencies.

### 2.2. Robust Ingestion Defenses (Zip & Protobuf Bomb Protection)
- **Location**: [`internal/api/grpc.go:60-75`](file:///Users/den/code/triageagent-dev/ego/internal/api/grpc.go#L60-L75), [`internal/otlp/wirecheck.go`](file:///Users/den/code/triageagent-dev/ego/internal/otlp/wirecheck.go), [`internal/api/limits.go`](file:///Users/den/code/triageagent-dev/ego/internal/api/limits.go)
- **Design**: Unauthenticated OTLP ingestion is strictly protected against amplification and memory exhaustion attacks:
  - `maxOTLPBody = 16 MiB` (compressed) and `maxOTLPDecoded = 64 MiB` (decompressed).
  - `denseGuardCodec`: In gRPC, exports holding more nested messages than a real trace can possibly contain (ratio $>1$ element per 4 bytes or $>1.2\text{M}$ total) are rejected with `ResourceExhausted` *before protobuf unmarshalling occurs*.
  - HTTP JSON payloads are checked with `otlp.WireCheck` ($>1$ element per 8 bytes or $>2\text{M}$ elements rejected).

### 2.3. Universal Trace Ingestion & Semantic Adaptation
- **Location**: [`internal/adk/genai_converter.go`](file:///Users/den/code/triageagent-dev/ego/internal/adk/genai_converter.go), [`internal/otlp/claudecode.go`](file:///Users/den/code/triageagent-dev/ego/internal/otlp/claudecode.go), [`internal/otlp/langsmith.go`](file:///Users/den/code/triageagent-dev/ego/internal/otlp/langsmith.go)
- **Design**: Ingests traces across diverse conventions without requiring proprietary SDKs:
  - **Google ADK**: Parses `gcp.vertex.agent.*` attributes.
  - **OTel GenAI & OpenInference**: Matches `gen_ai.*` and `openinference.*` span attributes.
  - **Voice & Realtime Sessions**: Seamlessly stitches single-sided OpenInference LLM spans into conversational turns.
  - **Claude Code**: Extracts `claude_code.*` spans, accounting for prompt cache reads and writes separately in usage pricing.
  - **LangSmith / LangGraph**: Transparently translates `langsmith.span.kind` spans into standard GenAI turns.

### 2.4. Statistical Rigor in Regression Detection
- **Location**: [`internal/online/detector.go:50-95`](file:///Users/den/code/triageagent-dev/ego/internal/online/detector.go#L50-L95)
- **Design**: Implements Welch's two-sample t/z test ($Z > 2.5$) with a variance floor ($se \ge 0.01$) to prevent false alarms on low-variance baselines. Automatic attribution detects if a candidate model or version accounts for $\ge 50\%$ of the regressed window and $< 10\%$ of baseline.

### 2.5. Multi-Tenant Role Isolation & Delegated Scopes
- **Location**: [`internal/api/roles.go`](file:///Users/den/code/triageagent-dev/ego/internal/api/roles.go), [`internal/api/delegate.go`](file:///Users/den/code/triageagent-dev/ego/internal/api/delegate.go)
- **Design**: Roles (`admin`, `member`, `viewer`, `none`) support hierarchical scoping by agent name (`Agents: []string`) and tenant keys (`Tenants: []string`). Delegate grants (`<sender>/=`) allow cross-sender visibility across related workspaces while strictly preventing privilege escalation.

### 2.6. Resilient Non-Blocking Event Streaming
- **Location**: [`internal/api/sse.go:70-130`](file:///Users/den/code/triageagent-dev/ego/internal/api/sse.go#L70-L130)
- **Design**: SSE and WebSocket hubs buffer 1,024 events per subscriber. Channel writes are non-blocking (`select ... default`); slow clients drop individual events and receive a `resyncEvent` when their buffer frees up, guaranteeing that a laggy UI never stalls trace ingestion pipelines.

---

## 3. Findings & Improvement Opportunities

### 3.1. Priority 1 (High): Lack of Transactions in SQLite Multi-Table Mutations
- **Location**: [`internal/api/sqlitestore.go:244-248`](file:///Users/den/code/triageagent-dev/ego/internal/api/sqlitestore.go#L244-L248) and [`internal/api/sqlitestore.go:375-385`](file:///Users/den/code/triageagent-dev/ego/internal/api/sqlitestore.go#L375-L385)
- **Issue**:
  In `Delete`:
  ```go
  func (s *SQLiteStore) Delete(sessionID string) error {
      if _, err := s.db.Exec("DELETE FROM session_index WHERE session_id = ?", sessionID); err != nil {
          return err
      }
      _, err := s.db.Exec("DELETE FROM sessions WHERE session_id = ?", sessionID)
      return err
  }
  ```
  And in `PruneSessions`:
  ```go
  if _, err := s.db.Exec("DELETE FROM session_index WHERE session_id IN (SELECT session_id FROM sessions WHERE updated_at < ?)", at); err != nil {
      return 0, err
  }
  res, err := s.db.Exec("DELETE FROM sessions WHERE updated_at < ?", at)
  ```
  Both operations execute two separate statements without wrapping them in an explicit SQLite transaction (`s.db.Begin()`). If the process crashes, the disk fills, or a timeout occurs after the first `Exec`, `session_index` and `sessions` will permanently fall out of sync.
- **Impact**: Index corruption and orphan rows in SQLite on unexpected termination.
- **Fix**: Wrap in an explicit transaction:
  ```go
  tx, err := s.db.Begin()
  if err != nil { return err }
  defer tx.Rollback()
  if _, err := tx.Exec("DELETE FROM session_index WHERE session_id = ?", sessionID); err != nil {
      return err
  }
  if _, err := tx.Exec("DELETE FROM sessions WHERE session_id = ?", sessionID); err != nil {
      return err
  }
  return tx.Commit()
  ```

---

### 3.2. Priority 1 (High): Uncapped SQLite Connection Pool & Write Contention
- **Location**: [`internal/api/sqlitestore.go:61`](file:///Users/den/code/triageagent-dev/ego/internal/api/sqlitestore.go#L61)
- **Issue**:
  `sql.Open("sqlite", sqliteDSN(path))` leaves Go's `database/sql` default connection pool settings intact (`MaxOpenConns = 0`, unlimited).
  Although SQLite runs in WAL mode with `busy_timeout(5000)`, SQLite is fundamentally a **single-writer** engine. When high-volume OTLP trace ingestion, background session index backfills, usage ledger insertions, and findings sweeps run concurrently, multiple goroutines acquire separate connections from the pool and queue for the write lock. Under heavy bursts, the 5,000 ms busy timeout can be exceeded, returning `SQLITE_BUSY: database is locked`.
- **Impact**: Periodic 500 errors on trace ingestion or background jobs during peak traffic.
- **Fix**:
  Set pool limits in `NewSQLiteStore`:
  ```go
  // In WAL mode, allow multiple concurrent readers but bound max open connections.
  db.SetMaxOpenConns(16)
  db.SetMaxIdleConns(8)
  db.SetConnMaxLifetime(30 * time.Minute)
  ```
  Or route write operations through a dedicated single-connection write transaction queue.

---

### 3.3. Priority 2 (Medium): Heavy Memory Overhead in `SessionSummary` via `Invocations`
- **Location**: [`internal/api/sessions.go:135-185`](file:///Users/den/code/triageagent-dev/ego/internal/api/sessions.go#L135-L185), [`internal/api/segments.go:535-540`](file:///Users/den/code/triageagent-dev/ego/internal/api/segments.go#L535-L540)
- **Issue**:
  When a session completes, `completeLocked` converts the trace tree into `session.Invocations` (`extractInvocationsForUI`).
  Each `InvocationDTO` holds full user messages, model responses, and tool call parameters/outputs.
  In `summarize()`:
  ```go
  var invocations []InvocationDTO
  if s.IsComplete && len(s.Invocations) > 0 {
      invocations = s.Invocations
  }
  ```
  `SessionSummary` includes the entire invocation tree.
  With default `sessionMemory: 200`, keeping 200 completed sessions in RAM—each with full invocation payloads—can consume hundreds of megabytes of heap.
  Furthermore, `GET /api/streaming/sessions` (without `?id=`) serializes these full invocation lists across all sessions unless specifically paged.
- **Impact**: Inflated resident heap and high JSON serialization overhead on session listing endpoints.
- **Fix**:
  Omit `Invocations` from unpaged/summary listings (`SessionSummary.Invocations = nil`). Only hydrate `Invocations` when querying a specific session via `?id=<session-id>` or when requesting full trace details via `GET /api/streaming/get-trace`.

---

### 3.4. Priority 2 (Medium): Unbounded `c.seen` Map Growth in Scoped SSE Clients
- **Location**: [`internal/api/sse.go:120-135`](file:///Users/den/code/triageagent-dev/ego/internal/api/sse.go#L120-L135)
- **Issue**:
  For scoped clients (`c.scoped == true`), `c.seen` is instantiated as `seen: map[string]bool{}`. Every session broadcast adds an entry:
  ```go
  if !c.seen[sessionID] {
      c.seen[sessionID] = true
      ...
  }
  ```
  Entries in `c.seen` are never purged when sessions complete, expire, or are pruned from the store. In production dashboards where browser tabs remain open for days, `c.seen` monotonically expands.
- **Impact**: Slow, unbounded heap growth for persistent SSE connections.
- **Fix**:
  Replace `c.seen` with an LRU cache or ring buffer capped at e.g. 5,000 session IDs, or clean up evicted IDs during `evictLocked`.

---

### 3.5. Priority 3 (Low): `VACUUM` Lock Contention During Daily Archive Pruning
- **Location**: [`internal/api/sqlitestore.go:390-395`](file:///Users/den/code/triageagent-dev/ego/internal/api/sqlitestore.go#L390-L395)
- **Issue**:
  `Compact()` executes:
  ```go
  if _, err := s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
      return fmt.Errorf("checkpointing WAL: %w", err)
  }
  if _, err := s.db.Exec("VACUUM"); err != nil {
      return fmt.Errorf("vacuuming database: %w", err)
  }
  ```
  `VACUUM` requires an exclusive lock on the database file. If long read queries or active OTLP trace batch writes are running, `VACUUM` will block all subsequent writes or fail with `database is locked`.
- **Impact**: Transient latency spike or error during the daily archive compaction window.
- **Fix**:
  Use `PRAGMA incremental_vacuum` with `auto_vacuum = INCREMENTAL`, or run `PRAGMA wal_checkpoint(PASSIVE)` and execute `VACUUM` with exponential backoff and timeout guards.

---

## 4. Assessment Matrix

| Category | Rating | Notes |
|---|:---:|---|
| **Architectural Design** | **Excellent (9.5/10)** | Uniquely cohesive single-binary design; shared MCP registry across stdio and HTTP; clean separation of domain models. |
| **Security & Tenancy** | **Strong (9.0/10)** | Wire-level bomb protection; fine-grained role and tenant isolation; strict HTTP security headers; non-root defaults. |
| **Statistical & RSI Loop** | **Excellent (9.5/10)** | Solid statistical foundations (Welch's t-test with variance floors); realistic state transitions; evidence attribution. |
| **Code Quality & Hygiene** | **Excellent (9.5/10)** | Zero `go vet` issues; 100% formatted with `gofmt`; clean test coverage across packages; strict type safety in UI. |
| **Storage & Concurrency** | **Good (8.0/10)** | SQLite WAL is well-suited, but needs transaction boundary fixes and explicit pool connection constraints. |

---

## 5. Summary of Recommended Actions

1. **Immediate / Safe**:
   - Wrap `Delete` and `PruneSessions` multi-statement deletes in transactions in `internal/api/sqlitestore.go`.
   - Set `db.SetMaxOpenConns(16)` and `db.SetMaxIdleConns(8)` in `NewSQLiteStore`.
2. **Performance / Scale**:
   - Strip `Invocations` from list-level `SessionSummary` objects to reduce heap and wire size.
   - Bound `hubClient.seen` in `internal/api/sse.go`.
3. **Storage Hygiene**:
   - Shift from full `VACUUM` to incremental vacuuming in SQLite to prevent exclusive lock stalls during daily prunes.
