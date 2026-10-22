import type { LimitItem, UsageSession } from './types';

// A burst is a session that makes many model calls in a short span: a run that does not stop, each turn re-sending
// its growing context. Its tokens are one session's, so it ranks first in "Largest sessions" - but a daily total
// hides it.
export const BURST_CALLS = 20;
export const BURST_WINDOW_MS = 10 * 60 * 1000;

export function isBurst(r: UsageSession): boolean {
  return r.calls >= BURST_CALLS && r.end - r.start <= BURST_WINDOW_MS;
}

// A daily budget is an alert from here: the work it pays for stops when it is spent.
export const BUDGET_ALERT = 0.9;

/** The budgets (tokens, calls) that are at least 90% spent, fullest first. */
export function budgetAlerts(items: LimitItem[]): Array<LimitItem & { used: number; fraction: number }> {
  return items
    .filter((i): i is LimitItem & { used: number } => (i.unit === 'tokens' || i.unit === 'calls') && i.limit > 0 && i.used !== undefined)
    .map((i) => ({ ...i, fraction: i.used / i.limit }))
    .filter((i) => i.fraction >= BUDGET_ALERT)
    .sort((a, b) => b.fraction - a.fraction);
}
