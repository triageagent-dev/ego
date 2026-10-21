# RSI: the agent corrects itself

- **Status**: Implemented (2026-10-21)
- **Scope**: ego (`serve`, `mcp`). Builds on `docs/online-evaluation.md`.

Online evaluation closes at a notification: **sample → score → detect → notify**. A person reads the
alert. This document adds the other half, so the agent that produced the traces can read the verdicts,
change itself, and have ego verify that the change worked:

**sample → score → detect → diagnose → correct → verify**

ego never changes an agent. It diagnoses from the traces it already has, hands the diagnosis to the agent over
MCP, records what the agent says it changed, and reports whether the scores recovered. The agent's side is a
skill (section 6). Everything stays producer-agnostic: nothing in ego names an agent framework.

## Vocabulary

| Word | Meaning |
|---|---|
| **score** | one metric on one session (`online_scores`) |
| **alert** | a statistically significant drop of a (tenant, kind, metric) series (`online_alerts`) |
| **finding** | a diagnosis: the failure mode behind low scores, with evidence sessions and a suggested correction |
| **correction** | what the agent (or a person) changed in answer to a finding, with the version that carries it |
| **verification** | ego's verdict on a correction: did the series recover after it |
| **label** | a person's verdict on a judge's score: agree or disagree, optionally with a score |

## 1. MCP inside `serve`

`ego mcp` (stdio, a REST client) stays for people in an editor. For agents, the same tool registry is mounted in
`serve` at `POST /mcp` (MCP streamable HTTP transport, `github.com/mark3labs/mcp-go/server`).

- **One registry, two transports.** Tool definitions and handlers live in one package (`internal/mcptools`)
  behind a small backend interface; the stdio command implements it with REST calls, `serve` implements it
  in-process against the stores. No tool is defined twice.
- **Auth.** `/mcp` is behind `requireSession` + `withRole` like `/api`. In addition, a **sender token** (the
  OTLP ingest token the agent already sends its traces with, `SenderStore.Resolve`) authenticates as role
  `member` limited to tenants `<sender>/*`. The shared `--otlp-token` does not: it names no sender. One token
  for sending traces and reading verdicts; nothing new to mint.
- **Tenant limits apply to every tool.** A tenant-limited role sees only its tenants' alerts, series, findings,
  corrections, labels, sessions and online runs. Tools that cannot be attributed to a tenant (role bindings,
  interactive `evaluate_traces` on the server) are refused for it.
- **Runs for tenant-limited roles.** Today such a role gets no runs. Online runs are attributable: `online_evals`
  maps `run_id` to a tenant. A tenant-limited role may read runs and run results whose run id appears in
  `online_evals` for one of its tenants; every other run stays closed. Judge rationales live in run results, so
  without this the agent cannot read its own verdicts.
- `httpRoute.rootPaths` in the chart gets `/mcp`.
- **Transport.** Stateless streamable HTTP: no session id is issued, so replicas share nothing. `GET` answers 405
  (nothing is pushed to a client). `evaluate_traces` reads local files and stays stdio only; `serve` does not
  register it.

Tools (all transports; tenant-scoped unless noted):

| Tool | Does |
|---|---|
| `get_quality` | `kind?`, `hours?` (default 24): open alerts, hourly series per metric, sessions scored, budget used today (not given to a tenant-limited caller: the meter is shared). One call to answer "how am I doing". |
| `explain_regression` | `alert_id`: the alert, its suspect, and its worst sessions each with per-metric scores, the judge rationale, and the per-sentence verdicts (`unsupported`, `contradictory`, `disputed` first) or rubric verdicts. |
| `explain_session` | `session_id`: the same evidence for one session, so an agent can look at a session it chooses. |
| `get_findings` | `kind?`, `state?` (open, corrected, resolved, stale; default open), `since?`: findings newest first. |
| `register_correction` | `finding_id?`, `kind`, `what` (free text, ≤ 2000 chars), `version?` (the `service.version` the change ships as), `by?`: records a correction, marks the finding `corrected`, returns the correction with its verification window. |
| `list_corrections` | `kind?`, `state?`: corrections with their verification. |
| `label_session` | `session_id`, `metric`, `verdict` (agree, disagree), `score?`, `note?`: stores a person's or agent-operator's verdict on a score. |
| existing | `list_metrics`, `list_sessions`, `summarize_session`, `list_runs`, `get_run_results`; admin-only `list_role_bindings`, `set_role_binding`, `delete_role_binding`. |

A **prompt** `rsi_loop` (MCP prompts capability) returns the skill text of section 6, so any MCP client can
load the loop without the repository.

## 2. Findings

A finding is written by the **findings pass** (`internal/online/findings.go`, wired in `internal/api`):

- **When.** (a) An alert opens or its drop grows (`open`, `updated` events): one finding per alert, refreshed on
  update. (b) A sweep every `findings.every` (default 6h): for each (tenant, kind, metric) series with at least
  `findings.minSessions` (default 8) sessions in the last `findings.window` (default 24h) whose mean is below
  `findings.minScore` (default 0.7) and that has no open finding, one finding. Alerts catch drops; the sweep
  catches "always been bad".
- **Evidence.** Up to `findings.maxSessions` (default 8) lowest-scoring sessions of the series in the window,
  by the score of record (section 4); a session a person scored or confirmed below `minScore` comes before any
  judge-only one. For each: the first user message (truncated to 500 chars), the tool-call name sequence, the
  person's note when they checked it, the metric's rationale, and the per-sentence or per-rubric verdicts that
  failed, read from the online run results. The finding's `confirmed` counts the checked sessions among its
  evidence, and the notification says "n checked by a person".
- **Diagnosis.** One call to the judge model (`judge.Model`, Gemini, metered against `budget.judgeTokensPerDay`;
  skipped with reason `budget` when the cap is reached) with the evidence fenced as data (the existing defang)
  and this contract, answered as JSON:
  - `failure_mode`: one line, what goes wrong (e.g. "claims pod restart counts the tool output does not contain");
  - `summary`: 2–5 sentences, the pattern across the sessions, with session ids as evidence;
  - `suggested_correction`: what to change in the agent (prompt, tool use, skill), concrete and testable;
  - `confidence`: low, medium, high;
  - `evidence`: `[{session_id, excerpt, why}]`.
  Without a judge model configured, the finding is written from the evidence alone (`failure_mode` =
  "<n> of <m> sessions score below <minScore> on <metric>", no suggestion) and `model` is empty.
- **Record.** `online_findings(finding_id, tenant, kind, metric, state, alert_id, created_ms, updated_ms,
  data JSON)`. `data` holds the diagnosis, the evidence session ids, the window, the model and tokens spent.
- **State.** `open` → `corrected` (a correction names it) → `resolved` (its correction verified, or its alert
  resolved) | `stale` (no correction and the series has been above `minScore` for `findings.window`).
- **Notify.** Every sink gets findings as `{"type": "ego.finding.v1", "event": "open|updated|resolved|stale",
  "findingId", "tenant", "kind", "metric", "failureMode", "summary", "suggestedCorrection", "confidence",
  "sessions", "link"}`; the Discord text is the failure mode, the suggestion and the session links; the generic
  event is `EvalFindingOpened` (warning) / `EvalFindingResolved` (info), source `<kind>/<metric>/finding`.
- **API.** `GET /api/online/findings?state=&kind=&tenant=`, `GET /api/online/findings/{id}`.
- **Budget.** Findings count against the judge budget and are reported in `/api/online/status` as
  `findings: {written, skipped: {budget, noEvidence}, tokens}`.

## 3. Corrections and verification

- `POST /api/online/corrections` body `{findingId?, tenant, kind, what, version?, by?}` → `201` with the
  correction. `GET /api/online/corrections?state=&kind=&tenant=`. A tenant-limited role may only post for its
  tenants. `tenant` is implied when the role has exactly one.
- **Record.** `online_corrections(correction_id, tenant, kind, metric, finding_id, version, state, created_ms,
  verified_ms, data JSON)`. `metric` comes from the finding (empty when none).
- **Sampling.** A correction starts a change burst for its (tenant, kind) exactly as a new `service.version`
  does (`afterChangeSessions` boosted), so the verification window fills fast. If `version` is given, sessions
  before that version do not count toward verification.
- **Verification.** On every detector tick, for each `pending` correction: `before` = the finding's evidence
  window (or `detector.baseline` before the correction when there is no finding); `after` = scores since the
  correction (and at `version` when set). With at least `detector.minSamples` in `after`:
  - `verified` when the after-mean exceeds the before-mean by at least `detector.minDrop / 2`, or the alert the
    finding came from has resolved;
  - `failed` when `after` has `minSamples` sessions and the mean did not improve by `minDrop / 2`,
    or `findings.verifyWithin` (default 48h) has elapsed;
  - otherwise it stays `pending` with the current numbers in `data.verification`.
  A verified correction resolves its finding; a failed one reopens it (`open`, with the correction kept in the
  finding's history), so the next loop iteration sees that the first attempt did not work.
- **Notify.** `ego.correction.v1` events `verified` and `failed` through every sink; generic
  `EvalCorrectionVerified` (info) / `EvalCorrectionFailed` (warning).

**As built (phase 2).**
- Findings and corrections share one wire shape, the snake_case JSON of `online.Finding` / `online.Correction`, on REST, MCP and in the `data` column. The notification payloads stay camelCase.
- The pass runs inside the detector tick (alert findings, verification, then the sweep when `findings.every` has passed, remembered in `online_state`), not on alert events, so a finding skipped for budget is retried on the next tick.
- The sweep skips a series that has an `open` or `corrected` finding. An alert that opens on a series with an alert-less sweep finding adopts it instead of adding a second one.
- A series whose sessions have no stored evidence (pruned) is skipped as `noEvidence`; a diagnosis the model cannot give (no model can be built, the budget has no judge allowance, an error, an unusable answer) is written from the evidence alone, the last two keeping the model's name with `model_error`.
- `before` is the finding's window mean and n, fixed at registration; a correction with no finding uses the detector baseline span before it, pooled over every metric of the kind. With no baseline sessions at all the bar is `findings.minScore`.
- `after` is the scores of the correction's series since it was registered, and only those at its `version` when given. `alert resolved` verifies only once `detector.minSamples` sessions exist after the correction. A finding with a second, still pending correction is not reopened by the first one failing.
- `register_correction` takes an optional `tenant`, for roles that do not imply one; a finding that is `resolved` or `stale` takes no more corrections (409). `what` is at most 2000 characters and `kind`, `tenant`, `version` and `by` at most 200 (400 beyond). A (tenant, kind) holds at most 20 `pending` corrections (429 beyond; verified and failed ones free their slot), which keeps the verification pass, which reads every pending correction, bounded.
- A manual sweep (`run_findings`, `POST /api/online/findings/sweep`) is tied to its request and stops after 5 minutes; while another sweep runs it answers 409 `a sweep is already running`.

## 4. Labels

A label is the truth about one score, and ego counts it as such:

- **Score of record.** Wherever ego aggregates scores (the detector's baseline and window, alerts, the sweep, a
  finding's window and evidence, a correction's baseline and verification, the hourly series behind the Quality
  view, `get_quality` and `explain_regression`, the digest), each (session, metric) is read with its newest
  label joined: the person's score, when they gave one, replaces the judge's; an agreement keeps the judge's
  score and marks the sample as confirmed; a disagreement without a score leaves the session out, because the
  judge is known wrong and no truth was given. `explain_session` and the session card show the person's verdict
  and score next to the judge's. A label never rewrites the `online_scores` row: the judge's score stays as what
  the judge said, and the join decides what counts.
- **Evidence.** Findings cite checked sessions first and show the diagnosis the person's note (section 2).

- `POST /api/online/labels` body `{sessionId, metric, verdict: agree|disagree, score?, note?}`;
  `GET /api/online/labels?metric=&tenant=&since=`. Stored in `online_labels(label_id, session_id, tenant, kind,
  metric, judge_score, verdict, score, note, by, created_ms)`; `judge_score` is the online score at label time.
- **Agreement** per metric over the last 7 days = agree / (agree + disagree), in `/api/online/status` as
  `labels: {perMetric: {metric: {agree, disagree, agreement}}}` and in the digest. A metric with
  `agreement < findings.minAgreement` (default 0.7) and at least 10 labels is **not** used to write findings,
  and the status says so: an uncalibrated judge must not drive corrections.
- **Anchor set.** A labelled session is a calibration anchor. `findings.recalibrateEvery` (default 24h)
  re-scores up to 20 anchors per metric (oldest re-score first) against the judge budget and reports the mean
  absolute difference to the labelled score as `labels.perMetric[metric].drift`. No action is taken on drift
  beyond reporting it.
- **UI.** The session inspector gets agree/disagree on each online score; the Quality view lists findings,
  corrections and the per-metric agreement.

**As built (phase 3).**
- `POST /api/online/labels` answers 404, not 400, for a session with no online score for the metric and for a session of a tenant the caller may not see, so a refusal does not say which. A score of `null` (an errored metric) counts as no score. `GET` also takes `session=`, which the session card uses.
- A session can be labelled more than once (a changed mind is a newer row). Agreement and anchors use the newest label of each (session, metric), so one session counts once however often it was labelled.
- The gate is per tenant: a tenant's findings are held back only by that tenant's own labels, so one tenant's disagreements cannot stop the findings of another. The status counts the labels the caller may see (a tenant-limited caller its own tenants', an admin all); there `gated` is true when at least one of those tenants is gated and `gatedTenants` names them. A tenant-limited caller gets no drift (drift is not attributable to a tenant).
- Label reads are bounded: agreement, the gate and the anchor pass read the newest 5000 labels of the agreement window (the anchor pass the longer of that window and `findings.recalibrateEvery`), and one (session, metric) takes at most 50 label versions (409 beyond).
- `labels.perMetric[metric]` has `gated` always, `agreement: null` when the window has no labels, and `drift`, `driftN`, `driftAt` once an anchor pass has produced one (a metric whose labels have aged out of the window still shows its last drift). `labels.skipped.budget` counts anchor passes cut short by the budget; `findings.skipped.uncalibrated` counts findings not written because of the gate (a gated alert is counted again on each tick).
- The gate applies to every finding write, alert refreshes included. `findings.enabled: false` keeps labels, agreement and anchor re-scoring running.
- An anchor pass re-scores a session with its whole profile (same evaluators, same `maxInvocations`), not only the labelled metric, so the reserve and settle are the profile's own and every labelled metric of that session gets a new score from one run. The session's profile is the one recorded when it was scored; a session whose trace has left memory or whose profile left the config is skipped and goes to the back of the queue.
- The last re-score time of each anchor is kept in `online_state` (`anchor/<metric>/<session>`), the last drift in `drift/<metric>`, and the pass time in `labels_recalibrated_ms`. A pass cut short by the budget does not set the pass time, so the next tick retries it. Judge tokens of a pass are added to the day's persisted extra spend (shared with findings); Jev calls of a pass are metered in the budget but not persisted across a restart.
- `label_session` takes the author from the caller's identity (`by` is empty in the no-login mode); it is not an argument. The `rsi_loop` prompt is registered by `mcptools.NewServer`, so stdio and `/mcp` serve the same text from `skills/embed.go`.
- The UI offers the buttons on the expanded card of a completed session, not in a separate queue (the Sessions tab's "annotation queue" is a golden-session picker kept in the browser).

## 5. Config

In the online config file (section 2 of `docs/online-evaluation.md`), all optional:

```json
"findings": {"enabled": true, "every": "6h", "window": "24h", "minSessions": 8, "minScore": 0.7,
             "maxSessions": 8, "verifyWithin": "48h", "minAgreement": 0.7, "recalibrateEvery": "24h"}
```

`enabled: false` keeps alerts and labels working and writes no findings; corrections can still be registered and
verified. Helm renders it from `online.findings` values.

## 6. The skill

`skills/ego-rsi/SKILL.md` (also the `rsi_loop` MCP prompt) tells an agent how to run one iteration:

1. `get_quality`: if nothing is open and no finding exists, stop.
2. `get_findings` (open). For each, `explain_regression` or `explain_session` on its evidence, and read the
   unsupported claims and failing rubrics yourself before trusting the summary.
3. Decide one correction per finding: a prompt change, a tool-use rule, a new check in a skill. Keep it as small
   as the evidence allows. Ship it under a new `service.version`.
4. `register_correction` with the finding id, what changed and the version.
5. Come back later (the verification window is in the reply). `list_corrections`: `verified` closes the
   finding; `failed` means the finding is open again with your attempt in its history, so try something else
   or escalate to a person.
6. Never label your own sessions; labels are for people.

## Phases

1. MCP in `serve` with sender-token auth, the tool registry package, tenant-limited runs, `get_quality`,
   `explain_regression`, `explain_session`; chart `rootPaths`. **Implemented (2026-10-21).**
2. Findings pass, store, API, sinks, MCP `get_findings`; corrections store, API, sampling burst, verification,
   sinks, MCP `register_correction` + `list_corrections`. **Implemented (2026-10-21).**
3. Labels store, API, agreement gate, anchor re-scoring, MCP `label_session`; Quality view and inspector; the
   skill and the `rsi_loop` prompt; docs (`STATUS.md`, `online-evaluation.md` phase 3 moves here, README
   paragraph). **Implemented (2026-10-21).**
