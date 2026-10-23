import { useCallback, useEffect, useMemo, useState } from 'react';
import { css } from '@emotion/react';
import { getThreads, getThreadTurns } from '../../api/client';
import { useLiveData } from '../../lib/connection';
import { stored, store } from '../../lib/stored';
import { useSelectedTenant } from '../../lib/tenant';
import { buildGraph, pathOf, type Graph } from '../../lib/agentGraph';
import { fmtDuration, shortCount, turnStats } from '../../lib/turns';
import { useTurnDetail } from '../../lib/useTurnDetail';
import type { ThreadRow, TurnRow, TurnsResult } from '../../lib/types';
import { sessionLink } from '../shell/routes';
import { errorStyle } from '../ui/page';
import { AgentGraph } from './AgentGraph';
import { TurnTimeline } from './TurnTimeline';

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
  const [nodeId, setNodeId] = useState<string | null>(null);

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


  const stats = useMemo(() => (data ? turnStats(data.turns, data.fromMs, data.toMs) : null), [data]);
  const picked = selected && data?.turns.find((t) => t.session === selected.session && t.rootSpanId === selected.rootSpanId) ? selected : null;
  // Until one is picked, the newest turn is the one shown.
  const shown = picked ?? (data && data.turns.length > 0 ? data.turns[data.turns.length - 1] : null);

  // New turns arrive all the time; the graph only starts its layout again when what it draws has changed.
  const built = useMemo(() => (data ? buildGraph(data.turns, thread) : null), [data, thread]);
  const [graph, setGraph] = useState<Graph | null>(null);
  if (built && (!graph || JSON.stringify(graph) !== JSON.stringify(built))) setGraph(built);

  const node = graph?.nodes.find((n) => n.id === nodeId) ?? null;
  const { detail, error: detailError } = useTurnDetail(shown);
  const path = useMemo(() => (detail && graph ? pathOf(detail, graph) : null), [detail, graph]);

  if (threads && threads.length === 0) return <p css={noteStyle}>no threads yet: an agent shows here once it has sent spans</p>;

  return (
    <div>
      <div css={barStyle}>
        <label css={fieldStyle}>
          thread
          <select value={thread} onChange={(e) => { setThread(e.target.value); setSelected(null); }} css={selectStyle}>
            {threads === null && <option>loading…</option>}
            {threads?.map((t) => (
              <option key={t.thread} value={t.thread}>{t.thread}{t.open ? ' · open' : ''}</option>
            ))}
          </select>
        </label>
        <span css={segStyle}>
          {WINDOWS.map((w) => (
            <button key={w.hours} onClick={() => { setHours(w.hours); setSelected(null); }} aria-pressed={hours === w.hours} css={[segBtnStyle, hours === w.hours && segOnStyle]}>{w.label}</button>
          ))}
        </span>
        {stats && (
          <span css={noteInlineStyle}>
            {stats.turns.toLocaleString()} turn{stats.turns === 1 ? '' : 's'} · awake {pct(stats.awake)}
            {stats.failedTurns > 0 && <span css={failedStyle}> · {stats.failedTurns} with a failure</span>}
            {stats.tokens > 0 && <> · {shortCount(stats.tokens)} tokens, {pct(stats.cachedShare)} from cache</>}
          </span>
        )}
        {data?.truncated && <span css={noteInlineStyle}>newest turns only</span>}
      </div>

      {error && <div css={errorStyle} role="alert">{error}</div>}
      {!error && !data && thread && <p css={noteStyle}>loading turns…</p>}

      {data && graph && (
        data.turns.length === 0 ? (
          <p css={noteStyle}>no turns in this window</p>
        ) : (
          <div css={layoutStyle}>
            <div css={mainStyle}>
              <div css={graphBoxStyle}>
                <AgentGraph graph={graph} path={path} selectedId={nodeId} onSelectNode={setNodeId} />
              </div>
              <div css={legendStyle}>
                <span><i style={{ background: '#a79ff0' }} />agent</span>
                <span><i style={{ background: '#10b981' }} />model</span>
                <span><i style={{ background: '#5eead4' }} />MCP server</span>
                <span><i style={{ background: '#b5abfc' }} />tool</span>
                <span>size and link width: calls · red: failures · drag, scroll to zoom, double-click to fit</span>
              </div>
              <div css={stripStyle}>
                <TurnTimeline compact turns={data.turns} segments={data.segments} fromMs={data.fromMs} toMs={data.toMs} selected={shown} onSelect={setSelected} />
              </div>
            </div>

            <aside css={sideStyle}>
              {node && (
                <section css={sectionStyle}>
                  <div css={headStyle}>
                    <span css={sideTitleStyle}>{node.label}<span css={stepDimStyle}> · {node.kind === 'tool' && node.server ? `tool of ${node.server}` : node.kind}</span></span>
                    <button css={closeStyle} onClick={() => setNodeId(null)} aria-label="Clear the picked node">×</button>
                  </div>
                  {node.kind === 'agent' ? (
                    <p css={factStyle}>{node.calls.toLocaleString()} turns in the window{node.failed > 0 && <span css={failedStyle}>, {node.failed} with a failure</span>}</p>
                  ) : (
                    <>
                      <p css={factStyle}>{node.calls.toLocaleString()} call{node.calls === 1 ? '' : 's'}{node.failed > 0 && <span css={failedStyle}>, {node.failed} failed ({pct(node.failed / node.calls)})</span>}</p>
                      {node.ms > 0 && <p css={factStyle}>{fmtDuration(node.ms / Math.max(1, node.calls))} on average, {fmtDuration(node.ms)} in all</p>}
                    </>
                  )}
                </section>
              )}

              {shown && (
                <section css={sectionStyle}>
                  <div css={headStyle}>
                    <span css={sideTitleStyle}>
                      {picked ? 'turn' : 'latest turn'}
                      <span css={stepDimStyle}> · {new Date(shown.startMs).toLocaleString(undefined, { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false })} · {fmtDuration(shown.durationMs)}{(shown.part ?? 1) > 1 ? ` · part ${shown.part}` : ''}</span>
                    </span>
                    <a href={sessionLink(shown.session)} css={linkStyle}>open ›</a>
                  </div>
                  {detailError && <div css={errorStyle}>{detailError}</div>}
                  {!detailError && detail === undefined && <span css={noteInlineStyle}>loading the turn…</span>}
                  {detail === null && <span css={noteInlineStyle}>the spans of this turn are no longer held</span>}
                  {detail && (
                    <ol css={stepsStyle}>
                      {detail.steps.map((st, i) => (
                        <li key={i} css={st.failed ? stepFailedStyle : undefined}>
                          <b>{i + 1}</b>
                          <span css={stepNameStyle}>
                            {st.kind === 'model' ? '◆' : '→'} {st.label}
                            {st.server && <span css={stepDimStyle}> {st.server}</span>}
                            {st.detail && <span css={stepDimStyle}> {st.detail}</span>}
                          </span>
                          <span css={stepTimeStyle}>{fmtDuration(st.durationUs / 1000)}</span>
                          {st.approvalUs ? <span css={stepNoteStyle}>{fmtDuration(st.approvalUs / 1000)} waiting to be approved</span> : null}
                          {st.kind === 'model' && (st.tokensIn || st.tokensOut) ? <span css={stepNoteStyle}>↓{shortCount(st.tokensIn ?? 0)} ↑{shortCount(st.tokensOut ?? 0)}{st.cached ? `, ${shortCount(st.cached)} from cache` : ''}</span> : null}
                          {st.failed && st.message && <span css={stepFailNoteStyle}>✗ {st.message}</span>}
                        </li>
                      ))}
                    </ol>
                  )}
                </section>
              )}
            </aside>
          </div>
        )
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
  margin: 8px 0 10px;
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

const linkStyle = css`
  color: var(--accent-primary);
  font-size: 12px;
  text-decoration: none;
  &:hover { text-decoration: underline; }
`;

const layoutStyle = css`
  display: grid;
  grid-template-columns: minmax(0, 1fr) 360px;
  gap: 12px;
  align-items: start;

  @media (max-width: 1100px) {
    grid-template-columns: minmax(0, 1fr);
  }
`;

const mainStyle = css`
  min-width: 0;
`;

const graphBoxStyle = css`
  height: calc(100vh / 1.15 - 250px);
  min-height: 420px;
  overflow: hidden;
  border: 1px solid var(--border-default);
  border-radius: var(--radius);
  background: var(--card-surface);
`;

const stripStyle = css`
  border: 1px solid var(--border-default);
  border-radius: var(--radius);
  background: var(--card-surface);
  padding: 4px 10px 0;
`;

const sideStyle = css`
  position: sticky;
  top: 8px;
  display: flex;
  flex-direction: column;
  gap: 10px;
  max-height: calc(100vh / 1.15 - 70px);
  overflow-y: auto;
`;

const sectionStyle = css`
  padding: 10px 12px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius);
  background: var(--card-surface);
`;

const sideTitleStyle = css`
  color: var(--text-primary);
  font-size: 13px;
  font-weight: 500;
  overflow-wrap: anywhere;
`;

const factStyle = css`
  margin: 4px 0 0;
  color: var(--text-secondary);
  font-size: 12.5px;
`;

const closeStyle = css`
  padding: 0 4px;
  border: none;
  background: none;
  color: var(--text-tertiary);
  font-size: 16px;
  line-height: 1;
  cursor: pointer;
  &:hover { color: var(--text-primary); }
`;

const stepsStyle = css`
  margin: 10px 0 0;
  padding: 0;
  list-style: none;
  display: flex;
  flex-direction: column;
  gap: 2px;
  font-size: 12px;

  li {
    display: grid;
    grid-template-columns: 22px minmax(0, 1fr) auto;
    column-gap: 6px;
    padding: 4px 6px;
    border-radius: var(--radius);
  }
  li:hover { background: var(--bg-elevated); }
  b {
    color: var(--text-muted);
    font-weight: 600;
    font-variant-numeric: tabular-nums;
  }
`;

const stepNameStyle = css`
  min-width: 0;
  overflow-wrap: anywhere;
  font-family: var(--font-mono);
  color: var(--text-primary);
`;

const stepTimeStyle = css`
  color: var(--text-tertiary);
  font-family: var(--font-mono);
  font-variant-numeric: tabular-nums;
`;

const stepNoteStyle = css`
  grid-column: 2 / -1;
  color: var(--text-muted);
  font-size: 11px;
`;

const stepFailNoteStyle = css`
  grid-column: 2 / -1;
  color: var(--status-failure);
  font-family: var(--font-mono);
  font-size: 11px;
  overflow-wrap: anywhere;
`;

const stepFailedStyle = css`
  box-shadow: inset 0 0 0 1px var(--status-failure);
`;

const stepDimStyle = css`
  color: var(--text-tertiary);
`;
