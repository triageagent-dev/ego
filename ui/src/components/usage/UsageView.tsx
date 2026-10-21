import React, { useCallback, useMemo, useState } from 'react';
import { css } from '@emotion/react';
import { sessionLink } from '../shell/routes';
import { pageStyle, headerStyle, titleStyle, subtitleStyle, filterRowStyle, segmentStyle, segLabelStyle, segBtnStyle, segActiveStyle, errorStyle, tilesStyle, tileStyle, tileLabelStyle, tileValueStyle, tileSubStyle, cardStyle, cardTitleStyle } from '../ui/page';
import { Bar } from 'react-chartjs-2';
import {
  Chart as ChartJS,
  CategoryScale,
  LinearScale,
  BarElement,
  Tooltip,
  Legend,
} from 'chart.js';
import { useLiveData } from '../../lib/connection';
import { getUsage, getUsageSessions, StorageUnavailableError } from '../../api/client';
import { tenantNames, useSelectedTenant, useTenantNames } from '../../lib/tenant';
import { estimateCostUsd, formatCostUsd } from '../../lib/pricing';
import type { UsageBucket, UsageSeries, UsageSession } from '../../lib/types';
import { PageTitle } from '../shell/PageTitle';
import { store, stored } from '../../lib/stored';

ChartJS.register(CategoryScale, LinearScale, BarElement, Tooltip, Legend);

// Token Usage view.
// Reads the LLM call ledger (GET /api/usage, /api/usage/calls?group=session).

type Dimension = 'kind' | 'tenant' | 'model' | 'service';

const RANGES: { label: string; hours: number }[] = [
  { label: '24h', hours: 24 },
  { label: '7d', hours: 24 * 7 },
  { label: '30d', hours: 24 * 30 },
  { label: '90d', hours: 24 * 90 },
];

const DIMENSIONS: { key: Dimension; label: string }[] = [
  { key: 'kind', label: 'Call kind' },
  { key: 'tenant', label: 'Tenant' },
  { key: 'model', label: 'Model' },
  { key: 'service', label: 'Service' },
];

// Categorical slots for the dark surface, in fixed order (validated against
// --card-bg #252a36: CVD ΔE 8.4, normal-vision ΔE 19.3; green sits under
// 3:1, which the breakdown table relieves). An eighth group and beyond fold
// into "Other", which names its members in the legend and tooltip.
const SLOTS = ['#3987e5', '#d95926', '#199e70', '#c98500', '#d55181', '#008300', '#9085e9'];
// Call kinds never fold: every kind is drawn under its own name. Seven more
// slots (red, olive, purple, lime, cyan, indigo, brown) are appended in an
// order that keeps the same checks on adjacent pairs (worst CVD ΔE still 8.4,
// normal-vision 19.3; all seven clear 3:1). The last four sit at least ΔE 9.1
// from every other slot, above the existing worst pair (7.1). Past fourteen
// kinds the slots repeat, and the legend, tooltip and table still name each.
const KIND_SLOTS = [...SLOTS, '#e66767', '#887800', '#a858b8', '#80a010', '#20a0b8', '#7060e0', '#a86048'];
const OTHER_COLOR = '#6b7586';
const OTHER = 'Other';
const OTHER_NAMED = 3;
const NONE = '(none)';

const SURFACE = '#252a36';

interface Totals {
  calls: number;
  errors: number;
  input: number;
  output: number;
  durationMs: number;
  cost: number;
  unpricedTokens: number;
}

function emptyTotals(): Totals {
  return { calls: 0, errors: 0, input: 0, output: 0, durationMs: 0, cost: 0, unpricedTokens: 0 };
}

function addBucket(t: Totals, b: UsageBucket) {
  t.calls += b.calls;
  t.errors += b.errors;
  t.input += b.inputTokens;
  t.output += b.outputTokens;
  t.durationMs += b.durationMs;
  const cost = estimateCostUsd(b.model, b.inputTokens, b.outputTokens);
  if (cost == null) t.unpricedTokens += b.inputTokens + b.outputTokens;
  else t.cost += cost;
}

// Legacy decision-model kinds. The core used to tag these calls with several
// kinds and now records them all as `decision` (ADR-063); spans still in the
// window carry the old names, so fold them into one row. Plain `announce` and
// `decide_event` (the MCP dry-run kind) stay as they are.
const LEGACY_DECISION_KIND = /^(decide_event[.+]|announce\+)/;

function groupName(b: UsageBucket, dim: Dimension): string {
  const name = b[dim] || NONE;
  if (dim === 'kind' && LEGACY_DECISION_KIND.test(name)) return 'decision';
  return name;
}

function formatTokens(n: number): string {
  if (n >= 1e9) return `${(n / 1e9).toFixed(2)}B`;
  if (n >= 1e6) return `${(n / 1e6).toFixed(2)}M`;
  if (n >= 1e3) return `${(n / 1e3).toFixed(1)}k`;
  return String(n);
}

// "voice, batch, chat +2": the first few folded members, largest first.
function memberList(members: string[]): string {
  const more = members.length - OTHER_NAMED;
  return members.slice(0, OTHER_NAMED).join(', ') + (more > 0 ? ` +${more}` : '');
}

function formatMs(ms: number): string {
  return ms >= 1000 ? `${(ms / 1000).toFixed(1)}s` : `${Math.round(ms)}ms`;
}

// A burst is a session that makes many model calls in a short span: a run
// that does not stop, each turn re-sending its growing context. Its tokens
// are one session's, so it ranks first in "Largest sessions" - but a daily
// total hides it.
const BURST_CALLS = 20;
const BURST_WINDOW_MS = 10 * 60 * 1000;

function isBurst(r: UsageSession): boolean {
  return r.calls >= BURST_CALLS && r.end - r.start <= BURST_WINDOW_MS;
}

function formatSpan(ms: number): string {
  const s = Math.max(1, Math.round(ms / 1000));
  return s >= 60 ? `${Math.floor(s / 60)}m ${s % 60}s` : `${s}s`;
}

function bucketLabel(start: number, bucketMs: number): string {
  const d = new Date(start);
  if (bucketMs >= 24 * 3600 * 1000) {
    return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
  }
  const hh = d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', hour12: false });
  return d.getHours() === 0 ? `${d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })} ${hh}` : hh;
}

export const UsageView: React.FC = () => {
  // The range and the grouping are remembered per browser; the range starts at 24h.
  const [hours, setHoursState] = useState(() => {
    const saved = Number(stored('ego.usage.hours'));
    return RANGES.find((r) => r.hours === saved)?.hours ?? 24;
  });
  const [dim, setDimState] = useState<Dimension>(() => {
    const saved = stored('ego.usage.dim');
    return DIMENSIONS.find((d) => d.key === saved)?.key ?? 'kind';
  });
  const setHours = (h: number) => {
    setHoursState(h);
    store('ego.usage.hours', String(h));
  };
  const setDim = (d: Dimension) => {
    setDimState(d);
    store('ego.usage.dim', d);
  };
  const [series, setSeries] = useState<UsageSeries | null>(null);
  const [sessions, setSessions] = useState<UsageSession[]>([]);
  const [error, setError] = useState<string | null>(null);
  const tenant = useSelectedTenant();
  useTenantNames();

  const load = useCallback(async (current: () => boolean) => {
    try {
      const [s, c] = await Promise.all([getUsage(hours), getUsageSessions(hours, 25)]);
      if (!current()) return;
      setSeries(s);
      setSessions(c);
      setError(null);
    } catch (err) {
      if (!current()) return;
      setError(
        err instanceof StorageUnavailableError
          ? 'Token usage needs the session database (start the server with --session-db).'
          : err instanceof Error ? err.message : String(err),
      );
    }
  }, [hours, tenant]);

  useLiveData('usage', load);

  const view = useMemo(() => {
    if (!series) return null;
    const totals = emptyTotals();
    const byGroup = new Map<string, Totals>();
    for (const b of series.buckets) {
      addBucket(totals, b);
      const g = groupName(b, dim);
      if (!byGroup.has(g)) byGroup.set(g, emptyTotals());
      addBucket(byGroup.get(g)!, b);
    }

    // The seven largest groups keep a colour; the rest fold into Other.
    // Call kinds never fold. Colours follow the group, not its rank: kept
    // groups are coloured alphabetically.
    const ranked = [...byGroup.entries()].sort((a, b) => (b[1].input + b[1].output) - (a[1].input + a[1].output));
    const slots = dim === 'kind' ? KIND_SLOTS : SLOTS;
    const kept = new Set((dim === 'kind' ? ranked : ranked.slice(0, slots.length)).map(([g]) => g));
    const colourOrder = [...kept].sort((a, b) => a.localeCompare(b));
    const colour = new Map<string, string>();
    colourOrder.forEach((g, i) => colour.set(g, slots[i % slots.length]));
    const folded = (g: string) => (kept.has(g) ? g : OTHER);
    const otherMembers = ranked.slice(kept.size).map(([g]) => g);

    // Time axis: every bucket in the window, empty ones included, so gaps read as gaps.
    const startAligned = Math.floor(series.from / series.bucketMs) * series.bucketMs;
    const starts: number[] = [];
    for (let t = startAligned; t < series.to; t += series.bucketMs) starts.push(t);
    const index = new Map(starts.map((t, i) => [t, i]));

    const stacks = new Map<string, number[]>();
    for (const g of [...colourOrder, ...(otherMembers.length ? [OTHER] : [])]) {
      stacks.set(g, new Array(starts.length).fill(0));
    }
    // Per time bucket, what Other is made of, so its tooltip can name it.
    const otherParts = starts.map(() => new Map<string, number>());
    for (const b of series.buckets) {
      const i = index.get(b.start);
      if (i == null) continue;
      const g = groupName(b, dim);
      const tokens = b.inputTokens + b.outputTokens;
      stacks.get(folded(g))![i] += tokens;
      if (!kept.has(g)) otherParts[i].set(g, (otherParts[i].get(g) ?? 0) + tokens);
    }

    const datasetOrder = [...stacks.keys()];
    const datasets = datasetOrder.map((g, di) => ({
      label: g === OTHER ? `${OTHER} (${memberList(otherMembers)})` : g,
      data: stacks.get(g)!,
      backgroundColor: colour.get(g) ?? OTHER_COLOR,
      borderColor: SURFACE,
      borderWidth: { top: di === datasetOrder.length - 1 ? 0 : 2 },
      borderSkipped: 'bottom' as const,
      borderRadius: di === datasetOrder.length - 1 ? { topLeft: 4, topRight: 4 } : 0,
      maxBarThickness: 28,
    }));

    const rows = ranked.map(([g, t]) => ({ group: g, colour: colour.get(g) ?? OTHER_COLOR, ...t }));
    // Index of the Other dataset (always last), or -1 when nothing folds.
    const otherIndex = otherMembers.length ? datasets.length - 1 : -1;
    return { totals, rows, labels: starts.map(t => bucketLabel(t, series.bucketMs)), datasets, otherIndex, otherParts };
  }, [series, dim]);

  const total = view ? view.totals.input + view.totals.output : 0;
  const bursts = useMemo(() => sessions.filter(isBurst), [sessions]);

  return (
    <div css={pageStyle}>
      <header css={headerStyle}>
        <PageTitle>
          <h1 css={titleStyle}>usage</h1>
          <p css={subtitleStyle}>
            from LLM spans · cost is estimated from list prices, not billed
          </p>
        </PageTitle>
      </header>

      <div css={filterRowStyle}>
        <div css={segmentStyle} role="group" aria-label="Time range">
          {RANGES.map(r => (
            <button key={r.hours} css={[segBtnStyle, hours === r.hours && segActiveStyle]} onClick={() => setHours(r.hours)}>
              {r.label}
            </button>
          ))}
        </div>
        <div css={segmentStyle} role="group" aria-label="Group by">
          <span css={segLabelStyle}>Group by</span>
          {DIMENSIONS.map(d => (
            <button key={d.key} css={[segBtnStyle, dim === d.key && segActiveStyle]} onClick={() => setDim(d.key)}>
              {d.label}
            </button>
          ))}
        </div>
      </div>

      {error && <div css={errorStyle}>{error}</div>}

      {view && bursts.length > 0 && (
        <div css={burstBannerStyle} role="alert">
          <strong>
            {bursts.length === 1 ? 'One session ran away' : `${bursts.length} sessions ran away`}
          </strong>
          {bursts.slice(0, 3).map(r => (
            <div key={`${r.sessionId}|${r.model}`} css={burstLineStyle}>
              {r.calls.toLocaleString()} calls in {formatSpan(r.end - r.start)}, {formatTokens(r.inputTokens + r.outputTokens)} tokens
              {' · '}{r.kinds || NONE}{' · '}{r.tenants ? tenantNames(r.tenants) : NONE}{' · '}{r.model}{' · '}
              <a css={[monoStyle, sessionLinkStyle]} href={sessionLink(r.sessionId)} title="Open this session">{r.sessionId}</a>
            </div>
          ))}
        </div>
      )}

      {view && (
        <>
          <div css={tilesStyle}>
            <Tile label="Total tokens" value={formatTokens(total)}
              sub={`${formatTokens(view.totals.input)} in · ${formatTokens(view.totals.output)} out`} />
            <Tile label="Estimated cost" value={formatCostUsd(view.totals.cost)}
              sub={view.totals.unpricedTokens > 0 ? `${formatTokens(view.totals.unpricedTokens)} tokens unpriced` : 'all models priced'} />
            <Tile label="LLM calls" value={view.totals.calls.toLocaleString()}
              sub={view.totals.calls ? `${formatTokens(Math.round(total / view.totals.calls))} tokens per call` : '–'} />
            <Tile label="Avg latency" value={view.totals.calls ? formatMs(view.totals.durationMs / view.totals.calls) : '–'}
              sub="per LLM call" />
            <Tile label="Failed calls" value={view.totals.errors.toLocaleString()}
              sub={view.totals.calls ? `${((view.totals.errors / view.totals.calls) * 100).toFixed(1)}% of calls` : '–'} />
          </div>

          <section css={cardStyle}>
            <h2 css={cardTitleStyle}>Tokens per {series!.bucketMs >= 24 * 3600 * 1000 ? 'day' : 'hour'}, by {DIMENSIONS.find(d => d.key === dim)!.label.toLowerCase()}</h2>
            {view.totals.calls === 0 ? (
              <p css={emptyStyle}>No LLM calls with token counts in this window.</p>
            ) : (
              <div css={chartBoxStyle}>
                <Bar
                  data={{ labels: view.labels, datasets: view.datasets }}
                  options={{
                    responsive: true,
                    maintainAspectRatio: false,
                    animation: false,
                    interaction: { mode: 'index', intersect: false },
                    plugins: {
                      legend: {
                        position: 'top',
                        align: 'start',
                        labels: { color: '#a8b2c1', boxWidth: 12, boxHeight: 12, useBorderRadius: true, borderRadius: 2 },
                      },
                      tooltip: {
                        filter: item => (item.raw as number) > 0,
                        itemSort: (a, b) => (b.raw as number) - (a.raw as number),
                        callbacks: {
                          label: item => {
                            if (item.datasetIndex !== view.otherIndex) {
                              return ` ${item.dataset.label}: ${formatTokens(item.raw as number)}`;
                            }
                            const parts = [...view.otherParts[item.dataIndex].entries()].sort((a, b) => b[1] - a[1]);
                            const named = memberList(parts.map(([g, n]) => `${g} ${formatTokens(n)}`));
                            return ` ${OTHER}: ${formatTokens(item.raw as number)} — ${named}`;
                          },
                          footer: items => `Total: ${formatTokens(items.reduce((s, i) => s + (i.raw as number), 0))}`,
                        },
                      },
                    },
                    scales: {
                      x: {
                        stacked: true,
                        grid: { display: false },
                        ticks: { color: '#6b7586', maxRotation: 0, autoSkipPadding: 16 },
                        border: { color: '#3a4052' },
                      },
                      y: {
                        stacked: true,
                        beginAtZero: true,
                        grid: { color: 'rgba(58, 64, 82, 0.5)' },
                        border: { display: false },
                        ticks: { color: '#6b7586', callback: v => formatTokens(Number(v)) },
                      },
                    },
                  }}
                />
              </div>
            )}
          </section>

          <section css={cardStyle}>
            <h2 css={cardTitleStyle}>Breakdown by {DIMENSIONS.find(d => d.key === dim)!.label.toLowerCase()}</h2>
            <div css={tableWrapStyle}>
              <table css={tableStyle}>
                <thead>
                  <tr>
                    <th>{DIMENSIONS.find(d => d.key === dim)!.label}</th>
                    <th css={numStyle}>Calls</th>
                    <th css={numStyle}>Input</th>
                    <th css={numStyle}>Output</th>
                    <th css={numStyle}>Total</th>
                    <th>Share</th>
                    <th css={numStyle}>Est. cost</th>
                    <th css={numStyle}>Tokens / call</th>
                    <th css={numStyle}>Avg latency</th>
                    <th css={numStyle}>Failed</th>
                  </tr>
                </thead>
                <tbody>
                  {view.rows.map(r => {
                    const t = r.input + r.output;
                    const share = total ? t / total : 0;
                    return (
                      <tr key={r.group}>
                        <td><span css={swatchStyle} style={{ background: r.colour }} />{r.group}</td>
                        <td css={numStyle}>{r.calls.toLocaleString()}</td>
                        <td css={numStyle}>{formatTokens(r.input)}</td>
                        <td css={numStyle}>{formatTokens(r.output)}</td>
                        <td css={numStyle}>{formatTokens(t)}</td>
                        <td>
                          <div css={shareStyle}>
                            <div css={shareTrackStyle}><div css={shareFillStyle} style={{ width: `${share * 100}%`, background: r.colour }} /></div>
                            <span>{(share * 100).toFixed(1)}%</span>
                          </div>
                        </td>
                        <td css={numStyle}>{r.unpricedTokens === t ? '–' : formatCostUsd(r.cost)}</td>
                        <td css={numStyle}>{r.calls ? formatTokens(Math.round(t / r.calls)) : '–'}</td>
                        <td css={numStyle}>{r.calls ? formatMs(r.durationMs / r.calls) : '–'}</td>
                        <td css={numStyle}>{r.errors || '–'}</td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          </section>

          <section css={cardStyle}>
            <h2 css={cardTitleStyle}>Largest sessions by tokens</h2>
            {sessions.length === 0 ? (
              <p css={emptyStyle}>No calls in this window.</p>
            ) : (
              <div css={tableWrapStyle}>
                <table css={tableStyle}>
                  <thead>
                    <tr>
                      <th>Last call</th>
                      <th>Kind</th>
                      <th>Tenant</th>
                      <th>Model</th>
                      <th css={numStyle}>Calls</th>
                      <th css={numStyle}>Input</th>
                      <th css={numStyle}>Output</th>
                      <th css={numStyle}>Largest call</th>
                      <th css={numStyle}>Est. cost</th>
                      <th>Session</th>
                    </tr>
                  </thead>
                  <tbody>
                    {sessions.map(r => {
                      const cost = estimateCostUsd(r.model, r.inputTokens, r.outputTokens);
                      return (
                        <tr key={`${r.sessionId}|${r.model}`}>
                          <td css={monoStyle}>{new Date(r.end).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false })}</td>
                          <td>{r.kinds || NONE}{r.errors > 0 && <span css={failTagStyle}>{r.errors} failed</span>}</td>
                          <td title={r.tenants}>{r.tenants ? tenantNames(r.tenants) : NONE}</td>
                          <td css={monoStyle}>{r.model}</td>
                          <td css={numStyle}>{isBurst(r) && <span css={failTagStyle} title={`${r.calls} calls in ${formatSpan(r.end - r.start)}`}>burst</span>}{r.calls.toLocaleString()}</td>
                          <td css={numStyle}>{formatTokens(r.inputTokens)}</td>
                          <td css={numStyle}>{formatTokens(r.outputTokens)}</td>
                          <td css={numStyle}>{formatTokens(r.peakTokens)}</td>
                          <td css={numStyle}>{cost == null ? '–' : formatCostUsd(cost)}</td>
                          <td css={monoStyle}>
                            <a css={sessionLinkStyle} href={sessionLink(r.sessionId)} title="Open this session">{r.sessionId}</a>
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            )}
          </section>
        </>
      )}
    </div>
  );
};

const Tile: React.FC<{ label: string; value: string; sub: string }> = ({ label, value, sub }) => (
  <div css={tileStyle}>
    <div css={tileLabelStyle}>{label}</div>
    <div css={tileValueStyle}>{value}</div>
    <div css={tileSubStyle}>{sub}</div>
  </div>
);

const burstBannerStyle = css`
  padding: 12px 16px;
  border: 1px solid var(--status-failure);
  border-radius: var(--radius);
  color: var(--text-primary);
  margin-bottom: 20px;
`;

const burstLineStyle = css`
  margin-top: 4px;
  font-size: 0.85rem;
  color: var(--text-secondary);
`;

const chartBoxStyle = css`
  height: 240px;
  position: relative;
`;

const emptyStyle = css`
  color: var(--text-tertiary);
  font-size: 0.875rem;
  margin: 8px 0;
`;

const tableWrapStyle = css`
  overflow-x: auto;
`;

const tableStyle = css`
  width: 100%;
  border-collapse: collapse;
  font-size: 0.82rem;
  th {
    text-align: left;
    font-weight: 600;
    font-size: 0.72rem;
    color: var(--text-tertiary);
    text-transform: uppercase;
    letter-spacing: 0.03em;
    padding: 8px 10px;
    border-bottom: 1px solid var(--border-default);
    white-space: nowrap;
  }
  td {
    padding: 8px 10px;
    border-bottom: 1px solid rgba(58, 64, 82, 0.5);
    white-space: nowrap;
  }
  tbody tr:hover { background: var(--bg-elevated); }
  tbody tr:last-child td { border-bottom: none; }
`;

const numStyle = css`
  text-align: right !important;
  font-family: var(--font-mono);
  font-variant-numeric: tabular-nums;
`;

const monoStyle = css`
  font-family: var(--font-mono);
  font-size: 0.78rem;
  color: var(--text-secondary);
  max-width: 260px;
  overflow: hidden;
  text-overflow: ellipsis;
`;

const swatchStyle = css`
  display: inline-block;
  width: 10px;
  height: 10px;
  border-radius: var(--radius);
  margin-right: 8px;
  vertical-align: baseline;
`;

const shareStyle = css`
  display: flex;
  align-items: center;
  gap: 8px;
  font-family: var(--font-mono);
  font-size: 0.75rem;
  color: var(--text-secondary);
`;

const shareTrackStyle = css`
  width: 120px;
  height: 6px;
  border-radius: var(--radius);
  background: var(--bg-elevated);
  overflow: hidden;
`;

const shareFillStyle = css`
  height: 100%;
  border-radius: var(--radius);
`;

const failTagStyle = css`
  margin-left: 6px;
  font-size: 0.7rem;
  padding: 1px 6px;
  border-radius: var(--radius);
  border: 1px solid var(--status-failure);
  color: var(--text-primary);
`;

const sessionLinkStyle = css`
  color: var(--accent-primary);
  text-decoration: none;

  &:hover {
    text-decoration: underline;
  }
`;
