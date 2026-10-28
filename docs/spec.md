# ego specification

Reality testing for **AI agents**. ego reads the **OpenTelemetry traces** an agent already emits and scores its behavior without running it again. When scores drop it **diagnoses** the cause, hands the diagnosis to the agent over **MCP**, records what the agent changed and checks that the scores recovered. It ships as one static **Go binary**: a REST API, a web UI, OTLP receivers and an MCP server.

## Architecture

ego is one process. `ego serve` listens on `:8001` for the REST API, the web UI, the live feed and the MCP endpoint, on `:4318` for OTLP/HTTP, on `:4317` for OTLP/gRPC, and on `:9090` for health. The UI is built into the binary. The image carries nothing else, so there is no runtime to install and no SDK to add to an agent.

Every receiver feeds the same session store through the same conversion path. Sessions live in memory up to a bound and are archived to one SQLite file, written without CGO. The archive holds sessions, the session index, runs and their result rows, eval sets, role bindings, sender tokens, the token usage ledger, online scores, alerts, findings, corrections and labels. Older sessions are read back from the archive when someone asks for them.

The commands are `run` (score trace files from the command line), `serve`, `mcp` (the tool registry over standard input and output, as a client of a running `serve`), `auth mint-token` and `version`.

### Persistence and replicas

Persistence is optional. Without a session database, ego keeps the newest sessions in memory only and the run history, online evaluation and every view that needs the archive answer 503. One SQLite file allows one replica, so ego does not scale out; a restart is a short gap in intake until the pod is back. Large migrations at start run in batches and the chart has a long startup probe for them.

| Concern | Behaviour |
|---|---|
| Memory | The newest completed sessions stay in memory (`--session-memory`, default 200); older ones are read from the archive by id |
| Retention | `--session-retention`, default 720 hours; stale sessions are deleted daily and the file is compacted |
| Live feed | A WebSocket per open page with a heartbeat every 15 s; a debounced `changed` message per topic makes the usage, quality and runs views read again. Server-Sent Events remain as a fallback for curl |
| Re-execution | None. ego scores recorded traces; it never calls the agent |

## Ingest and sessions

Traces arrive as OTLP over HTTP (`POST /v1/traces`) or gRPC, both accepting gzip, or as files in Jaeger JSON, OTLP JSON or JSONL, which the loader tells apart on its own. Three attribute conventions are understood: ADK (`gcp.vertex.agent.*`), the OpenTelemetry GenAI semantic conventions and OpenInference. The converter turns a trace into invocations: the user input, the tool calls, and the final answer. The answer is the last call of the agent that opened the invocation, so a verifier or guard that speaks later is not scored as the answer.

### Sessions

Spans group into sessions by `ego.session_name`, then `gen_ai.conversation.id`, then a synthetic name, always under the sender and the tenant label. A session completes after 30 s of quiet (3 s after a root span) and is queued for online scoring then. A late span reopens it, and a session already scored is not scored twice. A session holds at most 10000 spans.

### Always-on agents: threads and segments

For an agent that wakes every few minutes for weeks, a session id names the agent and not a unit of work. A sender that declares the resource attribute `ego.thread` gets three levels. The **thread** is the declared value. A **segment** is what ego stores, lists, scores and cites as a session: a run of whole traces whose first one keeps the thread's id and whose n-th is `<thread>~n`. A **turn** is one trace, and a segment never cuts one. A trace goes to the segment its first span chose, so late spans still land in it. Segmentation is opt-in; every session without the attribute behaves as described above.

| A segment closes when | Setting | Default |
|---|---|---|
| its last span ended long ago, on the next trace or from a sweep | `--segment-idle` | 30m |
| it holds this many spans, checked between traces | `--segment-max-spans` | 2000 |
| its first span is this old | `--segment-max-age` | 6h |

A closed segment takes no new trace and is scored once, with the end of the previous segment as fenced context for the Gemini judges. A single runaway trace is capped at 50000 spans. `GET /api/threads` and the `list_threads` tool give one row per declared thread.

### Claude Code

Claude Code's native traces (`claude_code.*` spans) are translated to GenAI attributes at ingest: sessions come from `session.id`, the agent from `service.name`, and tokens count cache reads and writes in the input. A tool call that failed or was rejected is a failed step. The cache parts are kept in the usage ledger so a cost estimate is not inflated by an agent that mostly reads its prompt from the cache.

### LangChain and LangGraph

LangSmith's OpenTelemetry export, which LangChain documents for LangChain and LangGraph, sends each run's input and output as the run's own JSON. ego translates it at ingest: the graph's thread id is the session, the root run is the agent invocation, the latest user message and the final answer come from the graph's state, and tool arguments and results are read from the tool runs. A tool call that the model made and the tool run that executed it count once.

## Senders and tenants

A token identifies a **sender**. A sender sends many **tenants**, labelled on its spans, and the tenant key is `<sender>/<label>`. Two senders can both send a tenant called `acme` and stay apart. The label is read from the span or resource attribute named by `--usage-tenant-attr`; a span with no label belongs to `default`. The shared `--otlp-token` names no sender, so its tenants are bare labels.

### Names

A tenant key identifies a tenant but is hard to read. A sender can say the display name in the span or resource attribute `ego.tenant.name`. The first name seen is kept while the tenant has none, only a named sender can set one, a name is at most 80 characters with no control or format characters, and an admin's name always wins. A bound token takes its name when the admin mints it.

### Senders and tokens

An admin creates senders and mints their tokens with `POST /api/admin/senders/{id}/tokens`. The body names the token's label and, for a bound token, the tenant and its display name. Each sender is limited to 5000 open sessions, 1000 tenants and 1000 names. A failed token check counts against its source, and a source with more than 30 failures in a minute is answered 429 (gRPC `ResourceExhausted`).

### Removing tenants

`POST /api/admin/purge-tenants` removes tenants that no longer exist. The body lists tenant keys, never patterns, and has a dry-run flag. It deletes the sessions whose tenants are all among the keys, their usage and online-score rows, and the names. A session that also belongs to another tenant stays, and so do the labels people gave. The caller is written to the log.

## Metrics and judges

Every result says whether an algorithm or a model produced it. Algorithms run locally with no key. The other families call a judge: Gemini through the GenAI client, the Vertex AI Managed Evaluation service through Application Default Credentials, or Jev, the decision model.

| Family | Metrics | Needs |
|---|---|---|
| Algorithm | `tool_trajectory_avg_score`, `response_match_score` (ROUGE-1) | A golden eval set |
| Gemini judge | `final_response_match_v2`, `hallucinations_v1`, `rubric_based_final_response_quality_v1`, `rubric_based_tool_use_quality_v1` | A judge model; rubrics for the rubric metrics; a golden set for the match metric. Each reports its rationale and per-sentence or per-rubric verdicts |
| Vertex managed | `safety_v1`, `multi_turn_task_success_v1`, `multi_turn_trajectory_quality_v1`, `multi_turn_tool_use_quality_v1` | Application Default Credentials, a project and a location. The multi-turn metrics judge a whole conversation in one call |
| Jev decision | `jev_task_resolved`, `jev_grounded`, `jev_tool_use_appropriate`, `jev_matches_reference`, `jev_response_kind`, custom `jev:<key>` questions | The Jev endpoint and key; fast and cheap, a first pass over many sessions |

The default judge model is `gemini-3-flash-preview`, and each evaluator can name another. Quoted trace text in a judge prompt is fenced, block markers inside it are defanged, and every call opens with a notice that the quoted text is data. A judge call is capped at 8192 output tokens.

### Interactive evaluation

`POST /api/evaluate` and its streaming twin score traces against an optional golden eval set. The request names stored sessions by id or uploads files, and a golden set can be built from a session. The streaming form reports progress per trace and per metric, can be stopped from the page, and a request can limit itself to chosen turns of the sessions. A run is recorded when it starts and closes as succeeded, cancelled or failed with its reason, so the run list shows it while it works. A run still marked running after a restart is closed as failed.

| Limit | Value |
|---|---|
| Concurrent evaluations | 4 |
| Per request | 50 trace files, 20 evaluators, 8 judge samples, 50 rubrics per evaluator |
| Daily judge tokens | One cap for all callers (`--eval-daily-tokens`, default 5,000,000 per UTC day; 429 when used up). One user can take a quarter of it; admins are exempt |
| Vertex and Jev | Report no token counts, so only the concurrency and request caps limit them |
| Vertex call | Each call is bounded to 3 minutes; Google's status and message are shown when it fails |

## Online evaluation

A one-off run scores traces when someone supplies them. Online evaluation adds a continuous loop over the traces ego already holds: **sample, score, detect, notify**. It takes no side in what produced the traces. The call kind is `gen_ai.agent.name`, else the root span name, and nothing in the code names a producer.

### Sampling

A profile matches the kind of a session, and only a matching session is scored. The sampler reads each session's features: kind, tenant, service, `service.version`, model, error spans, tokens, latency and a fingerprint of the kind, the normalised first input and the tool-call names. Four rules decide what happens next.

- **Interest.** A session with error spans, with tokens or latency above the kind's rolling mean plus three deviations on a log scale, or running a version or model not seen for the kind in 24 hours, goes in the boost lane. A change starts a burst: the next `afterChangeSessions` sessions of the kind are boosted.
- **Trivial.** A session with no tool calls, a first message of at most 40 characters and a last reply of at most 100 is skipped before the rate buckets and counted as trivial. A session with error spans or unreadable text is never skipped this way.
- **Dedup.** A fingerprint scored inside `dedupWindow` without an interest reason is skipped.
- **Rate and budget.** Two token buckets per tenant and kind, one per lane, then a daily cap on judge tokens and on Jev calls. The scheduler reserves an estimate before scoring and books the actual cost after. Vertex metrics and metrics that need a golden set are refused, because the budget cannot meter the first and no golden answer exists for the second.

Every decision is counted by reason at `GET /api/online/status`.

### Configuration

Profiles live in a JSON file, `--online-config`. Evaluators have the shape of the interactive request, with `numSamples` defaulting to 1, since one judge sample per invocation is the cost lever. `maxInvocations` scores only the last N invocations of a long session, and a profile may also match a service or a tenant, the most specific winning. Live traffic has no golden answers, so profiles use reference-free metrics.

```
{
  "budget":   { "judgeTokensPerDay": 2000000, "jevCallsPerDay": 1000 },
  "sampling": { "perKindPerHour": 6, "boostPerKindPerHour": 6,
                "afterChangeSessions": 20, "dedupWindow": "6h" },
  "profiles": [
    { "kind": "rca", "maxInvocations": 3,
      "evaluators": [ { "name": "jev_grounded" },
                      { "name": "hallucinations_v1", "numSamples": 1 } ] }
  ],
  "detector": { "window": "6h", "baseline": "168h", "minSamples": 12, "minDrop": 0.1 },
  "sinks":    [ { "type": "discord", "urlEnv": "EGO_DISCORD_WEBHOOK" } ]
}
```

### Detection and alerts

Every 10 minutes, for each tenant, kind and metric, the detector compares the scores in the window with those in the baseline before it. A regression needs at least `minSamples` on both sides, a drop of the mean of at least `minDrop`, and a drop above 2.5 standard errors (Welch). One bad score never alerts. When one `service.version` or model holds at least half of the current samples and under a tenth of the baseline, it is named as the suspected change.

An alert is open, updated when the drop grows by another `minDrop`, and resolved after two checks in a row with the drop under half of `minDrop`, so it does not flap. Discord gets open, update and resolve with the baseline and current means, the suspect and the three worst sessions. The webhook carries `ego.quality.v1` events, and the event sink posts `EvalQualityDegraded` and `EvalQualityRecovered` to a receiver that takes that shape. A daily digest goes to Discord at 08:00 UTC.

## The RSI loop

Online evaluation ends at a notification and a person reads it. The rest of the loop lets the agent that produced the traces read the verdicts, change itself, and have ego check that the change worked. ego never changes an agent. It diagnoses from the traces it holds, records what the agent says it changed, and reports whether the scores recovered.

### Findings

A findings pass diagnoses low-scoring series with one judge call. It runs for each open alert, refreshed when the drop grows, and in a sweep every `findings.every` over series with at least `findings.minSessions` sessions in `findings.window` and a mean under `findings.minScore`. A finding holds the failure mode, a summary, a suggested correction and the evidence sessions, selected and fenced as data. The call is metered against the daily judge budget. Without a judge model, or when its answer is unusable, the finding is written from the evidence alone. States are open, corrected, resolved and stale.

### Corrections

A correction records what the agent or a person changed, with the `service.version` that carries it. It marks its finding corrected and starts a sampler burst, as a new version does. On every detector tick it is verified against the scores since it was registered, at its version when given. Verified resolves the finding. Failed reopens it with the attempt in its history. A tenant and kind hold at most 20 pending corrections, and `what` is capped at 2000 characters.

### Labels and the score of record

A label is a person's verdict on one metric of one session: agree or disagree, with an optional score and note, stored with the judge's score at that time and the caller's identity. Every aggregate read of scores joins each score with its newest label. A person's score replaces the judge's, an agreement keeps the judge's score and marks it confirmed, and a disagreement without a score leaves the session out. The judge's row is never rewritten. A sender's ingest token is refused by the label endpoint, so an agent cannot write the score of record on itself.

| Mechanism | Behaviour |
|---|---|
| Agreement | agree over agree plus disagree, over the last 7 days, counting the newest label of each session and metric; reported per metric in the status and the daily digest |
| Gate | Per tenant, a metric with agreement under `findings.minAgreement` (default 0.7) over at least 10 of that tenant's labels writes no findings for it. Other tenants are unaffected |
| Anchor re-scoring | Every `findings.recalibrateEvery` (default 24h), up to 20 labelled sessions per metric are scored again with the profile that scored them, against the budget. The mean absolute difference to the label is reported as drift, and nothing acts on it |

The agent's side is one skill, served by the server as the MCP prompt `rsi_loop`: check the findings, read their evidence, make one change per finding, register it, and come back for the verdict. An agent that no judge scores has no findings to read. It reads its own turns instead, with `get_ego_score`, `get_ego_tools` and `get_ego_errors`, and treats a tool that keeps failing, or a fall in the share of turns without a failure, as a finding of its own, reading the failed spans with `get_session_spans` before it decides a correction.

## Activity and usage

### Sessions and runs

The Sessions view pages from the server, newest first, with a cursor and a total. Its filters (time range, tenant, agent, complete, LLM calls only, search) run against an index table in the archive, and the caller's role is checked on every row. A search reads memory and the newest 2000 archived rows that mention the text. The newest session opens its details when the list first loads. An open session shows its spans with attributes and errors, and its online scores with thumbs for labels. The summary lists the spans with an error status in a folded "N errors" list: the operation, the text the tool or span gave and the start, earliest first, at most 50, while the count covers all of them. A sender that runs its turns in a Temporal workflow puts `temporal.*` attributes and `tria.wake.key` on the root span of a turn, and the header shows the workflow and run ids, the rest on a line below; ego does not link to a Temporal UI because it does not know its address. The Runs view lists every evaluation and groups them by eval set or agent, with pass rate and metric trends.

### Activity of an agent

An agent that works in turns, one root span per prompt or message, has a live view, a switch next to the Sessions heading. `GET /api/activity` lists the agents with turns, one row per agent and tenant, from sessions that hold `claude_code.interaction` spans or declared a thread. `GET /api/activity/turns` returns one row per turn across the agent's newest 80 sessions, oldest first, at most 3000 rows: start, duration, model requests and tool calls, the time of the model, of the tools and of the wait for approval, tokens with the cached part, which tool calls failed and what followed what. The window runs from 1 minute to 30 days.

| View | Shows |
|---|---|
| Tree | The agent at the top, under it the models and the MCP servers it used, under each server its tools (its busiest ten, the rest in one row). A row has a bar as long as its calls, red for the share that failed, with the calls, the failures and the average time written out. A server folds away its tools. A turn picked on the strip marks the rows it used with the number of its steps that went through them and fades the others |
| Chord | The call flow: models and tools on a circle, grouped by server, with bundled links for what followed what. Hover a dot to see what led to it and what came after it |
| Treemap | Models, and each server with its tools, as tiles sized by calls, errors or time and warmed by their share of failed calls |

The strip of turns sits above the panel, the legend of the view at the foot of the panel. Beside the panel is the picked turn, or the newest, as a list of its steps: each model request with its time and tokens (the cached part apart), each tool call with its time, whether it waited to be approved and why it failed. A tool call also shows the tokens it put into the context: the input of the request after it, less the input of the request before and the reply that request gave. Calls that ran between the same two requests share that growth by the length of their results, and show it with a tilde. The column is as tall as the panel and its width is dragged, and remembered by the browser.

### The ego page

The ego page is the first the app opens and the one the logo leads to. It shows the judge scores of online evaluation: the ego score, the scores per hour and per metric, the agreement of people with each judge, the alerts, the findings and the corrections. An agent that works in turns often has no profile that scores it, so the page also reads its turns. Under the tiles a Turns block, for the agent picked in the live view and the time range of the page, shows the share of turns that ended without a failure, the failed tool calls out of all tool calls, the model requests, the tokens with the part read from cache, the share of the window the agent was awake, the turns per hour in green and red, and a table with each tool's calls, failures and average time, worst first, and one with each model's requests, failures and average time, worst first. A turn has a failure when any of its spans failed or any of its tool calls failed or was rejected. While nothing is judged, the ego score tile and the score in the sidebar are that share, and say so. `GET /api/activity/score`, `/api/activity/tools` and `/api/activity/errors` (all take `agent`, `tenant`, `hours` and `session`; the score also takes `by` and `limit`) return what the MCP tools `get_ego_score`, `get_ego_tools` and `get_ego_errors` return, one entry per agent, and with no agent every agent the caller may see, at most 20. A turn keeps its first five failed tool calls with the text each gave.

### Token usage

The usage ledger is built from `gen_ai.usage.*` on LLM spans and grouped by call kind, tenant, model and service. Each row also carries the cache read and cache write tokens, parts of the input, and the usage chart draws the cached tokens in their own colour. The UI prices Claude models with the cache read at a tenth and the cache write at 1.25 times the input rate, and marks a cost as an approximation: billing is computed from the traces and is not exact. A failed request that reported no tokens has no ledger row, so it counts as an error span and not as a failed call. `GET /api/usage/limits` shows the caps in force and what is used, read only.

### Alerts flag

The red flag in the sidebar lists the latest alert of each tab, five at most: a session with a failed LLM call, a daily budget at 90% or more, a run that failed or had under half its decided results pass, and the latest low online score or open alert. Tenant-limited roles get their tenants' only. The quality, usage and runs views all follow the tenant chosen in the switcher.

## MCP

One tool registry serves two transports. `ego mcp` on standard input and output is a REST client of a running server. `serve` mounts the same tools at `POST /mcp` (streamable HTTP, stateless; `GET` answers 405, and a body over 1 MiB is refused with 413) and runs them in process against the stores, so a role's agent and tenant limits apply to every tool. No tool is defined twice.

`/mcp` sits behind the same login and roles as the API. A sender's ingest token also authenticates there, and only there, as a member limited to its own tenants. It is the token the agent already sends its traces with, so one token sends traces and reads verdicts. The shared OTLP token does not, because it names no sender.

| Group | Tools |
|---|---|
| Sessions | `list_sessions` (agent, tenant, since, query, complete, scored, thread), `list_threads`, `summarize_session`, `get_session_spans` (an operation, ancestors, status, message and attributes per span, with `errors_only`) |
| Usage and runs | `get_usage` (the ledger grouped by kind, tenant, model or service), `list_metrics`, `list_runs`, `get_run_results` |
| Quality | `get_quality`, `explain_regression`, `explain_session` |
| Ego (turns) | `get_ego_score` (agent, tenant, hours up to 720, by, limit, session_id): the ego of an agent from its turns with no judge, as text an agent reads at a glance. `by` cuts it per agent (the total of the window), per session (newest first, so a limit of 1 is the last session), per hour or per day. A row has the share of turns without a failure, the tool error rate, tokens and cache share, turn times and the five tools that failed most. `get_ego_tools`: every tool's calls, failures, failure rate and average time. `get_ego_errors`: the failures grouped by tool and error text, with count, first and last time and the session of the latest. Both take a session to look at one |
| Correction loop | `get_findings`, `register_correction`, `list_corrections`, `label_session` (its author is the caller's own identity) |
| Admin | `list_role_bindings`, `set_role_binding`, `delete_role_binding`, `run_findings`; over stdio only with `--admin-tools` |
| Local | `evaluate_traces` reads trace files under `--trace-dir` and exists on stdio only |

The evidence a tool returns is read from the stored online results: the per-sentence verdicts of `hallucinations_v1` (unsupported, contradictory and disputed first), per-rubric verdicts, trajectory comparisons and the rationale of the `jev_*` decisions. A tool that needs online evaluation answers an error when it is not configured. The prompt `rsi_loop` is registered on both transports.

## Access and roles

Authentication is active only with a session secret. Sign-in is GitHub OAuth restricted to an organisation and gives a signed session cookie. API tokens come from `ego auth mint-token`, and a raw GitHub token is accepted when its owner is a member of the organisation. Cookies and API tokens are different kinds, each with an id and an issue time. A token carries the role it was minted with. A delegate token is for a client that serves many tenants: it is accepted only for the tool endpoint, and every tool call has to name the one tenant it acts for, which ego checks against the token's grants and narrows the call to. An admin can revoke one token or everything issued to a subject, and logging out revokes the cookie.

| Role | Can |
|---|---|
| admin | Everything, including role bindings, senders and tokens, tenant names, purge, revocations and a findings sweep |
| member | Read, run evaluations, save eval sets, register corrections, label |
| viewer | Read; 403 on every write |

A binding names a user or a GitHub team and may be limited. An **agent scope** limits sessions, traces, runs and the live feed to named agents and fails closed: views that cannot be attributed to an agent, such as usage, quality and eval sets, are closed to it. A session with several agents is visible to a role scoped to any one of them. A **tenant limit** (keys, `<sender>/*` or `*`) narrows the same views to those tenants, and a tenant-limited role reads runs only for the online runs of its tenants. Unknown team membership is refused and never takes the default role. `--default-role none` gives nobody access without a grant. The whole feature is inert when no session secret is set.

A bound ingest token, on `/mcp`, reads only its own tenant. Nobody who holds an ingest token can write labels.

## Hardening and limits

| Area | Behaviour |
|---|---|
| Listening | `serve` binds 127.0.0.1 by default. Without a session secret it refuses other addresses unless `--no-auth` is given, and then accepts only loopback Host headers, so a missing secret cannot silently open it |
| Browsers | Cross-origin POST, PUT and DELETE and WebSocket upgrades are refused. Logout is POST only. A content security policy and frame denial are set on every response |
| Ingest | Bearer token per sender or the shared token; a refused push is logged once a minute without the token; failed checks are throttled per source |
| Quotas | Per sender: 5000 open sessions, 1000 tenants, 1000 names. A session carries one tenant and refuses a trace of another |
| Identifiers | Tenant labels, agent names and display names are cleaned to a safe alphabet and length on the way in, so a sender cannot inject separators or markup into an index or a notification. Chat notifications strip markup and mentions |
| Judge prompts | Quoted trace text is fenced and defanged, and every call starts with a notice that quoted text is data. This lowers the risk of prompt injection and does not remove it. Jev prompts are defanged for the state they carry |
| Evaluation | Bounded in concurrency, request size, output tokens and a daily judge-token cap, with a per-user share |
| Supply chain | Base images pinned by digest, `govulncheck` in CI, release checksums with a signed bundle, and an install script that checks the download against them |

## Configuration

Every flag of `ego serve` can be set from the environment as `EGO_<NAME>`. The settings that matter most:

| Variable | Flag | Purpose |
|---|---|---|
| EGO_SESSION_DB_PATH | `--session-db` | SQLite file; enables persistence, runs, online evaluation |
| EGO_SESSION_MEMORY | `--session-memory` | Completed sessions kept in memory |
| EGO_SESSION_RETENTION | `--session-retention` | Delete sessions older than this |
| EGO_SESSION_SECRET | `--session-secret` | Signs cookies and tokens; enables authentication |
| EGO_GITHUB_*, EGO_PUBLIC_URL | `--github-*`, `--public-url` | GitHub OAuth for an organisation |
| EGO_ADMIN_GITHUB_USERS, EGO_DEFAULT_ROLE | `--admin-github-users`, `--default-role` | Who is an admin; the role of anyone with no binding |
| EGO_OTLP_TOKEN | `--otlp-token` | Shared bearer token for the receivers; sender tokens work too, and with neither a push is refused |
| EGO_USAGE_TENANT_ATTR | `--usage-tenant-attr` | Span attribute the tenant label is read from |
| EGO_EVAL_DAILY_TOKENS | `--eval-daily-tokens` | Judge tokens interactive evaluation may use per UTC day |
| EGO_SEGMENT_* | `--segment-idle`, `--segment-max-spans`, `--segment-max-age` | When a thread's segment closes |
| EGO_ONLINE_CONFIG | `--online-config` | Online evaluation profiles, budget, detector and sinks |
| EGO_LOG_LEVEL, EGO_AUDIT_LOG | `--log-level`, `--audit-log` | Log threshold; log the user name of every sign-in, off by default |
| EGO_SERVER_URL, EGO_SESSION_TOKEN | `--server-url`, `--session-token` | For `ego mcp`, which talks to a running server |

The judge uses a Gemini key or, with `GOOGLE_GENAI_USE_VERTEXAI=true`, Application Default Credentials. Vertex managed evaluation needs `GOOGLE_CLOUD_PROJECT` and, optionally, `GOOGLE_CLOUD_LOCATION`.

## Deployment and releases

ego runs from the binary on a laptop, or in Kubernetes from a Helm chart. The image is a static binary on a minimal base with no shell. The chart renders the online evaluation config into a ConfigMap, runs a single replica with a persistent volume for the database, sets a startup probe that tolerates long migrations, and exposes the API through a gateway route. A new root-level API path must be added to the route's list of root paths or the gateway answers 404.

Each tagged release publishes through CI: binaries for Linux and macOS on amd64 and arm64 with `checksums.txt` and its signature bundle, a signed multi-arch container image, and the Helm chart as an OCI artifact. `ego version` prints the installed version, and the install script verifies its download against the checksums. CI on every push and pull request builds the UI, then runs `go build`, `go vet`, `gofmt`, the tests, the vulnerability scan and a container build.

## Not supported

| Item | State |
|---|---|
| Log ingestion | Not supported yet. Frameworks that keep message content in log records instead of span attributes are not read |
| Performance metrics | Token and latency percentiles are not in the evaluate response yet |
| Evaluate all sessions against a golden one in one call | Not supported yet. The UI does the same through the evaluate request with session ids |
| Custom evaluators | An evaluator over a standard input and output JSON contract, and per-request judge credentials, are not supported yet |
| Trace waterfall | Not supported yet, nor a span store with its own retention |
| User-simulator quality | Out of scope. It needs a conversation scenario that exists only for live simulator runs; ego scores recorded traces |
| Response evaluation score | Out of scope. There is no Vertex predefined metric to call |
| Several replicas | Out of scope while the archive is one SQLite file |

Two simplifications are known. The ROUGE-1 tokenizer does not stem and does not split unspaced scripts, so response match runs lower on inflected English and means nothing for those scripts. A GitHub organisation member who is removed from the organisation keeps a cookie for up to 12 hours.

## FAQ

**Does ego call my agent?**

No. It reads traces that already exist. The only outbound calls are to the judges, and only when a metric needs one.

**Which agents work with it?**

Any that emits OpenTelemetry traces with GenAI, OpenInference or ADK attributes, Claude Code through its own spans, and LangChain and LangGraph through LangSmith's OpenTelemetry export.

**Why does a session not appear in online scoring?**

Its kind has no profile, it was sampled out by dedup or the rate buckets, it was trivial, or the daily budget was used up. The status endpoint counts every decision by reason.

**Who can change what the loop treats as the score?**

People with a member role or above, by labelling. The judge's own score stays stored. An agent cannot label itself, because its ingest token is refused by the label endpoint.

**How do two senders keep their tenants apart?**

The tenant key starts with the sender. Two senders that both send a tenant called `acme` have two tenants, `one/acme` and `two/acme`, and a role limited to one never sees the other.
