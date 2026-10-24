# Security Audit — ego

Date: 2026-10-24. Revision audited: 299a9ae (v0.1.57). Focus: the changes since b41e326 (v0.1.49-3), plus a general pass over the MCP route, session secret handling, SQL building, request limits, SSRF and command execution surfaces, the Helm chart, the Dockerfile and the release path.

## Scope

| Area | Covered | Method |
|---|---|---|
| Ingest auth and tenancy (senders, ingest auth, session auth, role resolution, bound tokens) | Yes | Source review of both OTLP paths and `/mcp`; scratch tests that were run and then deleted |
| Tenant display names | Yes | Source review, UI render sites |
| Purge of tenants | Yes | Source review, schema check of every table that carries a tenant |
| Activity and turns endpoints | Yes | Source review against the session list |
| Usage ledger cache columns and startup backfill | Yes | Source review; synthetic archive of 3000 sessions with 300000 spans (86 MiB) timed on a laptop |
| MCP route and tools, session secret and token handling, SQL building, body limits, outbound requests, command execution | Yes | Source review, pattern search over the Go tree |
| Helm chart, Dockerfile, workflows, release script, install script | Yes | File review, trivy config |
| Dependencies | Yes | syft 1.46.0 (CycloneDX 1.6), trivy 0.56.2, govulncheck 1.7.0, npm audit, gitleaks |
| `go vet ./...`, `gofmt -l .`, `go test ./...` | Yes | Clean, nothing printed, all packages pass (including `TestMCPFindingToolsOverTheEndpoint`) |
| AI controls (ego calls LLM judges) | Yes | Checklist below |
| Source-level review | Yes | The above |
| Not covered | - | A running deployment, the live OAuth flow against GitHub, the judge and findings prompts beyond a skim of the defanging, the React UI beyond the render sites of tenant names and links, concurrency races under load (reasoned from the code, not run), load behaviour of the activity endpoints (not measured), the content of the Helm chart in a real cluster, the Dependabot configuration's effect |

AI controls (ego uses models as judges): user authorization before a model call is present (`CanWrite`, tenant-scoped roles refused on evaluation); per-call output cap (8192) and global daily token budgets (interactive and online) are present; a concurrency limit of 4 is present; there is no per-user or per-session rate limit and no injection classifier (F15).

## Findings

| # | Severity | Finding | Source |
|---|---|---|---|
| F1 | Medium | A token bound to one tenant can write into, and then read, a session of another tenant of the same sender when tenant labels contain a colon: session ids are built from unescaped parts and ownership is checked per sender only | internal/api/sessions.go:1107, internal/api/segments.go:505, internal/api/senders.go:181, internal/api/roles.go:303 |
| F2 | Medium | The index separator (0x1F) is not removed from tenant labels or agent names, so an unbound sender can make its sessions appear under another sender's tenant in the paged list, Activity, Turns and the tenant switcher | internal/api/sessionindex.go:24, :99-112; internal/api/sessions.go:249-273 |
| F3 | Medium | The cache-split migration and the ledger backfill read the whole archive before the health listener starts, keep their results in memory and commit once; on a large archive the liveness probe can kill the pod before the migration finishes, repeatedly | internal/api/sessions.go:537-600, internal/api/usagestore.go:196-218, internal/api/server.go:73, charts/ego/templates/deployment.yaml:143 |
| F4 | Medium | Purge of tenants leaves findings (which quote user messages), corrections, alerts and the tenant's scores in kept sessions; the purge has no dry run and no audit record | internal/api/purge.go:86-148, internal/api/onlinestore.go:56-100 |
| F5 | Medium | No per-sender quota on open sessions, memory, disk, tenant labels or names: one sender token can exhaust the single pod for every tenant | internal/api/sessions.go:834-872, internal/api/tenantnames.go:50-77 |
| F6 | Low | Turns and Activity do archive reads and trace rebuilds under the store-wide lock, up to 80 sessions per request, for any authenticated viewer | internal/api/turns.go:226-243, :245-281 |
| F7 | Low | The shared `--otlp-token` has no tenant namespace: its labels can spell another sender's tenant key | internal/api/senders.go:37-45 |
| F8 | Low | Tenant names accept format characters (bidirectional overrides, zero-width), a name over 80 bytes is dropped silently, the names table has no bound or cleanup, and Fill checks then writes without a lock across both steps | internal/api/tenantnames.go:40-88 |
| F9 | Low | A bound-token tenant may be `*` or contain `/`; `*` turns the token's `/mcp` grant into `<sender>/*` | internal/api/senders.go:181-186, internal/api/roles.go:536-540 |
| F10 | Low | No Host check: with no session secret (every caller admin) on loopback, a DNS-rebinding page passes the origin guard | internal/api/origin.go:34-53, cmd/ego/env.go:50-66 |
| F11 | Low | Chart default role is `member`: any signed-in organisation member can read and write every tenant unless bindings or `none` are set | charts/ego/values.yaml:58 |
| F12 | Low | Releases are made on a maintainer machine without signatures or attestations; CI and release workflows are manual only; `install.sh` trusts a checksum from the same release; images are pinned by tag; CI runs `govulncheck@latest`; the release script puts the token in a git argument | scripts/release-local.sh:36, .github/workflows/ci.yml:3-8,113, install.sh:42-44, Dockerfile:5,19,42 |
| F13 | Low | Usage ledger: the span id is the only key, so two senders that reuse a span id lose rows silently, and negative or huge token values are stored and summed | internal/api/usagestore.go:50,231; internal/api/turns.go:91-104 |
| F14 | Low | Purge race and ordering: a snapshot taken before the purge can be written back by the persistence loop, memory is purged before the archive, and a failed name clear returns 500 after the data is gone | internal/api/purge.go:35-82,189-194; internal/api/sessions.go:787-815 |
| F15 | Low | AI controls: no per-user or per-session rate limit on model-calling endpoints beyond a global concurrency of 4 and a shared daily cap; no injection classifier; Jev prompts are not defanged | internal/api/evalguard.go:63-100, internal/judge/client.go:82-99 |
| F16 | Low | NetworkPolicy is off by default and the ingest ports are ClusterIP-reachable from the whole cluster; failed ingest authentication is not throttled | charts/ego/values.yaml:117, internal/api/ingestauth.go:41-54 |
| F17 | Info | Sessions are visible to a role when any one of their tenants is granted, and the whole session is returned, as the session list always did; Activity, Turns and Threads list every tenant key on such a session | internal/api/roles.go:303-318, internal/api/activity.go:45-58 |
| F18 | Info | Dependencies: one High in a UI development dependency (zod 4.3.6, memory exhaustion in validation of oversized collections), absent from the production bundle; one Go advisory in golang.org/x/crypto openpgp, not called | ui/package-lock.json, go.mod |
| F19 | Info | docs/STATUS.md still lists GO-2026-6443 (grpc) as reachable through the OTLP/gRPC receiver; govulncheck no longer reports it | docs/STATUS.md:92-95 |

### Finding details

**F1 — Medium — bound token and session id ambiguity.**
Evidence: the bound token overwrites the tenant attribute of every span (sessions.go:1054-1062), so the span label cannot be chosen. The session name is then built as `sender:label:name` (sessions.go:1107) with no escaping, and an existing session is reused when its sender matches (segments.go:505), with no check of its tenant. `ValidTenantLabel` allows `:` (senders.go:181). With sender `core`, a token bound to `t1:x` sending session name `y`, and a token bound to `t1` sending session name `x:y`, both produce `core:t1:x:y`. The test I ran (deleted afterwards) produced one session with tenants `[core/t1:x core/t1]`, and `AllowsSession` returned true for a role limited to `core/t1`. A role is let in when any one tenant of a session is granted (roles.go:303-318), so the holder of the `t1` token reads the whole `t1:x` session over `/mcp` (`summarize_session`, `get_session_spans`) and the spans it injected sit in the `t1:x` data (scores, usage by session).
Impact: the binding does not hold between two tenants of one sender whose labels are prefixes of each other with a colon; it needs the attacker to know the victim's session name or id. Tenants whose labels are plain ids with no colon are not affected.
Recommendation: R1 and R2.

**F2 — Medium — index separator injection.**
Evidence: `sessionTenants` returns the label as sent (sessions.go:249-273); the index stores `0x1F` + keys joined by `0x1F` and reads them back with `strings.Split` (sessionindex.go:99-112). A label `x<0x1F>victim/t` from sender `evil` is one key in memory and two on the way out of the index; I confirmed `["evil/x" "victim/t"]` with a scratch test. The paged list (`Page`), the activity listing and the tenant counts use the split values, so the session shows in the list of a role granted `victim/t`, and `victim/t` gains a count in `/api/tenants`. Opening it answers not found (the summary loaded from the archive carries the single key), so there is no read of the victim's data. The same applies to agent names (`gen_ai.agent.name`) and agent-scoped roles. A bound token is not affected, since its tenant is fixed by an admin and control characters are refused when it is minted. Purge of `victim/t` does not remove such a session (not all its keys are in the set).
Impact: any sender can put content in front of another tenant's readers, including agents that read `list_sessions` or `list_threads` over MCP (indirect prompt injection through an agent name or session id), and can create phantom tenants in the switcher.
Recommendation: R3.

**F3 — Medium — startup migration cost.**
Evidence: `LoadPersisted` runs before any listener starts (server.go:73, health listener goroutine at server.go:210). On a ledger with rows from before v0.1.56 it reads every archived session blob to fill the cache split and collects all span ids into one map (sessions.go:568-583), then commits in a single transaction (usagestore.go:196-218); with an empty ledger it collects every usage row into one slice (sessions.go:547-562); kindless rows trigger a third pass (sessions.go:587-600). The liveness probe starts at 5 s with a 15 s period and the default threshold of 3, and there is no startup probe (deployment.yaml:137-148). Measurement: an 86 MiB archive (3000 sessions, 300000 spans) took 3.5 s per pass and allocated about 1.4 GiB in total on a laptop; the chart's volume is 1 GiB, the memory limit 512 MiB and the CPU limit one core, so on a full volume a pass of about 40 s or more is plausible, and passes stack up against the roughly 50 s the probe allows. I did not run it in a cluster.
Impact: an upgrade of a populated archive can restart-loop, and because nothing is committed before the end, each restart starts again.
Recommendation: R4.

**F4 — Medium — purge is not an erasure and leaves no trace.**
Evidence: the purge deletes `sessions`, `session_index`, `online_evals`, `online_scores`, `llm_calls` by session id and ledger rows by tenant (purge.go:86-148). The tables `online_findings`, `online_corrections`, `online_alerts` and `online_labels` carry the tenant (onlinestore.go:56-100) and are untouched; findings quote the first 500 characters of user messages. `online_scores` rows of a tenant inside a kept (shared) session stay, though the table has a tenant column. The handler writes no log line (purge.go:156-197) and has no dry run.
Impact: an admin who purges a tenant to remove its data still leaves user text in findings, and a mistyped key deletes live data with no record of who did it.
Recommendation: R5.

**F5 — Medium — no per-sender limits.**
Evidence: only completed sessions are evicted from memory and only when an archive exists (sessions.go:834-872); open sessions, tenants per sender, labels per sender and names are not capped (the names table gets one row per distinct label that carries a name, tenantnames.go:50-77, 81-88); a request may carry 16 MiB. The pod limit is 512 MiB and the archive 1 GiB. Spans per session are capped (10000).
Impact: a leaked or hostile sender token (the unit that separates customers) can take the shared pod down or fill the volume for every tenant. Not exercised under load.
Recommendation: R6.

**F6 — Low — cost of activity and turns.**
Evidence: `Turns` loads and rebuilds up to 80 sessions through `turnRows`, each taking the store-wide lock for the read from the archive and `BuildTrace` (turns.go:226-243, 245-281); `Activity` and `Turns` call `s.List()` which summarises every session in memory per request (activity.go:101). Window and size are bounded (minutes 1 to 43200, 80 sessions, 3000 rows). Not measured.
Impact: a viewer can slow ingest and every other reader by repeating the request.
Recommendation: R7.

**F7 — Low — shared token has no namespace.**
Evidence: `tenantKey("", label)` returns the label as it is (senders.go:37-45), so a label `core/acme` sent with the shared token is key `core/acme`, covered by grants `core/*`, counted in that sender's usage and shown in its tenant list. Session ids of the shared token have no sender prefix, so it can also hold a name another sender later needs (that sender's spans are dropped as owned by another).
Impact: the holder of the shared token (an operator-level credential) can impersonate a tenant of a named sender; low because that token is already trusted.
Recommendation: R8.

**F8 — Low — tenant names.**
Evidence: `printable` rejects only control characters (`unicode.IsControl`), so U+202E and zero-width characters pass (tenantnames.go:40-47); the 80 limit is bytes and a longer name is dropped with a debug log (tenantnames.go:55,85); `Fill` reads `Name(key)` and then calls `Set`, with no lock across both, so a concurrent admin `Set` can be overwritten by a sender's name in a narrow window (tenantnames.go:81-88). The key a sender can name is always in its own namespace, so it cannot rename or squat another sender's tenant; an admin name always wins afterwards. The UI renders names as React text; I found no `dangerouslySetInnerHTML`, `innerHTML` or script sink in `ui/src`, and hash links are `encodeURIComponent`-encoded, so I found no XSS. The growth of the names table is covered by F5.
Impact: look-alike names inside one sender's own tenants; no cross-sender effect.
Recommendation: R9.

**F9 — Low — bound tenant value.**
Evidence: `ValidTenantLabel` refuses spaces, commas, bars, control characters and over 200 bytes only (senders.go:181-186). A token bound to `*` forces every span's label to `*` and reads `core/*` on `/mcp` (roles.go:536-540); a tenant containing `/` is stored under keys such as `core/a/b`, which `core/a/*` grants cover. Minting needs an admin.
Impact: an admin typing `*` widens the token's read access to all the sender's tenants by accident.
Recommendation: R2.

**F10 — Low — Host header not checked.**
Evidence: `originAllowed` accepts an `Origin` equal to the request `Host` (origin.go:47-52). With no session secret the process serves only on loopback (env.go:50-66) and every caller is an admin, so a page on an attacker's domain that rebinds to 127.0.0.1 passes the guard and can call `/api/admin/*`, `/mcp` and `POST /api/admin/purge-tenants`. Not exercised.
Impact: local development use only; with a session secret the cookie and bearer rules apply.
Recommendation: R10.

**F11 — Low — default role.**
Evidence: `auth.defaultRole: member` (values.yaml:58). A signed-in member of the organisation with no binding reads and writes every tenant (tenant limits come from bindings). The chart comment names `none` for deployments with customers.
Impact: in a deployment that holds customers' tenants, the default grants staff-level access to each organisation member.
Recommendation: R11.

**F12 — Low — release and supply chain.**
Evidence: `scripts/release-local.sh` publishes binaries, image and chart from a maintainer machine and states it skips the keyless signatures; ci.yml and release.yml are `workflow_dispatch` only, so no check runs on a push; `install.sh` takes the checksum from the release it downloads, so it detects a damaged download but not a replaced release; base images and build images are pinned by tag (Dockerfile:5,19,42; ko base `cgr.dev/chainguard/static`); ci.yml runs `go run ...govulncheck@latest` (non-blocking); the git push helper in the script expands the token into a command-line argument (script line 36), readable by other local users in the process list while it runs. Workflow permissions are least-privilege (read at top level, write only in the gated release jobs) and every action is pinned by SHA.
Impact: a consumer cannot verify who built a release, and nothing gates a merge.
Recommendation: R12.

**F13 — Low — usage ledger keys and values.**
Evidence: `llm_calls.span_id` is the primary key and rows are written with `INSERT OR IGNORE` (usagestore.go:50,231), so a second sender that reuses a span id (fixed or counter ids in test harnesses) is ignored, and a sender that predicts another's ids can pre-empt rows. `tagInt` and the token extraction accept negative and string values (turns.go:91-104, adk/extract.go:317-330), which are summed unclamped in SQL.
Impact: usage totals and cost estimates can be skewed by a sender; they are not used for a budget decision (budgets use the judge counters).
Recommendation: R13.

**F14 — Low — purge ordering.**
Evidence: `PurgeTenants` removes sessions from memory first and the archive second (purge.go:35-82); `flushPersist` copies the sessions to save before it writes them (sessions.go:787-815), so a snapshot taken before the purge can write the purged sessions back, and pending usage rows are filtered but a flush in progress is not awaited. The handler clears names after the purge and answers 500 on a name error (purge.go:189-194), after the data is gone.
Impact: an incomplete purge that reports success or failure inconsistently.
Recommendation: R5.

**F15 — Low — AI controls.**
Evidence: `interactiveEval` limits concurrency to 4 and a daily token cap shared by every caller (evalguard.go:63-100); there is no per-user counter, so one member can use the day's budget. Judge prompts start with an untrusted-data notice and defang block markers (judge/client.go:82-99); there is no injection classifier or structured output schema, and the Jev prompts in `internal/decision` do not use the defanging. Per-call output is capped (8192) and online scoring has its own daily budgets.
Impact: a member (or a poisoned trace) can exhaust the shared evaluation budget or bias a judge score.
Recommendation: R14.

**F16 — Low — network exposure.**
Evidence: `networkPolicy.enabled: false` (values.yaml:117), Service type ClusterIP exposing 4317, 4318, 8001 and 9090 to the cluster. Ingest is token-gated and the logging of refusals is rate limited; there is no throttle on failed authentication. Sender tokens are 256-bit random values stored as SHA-256 hashes, so guessing is not practical.
Impact: any pod in the cluster can reach the receivers and the login page.
Recommendation: R15.

**F17 — Info — any-tenant visibility.** A session with several tenants is visible in full to a role that has one of them. This is how the session list has always worked, and F1 and F2 are the ways to get a multi-tenant session without the owner's consent. Activity (`keys()`), Turns and Threads list every tenant key and agent of such a session to the role.

**F18 — Info — dependencies.** trivy: Go 1 Unknown (GO-2026-5932, x/crypto openpgp, no fix, not called per govulncheck), npm 0. npm audit: 1 High (zod, development only through eslint-plugin-react-hooks; fixed in 4.6.6), 0 in production. gitleaks and trivy secret scan: nothing found.

**F19 — Info — stale note.** govulncheck on 2026-10-24 reports only the x/crypto module advisory; the STATUS note on GO-2026-6443 may be out of date.

### Verified and fine

| Check | Result | Evidence |
|---|---|---|
| Bound token overrides the span tenant on both OTLP paths | The override is applied after the export is decoded to every span of every trace, from the span or resource attribute alike; gRPC and HTTP call the same `IngestBound` with the principal the interceptor or middleware resolved | sessions.go:1054-1062, grpc.go:25-31, server.go:351 |
| Shared `--otlp-token` and bound tokens | The shared token resolves to no sender and no tenant, so it is never bound and never used on `/mcp`; a sender token is refused on `/api`, `/stream` and `/ws` | ingestauth.go:83-96, sessionauth.go:139-153 |
| Unbound sender token reads only its sender | `/mcp` role is `member` with tenants `<sender>/*`; `narrowTenants` returns nothing for another sender's key or `<other>/*`; usage queries use parameterised `tenant = ?` or an escaped LIKE prefix `<sender>/`; sender ids cannot contain `/`, so `core/*` does not match `core-x/...` | roles.go:534-541, tenantview.go:13-45, usagestore.go:280-307 |
| Sender token storage and entropy | 256 random bits, kept as SHA-256, shown once, constant-time compare for the shared token, deleted tokens stop working at once; a deleted sender's id is retired | senders.go:150-153,253-282,243-250 |
| Another sender's session is untouched | A session or segment owned by another sender is returned untouched and the spans are dropped | segments.go:394-398,505 |
| Purge authorization and SQL | Admin without limits (every caller when no session secret, which the binary allows only on loopback or with `--no-auth`); patterns and empty keys refused; all deletes parameterised in one transaction; a session shared with another tenant is kept | purge.go:156-197, env.go:50-66 |
| Activity and turns respect the role | Both go through `activityRows`, which applies `allowedBy(info)` (agent and tenant) to the in-memory and the archived rows before anything is loaded; `agent` is required for turns, `minutes` is 1 to 43200, `tenant` is an exact filter applied after the role | activity.go:86-119, turns.go:245-281, :283-307 |
| Usage totals keep tenant limits | The new cache sums are inside the same queries as the existing totals, behind `tenantClause` and `denyAgentScoped`; the MCP `get_usage` path uses the same queries | usagestore.go:280-470, usage.go:46-111, mcpbackend.go:86-114 |
| Backfill idempotence | Cache columns are added with `ALTER TABLE ... ADD COLUMN` (duplicate column ignored), `NULL` marks rows to fill, a row is filled once, rows whose session left the archive get 0, ledger inserts are `INSERT OR IGNORE` | usagestore.go:196-218, :231 |
| Session secret and tokens | Signed with HMAC-SHA256, compared with `hmac.Equal`; minimum length 32 bytes; cookie and API token are not interchangeable (kind check); expiry and revocation checked; raw GitHub tokens are sent to GitHub only if well formed | token.go:49-85, sessionauth.go:72-91, githubtoken.go:171 |
| `/mcp` | POST only, body capped at 1 MiB, stateless, same origin guard, admin tools need `IsAdmin` in the backend, `evaluate_traces` is not registered over HTTP | mcptools/http.go:14-50, mcpbackend.go:194-202, server.go:296-298 |
| Request limits | OTLP 16 MiB wire and 64 MiB decoded, gRPC 16 MiB, JSON bodies 8 MiB, uploads 64 MiB, list limits clamped, server timeouts on every listener | limits.go, grpc.go:38-48 |
| SQL building | All dynamic SQL is constants joined with `?` placeholders; LIKE patterns escaped | pattern search over `internal/api` |
| SSRF and command execution | Outbound URLs come from flags, environment or the online config file, never from a request; the only `exec` is `gh auth token` in the stdio client; `evaluate_traces` confines paths to `--trace-dir` after symlink resolution | oauth.go, notify.go:330-346, cmd/ego/mcp.go:99,331-345 |
| Cross-origin | State-changing methods and WebSocket upgrades must carry a matching `Origin` (or no cross-site fetch metadata); CSP, nosniff, frame-deny headers on 8001 | origin.go, limits.go:106-119 |
| Chart | Non-root (65532), read-only root filesystem, all capabilities dropped, seccomp RuntimeDefault, service account token not mounted, no RBAC objects, secrets by `secretKeyRef`, PVC kept on uninstall, Recreate strategy, ClusterIP only, HTTPRoute off | deployment.yaml, values.yaml |
| Dockerfile | Multi-stage, static CGO-free binary, distroless nonroot final image, no secrets in build args, `.dockerignore` present | Dockerfile |
| Workflows | Top-level `contents: read`, write scopes only on the gated release jobs, actions pinned by SHA, no `pull_request_target`, no untrusted input in shell | .github/workflows |
| Tests, vet, format | `go vet ./...` clean, `gofmt -l .` empty, `go test ./...` passes in every package | run 2026-10-24 |

## Recommendations

| # | Recommendation | Addresses | Status |
|---|---|---|---|
| R1 | Build session ids from escaped or length-prefixed parts (or a separator that labels cannot contain), and record the tenant on the session so that spans of a different tenant for an existing id are refused the way another sender's are | F1 | Open |
| R2 | Restrict a bound tenant (and every label a sender sends) to a safe alphabet: no `:`, `/`, `*`, `\|`, control or separator characters; refuse `*` for a bound token | F1, F9 | Open |
| R3 | Strip or reject 0x1F and other control characters in tenant labels, agent names and session names at ingest, and make `splitIndexList` reject keys that are not valid; rebuild the index rows already stored | F2 | Open |
| R4 | Make the migration incremental (batches with a cursor, committed as it goes), stream the backfill instead of collecting it, add a `startupProbe` or start the health listener before `LoadPersisted`, and log progress | F3 | Open |
| R5 | Make purge delete findings, corrections, alerts and scores of the tenants (or document what it keeps), add a dry run that returns counts, log the caller and keys, and persist before or inside the same step so a flush cannot write sessions back | F4, F14 | Open |
| R6 | Cap open sessions, distinct tenant labels, names and bytes per sender (configurable, 429 beyond), and set a ceiling on open sessions without an archive | F5 | Open |
| R7 | Read the sessions for turns outside the store lock (copy the spans), cache the answer for a few seconds, and cap concurrent requests | F6 | Open |
| R8 | Reject labels of the form `<sender>/...` from the shared token, or give the shared token its own prefix | F7 | Open |
| R9 | Refuse format characters (Unicode categories Cf, Zl, Zp) in names, truncate instead of dropping, hold the names lock across check and write, and bound the table with a purge on tenant removal | F8 | Open |
| R10 | Check the `Host` header against the listen address or an allow-list when no session secret is set | F10 | Open |
| R11 | Document `auth.defaultRole: none` as the setting for deployments with customer tenants, or make it the chart default | F11 | Open |
| R12 | Sign and attest local releases (cosign with a key or a short-lived identity), publish from CI again when minutes allow, pin base images by digest, pin the `govulncheck` version, pass the git credential through a file or a helper that does not put the token in argv, and sign or verify `checksums.txt` in `install.sh` | F12 | Open |
| R13 | Key the ledger by sender and span id, clamp token values to non-negative bounded integers | F13 | Open |
| R14 | Add a per-user daily share of the evaluation budget, defang the Jev prompt inputs, and consider a structured-output schema for the judge | F15 | Open |
| R15 | Ship a default NetworkPolicy for the ingest ports (opt-out) and throttle failed ingest authentication per source | F16 | Open |
| R16 | Update `docs/STATUS.md` for GO-2026-6443 and bump the UI development dependency to zod 4.6.6 or later | F18, F19 | Open |
