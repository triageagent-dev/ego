# Application Passport — ego

## 1. Identity

| Field | Value |
|---|---|
| Component | ego |
| Description | Scores AI agent behavior from OpenTelemetry traces: REST API, embedded web UI, OTLP receivers, offline CLI, MCP server (stdio and HTTP), findings and corrections for self-correcting agents |
| Repo | github.com/triageagent-dev/ego |
| Registry | ghcr.io/triageagent-dev/ego (image); oci://ghcr.io/triageagent-dev/charts (Helm chart) |
| Version | Tag v0.1.49 plus 3 commits (`git describe`: v0.1.49-3-gb41e326); Helm chart 0.1.2, appVersion 0.1.0; build version injected at link time via `internal/api.Version` |
| Owner | triageagent-dev |
| License | Apache-2.0 |
| Passport date | 2026-10-21 |
| Source revision | b41e326 (v0.1.49-3-gb41e326) |
| Audit status | Review of 2026-10-21 at revision b41e326, focused on the changes since 95f2f0c (v0.1.40): 0 Critical, 0 High, 6 Medium, 17 Low findings open (the earlier N1, N3, N6 and N15 closed, N4 and N11 to N14 partly); dependency scan has no Critical or High |
| Code review | Done 2026-10-21 for the changes since 95f2f0c (labels as the score of record, paged session index and search, session-id bodies for convert and evaluate, embedded UI serving, live-feed change messages, signed-out page, UI tenant picker and JSON tree, MCP `scored` filter and body cap); findings open; code before 95f2f0c last reviewed at v0.1.40 (changes since v0.1.37) and at revision 3106cff |

## 2. Classification

| Field | Value |
|---|---|
| Class | Service (HTTP/gRPC server) and CLI, one binary |
| Type | Evaluation service; uses LLMs as judges and to write diagnoses of low-scoring series (no autonomous agent loop of its own); serves MCP tools and an agent skill (prompt) that let other agents read verdicts and register corrections |
| Subcommands | `run`, `serve`, `auth mint-token`, `mcp` (stdio), `version` |
| Capabilities | Trace ingest (OTLP/HTTP, OTLP/gRPC, Jaeger JSON, OTLP JSON/JSONL); metric scoring; LLM-judge scoring; session store with a paged, searchable session index; run history; eval sets; token usage ledger; online evaluation with alerts and notifications; findings (judge diagnosis of low-scoring series); corrections with verification; labels on judge scores that are the score of record in every aggregate, with a per-tenant agreement gate and anchor re-scoring; evaluation and conversion of stored sessions named by id; live-feed change notices; signed-out landing page; MCP tool registry over stdio and over HTTP (`POST /mcp`); role-based access control with agent and tenant scopes; ingest senders with per-sender tokens; token revocation |
| Model role | Judge / evaluator of recorded agent traces; writer of findings (diagnosis text from evidence sessions) |
| Runtime | Single static Go binary (Go 1.26, CGO disabled); Kubernetes Deployment (1 replica, Recreate) or local process |
| Public accessibility | Chart default: ClusterIP Service, HTTPRoute disabled; optional Gateway API HTTPRoute (UI prefix `/evals` plus root paths, including `/mcp`) |
| Production data access | Read/write to its own SQLite database; receives agent traces pushed by producers; read-only calls to GitHub, Gemini/Vertex AI and the Jev API; notification sinks (Discord, webhook, event) receive alert, finding and correction messages |
| PII | Traces carry user prompts, agent responses, tool-call arguments and results; stored verbatim. Findings quote user messages (first 500 characters) and judge rationales |
| Sensitivity | Trace content stored on disk for the retention window (default 720h); GitHub logins and team memberships; SHA-256 hashes of sender tokens; token revocation records; labels and corrections with the caller's identity or a caller-supplied label |
| Authentication mode | Session secret (at least 32 bytes) enables GitHub OAuth, cookies, API tokens and roles. Without one, `serve` listens on loopback only and refuses other addresses unless `--no-auth` is given; chart default `auth.enabled: true`. Default role for a signed-in user no role binding covers: `member` (binary and chart default), settable to `none` (no access). On `/mcp` only, a sender ingest token also authenticates, as a `member` limited to `<sender>/*` |

## 3. Artifact

| Field | Value |
|---|---|
| Form | Container image (Dockerfile and ko), release binaries, Helm chart |
| Language | Go 1.26 (go.mod, `toolchain go1.27.3`); TypeScript/React UI (Vite, Ant Design; production build ships a source map beside the bundle) embedded with `go:embed`; a static HTML page (`signedout.html`) embedded with `go:embed`; agent skill (Markdown) embedded with `go:embed` |
| Build tool | `go build`; Docker multi-stage; `ko` (Makefile `image`); GitHub Actions release workflow |
| Release platforms | linux/amd64, linux/arm64, darwin/amd64, darwin/arm64 (binaries, tarballs with `checksums.txt`); linux/amd64 + linux/arm64 (multi-arch image `ghcr.io/triageagent-dev/ego` via Actions); linux/amd64 (ko); Helm chart pushed as an OCI artifact to `oci://ghcr.io/triageagent-dev/charts` |
| Base image (Dockerfile) | gcr.io/distroless/static-debian12:nonroot (tag, no digest); build stages node:22-bookworm-slim, golang:1.27-bookworm (tags, no digest), both pinned to the builder platform; the Go binary is cross-compiled per target (`TARGETOS`/`TARGETARCH`, `-trimpath`, CGO disabled); the build copies `cmd`, `internal`, `skills` and the built UI |
| Base image (ko) | cgr.dev/chainguard/static (tag, no digest) |
| Runtime user | `USER 65532:65532`; pod `runAsNonRoot`, `runAsUser: 65532`, `fsGroup: 65532`, seccomp `RuntimeDefault` |
| Container securityContext | `allowPrivilegeEscalation: false`, `readOnlyRootFilesystem: true` (with a `/tmp` emptyDir), capabilities drop ALL; CPU limit 1, memory limit 512Mi (requests 25m CPU, 32Mi memory); `/tmp` emptyDir sizeLimit 256Mi |
| Persistent storage | SQLite file on PVC `ego-session-db` (1Gi, RWO, `helm.sh/resource-policy: keep`) mounted at `/data`; WAL mode; directory 0700, files 0600 |
| SQLite tables | `sessions`, `session_index` (one row per archived session: start time, completion, LLM call count, tenant keys, agent names, lower-cased header text), `runs`, `run_results`, `eval_sets`, `role_bindings`, `revocations`, `senders`, `sender_tokens`, `llm_calls`, `online_evals`, `online_scores`, `online_alerts`, `online_findings`, `online_corrections`, `online_labels`, `online_state`; the earlier `tenants` and `tenant_tokens` tables are migrated into `senders`/`sender_tokens` and dropped. Without `--session-db`, senders, role bindings and revocations are held in memory only |
| Embedded skill | `skills/ego-rsi/SKILL.md`, compiled into the binary and served as the MCP prompt `rsi_loop`; also installable as a skill file |
| Embedded UI serving | `internal/api/staticui.go`: files read from the embedded filesystem once, gzipped once (text, JavaScript, JSON, SVG of 1 KiB or more), sent with a SHA-256 ETag and `Vary: Accept-Encoding`; paths under `assets/` carry `Cache-Control: private, max-age=31536000, immutable`, other files `no-cache`; GET and HEAD only; a name that is not an embedded file answers 404 and is not remembered; behind the same login as the rest of port 8001 |
| Chart online config | `online.config` plus an `online.findings` block rendered into the `--online-config` ConfigMap (`ego.onlineConfig` helper); config checksum annotation restarts the pod on change |
| Install script | `install.sh`: downloads release tarball and `checksums.txt` from GitHub Releases, compares SHA-256 |
| CI/CD | `.github/workflows/ci.yml` (push/PR to main); `release.yml` (tags `v*.*.*`, manual dispatch) |
| Workflow permissions | release.yml: `contents: read` at top level; `contents: write` + `id-token: write` (release job), `packages: write` + `id-token: write` (image job), `packages: write` (chart job); publishing jobs run in the `release` environment; ci.yml: `contents: read` |
| Action references | Pinned to commit SHAs with the version in a comment |
| Signing / attestation | `checksums.txt` (bundle `checksums.txt.bundle`) and the image digest signed keyless with cosign; image carries provenance and SBOM attestations; the Helm chart is not signed |
| Tracked binary | None; `/ego` and `/bin/` are in `.gitignore` |

## 4. AI / LLM Supply Chain

| Item | Detail |
|---|---|
| Models: Gemini judge | Default `gemini-2.5-flash`; model name overridable per evaluator in the request |
| Models: findings diagnosis | Fixed to the default Gemini judge model (`gemini-2.5-flash`); one call per finding written or refreshed; answer read as JSON |
| Models: Vertex AI Managed Eval | `safety_v1`, `multi_turn_task_success_v1`, `multi_turn_trajectory_quality_v1`, `multi_turn_tool_use_quality_v1`; fixed managed backend |
| Models: Jev decision model | Default `jev-1.13` (sent as `typesafe/jev-1.13`); override env `JEV_MODEL` or `--jev-model` |
| Local (non-LLM) metrics | `tool_trajectory_avg_score`, `response_match_score` (ROUGE-1) |
| LLM auth: Gemini | Vertex AI via Application Default Credentials (chart sets `GOOGLE_GENAI_USE_VERTEXAI=true`, project, location); or API key `GEMINI_API_KEY` / `GOOGLE_API_KEY` / `--judge-api-key` |
| LLM auth: Vertex eval | Application Default Credentials, cloud-platform scope; no key mode |
| LLM auth: Jev | Bearer `JEV_API_KEY` (env or `--jev-api-key`); chart reads it from Secret `ego-jev` (optional) |
| Data sent to models | User prompt, agent response, reference response, tool calls and rubrics extracted from traces; for findings: per evidence session the session id, score, first user message (500 characters), tool-call names (30), the metric's rationale (400), up to 6 failing verdicts (300 each) and, for a session a person checked, a note of at most 400 characters (the person's score and note are marked as theirs), for at most `findings.maxSessions` (default 8) sessions |
| Egress: github.com | OAuth authorize and code exchange (client secret) |
| Egress: api.github.com | `/user`, `/orgs/{org}/members/{user}`, `/user/teams` with the signed-in user's token |
| Egress: Gemini | `{location}-aiplatform.googleapis.com` (Vertex) or `generativelanguage.googleapis.com` (API key) |
| Egress: Vertex Managed Eval | `https://{location}-aiplatform.googleapis.com/v1beta1/projects/{project}/locations/{location}/:evaluateInstances` |
| Egress: Jev | `https://openrouter.ai/api/alpha/decisions` (override `JEV_API_URL`) |
| Egress: notification sinks | Discord webhook (URL from env, e.g. `EGO_DISCORD_WEBHOOK`), generic webhook, event sink; optional bearer token read from a file on each send. Payload types: alerts, `ego.finding.v1`, `ego.correction.v1`, daily digest |
| Egress: MCP client | `--server-url` / `EGO_SERVER_URL` (default `http://localhost:8001`); `gh auth token` output used as bearer when no token is supplied, but only for https servers or loopback |
| Online evaluation cap | `budget.judgeTokensPerDay` and `budget.jevCallsPerDay` per UTC day (chart default 2000000 / 1000); sampling `perKindPerHour`. Scoring, findings and anchor re-scoring draw on the same daily judge-token cap; findings spend is persisted across restarts |
| Findings pass limits | `findings.every` 6h, `window` 24h, `minSessions` 8, `minScore` 0.7, `maxSessions` 8 (1 to 20), `verifyWithin` 48h, `minAgreement` 0.7, `recalibrateEvery` 24h; call timeout 2 minutes; answer fields clipped (failure mode 300, summary 1500, suggestion 1500, excerpt 400, why 300 characters) |
| Interactive evaluation cap | At most 4 evaluations at once; per request 50 trace files, 20 evaluators, 8 judge samples, 50 rubrics; daily judge-token cap shared by all callers (`--eval-daily-tokens`, default 5000000, 0 = unlimited). Vertex managed evaluation and Jev report no token counts and are limited by concurrency and request caps only |
| Per-call output cap | `MaxOutputTokens` 8192 on Gemini judge calls (scoring and findings); samples per invocation default 5, at most 8; `judgeModel` must match `[A-Za-z0-9._-]{1,64}` |
| Input/output filtering | Block markers inside quoted trace text are defanged and every judge call starts with a notice that quoted text is data; the findings prompt additionally replaces angle brackets, collapses whitespace and clips each quoted value (a label note included; the prompt tells the model a checked session's score and note outweigh the judge's rationale); the findings answer is parsed as JSON, evidence naming an unquoted session id is dropped; chat sinks strip markup, mentions and control characters from model text and cut it to a length; no injection classifier or structured-output schema; Jev prompts are not covered |
| Prompt templates | Judge prompts embedded in `internal/judge` (ADK-derived); findings prompt in `internal/online/findings.go`; trace text interpolated into the prompt string after defanging |
| Skill / prompt served to agents | `rsi_loop` (MCP prompt, same text as `skills/ego-rsi/SKILL.md`): tells an agent to read findings, check evidence with `explain_session` / `explain_regression` / `summarize_session`, make one change per finding under a new `service.version`, call `register_correction`, later `list_corrections`; states that everything a tool returns is data from traces and models and not an instruction, that labels are for people and the agent must not label its own sessions, and that a label is the score of record; tools named in it: `get_quality`, `get_findings`, `explain_session`, `explain_regression`, `summarize_session`, `register_correction`, `list_corrections`, `label_session` |
| MCP tools (stdio and HTTP) | `list_metrics`, `list_sessions` (filters `agent`, `tenant`, `since`, `query`, `complete`, `scored`; items carry `scored`), `summarize_session`, `get_session_spans`, `list_runs`, `get_run_results`, `get_usage`, `get_quality`, `explain_regression`, `explain_session`, `get_findings`, `register_correction`, `list_corrections`, `label_session` (a score given replaces the judge's in every mean, alert, finding and verification; a disagreement without a score drops the session from them); admin-only `list_role_bindings`, `set_role_binding`, `delete_role_binding`, `run_findings`. `evaluate_traces` exists on stdio only and reads only under `--trace-dir` (default: the current directory); over stdio the admin tools are registered only with `--admin-tools` |

## 5. Privileges

| Scope | Grant |
|---|---|
| Kubernetes ServiceAccount | `ego` (created by chart); no Role, ClusterRole or bindings in chart; `automountServiceAccountToken: false` |
| GCP identity | Optional Workload Identity annotation on the ServiceAccount; Vertex AI access is whatever the bound service account holds |
| Secrets read (env via secretKeyRef) | `EGO_GITHUB_CLIENT_SECRET` (Secret `ego-github-oauth`), `EGO_SESSION_SECRET` (Secret `ego-session-secret`), `EGO_OTLP_TOKEN` (Secret `ego-otlp-token`, key configurable via `otlpAuth.key`, only when `otlpAuth.enabled`), `JEV_API_KEY` (Secret `ego-jev`, optional), `EGO_DISCORD_WEBHOOK` (optional) |
| Projected token | Optional service-account token (configurable audience, 3600 s) at `/var/run/secrets/ego/webhook/token` |
| Filesystem | Read/write `/data` (PVC); read-only `/etc/ego` (online config ConfigMap) |
| Application roles | `admin` (full, manages role bindings, senders and revocations), `member` (read/write), `viewer` (read-only); `none` exists only as a default role (no access without a binding, cannot be put in a binding); default role `member` (`--default-role` / `EGO_DEFAULT_ROLE` / chart `auth.defaultRole`) |
| Role binding subjects | GitHub user login or GitHub team `org/slug`; optional per-binding agent-name scope; optional per-binding `tenants` patterns |
| Tenant model | Tenant key `<sender>/<label>`; spans sent with the shared `--otlp-token` have the bare label as key. Label read from span attribute `ego.tenant` (override `--usage-tenant-attr`); a sender's spans with no label use `default`. Session ids of a named sender are `<sender>:<label>:<name>` (`<sender>:<name>` without a label); spans for a session owned by another sender are dropped |
| Tenant grants | Patterns `*` (all), `<sender>/*` (all tenants of a sender), `<sender>/<label>` (exact); on a role binding or on an API token (`ego auth mint-token --tenants`). A tenant-scoped role cannot use the evaluation, eval-set and admin write endpoints and sees no data without a tenant; `?tenant=` narrows within the grant only; `GET /api/tenants` lists the caller's permitted tenants (403 to an agent-scoped role) |
| Senders | An ingest identity (id `[a-z0-9-]`, up to 63 characters) with one or more ingest tokens `egs_…`; tokens shown once at creation and stored as SHA-256 hashes (table `sender_tokens`); managed by admins under `/api/admin/senders*` |
| Sender token on `/mcp` | Authenticates as role `member` with tenants `<sender>/*`, on `POST /mcp` only (not on `/api`, `/stream`, `/ws`); the shared `--otlp-token` does not authenticate there; active only with a session secret and a sender store; identity name `sender:<id>` |
| Bootstrap admins | `EGO_ADMIN_GITHUB_USERS` / chart `adminGithubUsers` |
| Write endpoints | `POST /api/evaluate`, `POST /api/evaluate/stream`, `POST /api/evalsets` (member or admin, not tenant-scoped; the first two take uploaded trace files or a JSON body `session_ids` and `eval_set_session_id` naming stored sessions); `POST /api/online/corrections` and `POST /api/online/labels` (member or admin, including tenant-limited roles for their own tenants and sender-token identities on `/mcp`; viewer and agent-scoped roles refused; bodies capped at 64 KiB and 16 KiB; a label is refused with 409 beyond 50 versions per session and metric; a (tenant, kind) holds at most 20 pending corrections, 429 beyond); `POST /api/online/findings/sweep` (admin); `POST /api/admin/roles`, `DELETE /api/admin/roles/{id}`, `POST /api/admin/revocations`, `POST /api/admin/senders`, `DELETE /api/admin/senders/{id}`, `POST /api/admin/senders/{id}/tokens`, `DELETE /api/admin/senders/{id}/tokens/{tokenId}` (admin); `GET` on `/api/admin/roles`, `/api/admin/revocations`, `/api/admin/senders`, `/api/admin/senders/{id}/tokens` (admin) |
| MCP write tools | `register_correction`, `label_session` (member or admin; tenant-limited roles for their own tenants); `set_role_binding`, `delete_role_binding`, `run_findings` (admin without agent or tenant limits) |
| Agent-scope enforcement | A scoped role sees only sessions, traces, runs and live-feed events involving one of its agents (results with no agent name are hidden); usage, eval sets and online views (scores, findings, corrections, labels, quality) answer 403 or an error to it, on REST and MCP alike. Unknown GitHub team membership is refused instead of given the default role |
| Tenant-scope enforcement | A tenant-scoped role sees only sessions (the paged session index is checked against the role on every row before it counts towards a page; `GET /api/tenants` is built from the index and narrowed to the role's grants), traces, usage, online series, alerts, findings, corrections, labels and live-feed events of its tenants, on REST and MCP alike; it reads runs and run results only when the run id appears in `online_evals` for one of its tenants (an hourly online run holds one tenant and one call kind); eval sets, budget status and evaluations answer 403; the shared judge budget is not shown in `get_quality` |
| Label agreement scope | The agreement gate counts, per metric, only the labels of the tenant whose finding is being written, so one tenant's labels do not gate another's; `GET /api/online/status` reports agreement and `gated`/`gatedTenants` over the tenants the caller sees (a tenant-limited role sees its own; drift is shown to unrestricted callers only) and answers 403 to agent-scoped roles; reads of labels for agreement, gate and anchors take the newest 5000 rows of the window |
| Score of record | Every aggregate read of online scores (detector samples, per-series samples for findings and verification, hourly series) joins each score with the newest label of its (session, metric) from `online_labels`: the label's score replaces the judge's, an agreement keeps the judge's score and marks it confirmed, a disagreement without a score leaves the session out; the judge's row is not rewritten. A label can be written only for a session scored online whose tenant the caller's grants cover (404 otherwise, the same answer as for a missing session) |
| Label and correction authorship | Label author is the authenticated identity (empty with no login); correction author `by` is a caller-supplied text of up to 200 characters; correction `what` up to 2000 characters, `version`, `kind` and `tenant` up to 200; label note up to 1000 characters |
| Token types | Session cookie `ego_session` (kind `s`, 12 h, accepted only as the cookie); API token from `ego auth mint-token` (kind `t`, default 90 days, carries its own role and optional tenants, accepted only as a bearer value); live GitHub access token validated against org membership (5 min cache, 1000 entries, at most 8 concurrent lookups, only well-formed tokens reach GitHub); sender token `egs_…` (OTLP receivers, and `POST /mcp`) |
| Token revocation | By token id or by subject (every token issued to a subject up to that moment) via `POST /api/admin/revocations`; records held in memory and persisted in SQLite (`revocations`) when a session database is configured; logout revokes its cookie; rotating the session secret invalidates all cookies and API tokens; a sender token stops working when an admin deletes it |
| Signature scheme | `base64url(payload "|" hex(HMAC-SHA256(secret, payload)))` with payload `3|kind|id|issuedAt|expiry|role|tenants|username` (format version 3; role and tenants empty for cookies) |
| OAuth | Authorization-code flow, scope `read:org`, state in `ego_oauth_state` cookie (10 min), fixed redirect `/evals`, cookies `HttpOnly; Secure; SameSite=Lax` |

## 6. Network Surface

| Port | Protocol | Paths | Auth state |
|---|---|---|---|
| 8001 | HTTP | REST API (`/api/*`), UI (`/`), SSE `/stream/ui-updates`, WebSocket `/ws/ui-updates`, `/auth/*` | Session cookie or bearer when `EGO_SESSION_SECRET` is set; with `--no-auth` open (every caller admin). `GET /api/streaming/sessions` pages with `limit`, `cursor` and filters `q`, `scored`, `llm`, `tenant`, `agent`, `since`, `complete` |
| 8001 | WebSocket / SSE | Live feed messages: session events filtered by the client's role; `changed` (topic `usage`, `quality` or `runs`, no data, debounced 2 s, sent to every client) after a store write; `heartbeat` every 15 s on the WebSocket; `resync` | Same login as `/api`; the `changed` and `heartbeat` messages are not filtered by role |
| 8001 | HTTP | `POST /mcp` (MCP streamable HTTP, stateless, `Content-Type: application/json` required; `GET` answers 405) | Session cookie, API token or GitHub token like `/api`; a sender ingest token also accepted; open (every caller admin) when no session secret is set |
| 8001 | HTTP | `/api/health`, `/auth/me`, `/auth/login`, `/auth/callback`, `/auth/logout`, `/auth/signed-out` | Not gated; `/auth/signed-out` is a static HTML page with no script (GET and HEAD, `Cache-Control: no-store`) that logout (POST only) redirects to |
| 4318 | HTTP | `POST /v1/traces` (protobuf or JSON, gzip) | Bearer token required; accepts the shared `--otlp-token` or a sender token (`egs_…`); with neither configured every push is refused, with a warning in the log |
| 4317 | gRPC | `TraceService/Export` | Same rule as 4318, token in `authorization` metadata |
| 9090 | HTTP | `GET /api/health` (status, version) | Never gated |
| Kubernetes Service | ClusterIP | 8001, 4318, 4317, 9090 (plus optional alias Services) | n/a |
| HTTPRoute (optional) | Gateway API | Prefix `/evals` with rewrite to `/`; root paths `/assets`, `/favicon.svg`, `/favicon.ico`, `/ws`, `/api/config`, `/api/convert`, `/api/evaluate`, `/api/evalsets`, `/api/admin`, `/api/health`, `/api/metrics`, `/api/runs`, `/api/usage`, `/api/online`, `/api/tenants`, `/api/streaming`, `/api/validate`, `/mcp`, `/stream`, `/auth` | Backend 8001 only; 4317, 4318, 9090 not routed; `rootRedirect` (chart default true) sends `/` on the host to the UI prefix |
| TLS | None in process | TLS terminated outside the pod | n/a |
| Cross-origin protection | POST/PUT/DELETE and WebSocket upgrades from a browser (including `POST /mcp`) must come from the server's own host, the public URL host or `X-Forwarded-Host` (403 otherwise); logout is POST-only; security headers (CSP, frame, nosniff, referrer) on 8001; the Host header is not checked | all of 8001 | n/a |
| HTTP server timeouts | Read-header, read, write (except the live feed and streaming evaluation) and idle timeouts on all HTTP listeners; gRPC keepalive and stream limits | n/a | n/a |
| Request size limits | OTLP/HTTP: 16 MiB on the wire, 64 MiB decoded (413); gRPC: 16 MiB receive; REST JSON bodies and multipart uploads capped; label bodies 16 KiB, correction bodies 64 KiB; `POST /mcp` bodies 1 MiB (413 beyond); JSON bodies of the write endpoints 8 MiB; `/api/convert` names at most 200 sessions and `/api/evaluate` at most 50 per request; session list pages at most 100 sessions, one request reads at most 20000 index rows (a count under a role SQL cannot express, 50000), an archive text search at most 2000 rows and 200 hits; list limits clamped; MCP argument bounds: `get_quality` hours 1 to 720, `get_usage` hours 1 to 2160 and top 1 to 100 | n/a | n/a |
| NetworkPolicy | Optional in the chart (`networkPolicy.enabled`, rules supplied by the deployer) | n/a | n/a |
| Defaults | Binary: listens on 127.0.0.1 (`:8001`, `:4318`, `:4317`, `:9090`); the image and chart set the listen addresses explicitly | n/a | n/a |

## 7. Dependency Vulnerabilities

| Ecosystem | Manifest | Scanner | Critical | High | Medium | Low | Unknown |
|---|---|---|---|---|---|---|---|
| Go | go.mod | trivy 0.56.2 | 0 | 0 | 0 | 0 | 1 (GO-2026-5932) |
| npm | ui/package-lock.json | trivy 0.56.2 | 0 | 0 | 0 | 0 | 0 |
| npm (all, incl. dev) | ui/package-lock.json | npm audit | 0 | 0 | 0 | 0 | 0 |
| npm (production deps only) | ui/package-lock.json | npm audit --omit=dev | 0 | 0 | 0 | 0 | 0 |

| Go advisory | Package | Installed | Fixed | Call-reachable (govulncheck) |
|---|---|---|---|---|
| GO-2026-5932 | golang.org/x/crypto | 0.57.0 | none | No |
| GO-2026-6809 | Go standard library, crypto/x509 | go1.27.2 (toolchain until this revision) | go1.27.3 | Not reported on go.mod toolchain go1.27.3 (govulncheck 2026-10-21) |
| GO-2026-6443 | google.golang.org/grpc | 1.84.0 | documented in docs/STATUS.md as awaiting a stable release | Not reported by govulncheck on 2026-10-21 (vulnerability database updated 2026-10-21) |

| Dependency | Version |
|---|---|
| golang.org/x/net | 0.60.0 |
| golang.org/x/crypto | 0.57.0 |
| google.golang.org/grpc | 1.84.0 |
| github.com/mark3labs/mcp-go | 1.1.1 |

| Config scan | Result |
|---|---|
| trivy secret scan | No secrets found |
| trivy misconfig: Dockerfile | 1 Low (no HEALTHCHECK) |
| trivy misconfig: chart | 2 Low (`runAsGroup`, default namespace in the template render), 1 Medium (image registry not on trivy's trusted list) |

| Tool | Version | Output |
|---|---|---|
| syft | 1.46.0 | CycloneDX 1.6 JSON, `bin/sbom/sbom-go.cdx.json`, `bin/sbom/sbom-npm.cdx.json` (scratch, not committed) |
| trivy | 0.56.2 | vuln scan of both lockfiles; secret and misconfig scan of repo |
| govulncheck | 1.7.0 with go1.27.3 | 0 affected; 1 module advisory not called (GO-2026-5932) |
