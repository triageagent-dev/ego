# Application Passport — ego

## 1. Identity

| Field | Value |
|---|---|
| Component | ego |
| Description | Scores AI agent behavior from OpenTelemetry traces: REST API, embedded web UI, OTLP receivers, offline CLI, MCP server (stdio and HTTP), findings and corrections for self-correcting agents |
| Repo | github.com/triageagent-dev/ego |
| Registry | ghcr.io/triageagent-dev/ego (image); oci://ghcr.io/triageagent-dev/charts (Helm chart) |
| Version | Tag v0.1.40; Helm chart 0.1.2, appVersion 0.1.0; build version injected at link time via `internal/api.Version` |
| Owner | triageagent-dev |
| License | Apache-2.0 |
| Passport date | 2026-10-21 |
| Source revision | 95f2f0c (v0.1.40) |
| Audit status | Review of 2026-10-21 at revision 95f2f0c, focused on the changes since v0.1.37: 0 Critical, 0 High, 6 Medium, 9 Low findings open; dependency scan has no Critical or High |
| Code review | Done 2026-10-21 for the changes since v0.1.37 (MCP endpoint, sender-token access, tenant-limited runs, findings, corrections, labels, usage tool, embedded skill, chart); findings open; code before v0.1.37 last reviewed at revision 3106cff |

## 2. Classification

| Field | Value |
|---|---|
| Class | Service (HTTP/gRPC server) and CLI, one binary |
| Type | Evaluation service; uses LLMs as judges and to write diagnoses of low-scoring series (no autonomous agent loop of its own); serves MCP tools and an agent skill (prompt) that let other agents read verdicts and register corrections |
| Subcommands | `run`, `serve`, `auth mint-token`, `mcp` (stdio), `version` |
| Capabilities | Trace ingest (OTLP/HTTP, OTLP/gRPC, Jaeger JSON, OTLP JSON/JSONL); metric scoring; LLM-judge scoring; session store; run history; eval sets; token usage ledger; online evaluation with alerts and notifications; findings (judge diagnosis of low-scoring series); corrections with verification; labels on judge scores with an agreement gate and anchor re-scoring; MCP tool registry over stdio and over HTTP (`POST /mcp`); role-based access control with agent and tenant scopes; ingest senders with per-sender tokens; token revocation |
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
| Language | Go 1.26 (go.mod, `toolchain go1.27.2`); TypeScript/React UI (Vite, Ant Design) embedded with `go:embed`; agent skill (Markdown) embedded with `go:embed` |
| Build tool | `go build`; Docker multi-stage; `ko` (Makefile `image`); GitHub Actions release workflow |
| Release platforms | linux/amd64, linux/arm64, darwin/amd64, darwin/arm64 (binaries, tarballs with `checksums.txt`); linux/amd64 + linux/arm64 (multi-arch image `ghcr.io/triageagent-dev/ego` via Actions); linux/amd64 (ko); Helm chart pushed as an OCI artifact to `oci://ghcr.io/triageagent-dev/charts` |
| Base image (Dockerfile) | gcr.io/distroless/static-debian12:nonroot (tag, no digest); build stages node:22-bookworm-slim, golang:1.27-bookworm (tags, no digest), both pinned to the builder platform; the Go binary is cross-compiled per target (`TARGETOS`/`TARGETARCH`, `-trimpath`, CGO disabled); the build copies `cmd`, `internal`, `skills` and the built UI |
| Base image (ko) | cgr.dev/chainguard/static (tag, no digest) |
| Runtime user | `USER 65532:65532`; pod `runAsNonRoot`, `runAsUser: 65532`, `fsGroup: 65532`, seccomp `RuntimeDefault` |
| Container securityContext | `allowPrivilegeEscalation: false`, `readOnlyRootFilesystem: true` (with a `/tmp` emptyDir), capabilities drop ALL; CPU limit 1, memory limit 512Mi (requests 25m CPU, 32Mi memory); `/tmp` emptyDir sizeLimit 256Mi |
| Persistent storage | SQLite file on PVC `ego-session-db` (1Gi, RWO, `helm.sh/resource-policy: keep`) mounted at `/data`; WAL mode; directory 0700, files 0600 |
| SQLite tables | `sessions`, `runs`, `run_results`, `eval_sets`, `role_bindings`, `revocations`, `senders`, `sender_tokens`, `llm_calls`, `online_evals`, `online_scores`, `online_alerts`, `online_findings`, `online_corrections`, `online_labels`, `online_state`; the earlier `tenants` and `tenant_tokens` tables are migrated into `senders`/`sender_tokens` and dropped. Without `--session-db`, senders, role bindings and revocations are held in memory only |
| Embedded skill | `skills/ego-rsi/SKILL.md`, compiled into the binary and served as the MCP prompt `rsi_loop`; also installable as a skill file |
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
| Data sent to models | User prompt, agent response, reference response, tool calls and rubrics extracted from traces; for findings: per evidence session the session id, score, first user message (500 characters), tool-call names (30), the metric's rationale (400) and up to 6 failing verdicts (300 each), for at most `findings.maxSessions` (default 8) sessions |
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
| Input/output filtering | Block markers inside quoted trace text are defanged and every judge call starts with a notice that quoted text is data; the findings prompt additionally replaces angle brackets, collapses whitespace and clips each quoted value; the findings answer is parsed as JSON, evidence naming an unquoted session id is dropped; chat sinks strip markup, mentions and control characters from model text and cut it to a length; no injection classifier or structured-output schema; Jev prompts are not covered |
| Prompt templates | Judge prompts embedded in `internal/judge` (ADK-derived); findings prompt in `internal/online/findings.go`; trace text interpolated into the prompt string after defanging |
| Skill / prompt served to agents | `rsi_loop` (MCP prompt, same text as `skills/ego-rsi/SKILL.md`): tells an agent to read findings, check evidence with `explain_session` / `explain_regression` / `summarize_session`, make one change per finding under a new `service.version`, call `register_correction`, later `list_corrections`; states that labels are for people and the agent must not label its own sessions; tools named in it: `get_quality`, `get_findings`, `explain_session`, `explain_regression`, `summarize_session`, `register_correction`, `list_corrections`, `label_session` |
| MCP tools (stdio and HTTP) | `list_metrics`, `list_sessions` (filters `agent`, `tenant`, `since`, `query`, `complete`), `summarize_session`, `list_runs`, `get_run_results`, `get_usage`, `get_quality`, `explain_regression`, `explain_session`, `get_findings`, `register_correction`, `list_corrections`, `label_session`; admin-only `list_role_bindings`, `set_role_binding`, `delete_role_binding`, `run_findings`. `evaluate_traces` exists on stdio only and reads only under `--trace-dir` (default: the current directory); over stdio the admin tools are registered only with `--admin-tools` |

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
| Write endpoints | `POST /api/evaluate`, `POST /api/evaluate/stream`, `POST /api/evalsets` (member or admin, not tenant-scoped); `POST /api/online/corrections` and `POST /api/online/labels` (member or admin, including tenant-limited roles for their own tenants; viewer and agent-scoped roles refused; bodies capped at 64 KiB and 16 KiB); `POST /api/online/findings/sweep` (admin); `POST /api/admin/roles`, `DELETE /api/admin/roles/{id}`, `POST /api/admin/revocations`, `POST /api/admin/senders`, `DELETE /api/admin/senders/{id}`, `POST /api/admin/senders/{id}/tokens`, `DELETE /api/admin/senders/{id}/tokens/{tokenId}` (admin); `GET` on `/api/admin/roles`, `/api/admin/revocations`, `/api/admin/senders`, `/api/admin/senders/{id}/tokens` (admin) |
| MCP write tools | `register_correction`, `label_session` (member or admin; tenant-limited roles for their own tenants); `set_role_binding`, `delete_role_binding`, `run_findings` (admin without agent or tenant limits) |
| Agent-scope enforcement | A scoped role sees only sessions, traces, runs and live-feed events involving one of its agents (results with no agent name are hidden); usage, eval sets and online views (scores, findings, corrections, labels, quality) answer 403 or an error to it, on REST and MCP alike. Unknown GitHub team membership is refused instead of given the default role |
| Tenant-scope enforcement | A tenant-scoped role sees only sessions, traces, usage, online series, alerts, findings, corrections, labels and live-feed events of its tenants, on REST and MCP alike; it reads runs and run results only when the run id appears in `online_evals` for one of its tenants (an hourly online run holds one tenant and one call kind); eval sets, budget status and evaluations answer 403; the shared judge budget is not shown in `get_quality` |
| Label agreement scope | Agreement per metric and the finding gate count the labels of every tenant; `GET /api/online/status`, which reports agreement, answers 403 to tenant-scoped and agent-scoped roles |
| Label and correction authorship | Label author is the authenticated identity (empty with no login); correction author `by` is a caller-supplied text of up to 200 characters; correction `what` up to 2000 characters, `version` up to 200 |
| Token types | Session cookie `ego_session` (kind `s`, 12 h, accepted only as the cookie); API token from `ego auth mint-token` (kind `t`, default 90 days, carries its own role and optional tenants, accepted only as a bearer value); live GitHub access token validated against org membership (5 min cache, 1000 entries, at most 8 concurrent lookups, only well-formed tokens reach GitHub); sender token `egs_…` (OTLP receivers, and `POST /mcp`) |
| Token revocation | By token id or by subject (every token issued to a subject up to that moment) via `POST /api/admin/revocations`; records held in memory and persisted in SQLite (`revocations`) when a session database is configured; logout revokes its cookie; rotating the session secret invalidates all cookies and API tokens; a sender token stops working when an admin deletes it |
| Signature scheme | `base64url(payload "|" hex(HMAC-SHA256(secret, payload)))` with payload `3|kind|id|issuedAt|expiry|role|tenants|username` (format version 3; role and tenants empty for cookies) |
| OAuth | Authorization-code flow, scope `read:org`, state in `ego_oauth_state` cookie (10 min), fixed redirect `/evals`, cookies `HttpOnly; Secure; SameSite=Lax` |

## 6. Network Surface

| Port | Protocol | Paths | Auth state |
|---|---|---|---|
| 8001 | HTTP | REST API (`/api/*`), UI (`/`), SSE `/stream/ui-updates`, WebSocket `/ws/ui-updates`, `/auth/*` | Session cookie or bearer when `EGO_SESSION_SECRET` is set; with `--no-auth` open (every caller admin) |
| 8001 | HTTP | `POST /mcp` (MCP streamable HTTP, stateless, `Content-Type: application/json` required; `GET` answers 405) | Session cookie, API token or GitHub token like `/api`; a sender ingest token also accepted; open (every caller admin) when no session secret is set |
| 8001 | HTTP | `/api/health`, `/auth/me`, `/auth/login`, `/auth/callback`, `/auth/logout` | Not gated |
| 4318 | HTTP | `POST /v1/traces` (protobuf or JSON, gzip) | Bearer token required; accepts the shared `--otlp-token` or a sender token (`egs_…`); with neither configured every push is refused, with a warning in the log |
| 4317 | gRPC | `TraceService/Export` | Same rule as 4318, token in `authorization` metadata |
| 9090 | HTTP | `GET /api/health` (status, version) | Never gated |
| Kubernetes Service | ClusterIP | 8001, 4318, 4317, 9090 (plus optional alias Services) | n/a |
| HTTPRoute (optional) | Gateway API | Prefix `/evals` with rewrite to `/`; root paths `/assets`, `/favicon.svg`, `/favicon.ico`, `/ws`, `/api/config`, `/api/convert`, `/api/evaluate`, `/api/evalsets`, `/api/admin`, `/api/health`, `/api/metrics`, `/api/runs`, `/api/usage`, `/api/online`, `/api/tenants`, `/api/streaming`, `/api/validate`, `/mcp`, `/stream`, `/auth` | Backend 8001 only; 4317, 4318, 9090 not routed; `rootRedirect` (chart default true) sends `/` on the host to the UI prefix |
| TLS | None in process | TLS terminated outside the pod | n/a |
| Cross-origin protection | POST/PUT/DELETE and WebSocket upgrades from a browser (including `POST /mcp`) must come from the server's own host, the public URL host or `X-Forwarded-Host` (403 otherwise); logout is POST-only; security headers (CSP, frame, nosniff, referrer) on 8001; the Host header is not checked | all of 8001 | n/a |
| HTTP server timeouts | Read-header, read, write (except the live feed and streaming evaluation) and idle timeouts on all HTTP listeners; gRPC keepalive and stream limits | n/a | n/a |
| Request size limits | OTLP/HTTP: 16 MiB on the wire, 64 MiB decoded (413); gRPC: 16 MiB receive; REST JSON bodies and multipart uploads capped; label bodies 16 KiB, correction bodies 64 KiB; list limits clamped; MCP argument bounds: `get_quality` hours 1 to 720, `get_usage` hours 1 to 2160 and top 1 to 100 | n/a | n/a |
| NetworkPolicy | Optional in the chart (`networkPolicy.enabled`, rules supplied by the deployer) | n/a | n/a |
| Defaults | Binary: listens on 127.0.0.1 (`:8001`, `:4318`, `:4317`, `:9090`); the image and chart set the listen addresses explicitly | n/a | n/a |

## 7. Dependency Vulnerabilities

| Ecosystem | Manifest | Scanner | Critical | High | Medium | Low | Unknown |
|---|---|---|---|---|---|---|---|
| Go | go.mod | trivy 0.56.2 | 0 | 0 | 0 | 0 | 1 |
| npm | ui/package-lock.json | trivy 0.56.2 | 0 | 0 | 0 | 0 | 0 |
| npm (all, incl. dev) | ui/package-lock.json | npm audit | 0 | 0 | 0 | 0 | 0 |
| npm (production deps only) | ui/package-lock.json | npm audit --omit=dev | 0 | 0 | 0 | 0 | 0 |

| Go advisory | Package | Installed | Fixed | Call-reachable (govulncheck) |
|---|---|---|---|---|
| GO-2026-5932 | golang.org/x/crypto | 0.57.0 | none | No |
| GO-2026-6809 | Go standard library, crypto/x509 | go1.27.2 | go1.27.3 | Yes (reported on the TLS verification path) |
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
| govulncheck | 1.7.0 with go1.27.2 | 1 standard-library advisory affected (GO-2026-6809), 1 module advisory not called (GO-2026-5932) |
