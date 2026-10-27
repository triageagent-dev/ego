# Online evaluation

- **Status**: Phases 1 and 2 implemented (2026-10-04); phase 3 belongs to the consumer, phase 4 open
- **Scope**: ego (`serve`). Consumers of the webhook live in their own repos.

A one-off run scores traces only when someone uploads them. Online evaluation adds a continuous loop over the
traces ego already ingests:
**sample → score → detect → notify**. It stays producer-agnostic: call kinds
come from `gen_ai.agent.name` (else the trace root span name), the tenant from
`--usage-tenant-attr`, and nothing in the code names a producer.

## 1. Sampling

The unit is a completed session (`SessionStore.completeSession`, 30 s of quiet). For a sender that declares a thread
(`ego.thread`, an always-on agent) it is instead a closed segment: a run of whole traces of that thread, closed for good
when the thread's next trace arrives after `--segment-idle` of quiet, past `--segment-max-spans` or
`--segment-max-age`, or from the sweep once the thread has stopped (`SessionStore.closeSegmentLocked`). The 30 s
quiet state is then only what the UI shows, and scoring follows the end of the thread's activity by up to `--segment-idle`. For a segment n >= 2 the
judge prompts start with the last user prompt and final answer of segment n-1, fenced as an earlier part of the same
thread. A hook hands each one to the sampler, which reads its features from the spans:

- **kind**: the call kind with the most tokens in the session (same rule as
  the usage ledger), **tenant**, **service**, `service.version`, **model**;
- error spans, total tokens, span-window latency;
- a **fingerprint**: hash of kind, normalised first user input and the tool-call
  name sequence.

A session is scored only when a profile matches its kind (section 2). Then:

1. **Interest reasons** put it in the boost lane:
   - `error`: it has error spans;
   - `outlier`: tokens or latency above the kind's rolling mean + 3σ (log scale);
   - `change`: a `service.version` or model not seen for this kind in the last
     24h starts a burst. The next `afterChangeSessions` sessions of the kind are
     boosted, because quality shifts happen at changes.
2. **Not worth judging**: a session with no tool calls whose first user message
   (leading `[...]` note lines such as a timestamp or screen context are
   ignored) is at most `sampling.trivial.maxUserChars` (40) and whose last reply
   is at most `maxReplyChars` (100) is skipped, counted as `trivial` in
   `/api/online/status`. A greeting or goodbye costs judge tokens and teaches
   nothing, and it is skipped before the rate buckets, so it neither uses a
   kind's allowance nor moves its baselines. A session whose text cannot be read
   is never skipped this way, and neither is one with error spans. `"trivial": {"disabled": true}` turns it off.
3. **Dedup**: a fingerprint already scored inside `dedupWindow` and without an
   interest reason is skipped. Novelty is not a boost reason: a producer whose
   prompts carry event text makes almost every session novel.
4. **Rate**: two token buckets per (tenant, kind). The base lane takes
   `perKindPerHour`, the boost lane `boostPerKindPerHour`. Rare kinds are not
   crowded out by frequent ones.
5. **Budget**: a daily cap on judge tokens (Gemini, metered from the response's
   usage metadata) and on Jev calls (one per scored invocation). The scheduler
   reserves an estimate before scoring (an EWMA of the profile's actual cost)
   and books the actual cost after. When the cap is reached, scoring stops until
   00:00 UTC and the skip reason is `budget`. A profile that uses a judge family
   whose cap is 0 is a config error. Vertex managed-eval metrics are refused,
   because the budget cannot meter them, and so are metrics that need a golden
   eval set.

Every decision is counted by reason and shown at `GET /api/online/status`.

## 2. Scoring

Profiles map a kind to evaluators. They live in a JSON file (`--online-config`,
a ConfigMap rendered from Helm values):

```json
{
  "budget": {"judgeTokensPerDay": 2000000, "jevCallsPerDay": 1000},
  "sampling": {"perKindPerHour": 6, "boostPerKindPerHour": 6,
               "afterChangeSessions": 20, "dedupWindow": "6h",
               "trivial": {"maxUserChars": 40, "maxReplyChars": 100}},
  "profiles": [
    {"kind": "rca", "maxInvocations": 3,
     "evaluators": [{"name": "jev_grounded"}, {"name": "hallucinations_v1", "numSamples": 1}]}
  ],
  "detector": {"window": "6h", "baseline": "168h", "minSamples": 12, "minDrop": 0.1},
  "sinks": [{"type": "discord", "urlEnv": "EGO_DISCORD_WEBHOOK"},
            {"type": "webhook", "url": "http://…", "bearerTokenFile": "/var/run/…/token"}]
}
```

- Evaluators use the `POST /api/evaluate` evaluator shape. Live traffic has no golden set, so the ones that run
  are the Gemini judges `hallucinations_v1`, `rubric_based_final_response_quality_v1` and
  `rubric_based_tool_use_quality_v1` (both need `rubrics`), and Jev: the `jev_*` questions and `jev_custom` (needs
  `questions`). A name that is none of these, a metric that needs a reference, and a Vertex-managed metric, which
  the budget cannot meter, are refused when the profile is loaded or saved. `numSamples` is new and defaults to 1
  here, since one judge sample per invocation is the cost lever (the upload path keeps 5).
- `maxInvocations` scores only the last N invocations of a long session.
- A profile may also match `service` or `tenant`; the most specific match wins. Its `kind` may be a glob
  (`*-spot` covers every spot, whatever it is called); a profile that names the kind exactly beats a glob.
- `perHour` gives a profile its own base rate per (tenant, kind); 0 takes `sampling.perKindPerHour`.
- **Jobs in the settings page.** The profiles and `sampling.perKindPerHour` can be edited at runtime: settings (account
  menu), "scoring jobs". An admin adds, changes and removes jobs there, picks a saved rubric set for a rubric metric
  and sees the kinds that sent sessions with no job to score them. Saving checks the jobs like a config file and
  applies them from the next session, with no restart; they are kept in the archive and win over the file's profiles
  until "Use the server's configuration" drops them. `GET /api/online/jobs` reads, `PUT` replaces (admin only),
  `DELETE` goes back to the file. Budgets, the detector, findings and sinks stay in the file.
- Production traffic has no golden answers, so profiles use reference-free
  metrics.

Each score is a row of `online_scores` (session, trace, time, tenant, kind,
service, version, model, metric, score, status, evaluator kind, judge model,
sample reason). The results also go into Run History: one run per (kind, UTC
hour), named `online · <kind> · <hour>`, so the existing run views and
drill-down work unchanged. The run finishes when its hour closes.

## 3. Detection

Every 10 minutes, for each (tenant, kind, metric):

- **baseline** = scores in `[now − baseline, now − window)`;
  **current** = scores in `[now − window, now]`;
- a regression needs both sides to have at least `minSamples`, a drop of the
  mean of at least `minDrop`, and `drop > 2.5 × SE` (Welch standard error).
  One bad score never alerts;
- **attribution**: a `service.version` or model that has at least half of the
  current samples and under 10% of the baseline is named as the suspected change.

Alerts have a lifecycle in `online_alerts`: **open → updated → resolved**.
They update when the drop grows by another `minDrop` and resolve after two
checks in a row with the drop under `minDrop / 2`. Hysteresis keeps them from
flapping.

## 4. Notifications

- **Discord** (general): open, update and resolve. Each message has the metric,
  kind and tenant, baseline → current with n, the suspected change, and links
  to the three worst sessions.
- **Webhook** (generic JSON, optional bearer token read from a file on every
  send, so a projected Kubernetes token rotates): same events as
  `{"type": "ego.quality.v1", "status": "open|updated|resolved", …}`.
  An agent can consume it as input to its own self-tuning loop. ego
  knows nothing about the consumer.
- **Event** (generic event-ingest body, same bearer token handling):
  `{"type": "EvalQualityDegraded|EvalQualityRecovered", "severity":
  "warning|info", "source": "<kind>/<metric>", "tenant", "message", "raw":
  <webhook body>}`. Point it at a receiver that already takes this shape;
  degraded and recovered on the same source open and close one record there.
- **Daily digest** to Discord at 08:00 UTC: per kind, mean score with a 24h
  delta, sessions scored, judge spend against the cap, and open alerts.

## 5. API and UI

- `GET /api/online/status`: config summary, queue, budget used today, and
  decisions by reason.
- `GET /api/online/series?hours=N`: hourly (tenant, kind, metric) mean and n.
- `GET /api/online/alerts?state=open|all`.
- `GET /api/online/quality?kind=&hours=N` (default 24), `GET /api/online/alerts/{id}/explain` and
  `GET /api/online/sessions/{id}/explain`: the data behind the MCP tools `get_quality`,
  `explain_regression` and `explain_session` (`docs/rsi.md`).
- A **Quality** view in the sidebar shows the trend per kind and metric, the
  alerts, and the budget gauge, then the findings, the corrections and the judge agreement of `docs/rsi.md`.
- **Labels.** A person can agree or disagree with a judge's score (`POST /api/online/labels`, the buttons on an
  expanded session card, the `label_session` MCP tool). Agreement per metric over seven days is in
  `/api/online/status` and the daily digest; a metric whose judge people mostly disagree with writes no findings,
  and labelled sessions are re-scored every `findings.recalibrateEvery` to report judge drift. See
  `docs/rsi.md`, section 4.

These need `--session-db`. Without it, they return 503 and the loop does not
start.

## Phases

1. Hook, features, sampler, budget, worker, `online_scores`, hourly runs,
   status and series endpoints, Helm values.
2. Detector, `online_alerts`, Discord and webhook sinks, digest, alerts
   endpoint, Quality view.
3. Consumer side: an agent reads quality alerts as input to its self-tuning
   loop. The diagnosis, correction and verification half lives in this repo now:
   see `docs/rsi.md` (findings, corrections, labels and the skill are implemented).
4. Judge cascade (1 sample, then 5 when the score is borderline). The calibration
   anchor set (labelled sessions re-scored for judge drift) and label agreement
   as a measure of the judge moved to `docs/rsi.md`, section 4.
