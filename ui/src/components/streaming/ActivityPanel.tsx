import { useCallback, useEffect, useMemo, useState } from 'react';
import { css } from '@emotion/react';
import { getThreads, getThreadTurns } from '../../api/client';
import { useLiveData } from '../../lib/connection';
import { stored, store } from '../../lib/stored';
import { useSelectedTenant } from '../../lib/tenant';
import { fmtDuration, shortCount, turnStats } from '../../lib/turns';
import type { ThreadRow, TurnRow, TurnsResult } from '../../lib/types';
import { sessionLink } from '../shell/routes';
import { cardStyle, cardTitleStyle, errorStyle, tileLabelStyle, tilesStyle, tileStyle, tileSubStyle, tileValueStyle } from '../ui/page';
import { TurnTimeline } from './TurnTimeline';
import { TurnWaterfall } from './TurnWaterfall';

const WINDOWS = [
  { label: '6h', hours: 6 },
  { label: '24h', hours: 24 },
  { label: '7d', hours: 168 },
  { label: '30d', hours: 720 },
];

const pct = (v: number) => `${Math.round(v * 100)}%`;

// How an always-on agent works, turn by turn: how often it wakes and how long it stays up, what it costs, what fails,
// and, for any one turn, where the time went. A thread is the agent's own id, however many weeks it has run.
export function ActivityPanel({ initialThread }: { initialThread?: string }) {
  const tenant = useSelectedTenant();
  const [threads, setThreads] = useState<ThreadRow[] | null>(null);
  const [thread, setThread] = useState(() => initialThread || stored('ego.activity.thread') || '');
  const [hours, setHours] = useState(() => Number(stored('ego.activity.hours')) || 24);
  const [data, setData] = useState<TurnsResult | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<TurnRow | null>(null);

  useEffect(() => {
    let live = true;
    getThreads()
      .then((rows) => {
        if (!live) return;
        setThreads(rows);
        // The remembered thread may be gone, or never was (first visit): fall back to the one seen last.
        setThread((cur) => (cur && rows.some((r) => r.thread === cur) ? cur : rows[0]?.thread ?? ''));
      })
      .catch((e: Error) => live && setError(e.message));
    return () => { live = false; };
    // The picked tenant narrows the threads: load again when it changes.
  }, [tenant]);

  useEffect(() => store('ego.activity.thread', thread), [thread]);
  useEffect(() => store('ego.activity.hours', String(hours)), [hours]);

  const load = useCallback(async (current: () => boolean) => {
    if (!thread) return;
    try {
      const res = await getThreadTurns(thread, hours);
      if (!current()) return;
      setData(res);
      setError(null);
    } catch (e) {
      if (current()) setError(e instanceof Error ? e.message : String(e));
    }
  }, [thread, hours]);
  // New turns arrive with the usage of their model requests.
  useLiveData('usage', load);

  // The picked turn belongs to the thread and window it was picked in.
  useEffect(() => setSelected(null), [thread, hours]);

  const stats = useMemo(() => (data ? turnStats(data.turns, data.fromMs, data.toMs) : null), [data]);
  const picked = selected && data?.turns.find((t) => t.session === selected.session && t.rootSpanId === selected.rootSpanId) ? selected : null;

  if (threads && threads.length === 0) return <p css={noteStyle}>no threads yet: an agent shows here once it has sent spans</p>;

  return (
    <div>
      <div css={barStyle}>
        <label css={fieldStyle}>
          thread
          <select value={thread} onChange={(e) => setThread(e.target.value)} css={selectStyle}>
            {threads === null && <option>loading…</option>}
            {threads?.map((t) => (
              <option key={t.thread} value={t.thread}>{t.thread}{t.open ? ' · open' : ''}</option>
            ))}
          </select>
        </label>
        <span css={segStyle}>
          {WINDOWS.map((w) => (
            <button key={w.hours} onClick={() => setHours(w.hours)} aria-pressed={hours === w.hours} css={[segBtnStyle, hours === w.hours && segOnStyle]}>{w.label}</button>
          ))}
        </span>
        {data?.truncated && <span css={noteInlineStyle}>showing the newest turns only</span>}
      </div>

      {error && <div css={errorStyle} role="alert">{error}</div>}
      {!error && !data && thread && <p css={noteStyle}>loading turns…</p>}

      {data && stats && (
        <>
          <div css={tilesStyle}>
            <div css={tileStyle}>
              <div css={tileLabelStyle}>turns</div>
              <div css={tileValueStyle}>{stats.turns.toLocaleString()}</div>
              <div css={tileSubStyle}>{stats.perHour >= 10 ? Math.round(stats.perHour) : stats.perHour.toFixed(1)} an hour</div>
            </div>
            <div css={tileStyle}>
              <div css={tileLabelStyle}>turn time</div>
              <div css={tileValueStyle}>{stats.turns ? fmtDuration(stats.medianMs) : '–'}</div>
              <div css={tileSubStyle}>{stats.turns ? `p95 ${fmtDuration(stats.p95Ms)}` : 'median'}</div>
            </div>
            <div css={tileStyle} title="Share of the window with a turn running">
              <div css={tileLabelStyle}>awake</div>
              <div css={tileValueStyle}>{pct(stats.awake)}</div>
              <div css={tileSubStyle}>asleep the other {pct(1 - stats.awake)}</div>
            </div>
            <div css={tileStyle}>
              <div css={tileLabelStyle}>failed turns</div>
              <div css={[tileValueStyle, stats.failedTurns > 0 && failedStyle]}>{stats.failedTurns}</div>
              <div css={tileSubStyle}>{stats.turns ? `${pct(stats.failedTurns / stats.turns)} of turns` : 'none'}</div>
            </div>
            <div css={tileStyle}>
              <div css={tileLabelStyle}>tool calls failed</div>
              <div css={[tileValueStyle, stats.failedToolCalls > 0 && failedStyle]}>{stats.failedToolCalls}</div>
              <div css={tileSubStyle}>of {stats.toolCalls.toLocaleString()}</div>
            </div>
            <div css={tileStyle} title={`${stats.tokens.toLocaleString()} tokens`}>
              <div css={tileLabelStyle}>tokens</div>
              <div css={tileValueStyle}>{shortCount(stats.tokens)}</div>
              <div css={tileSubStyle}>{pct(stats.cachedShare)} read from cache</div>
            </div>
          </div>

          <div css={cardStyle}>
            <h3 css={cardTitleStyle}>turns over time</h3>
            {data.turns.length === 0 ? (
              <p css={noteStyle}>no turns in this window</p>
            ) : (
              <>
                <TurnTimeline turns={data.turns} segments={data.segments} fromMs={data.fromMs} toMs={data.toMs} selected={picked} onSelect={setSelected} />
                <div css={legendStyle}>
                  <span><i style={{ background: '#10b981' }} />turn, as tall as it was long</span>
                  <span><i style={{ background: '#ef4444' }} />a failure in it</span>
                  <span><i style={{ background: '#a79ff0' }} />tokens, <i style={{ background: 'rgba(167,159,240,0.35)' }} />the part from cache</span>
                  <span>dotted: a new part of the thread</span>
                </div>
              </>
            )}
          </div>

          {picked ? (
            <div css={cardStyle}>
              <div css={headStyle}>
                <h3 css={cardTitleStyle} style={{ margin: 0 }}>
                  turn {new Date(picked.startMs).toLocaleString(undefined, { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false })}
                  <span css={subStyle}> · {fmtDuration(picked.durationMs)}{(picked.part ?? 1) > 1 ? ` · part ${picked.part}` : ''}</span>
                </h3>
                <a href={sessionLink(picked.session)} css={linkStyle}>open session ›</a>
              </div>
              <TurnWaterfall turn={picked} />
            </div>
          ) : (
            data.turns.length > 0 && <p css={noteStyle}>click a turn, or move with the arrow keys, to see where its time went</p>
          )}

          {stats.tools.length > 0 && (
            <div css={cardStyle}>
              <h3 css={cardTitleStyle}>tools</h3>
              <table css={tableStyle}>
                <thead>
                  <tr><th>tool</th><th css={numStyle}>calls</th><th css={numStyle}>failed</th><th css={numStyle}>failure rate</th></tr>
                </thead>
                <tbody>
                  {stats.tools.slice(0, 12).map((t) => (
                    <tr key={t.name}>
                      <td css={monoStyle}>{t.name}</td>
                      <td css={numStyle}>{t.calls.toLocaleString()}</td>
                      <td css={[numStyle, t.failed > 0 && failedStyle]}>{t.failed}</td>
                      <td css={numStyle}>{pct(t.failed / t.calls)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}
    </div>
  );
}

const barStyle = css`
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 14px;
  margin-bottom: 12px;
`;

const fieldStyle = css`
  display: inline-flex;
  align-items: center;
  gap: 8px;
  color: var(--text-tertiary);
  font-size: 12px;
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

const segStyle = css`
  display: inline-flex;
  gap: 2px;
  padding: 1px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius);
`;

const segBtnStyle = css`
  padding: 1px 9px;
  border: none;
  border-radius: var(--radius);
  background: transparent;
  color: var(--text-tertiary);
  font-size: 12px;
  cursor: pointer;
`;

const segOnStyle = css`
  background: var(--bg-selected);
  color: var(--accent-magenta);
`;

const noteStyle = css`
  color: var(--text-tertiary);
  font-size: 0.875rem;
  margin: 8px 0;
`;

const noteInlineStyle = css`
  color: var(--text-muted);
  font-size: 12px;
`;

const failedStyle = css`
  color: var(--status-failure);
`;

const legendStyle = css`
  display: flex;
  flex-wrap: wrap;
  gap: 4px 18px;
  margin-top: 8px;
  color: var(--text-muted);
  font-size: 11.5px;

  i {
    display: inline-block;
    width: 8px;
    height: 8px;
    margin-right: 5px;
    border-radius: 2px;
  }
`;

const headStyle = css`
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 10px;
`;

const subStyle = css`
  color: var(--text-tertiary);
  font-weight: 400;
`;

const linkStyle = css`
  color: var(--accent-primary);
  font-size: 12px;
  text-decoration: none;
  &:hover { text-decoration: underline; }
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
    padding: 6px 10px;
    border-bottom: 1px solid var(--border-default);
  }
  td {
    padding: 6px 10px;
    border-bottom: 1px solid rgba(58, 64, 82, 0.5);
  }
  tbody tr:last-child td { border-bottom: none; }
`;

const numStyle = css`
  text-align: right !important;
  font-family: var(--font-mono);
  font-variant-numeric: tabular-nums;
`;

const monoStyle = css`
  font-family: var(--font-mono);
`;
