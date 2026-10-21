# Status

What ego supports today, and what it does not.

## Supported

**Traces in**
- Files: Jaeger JSON, OTLP JSON and JSONL, auto-detected (`internal/loader`).
- Live: OTLP/HTTP (`POST /v1/traces`, gzip accepted) and OTLP/gRPC, both feeding one session store.
- Conventions: ADK (`gcp.vertex.agent.*`), OTel GenAI semantic conventions and OpenInference.
- Sessions group by `ego.session_name`, then `gen_ai.conversation.id`, then a synthetic name.
- A session search (the Sessions view's box, `GET /api/streaming/sessions?q=`, `list_sessions` `query`) reads memory and the archive: the newest 2000 archived rows that mention the text, up to 200 of them returned; the other filters only narrow memory.
- The Sessions view pages from the server: `GET /api/streaming/sessions?limit=&cursor=` returns the newest sessions first with a `next` cursor (absent on the last page) and, on the first page, a `total` of matching sessions (`totalApprox` when it is a lower bound: a role SQL cannot express is counted up to 50000 index rows). Filters `since`, `tenant`, `agent`, `complete`, `llm=true` (hide sessions with no LLM call) and `q` (session and trace id, tenant, agent, model, service, root span name; not the conversation) run against an index table in the archive (`session_index`, kept by every save and filled in at start for an older archive), the caller's role is checked on every row, and only the page's sessions are loaded. One request reads at most 20000 index rows, so a role that sees few sessions may get a short page with a cursor to go on. Without `limit` the answer is the whole in-memory list as before (what the MCP `list_sessions` uses).
- An invocation's answer is the last call of the agent that opened it (`gen_ai.agent.name`); a later call by another agent in the same conversation (a verifier, a guard) is not scored as the answer.

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
- REST API and embedded UI; live feed over WebSocket (`/ws/ui-updates`, one per open page, with a heartbeat every 15 s that the status dot reads; a debounced `changed` message per topic (`usage`, `quality`, `runs`) makes those views read again, so they have no refresh timer or button) with a Server-Sent Events fallback.
- Runs (`/api/runs`): evaluations are synchronous and every run is stored.
- Eval sets: build from a golden session, save, edit, download.
- Session spans with attributes and errors; readable rendering of long prompts and answers.
- Token usage ledger (`/api/usage`) from `gen_ai.usage.*`, grouped by call kind, tenant, model and service.
- Persistence with `--session-db` (SQLite): sessions, runs, eval sets, role bindings; bounded memory
  (`--session-memory`) and retention (`--session-retention`).
- Online evaluation: sampling, per-kind evaluators, daily judge budget, regression detection, Discord, webhook
  and event notifications (`docs/online-evaluation.md`); `GET /api/online/quality`, `/api/online/alerts/{id}/explain` and `/api/online/sessions/{id}/explain` back the MCP tools of the same names.
- Findings and corrections (`docs/rsi.md`, `internal/online/findings.go`, `corrections.go`, `internal/api/onlinefindings.go`): a findings pass diagnoses low-scoring series with one judge call (evidence fenced as data, metered against `budget.judgeTokensPerDay`; without a judge model, or when its answer is unusable, the finding is written from the evidence alone). It runs for each open alert (refreshed when the drop grows) and in a sweep every `findings.every` over series with at least `findings.minSessions` sessions in `findings.window` and a mean below `findings.minScore`. States `open`, `corrected`, `resolved`, `stale`; stored in `online_findings`. A correction (`online_corrections`) marks its finding corrected, starts a sampler burst like a new `service.version`, and is verified on every detector tick against the scores since it was registered (only at its `version` when given): `verified` resolves the finding, `failed` reopens it with the attempt kept in its history. Sinks get `ego.finding.v1` and `ego.correction.v1` (generic events `EvalFindingOpened`/`EvalFindingResolved`, `EvalCorrectionVerified`/`EvalCorrectionFailed`). `findings.enabled: false` writes no findings; corrections still verify. `register_correction` caps `what` at 2000 characters and `kind`, `tenant`, `version`, `by` at 200 (400), and a (tenant, kind) holds at most 20 pending corrections (429; decided ones free a slot); verification reads every pending correction. `run_findings` is bound to its request with a 5 minute timeout and answers 409 while a sweep is running.
- Findings endpoints: `GET /api/online/findings?state=&kind=&tenant=&since=`, `GET /api/online/findings/{id}`, `GET /api/online/corrections?state=&kind=&tenant=`, `POST /api/online/corrections` (a viewer or an agent-scoped role gets 403; a tenant-limited member posts for its own tenants only, and the tenant is implied when it has exactly one or the finding names it). `GET /api/online/status` has `findings: {enabled, written, skipped: {budget, noEvidence, uncalibrated}, tokens}` (counted since the process started).
- Labels (`docs/rsi.md`, `internal/online/labels.go`, `internal/api/onlinelabels.go`): a person's verdict, `agree` or `disagree` with an optional score and note, on the online score of one metric of one session, stored in `online_labels` with the judge's score at label time and the caller's identity. **Score of record**: every aggregate read of scores (`OnlineSamples`, `OnlineSamplesFor`, `OnlineSeries`: the detector, alerts, the sweep, findings, correction baselines and verification, the hourly series, the digest) joins each score with its newest label; the person's score replaces the judge's, an agreement keeps the judge's score and marks it confirmed (`Sample.Human`), a disagreement without a score leaves the session out. The judge's row is never rewritten. Findings cite checked sessions first, give the diagnosis the person's note, and carry `confirmed` (the count of checked evidence sessions; the notification says "n checked by a person"). `explain_session` metrics carry `human_verdict` and `human_score`. A sender's ingest token (the agent under evaluation, on `/mcp`) is refused by `label_session` and `POST /api/online/labels` (403, `RoleInfo.FromSender`): the agent must not write the score of record on itself. `POST /api/online/labels` (`{sessionId, metric, verdict, score?, note?}` gives 201; 404 when the session has no online score for the metric or belongs to a tenant the caller may not see; a viewer or an agent-scoped role gets 403), `GET /api/online/labels?metric=&session=&tenant=&since=`. Agreement per metric is agree / (agree + disagree) over the last 7 days, counting the newest label of each session and metric; `GET /api/online/status` reports it as `labels: {perMetric: {metric: {agree, disagree, agreement, gated, drift?, driftN?, driftAt?}}, skipped: {budget}}` (a tenant-limited caller counts its own tenants' labels and gets no drift; `gated` is true when at least one tenant the caller sees is gated, and `gatedTenants` names them) and the daily digest has one line per metric. **Gate**: per tenant, a metric with agreement under `findings.minAgreement` (default 0.7) over that tenant's own labels, and at least 10 of them in the window, writes no findings for that tenant (`findings.skipped.uncalibrated`, `gated: true`); other tenants are unaffected. Label reads are capped at the newest 5000 rows of the window, and one (session, metric) takes at most 50 label versions (409). **Anchor re-scoring**: every `findings.recalibrateEvery` (default 24h), up to 20 labelled sessions per metric, the ones re-scored longest ago first, are scored again with the profile that scored them, against the judge budget (reserve and settle like any scoring), and the mean absolute difference to the label's score (the judge's score at label time when the label gave none) is reported as `drift`. Nothing acts on drift. A pass cut short by the budget counts under `labels.skipped.budget` and is retried on the next tick.
- UI (Quality and Sessions): the Quality view lists **Findings** (state filter open, corrected, resolved, stale; a row expands to the summary, the suggested correction and the evidence sessions as links to their session cards), **Corrections** (kind, what, version, state, before and after means with n) and **Judge agreement** (a tile per metric: agree and disagree counts, agreement, drift, a `gated` badge). An expanded, completed session card lists the session's online scores with agree and disagree thumbs and an optional note, showing the latest label; a thumb down opens a picker for the right score (0, 0.25, 0.5, 0.75, 1, or `?` for none), and a person's score is shown next to the struck-through judge's score; the run detail page puts the same thumbs on each metric row of a scored session; the Sessions view's "scored only" toggle (`scored=true` on the list) shows only such sessions.

**Access**
- GitHub OAuth restricted to an org, signed session cookies, bearer tokens from `ego auth mint-token`, or a raw
  GitHub token validated against org membership.
- Roles `admin`, `member`, `viewer`, optionally scoped to agent names (sessions, traces, runs and the live feed;
  Tokens, Quality and eval sets are closed to a scoped role); inert when no session secret is set. Unknown team
  membership is refused, never defaulted. A session with several agents is visible to a role scoped to any one.
- Tenants (`senders.go`, `ingestauth.go`, `tenantview.go`): a token identifies a sender, a sender sends many tenants labelled on its spans, and a tenant is `<sender>/<label>`. Grants (role bindings, `mint-token --tenants`) name keys, `<sender>/*` or `*`; `--default-role none` gives nobody access without a grant; the UI has a tenant switcher (`/api/tenants`, `?tenant=` narrows usage and quality); an admin can give a tenant key a display name (`/api/admin/tenant-names`, `tenantnames.go`). Tenant-limited roles get no eval sets, budget status or evaluations. They read runs and run results only for the online evaluation runs of their tenants (`online_evals` maps a run to its tenant); every other run stays closed to them.
- Interactive evaluation is bounded: at most 4 run at once, 50 trace files, 20 evaluators and 8 judge samples per request, one daily judge-token cap shared by all callers (429 when used up), 8192 output tokens per judge call. Vertex managed evaluation and Jev report no token counts and are limited by the concurrency and request caps only.
- Judge prompts: quoted trace text is fenced (block markers in it are defanged) and every call starts with a notice that quoted text is data. This lowers prompt-injection risk, it does not remove it; Jev (decision) prompts are not covered. Chat notifications strip markup and mentions from span-derived names.
- Tokens (`token.go`): cookies and API tokens are different kinds, each with an id and issue time; API tokens carry the role they were minted with; an admin can revoke one token or everything issued to a subject (`/api/admin/revocations`), and logout revokes its cookie. The format changed in this release: older cookies and minted tokens stop working and must be re-minted. A GitHub org member removed from the org keeps a cookie for up to 12 h.
- `serve` listens on 127.0.0.1 by default; without a session secret it refuses other addresses unless `--no-auth` is given, so a missing or empty secret cannot silently open it.
- Cross-origin POST/PUT/DELETE and WebSocket upgrades from a browser are refused (`origin.go`); logout is POST-only. DNS rebinding of the no-login local mode is not checked.
- `/api/health` is never gated. The OTLP receivers take an optional bearer token (`--otlp-token`) instead of the login.

**MCP** (`internal/mcptools`): one tool registry, two transports. `ego mcp` (stdio) is a REST client of a running `serve`; `serve` mounts the same tools at `POST /mcp` (MCP streamable HTTP, stateless; `GET` answers 405, as nothing is ever pushed; a POST body over 1 MiB is refused with 413 before the transport reads it) and runs them in-process against the stores, so a role's agent and tenant limits apply to every tool.
- Tools: `list_metrics`, `list_sessions` (`limit?`, and filters `agent?` exact, `tenant?`, `since?` RFC 3339 or a duration, `query?` over the session id, agent names, service and, for completed sessions, the first user message, `complete?`, `scored?`; each item carries agent, tenant, service, error spans, total tokens and `scored`; the server applies the filters after the role limits), `get_usage` (`hours?` default 24, `tenant?`, `group?` kind, tenant, model or service, `top?` default 10: the token ledger the Usage view shows, with totals, the breakdown by group and the largest sessions; no cost), `summarize_session`, `list_runs`, `get_run_results`, and for online evaluation `get_quality` (open alerts, hourly series, sessions scored, today's judge budget), `explain_regression` (an alert, its suspect and its worst sessions with the judge's evidence) and `explain_session`, and for the correction loop `get_findings` (`kind?`, `state?` default open, `since?`), `register_correction` (`finding_id?`, `kind`, `what`, `version?`, `by?`, plus an optional `tenant` for callers whose role does not imply one) and `list_corrections` (`kind?`, `state?`), and `label_session` (`session_id`, `metric`, `verdict`, `score?`, `note?`; the label's author is the caller's own identity). The evidence is the per-sentence verdicts of `hallucinations_v1` (unsupported, contradictory and disputed first), per-rubric verdicts, trajectory comparisons and the rationale of `jev_*` decisions, read from the stored online run results. Tools that need online evaluation answer an error when it is not configured.
- Prompt `rsi_loop` (both transports): the text of `skills/ego-rsi/SKILL.md`, embedded in the binary (`skills/embed.go`), so an agent can load the self-correction loop from the server. The skill file is the single source; `skills/ego-rsi/README.md` says how to install it as a skill instead.
- `evaluate_traces` reads trace files from local disk and exists only on stdio (it reads only under `--trace-dir`); `serve` does not register it. `list_role_bindings`, `set_role_binding`, `delete_role_binding` and `run_findings` (one findings sweep now, whatever `findings.every`; REST twin `POST /api/online/findings/sweep`) need an admin on both transports; over stdio they are registered only with `--admin-tools`.
- `/mcp` is behind the same login and roles as `/api`. In addition a sender's ingest token (`SenderStore`) authenticates there, and only there, as a `member` limited to `<sender>/*`; the shared `--otlp-token` does not (it names no sender). Without `--session-secret` there is no login and `/mcp` is open like every other route.
- Tenant-limited callers get no shared judge budget in `get_quality`, and an agent-scoped role has no online tools and no `get_usage` (as on the REST endpoints).
- The `gh` token goes only to https or loopback servers.

## Not supported yet

- `/v1/logs` ingestion, for frameworks that keep message content in log records instead of span attributes.
- Performance metrics (token and latency percentiles) in the `/api/evaluate` response.
- `POST /api/streaming/evaluate-sessions` (evaluate every session against a golden one in one call); the UI
  does the same through `/api/evaluate` with `session_ids` and `eval_set_session_id` in a JSON body (the server
  reads its own stored traces under the caller's role; `/api/convert` takes `session_ids` the same way). Uploads
  as multipart files still work, but a WAF that blocks multipart bodies (OWASP signature 921120) refuses them.
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
- Logging (`internal/logx`): leveled `log/slog` lines (`--log-level`, default info); sign-in user names are logged only with `--audit-log`; refused OTLP pushes are logged once a minute without the token.
