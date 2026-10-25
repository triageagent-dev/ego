import React, { useCallback, useState } from 'react';
import { css } from '@emotion/react';
import { getOnlineSeries, getOnlineStatus, getUsage } from '../../api/client';
import type { OnlineSeriesPoint, UsageSeries } from '../../lib/types';
import { estimateCostUsd } from '../../lib/pricing';
import { useLiveData } from '../../lib/connection';
import { useSelectedTenant } from '../../lib/tenant';
import { useTraceContext } from '../../context/TraceContext';
import { useTurnQuality } from '../quality/TurnQuality';
import { useEgoSelection } from '../../lib/egoSelection';

interface Usage {
  tokens: number;
  calls: number;
  cost: number;
  // False while no model of the window has a list price: the cost is then unknown, not zero.
  priced: boolean;
}

interface Figures {
  usage?: Usage;
  scored?: number;
  // The ego score: the mean online score of the last 24 hours, weighted by how many scores each hour had.
  ego?: number | null;
}

// 1,234,567 -> 1.2M: short enough for the narrow rail.
function short(n: number): string {
  if (n >= 1e9) return `${(n / 1e9).toFixed(1)}B`;
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
  if (n >= 1e3) return `${Math.round(n / 1e3)}k`;
  return String(n);
}

// –, $0, <1¢, $1.23, $12.3, $345, $1.2k: at most five characters, which is what the narrow rail holds.
function shortCost(usd: number, priced: boolean): string {
  if (!priced) return '–';
  if (usd <= 0) return '$0';
  if (usd < 0.01) return '<1¢';
  if (usd >= 1000) return `$${(usd / 1000).toFixed(1)}k`;
  if (usd >= 100) return `$${Math.round(usd)}`;
  if (usd >= 10) return `$${usd.toFixed(1)}`;
  return `$${usd.toFixed(2)}`;
}

function usageTotals(series: UsageSeries): Usage {
  const t: Usage = { tokens: 0, calls: 0, cost: 0, priced: false };
  for (const b of series.buckets) {
    t.tokens += b.inputTokens + b.outputTokens;
    t.calls += b.calls;
    const cost = estimateCostUsd(b.model, b.inputTokens, b.outputTokens, b.cacheReadTokens, b.cacheWriteTokens);
    if (cost != null) {
      t.cost += cost;
      t.priced = true;
    }
  }
  return t;
}

// The mean of the hourly mean scores, each weighted by its count; null when nothing was scored.
function egoScore(series: OnlineSeriesPoint[]): number | null {
  let sum = 0;
  let n = 0;
  for (const p of series) {
    sum += p.mean * p.n;
    n += p.n;
  }
  return n > 0 ? sum / n : null;
}

type Row = [label: string, value: string, title: string];

// The ego score and the LLM usage of the last 24 hours, with the sessions scored in that time, for the empty part of
// the sidebar. Each part is left out where the role cannot see it or the server does not keep it. A click opens the
// view the numbers come from.
export const SidebarUsage: React.FC<{ collapsed: boolean }> = ({ collapsed }) => {
  const { actions } = useTraceContext();
  const tenant = useSelectedTenant();
  const [figures, setFigures] = useState<Figures>({});
  // An agent that works in turns has no judge score until online evaluation scores it: its turns stand in.
  const turnQuality = useTurnQuality();
  const { hours } = useEgoSelection();
  const windowName = hours === 24 ? '24 hours' : hours === 24 * 7 ? '7 days' : '30 days';

  // The tenant is read by the requests, which narrow by it.
  const load = useCallback(async (current: () => boolean) => {
    const [u, o, e] = await Promise.allSettled([getUsage(24), getOnlineStatus(), getOnlineSeries(hours)]);
    if (!current()) return;
    setFigures({
      usage: u.status === 'fulfilled' ? usageTotals(u.value) : undefined,
      scored: o.status === 'fulfilled' ? o.value.sessionsScored24h : undefined,
      ego: e.status === 'fulfilled' ? egoScore(e.value) : undefined,
    });
  }, [tenant, hours]);

  useLiveData(['usage', 'quality'], load);

  const day: Row[] = [];
  if (figures.usage) {
    day.push(['tokens', short(figures.usage.tokens), `${figures.usage.tokens.toLocaleString()} tokens`]);
    day.push(['calls', short(figures.usage.calls), `${figures.usage.calls.toLocaleString()} LLM calls`]);
    day.push(['cost', shortCost(figures.usage.cost, figures.usage.priced), 'computed from token counts and list prices: approximate, not the provider\'s bill']);
  }
  if (figures.scored != null) day.push(['scored', short(figures.scored), 'sessions scored by online evaluation (quality)']);
  const ego: Row[] = [];
  if (figures.ego != null) ego.push(['score', `${Math.round(figures.ego * 100)}%`, `mean online score of the last ${windowName}, over every metric`]);
  else if (turnQuality.turns > 0 && turnQuality.subject) {
    ego.push(['score', `${Math.round((turnQuality.ok / turnQuality.turns) * 100)}%`, `${turnQuality.ok} of ${turnQuality.turns} turns of ${turnQuality.subject.agent} ended without a failure in the last ${windowName}`]);
  }
  if (ego.length === 0 && day.length === 0) return null;

  const rowsOf = (list: Row[]) =>
    list.map(([label, value, title]) => (
      <span key={label} css={[rowStyle, collapsed && collapsedRowStyle]} title={title}>
        <span css={valueStyle}>{value}</span>
        <span css={labelStyle}>{label}</span>
      </span>
    ));

  return (
    <div css={[boxStyle, collapsed && collapsedBoxStyle]}>
      {ego.length > 0 && (
        <button css={sectionStyle} onClick={() => actions.setCurrentView('quality')} title={`Last ${windowName}. Open ego.`}>
          <span css={headingStyle}>ego</span>
          {rowsOf(ego)}
        </button>
      )}
      {day.length > 0 && (
        <button css={sectionStyle} onClick={() => actions.setCurrentView(figures.usage ? 'usage' : 'quality')} title="Last 24 hours. Open usage.">
          <span css={headingStyle}>24h</span>
          {rowsOf(day)}
        </button>
      )}
    </div>
  );
};

const boxStyle = css`
  margin: auto 8px 10px;
  display: flex;
  flex-direction: column;
  gap: 6px;
  font-family: var(--font-display);
`;

const sectionStyle = css`
  padding: 8px 10px;
  display: flex;
  flex-direction: column;
  gap: 6px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius);
  background: transparent;
  font-family: inherit;
  text-align: left;
  cursor: pointer;

  &:hover {
    background: var(--bg-elevated);
  }
`;

const collapsedBoxStyle = css`
  margin: auto 4px 10px;

  button {
    padding: 6px 2px;
    align-items: center;
    text-align: center;
  }
`;

const headingStyle = css`
  font-size: 11px;
  color: var(--text-tertiary);
`;

const rowStyle = css`
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
`;

const collapsedRowStyle = css`
  flex-direction: column;
  align-items: center;
  gap: 0;
`;

const valueStyle = css`
  font-size: 13px;
  font-variant-numeric: tabular-nums;
  color: var(--text-primary);
`;

const labelStyle = css`
  font-size: 11px;
  color: var(--text-tertiary);
`;
