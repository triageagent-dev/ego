import { useCallback, useEffect, useMemo, useState } from 'react';
import { css } from '@emotion/react';
import { Bar } from 'react-chartjs-2';
import { getActivity, getActivityTurns } from '../../api/client';
import { useLiveData } from '../../lib/connection';
import { stored, store } from '../../lib/stored';
import { tenantName, useSelectedTenant } from '../../lib/tenant';
import { fmtDuration, shortCount, turnStats, type TurnStats } from '../../lib/turns';
import type { ActivitySubject, TurnsResult } from '../../lib/types';
import { ChartBox } from '../ui/ChartBox';
import { cardStyle, cardTitleStyle, tileLabelStyle, tileStyle, tileSubStyle, tileValueStyle, tilesStyle } from '../ui/page';

// How an agent that works in turns (an always-on agent, such as a spot) does, from what its turns say and without a
// judge: how many turns ended without a failure, which tool calls failed, the calls, the tokens and each tool's record.
// The Activity view reads the same turns; the agent picked there is the one shown here.

const GOOD = '#4ade80';
const BAD = '#f87171';

export interface TurnQuality {
  subjects: ActivitySubject[];
  subject: ActivitySubject | null;
  pick: (s: ActivitySubject) => void;
  data: TurnsResult | null;
  stats: TurnStats | null;
  // Turns that ended without a failure, and all turns, in the window.
  ok: number;
  turns: number;
}

const keyOf = (s: ActivitySubject) => `${s.agent}\n${s.tenant ?? ''}`;

export function useTurnQuality(hours: number): TurnQuality {
  const tenant = useSelectedTenant();
  const [subjects, setSubjects] = useState<ActivitySubject[]>([]);
  const [key, setKey] = useState(() => stored('ego.activity.agent') || '');
  const [data, setData] = useState<TurnsResult | null>(null);

  useEffect(() => {
    let live = true;
    // Only the agents that worked in the window are offered: the rest have nothing to show.
    getActivity(`${hours}h`)
      .then((rows) => live && setSubjects(rows))
      .catch(() => live && setSubjects([]));
    return () => { live = false; };
    // The picked tenant narrows the agents.
  }, [tenant, hours]);

  const subject = subjects.find((s) => keyOf(s) === key) ?? subjects[0] ?? null;
  const load = useCallback(async (current: () => boolean) => {
    if (!subject) { setData(null); return; }
    try {
      const res = await getActivityTurns(subject.agent, subject.tenant, hours * 60);
      if (current()) setData(res);
    } catch {
      if (current()) setData(null);
    }
  }, [subject, hours]);
  // New turns arrive with the usage of their model requests.
  useLiveData('usage', load);

  const stats = useMemo(() => (data ? turnStats(data.turns, data.fromMs, data.toMs) : null), [data]);
  const turns = stats?.turns ?? 0;
  return {
    subjects,
    subject,
    pick: (s) => { setKey(keyOf(s)); store('ego.activity.agent', keyOf(s)); },
    data,
    stats,
    ok: stats ? stats.turns - stats.failedTurns : 0,
    turns,
  };
}

const pct = (x: number) => `${Math.round(x * 100)}%`;

function bucketLabel(ms: number, day: boolean): string {
  const d = new Date(ms);
  return day ? `${d.getMonth() + 1}/${d.getDate()}` : `${d.getMonth() + 1}/${d.getDate()} ${String(d.getHours()).padStart(2, '0')}:00`;
}

export function TurnQualityView({ q, hours }: { q: TurnQuality; hours: number }) {
  const { data, stats, subject } = q;

  // Turns per hour (per day over a week), those that ended without a failure and those that did not.
  const perBucket = useMemo(() => {
    if (!data || data.turns.length === 0) return null;
    const day = hours > 24 * 7;
    const size = day ? 86_400_000 : 3_600_000;
    const counts = new Map<number, { ok: number; failed: number }>();
    for (const r of data.turns) {
      const b = Math.floor(r.startMs / size) * size;
      const c = counts.get(b) ?? { ok: 0, failed: 0 };
      if (r.failed || r.failedTools) c.failed++;
      else c.ok++;
      counts.set(b, c);
    }
    const buckets = [...counts.keys()].sort((a, b) => a - b);
    return {
      labels: buckets.map((b) => bucketLabel(b, day)),
      datasets: [
        { label: 'without a failure', data: buckets.map((b) => counts.get(b)!.ok), backgroundColor: GOOD, borderRadius: 2 },
        { label: 'with a failure', data: buckets.map((b) => counts.get(b)!.failed), backgroundColor: BAD, borderRadius: 2 },
      ],
    };
  }, [data, hours]);

  // The record of each tool: the worst first.
  const tools = useMemo(() => {
    const m = new Map<string, { server: string; calls: number; failed: number; ms: number }>();
    for (const r of data?.turns ?? []) {
      for (const [name, c] of Object.entries(r.toolCalls ?? {})) {
        const t = m.get(name) ?? { server: c.server ?? '', calls: 0, failed: 0, ms: 0 };
        t.calls += c.calls;
        t.failed += c.failed ?? 0;
        t.ms += c.ms ?? 0;
        m.set(name, t);
      }
    }
    return [...m.entries()].map(([name, t]) => ({ name, ...t })).sort((a, b) => b.failed - a.failed || b.calls - a.calls);
  }, [data]);

  const requests = useMemo(() => (data?.turns ?? []).reduce((n, r) => n + r.requests, 0), [data]);

  if (q.subjects.length === 0 || !subject) return null;
  const windowLabel = hours === 24 ? '24h' : hours === 24 * 7 ? '7d' : '30d';
  // An agent is named alone. Where two tenants run an agent of the same name, the tenant's name is added unless it
  // only repeats the agent's; if the names still collide, the tenant's key tells them apart.
  const names = new Map<string, string>();
  for (const x of q.subjects) {
    const shared = q.subjects.some((o) => o !== x && o.agent === x.agent);
    const t = x.tenant ? tenantName(x.tenant) : '';
    names.set(keyOf(x), shared && t && t !== x.agent ? `${x.agent} · ${t}` : x.agent);
  }
  const seen = new Map<string, number>();
  for (const n of names.values()) seen.set(n, (seen.get(n) ?? 0) + 1);
  const label = (x: ActivitySubject) => {
    const n = names.get(keyOf(x)) ?? x.agent;
    return (seen.get(n) ?? 0) > 1 ? `${x.agent} · ${x.tenant || 'no tenant'}` : n;
  };
  // The picker stays when the agent picked has no turns, so another can be chosen.
  const head = (
    <div css={headStyle}>
      <h2 css={cardTitleStyle}>Turns, {label(subject)}</h2>
      {q.subjects.length > 1 && (
        <select value={keyOf(subject)} onChange={(e) => { const x = q.subjects.find((o) => keyOf(o) === e.target.value); if (x) q.pick(x); }} css={selectStyle} aria-label="Agent">
          {q.subjects.map((x) => (
            <option key={keyOf(x)} value={keyOf(x)}>{label(x)}</option>
          ))}
        </select>
      )}
      {data?.truncated && <span css={noteStyle}>newest turns only</span>}
    </div>
  );
  if (!stats || stats.turns === 0) {
    return (
      <section css={cardStyle}>
        {head}
        <p css={emptyStyle}>No turns in the last {windowLabel}.</p>
      </section>
    );
  }
  const failedShare = stats.toolCalls > 0 ? stats.failedToolCalls / stats.toolCalls : 0;
  return (
    <>
      <section css={cardStyle}>
        {head}
        <div css={tilesStyle}>
          <Tile label="Turn success" value={pct(q.ok / stats.turns)} sub={`${q.ok.toLocaleString()} of ${stats.turns.toLocaleString()} turns without a failure`} />
          <Tile label="Failed tool calls" value={stats.failedToolCalls.toLocaleString()} sub={`of ${stats.toolCalls.toLocaleString()} tool calls (${pct(failedShare)})`} />
          <Tile label="Model requests" value={requests.toLocaleString()} sub={`${(requests / stats.turns).toFixed(1)} a turn · median turn ${fmtDuration(stats.medianMs)}`} />
          <Tile label="Tokens" value={shortCount(stats.tokens)} sub={`${pct(stats.cachedShare)} of the input from cache`} />
          <Tile label="Awake" value={pct(stats.awake)} sub={`${stats.perHour.toFixed(1)} turns an hour`} />
        </div>
      </section>

      <div css={gridStyle}>
        <section css={cardStyle}>
          <h2 css={cardTitleStyle}>Turns per {hours > 24 * 7 ? 'day' : 'hour'}</h2>
          {perBucket && (
            <ChartBox css={chartBoxStyle}>
              <Bar
                data={perBucket}
                options={{
                  responsive: true,
                  maintainAspectRatio: false,
                  animation: false,
                  interaction: { mode: 'index', intersect: false },
                  plugins: { legend: { position: 'top', align: 'start', labels: { color: '#a8b2c1', boxWidth: 12, boxHeight: 12, useBorderRadius: true, borderRadius: 2 } } },
                  scales: {
                    x: { stacked: true, grid: { display: false }, ticks: { color: '#6b7586', maxRotation: 0, autoSkipPadding: 16 }, border: { color: '#3a4052' } },
                    y: { stacked: true, beginAtZero: true, grid: { color: 'rgba(58, 64, 82, 0.5)' }, border: { display: false }, ticks: { color: '#6b7586', precision: 0 } },
                  },
                }}
              />
            </ChartBox>
          )}
        </section>

        <section css={cardStyle}>
          <h2 css={cardTitleStyle}>Tools</h2>
          {tools.length === 0 ? (
            <p css={emptyStyle}>No tool calls in this window.</p>
          ) : (
            <div css={tableWrapStyle}>
              <table css={tableStyle}>
                <thead>
                  <tr><th>tool</th><th>calls</th><th>failed</th><th>average</th></tr>
                </thead>
                <tbody>
                  {tools.slice(0, 40).map((t) => (
                    <tr key={`${t.server}/${t.name}`} title={t.server ? `tool of ${t.server}` : 'built-in tool'}>
                      <td>{t.name}</td>
                      <td>{t.calls.toLocaleString()}</td>
                      <td css={t.failed > 0 ? failStyle : undefined}>{t.failed > 0 ? `${t.failed} (${pct(t.failed / t.calls)})` : ''}</td>
                      <td>{t.ms > 0 ? fmtDuration(t.ms / t.calls) : ''}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </section>
      </div>
    </>
  );
}

const Tile = ({ label, value, sub }: { label: string; value: string; sub: string }) => (
  <div css={tileStyle}>
    <div css={tileLabelStyle}>{label}</div>
    <div css={tileValueStyle}>{value}</div>
    <div css={tileSubStyle}>{sub}</div>
  </div>
);

const headStyle = css`
  display: flex;
  align-items: baseline;
  gap: 14px;
  flex-wrap: wrap;
  margin-bottom: 8px;

  h2 { margin: 0; }
`;

const selectStyle = css`
  max-width: 420px;
  padding: 3px 8px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius);
  background: var(--bg-primary);
  color: var(--text-primary);
  font-family: var(--font-mono);
  font-size: 12px;
`;

const noteStyle = css`
  color: var(--text-muted);
  font-size: 12px;
`;

const emptyStyle = css`
  color: var(--text-tertiary);
  font-size: 0.875rem;
  margin: 8px 0;
`;

const gridStyle = css`
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 16px;
  margin-bottom: 12px;

  > * {
    min-width: 0;
    margin-bottom: 0;
  }

  @media (max-width: 1100px) {
    grid-template-columns: minmax(0, 1fr);
  }
`;

const chartBoxStyle = css`
  height: 240px;
  position: relative;
`;

const tableWrapStyle = css`
  max-height: 240px;
  overflow: auto;
`;

const tableStyle = css`
  width: 100%;
  border-collapse: collapse;
  font-size: 12px;

  th, td {
    padding: 4px 8px;
    text-align: right;
    white-space: nowrap;
  }
  th:first-of-type, td:first-of-type {
    text-align: left;
    white-space: normal;
    overflow-wrap: anywhere;
  }
  th {
    position: sticky;
    top: 0;
    background: var(--card-surface);
    color: var(--text-muted);
    font-weight: 500;
  }
`;

const failStyle = css`
  color: var(--status-failure);
`;
