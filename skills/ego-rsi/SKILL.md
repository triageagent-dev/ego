---
name: ego-rsi
description: Use when an agent should check its own quality in ego and correct itself - reads ego's findings over MCP, changes the agent, registers the correction and waits for ego's verification.
---

# ego-rsi: check your own quality and correct yourself

ego scores the sessions you produce (from your OpenTelemetry traces) and, when scores are low, writes a
**finding**: the failure mode, the sessions that show it and a suggested correction. You read the finding, change
yourself, tell ego what you changed, and ego checks later whether the scores recovered. ego never changes you; it
only diagnoses and verifies.

You need ego's MCP tools (`/mcp` on the ego server, authenticated with the same token you send your traces with).
All tools are limited to your own tenants. Run one iteration of the loop below per invocation.

## The loop

### 1. Is anything wrong? `get_quality`

Call `get_quality` (optional `kind`, `hours`, default 24). The reply has `open_alerts` (statistically significant
drops, each with an `alertId`), `series` (the hourly mean score per tenant, kind and metric), `sessions_scored`
and the day's judge budget.

If there are no open alerts and `get_findings` (step 2) is empty, stop: nothing needs correcting. Do not invent
work.

### 2. Read the findings. `get_findings`

Call `get_findings` with `state: "open"` (the default; optional `kind`, `since`). Each finding has:
`finding_id`, `tenant`, `kind`, `metric`, `state`, `diagnosis` (`failure_mode`, `summary`,
`suggested_correction`, `confidence`, `evidence` as `{session_id, excerpt, why}`), `sessions` (evidence session
ids), `window` (the span and mean it was written from), `model` and `history`.

`history` lists the corrections already tried for this finding, with their state and numbers. Read it first: if an
attempt `failed`, do not repeat it.

The diagnosis is a judge's summary, not a fact. Before you trust it, look at the sessions yourself:

- `explain_session` with `session_id` returns, per metric, the score, the judge's rationale and the verdicts:
  per-sentence labels for `hallucinations_v1` (`unsupported`, `contradictory`, `disputed` first), per-rubric
  verdicts for rubric metrics, the compared tool calls for trajectory metrics and the decision for `jev_*`.
- `explain_regression` with `alert_id` (from `get_quality`, or the finding's `alert_id`) returns the alert, the
  version or model it is attributed to (`suspect`) and its worst sessions in the same shape.
- `summarize_session` with `session_id` shows what was said and which tools were called.

Read the unsupported claims and failing rubrics. If the evidence does not show the failure mode the summary
claims, say so to a person instead of acting on it.

### 3. Decide one correction per finding

A correction is a change to yourself: a prompt change, a tool-use rule, a new check in a skill. Keep it as small
as the evidence allows and make it testable ("only state figures that appear in a tool result"). One correction
answers one finding; do not bundle fixes for several findings into one change, because ego verifies each
correction against its own finding's series and a bundle cannot be told apart.

Ship the change under a **new `service.version`** (set it on your spans' resource). ego uses the version to count
only the sessions that already carry the change.

### 4. Tell ego. `register_correction`

Call `register_correction` with:

- `finding_id`: the finding this change answers;
- `kind`: the finding's kind;
- `what`: what you changed, in plain words (at most 2000 characters);
- `version`: the `service.version` the change ships as;
- `by`: your agent name;
- `tenant`: only when your token does not imply one and there is no `finding_id`.

The reply is the correction: `correction_id`, `state: "pending"` and `verification` with the `before` mean and n,
and `window_end`, the time by which ego decides. The finding is now `corrected`, and ego scores your next
sessions first so the window fills quickly.

### 5. Come back later. `list_corrections`

After the change has produced sessions (at least a few, up to the verification window), call `list_corrections`
(optional `kind`, `state`). Each correction shows its `state` and `verification` (`before`, `after` with the mean
and n, `reason`).

- `verified`: the scores recovered, and the finding is `resolved`. Done.
- `failed`: they did not. The finding is `open` again with your attempt in its `history`. Start again at step 2
  and try something different, or hand it to a person.
- `pending`: ego is still collecting sessions. Wait; do not register another correction for the same finding.

### 6. Never label your own sessions

`label_session` records a person's verdict on a judge's score (`session_id`, `metric`, `verdict` `agree` or
`disagree`, optional `score` and `note`). ego uses labels to measure whether the judge can be trusted: a metric
whose judge people mostly disagree with stops producing findings. Labels are for people reading the evidence, not
for the agent being judged. Do not call it on sessions you produced, and never to improve your own numbers.

## Rules

- Read evidence before acting; a finding is a hypothesis.
- One correction answers one finding and ships under a new `service.version`.
- Do not register a correction for a change you did not make.
- If a finding's `confidence` is low or its evidence contradicts it, escalate to a person.
