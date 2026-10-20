<p align="center">
  <img src="docs/images/ego.svg" alt="ego logo" width="96" height="96">
</p>

<h1 align="center">ego</h1>

<p align="center"><b>Reality testing for AI agents</b></p>

In psychology, the ego is the part of the mind that tests reality: it tells what is imagined from what
actually happened, and holds the self-image up against the world. An agent has the same split.

| | In the mind | In an agent | In ego |
|---|---|---|---|
| **Self-image** | what I think I did | the final answer, its claims, "done" | the response and claims in the session |
| **Reality** | what happened | tool calls, results, errors, retries, cost | the OpenTelemetry trace |
| **Ideal self** | how I should have acted | the intended behavior | the golden eval set, rubrics, reference answers |

ego reads the trace, compares reality with the self-image and the ideal, and scores the gap: did the agent call
the right tools, are its claims backed by what the tools returned, did it resolve the request, did it get worse
since last week. It never re-runs the agent; the traces it already emits are enough.

One static Go binary: REST API, web UI, OTLP receivers and an MCP server. No SDK to add, no extra runtime.

## What it does

- **Test reality** (`hallucinations_v1`, `jev_grounded`): are the agent's claims backed by what its tools returned?
- **Evaluate** a trace file against a golden eval set, from the CLI, the UI, the API or an MCP client.
- **Watch live**: point any OTel-instrumented agent at ego and its sessions, conversations and spans appear as
  they arrive.
- **Track quality over time**: every run is kept; online evaluation samples live sessions, scores them and
  alerts on regressions.
- **See what it costs**: a token usage ledger built from `gen_ai.usage.*` on LLM spans.

> Work in progress. Interfaces can change between releases. [docs/STATUS.md](docs/STATUS.md) lists what is
> supported, known simplifications and what is out of scope.

## Install

Each tagged release (`v*.*.*`) publishes a binary per platform (Linux, macOS, Windows; amd64 and arm64) with
`checksums.txt`, a multi-arch container image and the Helm chart.

```bash
# binary: downloads the latest release, verifies its checksum
curl -fsSL https://raw.githubusercontent.com/triageagent-dev/ego/main/install.sh | sh
# a private repo has no anonymous downloads; use the GitHub CLI instead
gh release download --repo triageagent-dev/ego --pattern 'ego_*_linux_amd64.tar.gz'

# container
docker run -p 8001:8001 -p 4318:4318 ghcr.io/triageagent-dev/ego:latest

# Kubernetes
helm install ego oci://ghcr.io/triageagent-dev/charts/ego --version 0.1.0 -f values.yaml
```

`ego version` prints the installed version. To cut a release, push a tag: `git tag v0.1.0 && git push origin v0.1.0`.
Running the *Release* workflow by hand builds the binaries without publishing anything.

## Quick start

```bash
make build-ui build           # the UI is embedded in the binary, so build it first

./bin/ego run samples/helm.json --eval-set samples/eval_set_helm.json -m tool_trajectory_avg_score
```

```
Trace: 3e289017fe03ffd7c4145316d2eb3d0d
[PASS]  tool_trajectory_avg_score      score=1.00 status=PASSED
```

Swap in `samples/k8s.json` and the same eval set mismatches (score 0, FAILED).

Run the server:

```bash
make serve    # UI + REST API :8001, OTLP/HTTP :4318, OTLP/gRPC :4317, health :9090
```

Then point an agent at it:

```bash
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318   # gRPC exporters use :4317
./your_agent

curl http://localhost:8001/api/streaming/sessions
```

Open <http://localhost:8001>.

## The UI

| View | What it does |
|---|---|
| **Live** | Streaming sessions: conversation, tool calls, spans with errors first, per-session metrics. Pick a golden session and evaluate the rest against it. |
| **Evaluate** | Upload traces and an eval set, choose metrics, run. |
| **Runs** | Every evaluation run: pass-rate and per-metric trends, per-case results. |
| **Usage** | Token spend by call kind, tenant, model and service, with per-session drill-down. |
| **Quality** | Online evaluation: judge budget, what was sampled, open regressions, hourly scores. |
| **Eval sets** | Build golden eval sets from traces and keep them. |

The header's **Debug bundle** button packs sessions, logs and app state into a zip you can load back later.

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
./bin/ego run samples/helm.json --eval-set samples/eval_set_helm.json -m final_response_match_v2
./bin/ego run samples/helm.json -m rubric_based_final_response_quality_v1 \
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
./bin/ego run samples/helm.json -m jev_task_resolved -m jev_grounded --jev-questions questions.json
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
./bin/ego serve --session-db ./ego.db
```

Only the newest `--session-memory` completed sessions (default 200) stay in memory; older ones load on demand.
`--session-retention` (default 30 days) deletes stale sessions daily.

## Online evaluation

`serve --online-config online.json` turns on a continuous loop: **sample** completed sessions, **score** them
per call kind with the evaluators you choose, **detect** regressions, **notify** (Discord, webhook or an event
sink). A daily judge budget caps spend. See [docs/online-evaluation.md](docs/online-evaluation.md).

## MCP server

`ego mcp` is a [Model Context Protocol](https://modelcontextprotocol.io/) server on stdio, for Claude Code,
Cursor, GitHub Copilot CLI and others.

| Tool | Needs `serve`? | Description |
|---|---|---|
| `list_metrics` | yes | Available metrics and their requirements. |
| `evaluate_traces` | no | Evaluate local trace file(s) against selected metrics. |
| `list_sessions` | yes | Recent streaming sessions, newest first. |
| `summarize_session` | yes | A session's invocations, tool calls and messages. |
| `list_runs` | yes, `--session-db` | Past evaluation runs. |
| `get_run_results` | yes, `--session-db` | One run's summary and per-case results. |
| `list_role_bindings`, `set_role_binding`, `delete_role_binding` | yes, `--session-secret`, admin | Manage roles. |

```bash
EGO_SERVER_URL=https://ego.example.com ./bin/ego mcp
```

If that server has GitHub OAuth on, `ego mcp` authenticates with `--session-token`/`EGO_SESSION_TOKEN`, or
with `gh auth token` when you are logged in as a member of the deployment's GitHub org. To mint a long-lived
token for a client with no `gh`:

```bash
./bin/ego auth mint-token --name ci --secret "$EGO_SESSION_SECRET"
```

## Authentication and roles

With no `--session-secret`, `serve` is open. With `--session-secret`, `--github-client-id`,
`--github-client-secret`, `--github-org` and `--public-url` it requires GitHub OAuth, restricted to members of
the org. OTLP receivers and `/api/health` are never gated.

Roles: `admin`, `member` (default, full read/write) and `viewer` (read-only, optionally limited to certain
agent names in Runs), assigned to a GitHub user or team:

```bash
./bin/ego serve --session-secret "$EGO_SESSION_SECRET" --session-db ./ego.db \
  --admin-github-users octocat --default-role member
```

Admins manage bindings with `GET`/`POST /api/admin/roles`, `DELETE /api/admin/roles/{id}` or the MCP tools.

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
| `EGO_USAGE_TENANT_ATTR` | `--usage-tenant-attr` | Span attribute the Usage view reads the tenant from |
| `EGO_ONLINE_CONFIG` | `--online-config` | Online evaluation config file |
| `EGO_SERVER_URL`, `EGO_SESSION_TOKEN` | `--server-url`, `--session-token` | For `ego mcp` |

## Deploying

A Helm chart lives in `charts/ego` (Deployment, Service, PVC, optional HTTPRoutes). Copy
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

## Development

```bash
make build-ui build        # UI first: ui/embed.go embeds ui/dist
make test vet
gofmt -l .                 # must print nothing
cd ui && npx tsc --noEmit

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

## License

[Apache License 2.0](LICENSE)
