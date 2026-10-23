import type { TurnRow } from './types';

// What a window of turns says about an always-on agent. Pure functions over the rows the server sends.

export function percentile(sorted: number[], p: number): number {
  if (sorted.length === 0) return 0;
  const i = Math.min(sorted.length - 1, Math.max(0, Math.ceil((p / 100) * sorted.length) - 1));
  return sorted[i];
}

export interface TurnStats {
  turns: number;
  perHour: number;
  medianMs: number;
  p95Ms: number;
  // Share of the window with a turn running, 0 to 1: the agent is awake that long, asleep the rest.
  awake: number;
  failedTurns: number;
  toolCalls: number;
  failedToolCalls: number;
  tokens: number;
  cachedShare: number;
  modelMs: number;
  toolMs: number;
  approvalMs: number;
  turnMs: number;
  tools: Array<{ name: string; calls: number; failed: number }>;
}

export function turnStats(rows: TurnRow[], fromMs: number, toMs: number): TurnStats {
  const durations = rows.map((r) => r.durationMs).sort((a, b) => a - b);
  // Turns can overlap (two wakes at once): the union of their spans is what "awake" means.
  const spans = rows.map((r) => [Math.max(r.startMs, fromMs), Math.min(r.startMs + r.durationMs, toMs)] as const).filter(([a, b]) => b > a).sort((a, b) => a[0] - b[0]);
  let awakeMs = 0;
  let end = -Infinity;
  for (const [a, b] of spans) {
    if (a > end) awakeMs += b - a;
    else if (b > end) awakeMs += b - end;
    end = Math.max(end, b);
  }
  const tools = new Map<string, { calls: number; failed: number }>();
  let input = 0;
  let cached = 0;
  let output = 0;
  const sum = { model: 0, tool: 0, approval: 0, turn: 0, calls: 0, failedCalls: 0, failedTurns: 0 };
  for (const r of rows) {
    input += r.inputTokens;
    cached += r.cacheReadTokens ?? 0;
    output += r.outputTokens;
    sum.model += r.modelMs;
    sum.tool += r.toolMs;
    sum.approval += r.approvalMs;
    sum.turn += r.durationMs;
    if (r.failed || r.failedTools) sum.failedTurns++;
    for (const [name, c] of Object.entries(r.toolCalls ?? {})) {
      const t = tools.get(name) ?? { calls: 0, failed: 0 };
      t.calls += c.calls;
      t.failed += c.failed ?? 0;
      tools.set(name, t);
      sum.calls += c.calls;
      sum.failedCalls += c.failed ?? 0;
    }
  }
  const hours = Math.max(1e-9, (toMs - fromMs) / 3_600_000);
  return {
    turns: rows.length,
    perHour: rows.length / hours,
    medianMs: percentile(durations, 50),
    p95Ms: percentile(durations, 95),
    awake: Math.min(1, awakeMs / Math.max(1, toMs - fromMs)),
    failedTurns: sum.failedTurns,
    toolCalls: sum.calls,
    failedToolCalls: sum.failedCalls,
    tokens: input + output,
    cachedShare: input > 0 ? cached / input : 0,
    modelMs: sum.model,
    toolMs: sum.tool,
    approvalMs: sum.approval,
    turnMs: sum.turn,
    tools: [...tools.entries()].map(([name, c]) => ({ name, ...c })).sort((a, b) => b.failed - a.failed || b.calls - a.calls),
  };
}

// 1.2s, 3m 05s, 2h 10m
export function fmtDuration(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(ms < 10_000 ? 1 : 0)}s`;
  const s = Math.round(ms / 1000);
  if (s < 3600) return `${Math.floor(s / 60)}m ${String(s % 60).padStart(2, '0')}s`;
  return `${Math.floor(s / 3600)}h ${String(Math.floor((s % 3600) / 60)).padStart(2, '0')}m`;
}

// 3,960,702 -> 3.9M, 132,391 -> 132.3k: cut, not rounded, to one decimal.
export function shortCount(n: number): string {
  if (n >= 1e6) return `${(Math.floor(n / 1e5) / 10).toFixed(1)}M`;
  if (n >= 1e3) return `${(Math.floor(n / 100) / 10).toFixed(1)}k`;
  return String(n);
}
