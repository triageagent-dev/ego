# Application Passport — ego

## 1. Identity

| Field | Value |
|---|---|
| Component | ego |
| Description | Scores AI agent behavior from OpenTelemetry traces: REST API, embedded web UI, OTLP receivers, offline CLI, MCP server |
| Repo | github.com/triageagent-dev/ego |
| Registry | ghcr.io/triageagent-dev/ego (image); oci://ghcr.io/triageagent-dev/charts (Helm chart) |
| Version | 0.1.0 (Chart appVersion); build version injected at link time via `internal/api.Version` |
| Owner | triageagent-dev (maintainer: Denys Vasyliev) |
| License | Apache-2.0 |
| Passport date | 2026-10-20 |
| Source revision | f7510c2 (main) |
| Audit status | HIGH findings open |
| Code review | Findings open (2026-10-20) |

## 2. Classification

| Field | Value |
|---|---|
| Class | Service (HTTP/gRPC server) and CLI, one binary |
| Type | Evaluation service; uses LLMs as judges (no autonomous agent loop) |
| Subcommands | `run`, `serve`, `auth mint-token`, `mcp` (stdio), `version` |
| Capabilities | Trace ingest (OTLP/HTTP, OTLP/gRPC, Jaeger JSON, OTLP JSON/JSONL); metric scoring; LLM-judge scoring; session store; run history; eval sets; token usage ledger; online evaluation with alerts and notifications; role-based access control |
| Model role | Judge / evaluator of recorded agent traces |
| Runtime | Single static Go binary (Go 1.25, CGO disabled); Kubernetes Deployment (1 replica, Recreate) or local process |
| Public accessibility | Chart default: ClusterIP Service, HTTPRoute disabled; optional Gateway API HTTPRoute (UI prefix `/evals` plus root paths) |
| Production data access | Read/write to its own SQLite database; receives agent traces pushed by producers; read-only calls to GitHub, Gemini/Vertex AI and the Jev API |
| PII | Traces carry user prompts, agent responses, tool-call arguments and results; stored verbatim |
| Sensitivity | Trace content stored on disk for the retention window (default 720h); GitHub logins and team memberships |
| Authentication mode | Off unless a session secret is set; chart default `auth.enabled: true` |

## 3. Artifact

| Field | Value |
|---|---|
| Form | Container image (Dockerfile and ko), release binaries, Helm chart |
| Language | Go 1.25.5 (go.mod); TypeScript/React UI (Vite, Ant Design) embedded with `go:embed` |
| Build tool | `go build`; Docker multi-stage; `ko` (Makefile `image`); GitHub Actions release workflow |
| Release platforms | linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64 (binaries); linux/amd64 + linux/arm64 (image via Actions); linux/amd64 (ko) |
| Base image (Dockerfile) | gcr.io/distroless/static-debian12:nonroot (tag, no digest); build stages node:22-bookworm-slim, golang:1.25-bookworm (tags, no digest) |
| Base image (ko) | cgr.dev/chainguard/static (tag, no digest) |
| Runtime user | nonroot (uid 65532); pod `fsGroup: 65532` |
| Container securityContext | Not set in chart |
| Persistent storage | SQLite file on PVC `ego-session-db` (1Gi, RWO, `helm.sh/resource-policy: keep`) mounted at `/data`; WAL mode; directory created 0755 |
| Install script | `install.sh`: downloads release tarball and `checksums.txt` from GitHub Releases, compares SHA-256 |
| CI/CD | `.github/workflows/ci.yml` (push/PR to main); `release.yml` (tags `v*.*.*`, manual dispatch) |
| Workflow permissions | release.yml: `contents: read` at top level; `contents: write` (release job), `packages: write` (image, chart jobs); ci.yml: no `permissions` block |
| Action references | Version tags (`@v3`..`@v6`); no commit SHAs |
| Signing / attestation | Release `checksums.txt` (sha256sum); no signatures, SBOM or provenance attestations published |
| Tracked binary | `ego` (Mach-O arm64 executable, 36 MB) at repo root, in git since the first commit |

## 4. AI / LLM Supply Chain

| Item | Detail |
|---|---|
| Models: Gemini judge | Default `gemini-2.5-flash`; model name overridable per evaluator in the request |
| Models: Vertex AI Managed Eval | `safety_v1`, `multi_turn_task_success_v1`, `multi_turn_trajectory_quality_v1`, `multi_turn_tool_use_quality_v1`; fixed managed backend |
| Models: Jev decision model | Default `jev-1.13` (sent as `typesafe/jev-1.13`); override env `JEV_MODEL` or `--jev-model` |
| Local (non-LLM) metrics | `tool_trajectory_avg_score`, `response_match_score` (ROUGE-1) |
| LLM auth: Gemini | Vertex AI via Application Default Credentials (chart sets `GOOGLE_GENAI_USE_VERTEXAI=true`, project, location); or API key `GEMINI_API_KEY` / `GOOGLE_API_KEY` / `--judge-api-key` |
| LLM auth: Vertex eval | Application Default Credentials, cloud-platform scope; no key mode |
| LLM auth: Jev | Bearer `JEV_API_KEY` (env or `--jev-api-key`); chart reads it from Secret `ego-jev` (optional) |
| Data sent to models | User prompt, agent response, reference response, tool calls and rubrics extracted from traces |
| Egress: github.com | OAuth authorize and code exchange (client secret) |
| Egress: api.github.com | `/user`, `/orgs/{org}/members/{user}`, `/user/teams` with the signed-in user's token |
| Egress: Gemini | `{location}-aiplatform.googleapis.com` (Vertex) or `generativelanguage.googleapis.com` (API key) |
| Egress: Vertex Managed Eval | `https://{location}-aiplatform.googleapis.com/v1beta1/projects/{project}/locations/{location}/:evaluateInstances` |
| Egress: Jev | `https://openrouter.ai/api/alpha/decisions` (override `JEV_API_URL`) |
| Egress: notification sinks | Discord webhook (URL from env, e.g. `EGO_DISCORD_WEBHOOK`), generic webhook, event sink; optional bearer token read from a file on each send |
| Egress: MCP client | `--server-url` / `EGO_SERVER_URL` (default `http://localhost:8001`); `gh auth token` output used as bearer when no token is supplied |
| Online evaluation cap | `budget.judgeTokensPerDay` and `budget.jevCallsPerDay` per UTC day (chart default 2000000 / 1000); sampling `perKindPerHour` |
| Interactive evaluation cap | No global token budget, request rate limit or per-user quota on `/api/evaluate` and `/api/evaluate/stream` |
| Per-call output cap | No `MaxOutputTokens` set on Gemini judge calls; samples per invocation default 5, request-controlled |
| Input/output filtering | No injection classifier or content filter on trace text placed into judge prompts |
| Prompt templates | Judge prompts embedded in `internal/judge` (ADK-derived); trace text interpolated into the prompt string |
| MCP tools | `list_metrics`, `evaluate_traces`, `list_sessions`, `summarize_session`, `list_runs`, `get_run_results`, `list_role_bindings`, `set_role_binding`, `delete_role_binding` |

## 5. Privileges

| Scope | Grant |
|---|---|
| Kubernetes ServiceAccount | `ego` (created by chart); no Role, ClusterRole or bindings in chart; `automountServiceAccountToken` not set (default) |
| GCP identity | Optional Workload Identity annotation on the ServiceAccount; Vertex AI access is whatever the bound service account holds |
| Secrets read (env via secretKeyRef) | `EGO_GITHUB_CLIENT_SECRET` (Secret `ego-github-oauth`), `EGO_SESSION_SECRET` (Secret `ego-session-secret`), `JEV_API_KEY` (Secret `ego-jev`, optional), `EGO_DISCORD_WEBHOOK` (optional) |
| Projected token | Optional service-account token (configurable audience, 3600 s) at `/var/run/secrets/ego/webhook/token` |
| Filesystem | Read/write `/data` (PVC); read-only `/etc/ego` (online config ConfigMap) |
| Application roles | `admin` (full, manages role bindings), `member` (read/write), `viewer` (read-only); default role `member` (`EGO_DEFAULT_ROLE`) |
| Role binding subjects | GitHub user login or GitHub team `org/slug`; optional per-binding agent-name scope |
| Bootstrap admins | `EGO_ADMIN_GITHUB_USERS` / chart `adminGithubUsers` |
| Write endpoints | `POST /api/evaluate`, `POST /api/evaluate/stream`, `POST /api/evalsets` (member or admin); `POST /api/admin/roles`, `DELETE /api/admin/roles/{id}` (admin) |
| Agent-scope enforcement | Applied to `/api/runs`, `/api/runs/{id}`, `/api/runs/{id}/results` |
| Token types | Signed session cookie `ego_session` (12 h); signed bearer token from `ego auth mint-token` (default 3650 days); live GitHub access token validated against org membership (5 min cache) |
| Signature scheme | `base64url(username "|" expiry "|" hex(HMAC-SHA256(secret, username "|" expiry)))` |
| OAuth | Authorization-code flow, scope `read:org`, state in `ego_oauth_state` cookie (10 min), fixed redirect `/evals`, cookies `HttpOnly; Secure; SameSite=Lax` |

## 6. Network Surface

| Port | Protocol | Paths | Auth state |
|---|---|---|---|
| 8001 | HTTP | REST API (`/api/*`), UI (`/`), SSE `/stream/ui-updates`, WebSocket `/ws/ui-updates`, `/auth/*` | Session cookie or bearer when `EGO_SESSION_SECRET` is set; open (every caller admin) when unset |
| 8001 | HTTP | `/api/health`, `/auth/me`, `/auth/login`, `/auth/callback`, `/auth/logout` | Not gated |
| 4318 | HTTP | `POST /v1/traces` (protobuf or JSON, gzip) | Never gated |
| 4317 | gRPC | `TraceService/Export` | Never gated |
| 9090 | HTTP | `GET /api/health` (status, version) | Never gated |
| Kubernetes Service | ClusterIP | 8001, 4318, 4317, 9090 (plus optional alias Services) | n/a |
| HTTPRoute (optional) | Gateway API | Prefix `/evals` with rewrite to `/`; root paths `/assets`, `/favicon.svg`, `/favicon.ico`, `/ws`, `/api/config`, `/api/convert`, `/api/evaluate`, `/api/evalsets`, `/api/admin`, `/api/health`, `/api/metrics`, `/api/runs`, `/api/usage`, `/api/online`, `/api/streaming`, `/api/validate`, `/stream`, `/auth` | Backend 8001 only; 4317, 4318, 9090 not routed |
| TLS | None in process | TLS terminated outside the pod | n/a |
| WebSocket origin check | `CheckOrigin` returns true | `/ws/ui-updates` | Behind 8001 auth |
| HTTP server timeouts | `http.ListenAndServe` defaults on all HTTP listeners | n/a | n/a |
| Request size limits | OTLP/HTTP: none; gRPC: library default receive size; multipart uploads: 32 MiB parsed form, 10 MiB per file read | n/a | n/a |
| NetworkPolicy | None in chart | n/a | n/a |
| Defaults | Binary: listens on all interfaces (`:8001`, `:4318`, `:4317`, `:9090`) | n/a | n/a |

## 7. Dependency Vulnerabilities

| Ecosystem | Manifest | Scanner | Critical | High | Medium | Low | Unknown |
|---|---|---|---|---|---|---|---|
| Go | go.mod | trivy 0.56.2 | 0 | 3 | 6 | 0 | 1 |
| npm | ui/package-lock.json | trivy 0.56.2 | 0 | 0 | 0 | 0 | 0 |
| npm (all, incl. dev) | ui/package-lock.json | npm audit | 0 | 5 | 2 | 2 | 0 |
| npm (production deps only) | ui/package-lock.json | npm audit --omit=dev | 0 | 0 | 0 | 0 | 0 |

| Go advisory detail | Package | Installed | Fixed | Call-reachable (govulncheck) |
|---|---|---|---|---|
| CVE-2026-56854 | golang.org/x/crypto | 0.54.0 | 0.55.0 | No |
| CVE-2026-56855, CVE-2026-78662 | golang.org/x/crypto | 0.54.0 | 0.56.0 | No |
| GO-2026-5932 | golang.org/x/crypto | 0.54.0 | none | No |
| CVE-2026-78669, CVE-2026-78659, CVE-2026-78660, CVE-2026-78663, CVE-2026-97032 | golang.org/x/net | 0.57.0 | 0.60.0 | Yes (http2 server paths) |
| CVE-2026-56851 | golang.org/x/text | 0.40.0 | 0.41.0 | No |
| Standard library (12 symbol-level, 1 package-level) | go1.27.1 local toolchain | go1.27.1 | go1.27.2 | Yes |

| Tool | Version | Output |
|---|---|---|
| syft | 1.46.0 | CycloneDX 1.6 JSON, `bin/sbom/sbom-go.cdx.json`, `bin/sbom/sbom-npm.cdx.json` (scratch, not committed) |
| trivy | 0.56.2 | vuln scan of both lockfiles; secret and misconfig scan of repo |
| govulncheck | local | 12 stdlib symbol findings, 1 package finding, 5 module findings not called; GO-2026-6443 not reported |
