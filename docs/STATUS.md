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
- Tenants (`senders.go`, `ingestauth.go`, `tenantview.go`): a token identifies a sender, a sender sends many tenants labelled on its spans, and a tenant is `<sender>/<label>`. Grants (role bindings, `mint-token --tenants`) name keys, `<sender>/*` or `*`; `--default-role none` gives nobody access without a grant; the UI has a tenant switcher (`/api/tenants`, `?tenant=` narrows usage and quality); an admin can give a tenant key a display name (`/api/admin/tenant-names`, `tenantnames.go`). Tenant-limited roles get no runs, eval sets, budget status or evaluations.
- Interactive evaluation is bounded: at most 4 run at once, 50 trace files, 20 evaluators and 8 judge samples per request, one daily judge-token cap shared by all callers (429 when used up), 8192 output tokens per judge call. Vertex managed evaluation and Jev report no token counts and are limited by the concurrency and request caps only.
- Judge prompts: quoted trace text is fenced (block markers in it are defanged) and every call starts with a notice that quoted text is data. This lowers prompt-injection risk, it does not remove it; Jev (decision) prompts are not covered. Chat notifications strip markup and mentions from span-derived names.
- Tokens (`token.go`): cookies and API tokens are different kinds, each with an id and issue time; API tokens carry the role they were minted with; an admin can revoke one token or everything issued to a subject (`/api/admin/revocations`), and logout revokes its cookie. The format changed in this release: older cookies and minted tokens stop working and must be re-minted. A GitHub org member removed from the org keeps a cookie for up to 12 h.
- `serve` listens on 127.0.0.1 by default; without a session secret it refuses other addresses unless `--no-auth` is given, so a missing or empty secret cannot silently open it.
- Cross-origin POST/PUT/DELETE and WebSocket upgrades from a browser are refused (`origin.go`); logout is POST-only. DNS rebinding of the no-login local mode is not checked.
- `/api/health` is never gated. The OTLP receivers take an optional bearer token (`--otlp-token`) instead of the login.

**MCP** (`ego mcp`, stdio): `list_metrics`, `evaluate_traces`, `list_sessions`, `summarize_session`,
`list_runs`, `get_run_results`; with `--admin-tools` also `list_role_bindings`, `set_role_binding`, `delete_role_binding`.
`evaluate_traces` reads only under `--trace-dir`; the `gh` token goes only to https or loopback servers.

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
