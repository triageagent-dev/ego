import React, { useEffect, useState } from 'react';
import { css } from '@emotion/react';
import { getUsage } from '../../api/client';
import { estimateCostUsd, formatCostUsd } from '../../lib/pricing';
import { useSelectedTenant } from '../../lib/tenant';
import { useTraceContext } from '../../context/TraceContext';

interface Totals {
  tokens: number;
  calls: number;
  cost: number;
}

const REFRESH_MS = 5 * 60_000;

// 1,234,567 -> 1.2M: short enough for the narrow rail.
function short(n: number): string {
  if (n >= 1e9) return `${(n / 1e9).toFixed(1)}B`;
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
  if (n >= 1e3) return `${Math.round(n / 1e3)}k`;
  return String(n);
}

// $0.0123, $12.34, $345, $1.2k: the cost in as few characters as keep it readable.
function shortCost(usd: number): string {
  if (usd >= 1000) return `$${(usd / 1000).toFixed(1)}k`;
  if (usd >= 100) return `$${Math.round(usd)}`;
  return formatCostUsd(usd);
}

// The last 24 hours of LLM usage in three numbers, for the empty part of the sidebar. A click opens the usage view.
// Nothing is shown where the role has no usage view or the server keeps no ledger.
export const SidebarUsage: React.FC<{ collapsed: boolean }> = ({ collapsed }) => {
  const { actions } = useTraceContext();
  const tenant = useSelectedTenant();
  const [totals, setTotals] = useState<Totals | null>(null);

  useEffect(() => {
    let active = true;
    const load = () =>
      getUsage(24)
        .then((series) => {
          if (!active) return;
          const t: Totals = { tokens: 0, calls: 0, cost: 0 };
          for (const b of series.buckets) {
            t.tokens += b.inputTokens + b.outputTokens;
            t.calls += b.calls;
            t.cost += estimateCostUsd(b.model, b.inputTokens, b.outputTokens) ?? 0;
          }
          setTotals(t);
        })
        .catch(() => active && setTotals(null));
    load();
    const id = setInterval(load, REFRESH_MS);
    return () => {
      active = false;
      clearInterval(id);
    };
  }, [tenant]);

  if (!totals) return null;
  const rows: Array<[string, string, string]> = [
    ['tokens', short(totals.tokens), `${totals.tokens.toLocaleString()} tokens`],
    ['calls', short(totals.calls), `${totals.calls.toLocaleString()} LLM calls`],
    ['cost', shortCost(totals.cost), 'estimated from list prices, not billed'],
  ];
  return (
    <button
      css={[boxStyle, collapsed && collapsedBoxStyle]}
      onClick={() => actions.setCurrentView('usage')}
      title="LLM usage, last 24 hours. Open usage."
    >
      <span css={headingStyle}>24h</span>
      {rows.map(([label, value, title]) => (
        <span key={label} css={[rowStyle, collapsed && collapsedRowStyle]} title={title}>
          <span css={valueStyle}>{value}</span>
          <span css={labelStyle}>{label}</span>
        </span>
      ))}
    </button>
  );
};

const boxStyle = css`
  margin: auto 8px 10px;
  padding: 8px 10px;
  display: flex;
  flex-direction: column;
  gap: 6px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius);
  background: transparent;
  font-family: var(--font-display);
  text-align: left;
  cursor: pointer;

  &:hover {
    background: var(--bg-elevated);
  }
`;

const collapsedBoxStyle = css`
  margin: auto 4px 10px;
  padding: 6px 2px;
  align-items: center;
  text-align: center;
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
