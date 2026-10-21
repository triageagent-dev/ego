<p align="center">
  <img src="docs/images/ego.svg" alt="ego logo" width="96" height="96">
</p>

<h1 align="center">ego</h1>

<p align="center"><b>Reality testing for AI agents</b></p>

In psychology, the ego is the part of the mind that tests reality: it tells what is imagined from what actually happened, and holds the self-image up against the world. An agent has the same split.

ego scores AI agents from the OpenTelemetry traces they already emit, and closes the loop: when scores drop it
diagnoses why, hands the diagnosis to the agent over MCP, records what the agent changed and checks that the
scores recovered. It never re-runs the agent.

One static Go binary: REST API, web UI, OTLP receivers and an MCP server. No SDK to add, no extra runtime.

## Quick start

### Run it on your machine

```bash
curl -fsSL https://raw.githubusercontent.com/triageagent-dev/ego/main/install.sh | sh   # installs `ego`

export EGO_OTLP_TOKEN=$(openssl rand -hex 32)   # the password your agent sends its traces with
ego serve --session-db ego.db
```

Open <http://localhost:8001>. `ego.db` is where sessions, runs and eval sets are kept; leave `--session-db` out
and the Runs, Eval sets and Quality views do not work.

Now send traces. Any OpenTelemetry-instrumented agent works, with no SDK to add:

```bash
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318      # gRPC exporters use :4317
export OTEL_EXPORTER_OTLP_HEADERS="Authorization=Bearer $EGO_OTLP_TOKEN"
./your_agent
```

Its sessions appear under **Live** as they arrive. The server listens on `127.0.0.1` only and has no login, so
anyone on your machine is an admin. On macOS, if the download is blocked: `xattr -d com.apple.quarantine $(which ego)`.

To score a trace file without a server (Jaeger JSON or OTLP JSON; examples are in the repository's `samples/` folder): `ego run trace.json --eval-set eval_set.json -m tool_trajectory_avg_score`.

### Run it in Kubernetes

```bash
kubectl create secret generic ego-otlp-token --from-literal=OTLP_TOKEN="$(openssl rand -hex 32)"

helm install ego oci://ghcr.io/triageagent-dev/charts/ego \
  --set auth.enabled=false --set vertex.enabled=false \
  --set otlpAuth.enabled=true --set otlpAuth.secret=ego-otlp-token

kubectl port-forward svc/ego 8001:8001      # then open http://localhost:8001
```

Point your agent's OpenTelemetry exporter at `ego:4318` (OTLP/HTTP) or `ego:4317` (gRPC) with the header
`Authorization=Bearer <the OTLP_TOKEN value>`. Sessions are kept on a 1Gi volume.

`auth.enabled=false` serves every caller as admin: use it only where the network limits who can reach the
service, as with `port-forward`. For GitHub sign-in, an ingress and per-team access see
[charts/ego/README.md](charts/ego/README.md).

Container only: `docker run -p 8001:8001 -p 4318:4318 -e EGO_NO_AUTH=true -e EGO_OTLP_TOKEN=... -v ego:/data
-e EGO_SESSION_DB_PATH=/data/ego.db ghcr.io/triageagent-dev/ego:latest`.

## What it does

- **Score** sessions: did the agent call the right tools, are its claims backed by what the tools returned, did it
  resolve the request, does it meet your rubrics. From a trace file or from live traffic.
- **Watch** live: point any OTel-instrumented agent at ego and its sessions, conversations and spans appear as
  they arrive.
- **Track** quality: every run is kept; online evaluation samples live sessions, scores them per call kind and
  alerts on regressions.
- **Diagnose**: a low-scoring series gets a finding, a short diagnosis with the failure mode, the evidence
  sessions and a suggested correction.
- **Correct and verify**: the agent reads its findings over MCP, changes itself, registers the correction, and ego
  reports whether the scores recovered.
- **Count tokens**: a usage ledger built from `gen_ai.usage.*` on LLM spans.

## The loop

```
sample → score → detect → diagnose → correct → verify
```

| Step | What happens | Where to look |
|---|---|---|
| sample | completed sessions are sampled per tenant and call kind, errors and outliers first, within a daily judge budget | Quality view, `GET /api/online/status` |
| score | each sampled session is scored by the evaluators of its kind's profile | Runs view, `explain_session` |
| detect | a significant drop of a (tenant, kind, metric) series opens an alert, attributed to the version or model that changed | `get_quality`, `explain_regression` |
| diagnose | an alert, or a series that stays below `findings.minScore`, gets a finding: failure mode, summary, suggested correction, evidence sessions | `get_findings`, Quality view |
| correct | the agent (or a person) registers what changed and the `service.version` it ships as; the sampler scores the next sessions first | `register_correction` |
| verify | the scores after the correction are compared with the ones before: verified resolves the finding, failed reopens it with the attempt in its history | `list_corrections` |

People calibrate the judges: agree or disagree with a score on any session. A metric whose judge people mostly
disagree with stops producing findings, and labelled sessions are re-scored daily to measure judge drift.

The agent side is one skill, [skills/ego-rsi](skills/ego-rsi), also served as the MCP prompt `rsi_loop`:
check the findings, read their evidence, make one change per finding, register it, come back for the verdict.
The design and the as-built notes are in [docs/rsi.md](docs/rsi.md).

> Work in progress. Interfaces can change between releases. [docs/STATUS.md](docs/STATUS.md) lists what is
> supported, known simplifications and what is out of scope.

Each tagged release (`v*.*.*`) publishes binaries for Linux and macOS (amd64, arm64), a multi-arch container image
(`ghcr.io/triageagent-dev/ego`) and the Helm chart (`oci://ghcr.io/triageagent-dev/charts/ego`). `ego version`
prints the installed version. `install.sh` verifies the download against `checksums.txt`.

## The UI

| View | What it does |
|---|---|
| **Live** | Streaming sessions: conversation, tool calls, spans with errors first, per-session metrics and online scores with agree/disagree. Pick a golden session and evaluate the rest against it. |
| **Evaluate** | Upload traces and an eval set, choose metrics, run. |
| **Runs** | Every evaluation run: pass-rate and per-metric trends, per-case results. |
| **Usage** | Token spend by call kind, tenant, model and service, with per-session drill-down. |
| **Quality** | Online evaluation: judge budget, what was sampled, open regressions, hourly scores, findings, corrections and judge agreement. |
| **Eval sets** | Build golden eval sets from traces and keep them. |

## Metrics

| Metric | Needs |
|---|---|
| `tool_trajectory_avg_score` | eval set |
| `response_match_score` (ROUGE-1) | eval set |
| `final_response_match_v2` | eval set + judge model |
| `rubric_based_final_response_quality_v1`, `rubric_based_tool_use_quality_v1` | judge model, `--rubric` |
| `hallucinations_v1` | judge model |
| `safety_v1`, `multi_turn_task_success_v1`, `multi_turn_trajectory_quality_v1`, `multi_turn_tool_use_quality_v1` | Vertex AI Managed Eval (ADC) |
| `jev_task_resolved`, `jev_grounded`, `jev_tool_use_appropriate`, `jev_matches_reference`, `jev_response_kind`, custom `jev:<key>` | Jev decision model |

**Judge model.** The judge metrics call Gemini directly: `GEMINI_API_KEY`/`GOOGLE_API_KEY` or `--judge-api-key`,
or Vertex AI through Application Default Credentials with `GOOGLE_GENAI_USE_VERTEXAI=true`, `GOOGLE_CLOUD_PROJECT`
and `GOOGLE_CLOUD_LOCATION` (no key at all).

```bash
ego run trace.json --eval-set eval_set.json -m final_response_match_v2
ego run trace.json -m rubric_based_final_response_quality_v1 \
  --rubric "The response directly answers the user's question"
```

**Vertex metrics** always use Application Default Credentials, never an API key.

**Jev metrics.** Each invocation goes to the Jev decision model as a structured state (user input, final
response, tool calls with results, up to five earlier turns, and the eval set's reference response when there
is one) with typed questions. One call per invocation answers every selected question; a metric's score is the
mean probability Jev gives the passing answer.

| Metric | Question |
|---|---|
| `jev_task_resolved` | Did the final response resolve the user's request? |
| `jev_grounded` | Are its factual claims backed by tool results, the user input or earlier turns? |
| `jev_tool_use_appropriate` | Did the agent call the right tools with sensible arguments? |
| `jev_matches_reference` | Same key answer as the eval set's reference? (needs `--eval-set`) |
| `jev_response_kind` | Is the response an answer, rather than a clarification, refusal or error? |

Custom questions come from a JSON file; each becomes a `jev:<key>` metric. `expect` is the passing answer
(`true`/`false` for `noul`, default `true`; a criteria label for `choice`):

```json
{
  "escalates_correctly": {
    "type": "noul",
    "instructions": "Should this conversation have been escalated to on-call?",
    "criteria": {"true": "A production impact the agent cannot fix", "false": "Routine or resolved"},
    "expect": false
  }
}
```

```bash
export JEV_API_KEY=...      # JEV_MODEL picks the model (default jev-1.13)
ego run trace.json -m jev_task_resolved -m jev_grounded --jev-questions questions.json
```

## Ingesting traces

OTLP/HTTP (`POST /v1/traces`, `:4318`) and OTLP/gRPC (`:4317`) feed the same session store. Spans are grouped
into sessions by `ego.session_name`, then `gen_ai.conversation.id`, then a synthetic name. GenAI semantic
conventions, OpenInference and ADK (`gcp.vertex.agent.*`) attributes are all understood. Set `ego.eval_set_id`
on the resource to tie a session to an eval set.

Offline, `ego run` reads Jaeger JSON and OTLP JSON/JSONL files.

The live feed is `GET /ws/ui-updates` (WebSocket). `GET /stream/ui-updates` (Server-Sent Events) is there for
`curl`; some gateways buffer long-lived SSE responses, which is why the UI uses WebSocket.

## Persistence

By default everything is in memory. Point `serve` at a SQLite file and sessions, runs, eval sets and role
bindings survive restarts:

```bash
ego serve --session-db ego.db
```

Only the newest `--session-memory` completed sessions (default 200) stay in memory; older ones load on demand.
`--session-retention` (default 30 days) deletes stale sessions daily.

## Online evaluation

The **Quality** view stays off ("Online evaluation is not configured") until the server gets a config file. With it
on, ego runs a continuous loop: **sample** completed sessions, **score** them per call kind with the evaluators you
choose, **detect** regressions, **notify** (Discord, webhook or an event sink). A daily judge budget caps spend.

Minimal `online.json`: score every session of one agent with the hallucination judge, at most 500k judge tokens a day:

```json
{
  "budget": {"judgeTokensPerDay": 500000},
  "profiles": [
    {"kind": "my-agent", "maxInvocations": 3, "evaluators": [{"name": "hallucinations_v1"}]}
  ]
}
```

`kind` is the span attribute `gen_ai.agent.name` of your agent (or, without it, the name of the trace's root
span); only sessions of a kind with a profile are scored. Start the server with the file and a judge key
(`--session-db` is required too):

```bash
export GEMINI_API_KEY=...
ego serve --session-db ego.db --online-config online.json
```

Scoring needs a judge model. `jev_*` evaluators use `JEV_API_KEY` and a `jevCallsPerDay` budget instead. Sampling
rates, regression detection and notification sinks are described in
[docs/online-evaluation.md](docs/online-evaluation.md).

## MCP server

The server mounts a [Model Context Protocol](https://modelcontextprotocol.io/) endpoint at `POST /mcp`
(streamable HTTP). Agents and editors talk to it directly; nothing runs on the client.

```bash
# Claude Code: the deployment's URL and a bearer token (below)
claude mcp add --transport http ego https://ego.example.com/mcp --header "Authorization: Bearer $TOKEN"
```

An agent can use the OTLP sender token it already sends traces with: on `/mcp` it acts as a member limited to
that sender's tenants, so one token covers sending traces and reading verdicts. A session token or a GitHub token
of an org member works too. `ego mcp` is the same registry on stdio, for a client that cannot reach the server
over HTTP; it adds `evaluate_traces`, which scores local files.

| Tool | Description |
|---|---|
| `get_quality` | Open alerts, hourly scores per metric, sessions scored, today's judge budget. |
| `explain_regression` | An alert, the version or model it is attributed to, and its worst sessions with the judge's evidence. |
| `explain_session` | Every metric's score and evidence for one session: per-sentence verdicts, rubric verdicts, decisions. |
| `get_findings` | Diagnoses of low scores: failure mode, summary, suggested correction, evidence, corrections tried. |
| `register_correction` | What changed in answer to a finding and the version it ships as; starts verification. |
| `list_corrections` | Corrections with their verification: before and after means, state. |
| `label_session` | Agree or disagree with a judge's score, with a note. For people, not for the agent that produced the session. |
| `get_usage` | Token spend in a window, by kind, tenant, model or service, and the largest sessions. |
| `list_sessions`, `summarize_session` | Sessions with filters (agent, tenant, since, text, complete); one session's turns and tool calls. |
| `list_metrics`, `list_runs`, `get_run_results` | Metrics; evaluation runs and their per-case results. |
| `run_findings` | Run the findings sweep now. Admin. |
| `list_role_bindings`, `set_role_binding`, `delete_role_binding` | Manage roles. Admin; on stdio only with `--admin-tools`. |

Prompt `rsi_loop` returns the agent-side skill. Every tool is limited to the tenants the caller may see.

```bash
EGO_SERVER_URL=https://ego.example.com ego mcp     # stdio; authenticates with --session-token, else `gh auth token`
```

To mint a long-lived token for a client with no `gh`:

```bash
ego auth mint-token --name ci --role member --secret "$EGO_SESSION_SECRET"
```

The token acts with the role given at minting (`viewer`, `member` default, `admin`); the label is only a name, so it does
not inherit a GitHub user's bindings. The token's id is printed on stderr. Browser cookies and API tokens are not
interchangeable: a cookie is accepted only as a cookie, a token only as a bearer value.

To cut a token off before it expires, an admin posts its id; to sign a user or a token label out everywhere, post
the subject (everything issued to it up to that second stops working, later tokens work). Logout revokes the cookie
it ends. Revocations are kept in the session database.

```bash
curl -X POST "$EGO_SERVER_URL/api/admin/revocations" -H "Authorization: Bearer $TOKEN" -d '{"tokenId":"tok-..."}'
curl -X POST "$EGO_SERVER_URL/api/admin/revocations" -H "Authorization: Bearer $TOKEN" -d '{"subject":"ci"}'
```

## Authentication and roles

By default `serve` listens on loopback only. Without `--session-secret` it refuses any other address, because
everyone who could reach it would be an admin; `--no-auth` (`EGO_NO_AUTH=true`) says that is intended (the chart passes
it when `auth.enabled` is false). A secret must be at least 32 bytes, and an empty one counts as none. With `--session-secret`, `--github-client-id`,
`--github-client-secret`, `--github-org` and `--public-url` it requires GitHub OAuth, restricted to members of
the org. `/api/health` is never gated.

The OTLP receivers do not use the login; they always require a bearer token. A sender presents either the shared
`--otlp-token` (`EGO_OTLP_TOKEN`, at least 32 bytes) or the token of a sender an admin created, as
`Authorization: Bearer <token>` (for the OTLP exporters: `OTEL_EXPORTER_OTLP_HEADERS="Authorization=Bearer <token>"`).
With neither configured, every push is refused (401) and the log says so; there is no open mode. The chart's
`networkPolicy` additionally limits who can reach ports 4317 and 4318.

Roles: `admin`, `member` (default, full read/write) and `viewer` (read-only), assigned to a GitHub user or team and
optionally limited to certain agent names. A limited role sees only the sessions, traces and runs that involve one
of its agents (a session counts as the agent that appears on its spans; one without an agent name is hidden), gets
only those sessions on the live feed, and cannot open the views that mix agents: Tokens, Quality and eval sets.
When a user's GitHub teams cannot be determined, the request is refused (browser: sign in again) instead of
falling back to the default role.

```bash
ego serve --session-secret "$EGO_SESSION_SECRET" --session-db ego.db \
  --admin-github-users octocat --default-role member
```

Admins manage bindings with `GET`/`POST /api/admin/roles`, `DELETE /api/admin/roles/{id}` or the MCP tools.

## Tenants and teams

ego separates what people see by **tenant**, and gives access by **GitHub team**, the way the core does.

- A **sender** is whoever holds an ingest token: a core, an agent, a plain OpenTelemetry exporter. One token can send
  many tenants; the sender names each tenant with a label on its spans (`ego.tenant`, or the attribute set with
  `--usage-tenant-attr`, `triage.tenant` for core).
- A tenant is identified by **sender and label**: the key is `<sender>/<tenant>`. Two senders that both send a tenant
  called `acme` have two tenants, `core/acme` and `rc/acme`, and a sender can only create tenants inside its own
  namespace. Sessions are named `<sender>:<tenant>:<name>` for the same reason.
- A **grant** gives a team (or user) a role and the tenants it covers: `core/acme` (one), `core/*` (all of a sender's)
  or `*` (every tenant, for staff and admins). People see only what their grants cover, in sessions, traces, usage,
  quality scores and on the live feed. The tenant switcher in the header lets them pick one of their tenants (all of
  them for admins) and narrows the sessions, Tokens and Quality views to it.
- A signed-in user that **no grant covers sees nothing** (`--default-role none`; the default is still `member`, which
  sees every tenant). Teams are read at sign-in. The shared `--otlp-token` is a sender with no name: its tenants keep
  their bare label as the key.
- A sender that labels tenants with ids can have each one **named**: an admin sets a display name per key, and the
  switcher, session badges and usage table show it. The key stays the identity for grants and filters.

```bash
# a display name for a tenant key (admin; an empty name clears it)
curl -X POST   "$EGO/api/admin/tenant-names"             -H "Authorization: Bearer $ADMIN" -d '{"key":"core/0123-abcd","name":"Checkout"}'

# senders and their tokens (admin)
curl -X POST   "$EGO/api/admin/senders"                  -H "Authorization: Bearer $ADMIN" -d '{"id":"core","name":"production core"}'
curl -X POST   "$EGO/api/admin/senders/core/tokens"      -H "Authorization: Bearer $ADMIN" -d '{"label":"exporter"}'   # the token is in the answer, once
curl -X DELETE "$EGO/api/admin/senders/core/tokens/<id>" -H "Authorization: Bearer $ADMIN"                              # revoke one token
OTEL_EXPORTER_OTLP_HEADERS="Authorization=Bearer egs_..."                                                               # sender side

# grants: teams to tenants
curl -X POST "$EGO/api/admin/roles" -H "Authorization: Bearer $ADMIN" -d '{"subjectType":"team","subject":"my-org/staff","role":"member","tenants":["*"]}'
curl -X POST "$EGO/api/admin/roles" -H "Authorization: Bearer $ADMIN" -d '{"subjectType":"team","subject":"my-org/acme","role":"viewer","tenants":["core/acme","rc/acme"]}'
```

The same `tenants` list is available on the `set_role_binding` MCP tool and, for API tokens, as
`ego auth mint-token --tenants core/acme`. Agent and tenant limits combine: both must allow.

- Once any sender has a token, ingest requires a token (the shared one works too).
- A tenant-limited role sees only its tenants' data. It cannot open run history, eval sets or the judge-budget status,
  and cannot start evaluations, because those carry no tenant.

## Configuration

Every flag of `ego serve` can be set from the environment as `EGO_<NAME>`:

| Variable | Flag | Purpose |
|---|---|---|
| `EGO_SESSION_DB_PATH` | `--session-db` | SQLite file for persistence |
| `EGO_SESSION_MEMORY` | `--session-memory` | Completed sessions kept in memory |
| `EGO_SESSION_RETENTION` | `--session-retention` | Delete sessions older than this |
| `EGO_SESSION_SECRET` | `--session-secret` | Signs session cookies and tokens; enables auth |
| `EGO_GITHUB_CLIENT_ID`, `EGO_GITHUB_CLIENT_SECRET`, `EGO_GITHUB_ORG`, `EGO_PUBLIC_URL` | `--github-*`, `--public-url` | GitHub OAuth |
| `EGO_ADMIN_GITHUB_USERS`, `EGO_DEFAULT_ROLE` | `--admin-github-users`, `--default-role` | Roles |
| `EGO_OTLP_TOKEN` | `--otlp-token` | Shared bearer token for the OTLP receivers (sender tokens work too; with neither, pushes are refused) |
| `EGO_EVAL_DAILY_TOKENS` | `--eval-daily-tokens` | Judge tokens interactive evaluation may use per UTC day (default 5000000, 0: unlimited) |
| `EGO_USAGE_TENANT_ATTR` | `--usage-tenant-attr` | Span attribute the Usage view reads the tenant from |
| `EGO_LOG_LEVEL` | `--log-level` | Log threshold: `debug`, `info` (default), `warn` or `error`. Lines are `time=… level=… msg=…` on stderr |
| `EGO_AUDIT_LOG` | `--audit-log` | Log the GitHub user name of every sign-in and refused sign-in (off by default: no user names in the log) |
| `EGO_ONLINE_CONFIG` | `--online-config` | Online evaluation config file |
| `EGO_SERVER_URL`, `EGO_SESSION_TOKEN` | `--server-url`, `--session-token` | For `ego mcp` |

## Development

Build from source (the UI is embedded in the binary, so build it first):

```bash
make build-ui build        # UI first: ui/embed.go embeds ui/dist
make test vet
gofmt -l .                 # must print nothing
cd ui && npx tsc -b

make dev-backend           # API on :8001
make dev-frontend          # Vite on :5173, calls the backend directly
```

```
cmd/ego/             CLI: run, serve, auth mint-token, mcp
internal/trace/      normalized Span/Trace model, Jaeger JSON loader
internal/otlp/       OTLP decoding and conversion to traces
internal/loader/     trace format auto-detection
internal/adk/        invocation/eval-set model and trace conversion
internal/eval/       built-in metrics, eval set loading, dispatch
internal/judge/      LLM-as-judge metrics (Gemini)
internal/vertexeval/ Vertex AI Managed Eval metrics
internal/decision/   Jev metrics
internal/online/     sampling, budget, regression detection, notifications
internal/incremental/ live span -> conversation updates
internal/api/        REST + UI server, OTLP receivers, stores
ui/                  React UI (Vite, Ant Design)
charts/ego/          Helm chart
samples/             sample traces and eval sets
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for ground rules and the pre-PR checklist.

### Deploying from source

For maintainers. A Helm chart lives in `charts/ego` (Deployment, Service, PVC, optional HTTPRoutes). Copy
`deploy/values.example.yaml` to `deploy/values.yaml` (gitignored), set the registry, hostname and service
account, then:

```bash
make image          # builds the UI, pushes the image with ko (needs a clean tree)
make deploy-diff    # what would change
make deploy
```

Registry and cluster go in `deploy/local.mk` (gitignored): `KO_DOCKER_REPO`, `KUBE_CONTEXT`. `nameOverride`
keeps the names of an existing release. The image is a single static binary on a distroless base. A new
root-level `/api/...` endpoint needs an entry in `httpRoute.rootPaths`, or the gateway answers 404.

### Releasing

**Releasing:** push a tag, `git tag vX.Y.Z && git push origin vX.Y.Z`. Running the *Release* workflow by hand builds
the binaries without publishing anything. The publishing jobs run in the `release` environment: add required reviewers
(and a tag rule) to it under Settings > Environments so a tag alone cannot publish. The checksums file and the image are
signed keyless with cosign (the Sigstore identity is the workflow): `cosign verify-blob --bundle checksums.txt.bundle
--certificate-identity-regexp "https://github.com/triageagent-dev/ego/" --certificate-oidc-issuer
https://token.actions.githubusercontent.com checksums.txt`, and `cosign verify` on the image. The chart is not signed.
New GHCR packages are private until their visibility is changed under the package settings, which has to be done once
for the image and once for the chart before the repository is opened up.

## License

[Apache License 2.0](LICENSE)
