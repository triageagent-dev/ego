# Status

What ego supports today, and what it does not.

## Supported

**Traces in**
- Files: Jaeger JSON, OTLP JSON and JSONL, auto-detected (`internal/loader`).
- Live: OTLP/HTTP (`POST /v1/traces`, gzip accepted) and OTLP/gRPC, both feeding one session store.
- Conventions: ADK (`gcp.vertex.agent.*`), OTel GenAI semantic conventions and OpenInference.
- Sessions group by `ego.session_name`, then `gen_ai.conversation.id`, then a synthetic name.

**Metrics**
- Local: `tool_trajectory_avg_score`, `response_match_score` (ROUGE-1).
- Gemini judge: `final_response_match_v2`, `hallucinations_v1`, `rubric_based_final_response_quality_v1`,
  `rubric_based_tool_use_quality_v1`; each reports its rationale and per-sentence or per-rubric verdicts.
- Vertex AI Managed Eval, with Application Default Credentials: `safety_v1`, `multi_turn_task_success_v1`,
  `multi_turn_trajectory_quality_v1`, `multi_turn_tool_use_quality_v1`.
- Jev decision model: `jev_task_resolved`, `jev_grounded`, `jev_tool_use_appropriate`, `jev_matches_reference`,
  `jev_response_kind`, and custom `jev:<key>` questions.
- Every result says whether an algorithm or an LLM judge produced it.

**Server**
- REST API and embedded UI; live feed over WebSocket (`/ws/ui-updates`) with a Server-Sent Events fallback.
- Runs (`/api/runs`): evaluations are synchronous and every run is stored.
- Eval sets: build from a golden session, save, edit, download.
- Session spans with attributes and errors; readable rendering of long prompts and answers.
- Token usage ledger (`/api/usage`) from `gen_ai.usage.*`, grouped by call kind, tenant, model and service.
- Persistence with `--session-db` (SQLite): sessions, runs, eval sets, role bindings; bounded memory
  (`--session-memory`) and retention (`--session-retention`).
- Online evaluation: sampling, per-kind evaluators, daily judge budget, regression detection, Discord, webhook
  and event notifications (`docs/online-evaluation.md`).

**Access**
- GitHub OAuth restricted to an org, signed session cookies, bearer tokens from `ego auth mint-token`, or a raw
  GitHub token validated against org membership.
- Roles `admin`, `member`, `viewer`, optionally scoped to agent names (sessions, traces, runs and the live feed;
  Tokens, Quality and eval sets are closed to a scoped role); inert when no session secret is set. Unknown team
  membership is refused, never defaulted. A session with several agents is visible to a role scoped to any one.
- OTLP receivers and `/api/health` are never gated.

**MCP** (`ego mcp`, stdio): `list_metrics`, `evaluate_traces`, `list_sessions`, `summarize_session`,
`list_runs`, `get_run_results`, `list_role_bindings`, `set_role_binding`, `delete_role_binding`.

## Not supported yet

- `/v1/logs` ingestion, for frameworks that keep message content in log records instead of span attributes.
- Performance metrics (token and latency percentiles) in the `/api/evaluate` response.
- `POST /api/streaming/evaluate-sessions` (evaluate every session against a golden one in one call); the UI
  does the same through `create-eval-set` plus `/api/evaluate`.
- Mounting the MCP server inside `serve`; it is a separate stdio process.
- Custom evaluators over a stdin/stdout JSON contract, and per-request judge credentials.
- A trace waterfall view and a span store with retention.

## Out of scope

- `per_turn_user_simulator_quality_v1`: it needs a conversation scenario (plan, persona, starting prompt) that
  only exists for live user-simulator runs. ego scores recorded traces.
- `response_evaluation_score`: it has no Vertex predefined metric to call.

`internal/eval/runner.go` is the single source of truth for both.

## Known limits

- The ROUGE-1 tokenizer does not stem and does not tokenize CJK or other unspaced scripts per character, so
  `response_match_score` runs a little lower on inflected English and is not meaningful on those scripts.
- `govulncheck` flags [GO-2026-6443](https://pkg.go.dev/vuln/GO-2026-6443), a `google.golang.org/grpc` server
  panic on a request without `:authority`, reachable through the OTLP/gRPC receiver. The only fix is an
  unreleased dev version, so `go.mod` stays on the latest stable release. Bump to the first stable release
  that carries the fix.
