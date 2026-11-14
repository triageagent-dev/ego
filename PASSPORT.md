# Application Passport — ego

## 1. Identity

| Field | Value |
|---|---|
| Component | ego |
| Description | Scores AI agent behaviour from OpenTelemetry traces: OTLP receivers, REST API, embedded web UI, online evaluation with LLM judges, findings and corrections for self-correcting agents, MCP server (stdio and HTTP), offline CLI |
| Repo | github.com/triageagent-dev/ego |
| Registry | ghcr.io/triageagent-dev/ego (image); oci://ghcr.io/triageagent-dev/charts (Helm chart) |
| Version | v0.7.0; Helm chart 0.7.0, appVersion 0.7.0 (chart tag chart-v0.7.0 on the same commit) |
| Owner | triageagent-dev |
| License | Apache-2.0 |
| Passport date | 2026-11-06 |
| Source revision | 705153b (v0.7.0) |
| Audit status | Audit of 2026-11-06 at revision 65bed3f (v0.6.4 plus one commit, whole tree) found 0 Critical, 0 High, 4 Medium, 10 Low, 4 Info. v0.7.0 carries the changes made for every finding that named a code change (sign-in paths, release gate, approval of suggested corrections, OTLP TLS, audit log, session lifetime, GitHub lookup limits, redaction, install script removed); the rest are informational or accepted. No Critical or High dependency vulnerability |
| Code review | Done 2026-11-06 for the v0.7.0 changes (the fixes of the audit findings), by a second reviewer over the whole range; its findings are fixed in the same release. Code before 65bed3f last reviewed at 462973d (v0.4.18, 2026-10-28) and earlier at 299a9ae and b41e326 |

## 2. Classification

| Field | Value |
|---|---|
| Class | Service (single-replica Deployment, SQLite archive on a PersistentVolumeClaim); CLI and stdio MCP server in the same binary |
| Type | Agent-evaluation service that calls LLMs as judges; ships one agent skill (ego-rsi) |
| Subcommands | `run`, `serve`, `auth mint-token`, `mcp` (stdio), `version` |
| Capabilities | Receive OTLP/gRPC and OTLP/HTTP traces; group traces into sessions, threads and tenants; score sessions online and interactively with LLM judges and built-in metrics; write findings and verify corrections, with a person's approval gate on judge-written suggestions; per-tenant role-based access; MCP tools (26) over HTTP at /mcp and over stdio; notifications to Discord, webhook and event sinks |
| Model role | Judge: scores session text against rubrics, hallucination checks and golden references; diagnosis: writes findings from the lowest-scoring sessions; decision model (jev_* metrics) |
| Runtime | Kubernetes (Helm chart) or container or local binary |
| Public accessibility | Not exposed by default (ClusterIP Service; HTTPRoute disabled by default); optional Gateway API HTTPRoute |
| Prod data access | Read and write on its own SQLite archive; holds trace content sent by senders (user prompts, agent responses, tool calls and results) |
| PII | Trace content may include end-user text; GitHub logins of signed-in users. Credentials and email addresses in trace text are masked before the text leaves for a model (on by default); the stored traces are unchanged |
| Sensitivity | Trace contents of every tenant; signing secret for session and API tokens; OAuth client secret; ingest tokens (stored as SHA-256 hashes) |
| Authentication mode | A session secret enables GitHub OAuth (org members), signed cookies, API and delegate tokens and roles; without one `serve` listens on loopback only unless `--no-auth`. Default role for a signed-in user with no binding: `none` in the chart, `member` in the binary (`serve` warns when every member gets a role). A raw GitHub token of an org member signs in on `/mcp` only. On `/mcp` only, a sender ingest token minted with `mcp` authenticates as a member limited to its sender's tenants, and a delegate token names one covered tenant per call |

## 3. Artifact

| Field | Value |
|---|---|
| Form | Container image (multi-arch linux/amd64, linux/arm64); release binaries (linux and darwin, amd64 and arm64) with signed checksums; Helm chart (OCI). No install script: a release is verified from its checksums and signatures |
| Language | Go 1.26 (toolchain go1.27.3); TypeScript with React 19, Vite 7 and Ant Design 6 (UI embedded in the binary) |
| Build tool | Dockerfile (Node 22 UI stage, Go build stage, CGO disabled); GitHub Actions; Helm |
| Base image | gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab |
| Build images | node:22-bookworm-slim@sha256:de170b39439eb0e5a3444ad7d60b015614a381e10fecae2e30ec9e735122f38d; golang:1.27-bookworm@sha256:972b4110ba0f47539a975c31e52c64765e1ff19f2d74469ae4a595c6676e80a5 |
| Runtime user | 65532:65532 |
| Signing | Keyless cosign signatures (GitHub OIDC) on the image digest, on the chart digest and on the release checksums file; image published with provenance and SBOM attestations |
| SBOM | CycloneDX 1.6 via syft: Go module 44 components; UI lockfile 144 components |
| Release workflows | release.yml (tag v*.*.*), release-chart.yml (tag chart-v*.*.*); both wait, through the reusable ci-gate.yml, for the CI checks (build, vet, gofmt, tests, vulnerability scan) of the tagged commit and publish nothing when one failed; all third-party actions pinned by commit SHA |

## 4. AI / LLM Supply Chain

| Item | Value | Auth | Note |
|---|---|---|---|
| Judge model | Gemini via google.golang.org/genai; default gemini-3-flash-preview; any model name matching the judge-model pattern may be requested per evaluation | Vertex AI with Application Default Credentials (Workload Identity) when vertex.enabled; Gemini API key from the environment otherwise | Per-call output cap 8192 tokens |
| Vertex managed evaluation | Vertex AI :evaluateInstances (safety and multi-turn metrics) | Application Default Credentials | Not metered by the online token budget; refused in online profiles |
| Decision model (jev_* metrics) | Endpoint set by decision.url; default hosted endpoint https://openrouter.ai/api/alpha/decisions | Bearer key from JEV_API_KEY, or the pod's projected ServiceAccount token file; for Google's Gemini Decisions API (generativelanguage.googleapis.com) the key in x-goog-api-key | Up to 5 attempts per call |
| Prompt hardening | Untrusted-data notice at the head of every judge prompt; prompt block markers neutralised in quoted trace text; credentials (tokens, API keys, JWTs, private keys, Authorization values, password-style key=value pairs) and email addresses masked as [REDACTED] in the judge prompt, the decision request and the Vertex request (online config `redact`: off, keep emails, extra patterns) | n/a | Applies to judge, findings, recalibration and decision-model calls built from trace text; masks known shapes only |
| Suggested corrections | A finding's judge-written suggestion is withheld from every token holder and from notification sinks until an admin signed in at the UI approves that exact text (`findings.requireApproval`, on by default; SHA-256 bound; a rewritten suggestion is unapproved again) | Session cookie only; no MCP tool can approve | The diagnosis, evidence and history are not withheld |
| Budgets | Interactive evaluation: daily judge-token cap (default 5,000,000), per-member share 1/4, 2 concurrent and 10 per minute per member (admin exempt), 4 concurrent overall, per-model weight and cap; online evaluation: judgeTokensPerDay and jevCallsPerDay (chart default 2,000,000 and 1,000), restored from the archive after a restart | n/a | |
| Agent skill | skills/ego-rsi/SKILL.md, embedded in the binary: instructs an agent to read findings over MCP, change itself, register the correction and wait for verification | Caller's own MCP credential | Skill text marks tool output as data, tells the agent never to label its own sessions, and that an unapproved suggestion waits for a person in the Quality view |
| Egress: GitHub | github.com (OAuth authorize and token), api.github.com (user, org membership, teams) | OAuth client secret; user access token | Only when GitHub sign-in is configured; a source whose tokens are refused 30 times in a minute gets no further lookups that minute; a lookup that got no answer is neither cached nor counted |
| Egress: judge | Vertex AI regional or global endpoint; generativelanguage.googleapis.com with an API key | ADC or API key | Carries session text, redacted |
| Egress: decision | decision.url | Bearer key, projected token, or x-goog-api-key (Google) | Carries session text and eval state, redacted |
| Egress: sinks | Discord webhook, generic webhook, event endpoint (operator-configured URLs) | Webhook URL secret or bearer token file | Plain http with a token file only to loopback or in-cluster Service names |
| Egress: MCP client (`ego mcp`) | `--server-url` / `EGO_SERVER_URL` (default http://localhost:8001), the REST API | `--session-token` / `EGO_SESSION_TOKEN` only, sent to https servers or loopback; no `gh` fallback | A raw GitHub token is not a REST credential |

## 5. Privileges

| Scope | Grant | Note |
|---|---|---|
| Kubernetes RBAC | None: chart creates a ServiceAccount only, no Role or ClusterRole | automountServiceAccountToken false |
| Projected tokens | Optional audience-bound ServiceAccount tokens for the decision gateway and the webhook sink | Off unless audience is set |
| Pod security | runAsNonRoot, runAsUser 65532, fsGroup 65532, seccomp RuntimeDefault, allowPrivilegeEscalation false, readOnlyRootFilesystem true, capabilities drop ALL | |
| Volumes | PersistentVolumeClaim at /data (1Gi default, kept on uninstall); emptyDir /tmp (256Mi); read-only ConfigMaps for eval limits and online config; optional read-only TLS Secret at /etc/ego/otlp-tls | |
| Secrets read | GITHUB_CLIENT_SECRET, SESSION_SECRET, OTLP_TOKEN (optional), JEV_API_KEY (optional), DISCORD_WEBHOOK_URL (optional) via secretKeyRef; otlpTLS.secretName (optional, kubernetes.io/tls) as a volume | |
| Cloud IAM | Set by the operator through a Workload Identity annotation on the ServiceAccount | Not defined in the chart |
| Application roles | admin, member, viewer; default role for a signed-in user with no binding: none in the chart, member in the binary (startup warning) | Bindings by GitHub login or team, with optional agent and tenant limits |
| Bootstrap | --admin-github-users grants admin once per login at startup | |
| Credential kinds | Session cookie (HMAC, 8 h; revoking the subject ends it and makes GitHub be asked again about the user); API token (HMAC, role and tenants inside, default 90 days); delegate token (/mcp only, one tenant per call); sender ingest token (egs_ prefix, SHA-256 hashed at rest, optional tenant binding, optional /mcp read); shared OTLP token; raw GitHub access token of an org member (/mcp only, cached 5 min by hash) | |
| Admin-only operations | Role bindings, sender and ingest-token minting and revocation, token and subject revocation, tenant rename, purge and merge, eval limits, manual findings sweep, review of a suggested correction (session only) | Enforced per handler and per MCP tool |
| Audit trail | Every change of access (role binding set or removed, sender created or removed, ingest token minted or revoked, revocation recorded) and every finding review writes one `audit` log line with the acting identity and the object, by REST or MCP, whatever `--audit-log` is set to; never a token value. `--audit-log` adds the sign-in trail by GitHub login (off by default) | |

## 6. Network Surface

| Port | Protocol | Purpose | Auth state |
|---|---|---|---|
| 8001 (listenPort) | HTTP | UI, REST API, SSE and WebSocket live feed, /mcp | Session cookie or bearer token on every path except /api/health, /auth/me, /auth/login, /auth/callback, /auth/logout, /auth/signed-out; a raw GitHub token on /mcp only; refuses to start on a non-loopback address without a session secret unless --no-auth is set |
| 4318 | HTTP | OTLP/HTTP POST /v1/traces (protobuf or JSON, gzip) | Bearer: shared token or sender ingest token; every push refused when neither exists |
| 4317 | gRPC | OTLP/gRPC trace service | Bearer: shared token or sender ingest token; checked before the request body is read |
| 9090 | HTTP | GET /api/health | None |
| Transport | Optional TLS on 4317 and 4318 (`--otlp-tls-cert`/`--otlp-tls-key`, chart `otlpTLS.secretName`; TLS 1.2 minimum; the pair is re-read when the files change, at most every 30 s); none in the process on 8001 and 9090 (a gateway terminates TLS) | | |
| Service | ClusterIP exposing 8001, 4318, 4317, 9090 | | |
| HTTPRoute | Optional (httpRoute.enabled false by default); routes the UI prefix and the listed root paths including /api/admin and /mcp to port 8001; OTLP ports not routed | Chart refuses auth.enabled=false with a route unless allowOpenRoute is set | |
| NetworkPolicy | Optional (networkPolicy.enabled false by default); ingress rules supplied by the operator | | |
| Request limits | OTLP body 16 MiB received and 64 MiB decoded; JSON body 8 MiB; upload 64 MiB; /mcp body 1 MiB; gRPC 64 concurrent streams | | |
| Abuse limits | Per source address, 30 refused ingest tokens or GitHub tokens in a minute end that source's attempts for the rest of the minute (429 "slow down"; at most 4096 sources tracked); `--trusted-proxies` (chart `trustedProxies`) names the gateways whose X-Forwarded-For tells clients apart, otherwise the header is ignored | | |
| Browser protections | Cookie HttpOnly, Secure, SameSite=Lax; origin check on state-changing requests and WebSocket upgrades; CSP, X-Frame-Options DENY, nosniff | | |

## 7. Dependency Vulnerabilities

| Ecosystem | Source | Tool | Critical | High | Medium | Low | Unknown | Note |
|---|---|---|---|---|---|---|---|---|
| Go | go.mod | trivy | 0 | 0 | 0 | 0 | 1 | GO-2026-5932 golang.org/x/crypto 0.58.0 (openpgp package unmaintained) |
| Go | go.mod | govulncheck | 0 | 0 | 0 | 0 | 0 | 0 reachable; 2 in required modules not called: GO-2026-6911 mark3labs/mcp-go (no fixed version), GO-2026-5932 |
| npm | ui/package-lock.json | trivy | 0 | 0 | 0 | 0 | 0 | |
| npm | ui/package-lock.json | npm audit | 0 | 0 | 10 | 0 | 0 | Development dependencies only (eslint chain, one advisory GHSA-g3hv-75wg-m249 in minimatch); 0 with --omit=dev |

Scanned 2026-11-06 at 65bed3f; no dependency changed between that revision and v0.7.0.
