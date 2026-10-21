# RSI: the agent corrects itself

- **Status**: Proposed (2026-10-21)
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

Tools (all transports; tenant-scoped unless noted):

| Tool | Does |
|---|---|
| `get_quality` | `kind?`, `hours?` (default 24): open alerts, hourly series per metric, sessions scored, budget used today. One call to answer "how am I doing". |
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
- **Evidence.** Up to `findings.maxSessions` (default 8) lowest-scoring sessions of the series in the window.
  For each: the first user message (truncated to 500 chars), the tool-call name sequence, the metric's rationale,
  and the per-sentence or per-rubric verdicts that failed, read from the online run results.
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

## 4. Labels

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
   `explain_regression`, `explain_session`; chart `rootPaths`.
2. Findings pass, store, API, sinks, MCP `get_findings`; corrections store, API, sampling burst, verification,
   sinks, MCP `register_correction` + `list_corrections`.
3. Labels store, API, agreement gate, anchor re-scoring, MCP `label_session`; Quality view and inspector; the
   skill and the `rsi_loop` prompt; docs (`STATUS.md`, `online-evaluation.md` phase 3 moves here, README
   paragraph).
