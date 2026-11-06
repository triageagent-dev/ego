# ego — Latest Commits & Features Reference (v0.4.19 – v0.4.30)

## 1. Release Timeline Overview

Between **October 30, 2026** and **November 1, 2026**, the `ego` repository progressed from `v0.4.19` to **`v0.4.30`** (HEAD: [`d31e892`](file:///Users/den/code/triageagent-dev/ego)). These releases delivered significant improvements in **AI agent autonomy over MCP**, **multi-turn evaluation accuracy**, **Kubernetes security & token rotation**, **storage reliability**, and **voice/conversational trace attribution**.

```
  v0.4.19 ──► v0.4.20/21 ──► v0.4.22 ──► v0.4.23/24 ──► v0.4.25/26 ──► v0.4.27/28 ──► v0.4.29/30
     │           │            │             │              │              │              │
 Delegate    Storage Alert   MPTCP       Turn Window    Multi-Turn    Projected SA    Tenant Status
  Grants     & Fast Startup  CVE Fix     Attribution    & Voice Fix    & list_tenants  & Note Enter
```

---

## 2. Commit Log Reference

| Hash | Date | Tag | Commit Title | Key Modules |
|---|---|---|---|---|
| [`d31e892`](file:///Users/den/code/triageagent-dev/ego) | 2026-11-01 | `v0.4.30` | `fix(ui): a note typed after the thumb is saved on Enter` | `ui/src/components/streaming` |
| [`e1f5156`](file:///Users/den/code/triageagent-dev/ego) | 2026-11-01 | `v0.4.29` | `fix(online): serve the status to a tenant-limited role, narrowed to its tenants` | `internal/api/online.go`, `ui/src/components/quality` |
| [`75f323e`](file:///Users/den/code/triageagent-dev/ego) | 2026-11-01 | `v0.4.28` | `feat(mcp): list_tenants tool so an agent finds a tenant's key by name before granting access` | `internal/mcptools`, `internal/api/tenantview.go` |
| [`a9dcf66`](file:///Users/den/code/triageagent-dev/ego) | 2026-11-01 | `v0.4.27` | `feat(decision): a token file sent as the bearer for the jev_* metrics, a projected ServiceAccount token in the chart` | `internal/decision`, `charts/ego` |
| [`0615cb2`](file:///Users/den/code/triageagent-dev/ego) | 2026-11-01 | `v0.4.26` | `fix(adk): a voice round that carries both the words and the answer is one turn, not dropped` | `internal/adk/genai_converter.go` |
| [`20f75d8`](file:///Users/den/code/triageagent-dev/ego) | 2026-11-01 | `v0.4.25` | `fix(online): a grown session is scored again, a pending turn is not rated, the judges see the earlier turns and the called tools` | `internal/api/evaluate.go`, `internal/judge` |
| [`ed7353b`](file:///Users/den/code/triageagent-dev/ego) | 2026-11-01 | `v0.4.24` | `fix(chart): the helm.sh/chart label of a chart pulled by digest` | `charts/ego/templates/_helpers.tpl` |
| [`3e0fc9b`](file:///Users/den/code/triageagent-dev/ego) | 2026-10-31 | `v0.4.23` | `fix: a tool call belongs to the round that consumed it; mint-token --help no longer prints the signing secret` | `internal/adk`, `cmd/ego/auth.go` |
| [`514dd92`](file:///Users/den/code/triageagent-dev/ego) | 2026-10-30 | `v0.4.22` | `fix: every listener is plain TCP, godebug multipathtcp=0 (CVE-2026-98432) (#36)` | `go.mod` |
| [`1cb0e76`](file:///Users/den/code/triageagent-dev/ego) | 2026-10-30 | `v0.4.21` | `fix(api): a start no longer reads the whole archive for ledger rows that can never get a kind; the archive alert percent is a parameter` | `internal/api/sessions.go`, `charts/ego` |
| [`02d65ce`](file:///Users/den/code/triageagent-dev/ego) | 2026-10-30 | — | `feat(ui): a banner while the session archive is failing or its volume is running out` | `ui/src/components/ArchiveBanner.tsx` |
| [`6f2545e`](file:///Users/den/code/triageagent-dev/ego) | 2026-10-30 | `v0.4.20` | `feat(api): a failed archive write or a volume running out is announced to the Discord sinks and shown in /api/health` | `internal/api/storagewatch.go`, `internal/online/notify.go` |
| [`975742f`](file:///Users/den/code/triageagent-dev/ego) | 2026-10-30 | `v0.4.19` | `feat(api): a delegate grant <sender>/= follows the call's tenant label into another sender (#35)` | `internal/api/delegate.go`, `cmd/ego/auth.go` |

---

## 3. Deep Dive into New Features & Fixes

### 3.1. Agent Autonomy & MCP Registry Enhancements

#### `list_tenants` MCP Tool (`v0.4.28`)
- **Problem**: When an agent wanted to grant access to a workspace via `set_role_binding`, human users often referred to tenants by their human-readable display names (e.g., `"Acme Production"`). The `set_role_binding` tool strictly required canonical tenant keys (e.g., `"core/acme"` or `"<sender>/*"`), leaving the agent unable to resolve the parameter.
- **Solution**: Added the `list_tenants` tool to both stdio and HTTP MCP registries ([`internal/mcptools/registry.go`](file:///Users/den/code/triageagent-dev/ego/internal/mcptools/registry.go)):
  - Accepts an optional `query?` string matching against keys or display names.
  - Returns tenant keys, senders, labels, display names, and session counts.
  - Automatically restricted to tenants visible under the caller's role (refused with 403 to agent-scoped roles).
- **Usage**:
  ```json
  // Request
  { "method": "tools/call", "params": { "name": "list_tenants", "arguments": { "query": "acme" } } }
  // Response
  [ { "key": "core/acme", "sender": "core", "label": "acme", "name": "Acme Corp", "sessions": 142 } ]
  ```

#### Cross-Sender Delegate Grants (`<sender>/=`, `v0.4.19`)
- **Problem**: In multi-tenant worker environments, traces for a single workspace arrive from multiple senders (e.g. the primary `core` pod and secondary spot worker pods). Previously, granting access across multiple senders required issuing multiple tokens or overly permissive wildcards.
- **Solution**: Introduced the delegate grant syntax `<sender>/=` in [`internal/api/delegate.go`](file:///Users/den/code/triageagent-dev/ego/internal/api/delegate.go). When a token is granted `core/acme` and `worker/=`, any query targeting `core/acme` automatically reads both `core/acme` and `worker/acme`, without exposing other tenants of `worker`.

---

### 3.2. Evaluation Judge Accuracy & Grounding

#### Multi-Turn Context Grounding for Hallucination Judges (`v0.4.25`)
- **Problem**: In multi-turn chat sessions, the hallucination detection judge (`hallucinations_v1`) evaluated only the current turn's tool outputs. When the agent referenced facts stated in an earlier turn ("as mentioned previously, your order was shipped on Monday"), the judge flagged the statement as `unsupported` or `contradictory`.
- **Solution**: The hallucination judge prompt is now supplied with the preceding 6 conversation turns ([`internal/api/evaluate.go`](file:///Users/den/code/triageagent-dev/ego/internal/api/evaluate.go)). Prior turns provide explicit grounding context, eliminating false-positive hallucination alerts.

#### Available Tools Context for Rubric Judges (`v0.4.25`)
- **Problem**: OTel traces capture which tools were executed, but not the tool schemas offered to the model. Rubric quality judges evaluating prompt compliance often produced false critiques such as: *"Agent has no tools available, so calling payment_api was inappropriate."*
- **Solution**: Rubric judges are now explicitly instructed that any tool called by the agent was part of its available toolset, focusing evaluation on usage appropriateness rather than tool existence.

#### Re-Scoring of Grown Sessions (`v0.4.25`)
- **Problem**: A completed session that received a late user message reopened and executed additional turns. However, because it had already been scored, the new turns remained unrated.
- **Solution**: The sampler fingerprint now incorporates the session turn count. When a session reopens and grows, it is re-queued and evaluated for its new turns.

#### Skip Rating for Pending Acknowledgment Turns (`v0.4.25`)
- **Problem**: Conversational agents frequently emit intermediate acknowledgment turns (e.g., *"Looking into that now..."*) while asynchronously awaiting tool execution. Rating these as final responses caused artificial drops in response quality scores.
- **Solution**: Root spans can be annotated with the attribute `ego.turn.pending = true`. Quality metrics (`rubric_based_*`, `jev_*`) automatically skip evaluating these turns, reporting status `SKIPPED` without penalizing the agent.

---

### 3.3. Conversational & Voice Turn Attribution

#### Two-Sided Turn Preservation (`v0.4.26`)
- **Problem**: In streaming voice interactions (such as Gemini Live or Google ADK), when an answer arrives in the exact same model round that received the user's speech, the span carries both `input.value` and `output.value`. The converter previously assumed single-sided spans and dropped these rounds, resulting in lost turns in the UI.
- **Solution**: In [`internal/adk/genai_converter.go`](file:///Users/den/code/triageagent-dev/ego/internal/adk/genai_converter.go), two-sided spans close any prior pending turn and immediately form a complete conversational turn, retaining all associated tool calls.

#### Tool-Call Round Binding (`v0.4.23`)
- **Problem**: Voice sessions cut turns at the user's speech. Autonomous model tool calls executed between the assistant's verbal reply and the next user speech were misattributed to the subsequent turn's tool set.
- **Solution**: Tool calls are now strictly bound to the model round that consumed them (the first LLM span at or after the tool execution), correctly aligning execution timing and token costs.

---

### 3.4. Kubernetes & Infrastructure Security

#### Projected ServiceAccount Token Files for Decision Metrics (`v0.4.27`)
- **Problem**: Decision evaluation metrics (`jev_*`) contacting external decision gateways required static API keys. Static keys stored in Kubernetes secrets cannot be automatically rotated without pod restarts.
- **Solution**: Added support for `--jev-token-file` / `EGO_DECISION_TOKEN_FILE` ([`internal/decision/client.go`](file:///Users/den/code/triageagent-dev/ego/internal/decision/client.go)).
  - The token file is re-read on every evaluation request, allowing seamless rotation of Kubernetes projected ServiceAccount tokens.
  - Helm chart support added via `decision.token.audience`, mounting tokens at `/var/run/ego/decisions/token`.
  - Security checks prevent passing both a token file and a static key, and restrict unencrypted HTTP usage to loopback or cluster `.svc` endpoints.

#### MPTCP Security Mitigation (CVE-2026-98432, `v0.4.22`)
- **Problem**: Go 1.24+ automatically attempts to create Multipath TCP (MPTCP) sockets on `net.Listen`. A kernel use-after-free vulnerability in IPv4 TCP setup (CVE-2026-98432, CVSS 9.8) can be triggered through MPTCP listeners on unpatched Linux kernels.
- **Solution**: Disabled Multipath TCP across all `ego` binaries at build time by embedding `//go:debug multipathtcp=0` in `go.mod`. Verified via `go version -m bin/ego`.

#### Secret Redaction in CLI Help (`v0.4.23`)
- In `cmd/ego/auth.go`, `mint-token --help` previously printed the default signing secret read from `EGO_SESSION_SECRET`. Flags now read environment defaults after argument parsing to avoid leaking secrets in terminal logs.

---

### 3.5. Storage Reliability & Performance

#### Storage Free-Space Watcher & Discord Alerts (`v0.4.20`, `v0.4.21`, `02d65ce`)
- **Problem**: When the session SQLite PVC ran out of disk space, database writes failed silently while the in-memory server continued running. On pod restart, unsaved sessions were lost.
- **Solution**: Added [`internal/api/storagewatch.go`](file:///Users/den/code/triageagent-dev/ego/internal/api/storagewatch.go):
  - Checks filesystem free space against `--archive-alert-percent` (default 10%, configured via Helm `persistence.alertPercent`).
  - Broadcasts critical alerts to configured Discord webhooks.
  - Reflects degraded storage status in `GET /api/health`.
  - Displays a persistent, real-time warning banner across the web UI (`ArchiveBanner.tsx`).

#### Fast Startup Optimization (`v0.4.21`)
- **Problem**: Startup reconciliation scanned the entire historical SQLite archive to find root spans for kindless token ledger rows, taking multiple minutes of 100% CPU on large archives.
- **Solution**: Updated [`internal/api/sessions.go`](file:///Users/den/code/triageagent-dev/ego/internal/api/sessions.go) to query only the specific session IDs associated with unclassified ledger rows, reducing startup time from minutes to milliseconds.

#### Multi-Tenant Quality Status Isolation (`v0.4.29`)
- `GET /api/online/status` previously rejected tenant-limited users with 403 Forbidden. It now serves a sanitized status strip showing only the caller's tenant-specific spend, open alerts, and scored sessions, omitting shared global queue metrics.

#### UI Label Note Save on Enter (`v0.4.30`)
- When manually calibrating sessions with thumbs up/down, notes typed into the text field after clicking the thumb rating were previously lost. Pressing `Enter` now immediately persists the note as an updated label revision.

---

## 4. Configuration & Deployment Reference

### New Helm Values (`charts/ego/values.yaml`)

```yaml
# Decision model authentication via Kubernetes Projected ServiceAccount Token
decision:
  token:
    # Set to your gateway's expected audience to mount a projected token at /var/run/ego/decisions/token
    audience: "https://decision-gateway.internal"

persistence:
  # Trigger storage warnings (Discord, /api/health, UI banner) when free disk drops below this %
  alertPercent: 10

# Go runtime memory limit protecting against cgroup OOM kills
goMemLimit: 400MiB
```

### New Environment Variables & Flags

| Variable / Flag | Default | Description |
|---|---|---|
| `EGO_DECISION_TOKEN_FILE` / `--jev-token-file` | `""` | Path to a file containing a bearer token for `jev_*` decision metrics (re-read on every request). |
| `EGO_ARCHIVE_ALERT_PERCENT` / `--archive-alert-percent` | `10.0` | Free space percentage threshold below which storage alerts are triggered. |
| `ego.turn.pending` (OTel Span Attribute) | `false` | When set on a root span, skips evaluating incomplete conversational acknowledgment turns. |
