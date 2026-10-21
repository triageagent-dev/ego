import type { MetricMetadata, Run } from '../../lib/types';

// What a metric group tells you and when to reach for it, for the evaluate page's cards. Keyed by the category the
// server lists; a category not named here gets a line made from the metrics' own requirements.
const BLURBS: Record<string, { what: string; when: string }> = {
  trajectory: {
    what: 'Compares the tools the agent called, and their order, with a golden session.',
    when: 'Use it when the agent acts through tools and a good run is known.',
  },
  response: {
    what: 'Compares the final answer with a golden answer, by overlap or by an LLM verdict.',
    when: 'Use it when the answer has a right form; the overlap score is free and runs locally.',
  },
  quality: {
    what: 'An LLM judge scores the answer or the tool use against rubrics you write.',
    when: 'Use it when there is no golden session but you can say what good looks like.',
  },
  safety: {
    what: 'Flags claims the context does not support, and unsafe content.',
    when: 'A sound default for any agent that answers people; no golden session needed.',
  },
  'multi-turn': {
    what: 'Judges a whole conversation: did the task succeed, were the steps and tools sound.',
    when: 'Use it for long sessions where one answer does not tell the story.',
  },
  jev: {
    what: 'The decision model judges whether the request was resolved and the answer grounded.',
    when: 'Fast and cheap; a good first pass over many sessions.',
  },
};

export function blurbFor(category: string, metrics: MetricMetadata[]): { what: string; when: string } {
  const known = BLURBS[category.toLowerCase()];
  if (known) return known;
  const needs: string[] = [];
  if (metrics.some((m) => m.requiresEvalSet)) needs.push('a golden set');
  if (metrics.some((m) => m.requiresLLM)) needs.push('an LLM judge');
  if (metrics.some((m) => m.requiresGCP)) needs.push('GCP');
  if (metrics.some((m) => m.requiresRubrics)) needs.push('rubrics');
  return {
    what: `${metrics.length} metric${metrics.length === 1 ? '' : 's'} in this group.`,
    when: needs.length ? `Needs ${needs.join(', ')}.` : 'Needs nothing beyond the traces.',
  };
}

export interface GroupStats {
  // Runs that scored at least one metric of the group.
  runs: number;
  // Passed over decided (passed + failed) results; null when none was decided.
  pass: number | null;
  // Mean of the metrics' average scores; null when none produced a number.
  avg: number | null;
  // Errored over every result of the group, 0..1.
  errored: number;
  lastAt: string | null;
}

/** What the finished runs already say about a group's metrics. */
export function groupStats(metrics: MetricMetadata[], runs: Run[]): GroupStats {
  const names = new Set(metrics.map((m) => m.name));
  let used = 0;
  let passed = 0;
  let failed = 0;
  let errored = 0;
  const scores: number[] = [];
  let lastAt: string | null = null;
  for (const run of runs) {
    const perMetric = run.summary?.per_metric;
    if (!perMetric) continue;
    let touched = false;
    for (const [name, m] of Object.entries(perMetric)) {
      if (!names.has(name)) continue;
      touched = true;
      passed += m.passed;
      failed += m.failed;
      errored += m.errored;
      if (m.avg_score != null) scores.push(m.avg_score);
    }
    if (touched) {
      used++;
      if (!lastAt || Date.parse(run.createdAt) > Date.parse(lastAt)) lastAt = run.createdAt;
    }
  }
  const decided = passed + failed;
  const all = decided + errored;
  return {
    runs: used,
    pass: decided ? passed / decided : null,
    avg: scores.length ? scores.reduce((a, b) => a + b, 0) / scores.length : null,
    errored: all ? errored / all : 0,
    lastAt,
  };
}

/** `5m`, `3h`, `2d`: how long ago, in the fewest characters. */
export function ago(iso: string, now = Date.now()): string {
  const s = Math.max(0, (now - Date.parse(iso)) / 1000);
  if (s < 3600) return `${Math.max(1, Math.round(s / 60))}m`;
  if (s < 86400) return `${Math.round(s / 3600)}h`;
  return `${Math.round(s / 86400)}d`;
}
