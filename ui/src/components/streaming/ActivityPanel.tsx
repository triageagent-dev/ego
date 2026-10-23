import { useCallback, useEffect, useMemo, useState } from 'react';
import { css } from '@emotion/react';
import { getActivity, getActivityTurns } from '../../api/client';
import { useLiveData } from '../../lib/connection';
import { stored, store } from '../../lib/stored';
import { useSelectedTenant } from '../../lib/tenant';
import { buildGraph, pathOf, turnNodeId, type Graph } from '../../lib/agentGraph';
import { fmtDuration, shortCount, turnStats } from '../../lib/turns';
import { useTurnDetail } from '../../lib/useTurnDetail';
import type { ActivitySubject, TurnRow, TurnsResult } from '../../lib/types';
import { errorStyle } from '../ui/page';
import { AgentGraph } from './AgentGraph';
import { TurnTimeline } from './TurnTimeline';

const WINDOWS = [
  { label: '5m', minutes: 5 },
  { label: '15m', minutes: 15 },
  { label: '1h', minutes: 60 },
  { label: '6h', minutes: 360 },
  { label: '24h', minutes: 1440 },
  { label: '7d', minutes: 10080 },
  { label: '30d', minutes: 43200 },
];

const keyOf = (s: ActivitySubject) => `${s.agent}\n${s.tenant ?? ''}`;
const pct = (v: number) => `${Math.round(v * 100)}%`;

// How an always-on agent works, turn by turn: how often it wakes and how long it stays up, what it costs, what fails,
// and, for any one turn, where the time went. An agent is shown across all its sessions, however many weeks it has run.
export function ActivityPanel({ onOpenSession }: { onOpenSession: (session: string) => void }) {
  const tenant = useSelectedTenant();
  const [subjects, setSubjects] = useState<ActivitySubject[] | null>(null);
  const [subjectKey, setSubjectKey] = useState(() => stored('ego.activity.agent') || '');
  const [minutes, setMinutes] = useState(() => Number(stored('ego.activity.minutes')) || 1440);
  const [data, setData] = useState<TurnsResult | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<TurnRow | null>(null);
  const [nodeId, setNodeId] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    getActivity()
      .then((rows) => {
        if (!live) return;
        setSubjects(rows);
        // The remembered agent may be gone, or never was (first visit): fall back to the one seen last.
        setSubjectKey((cur) => (cur && rows.some((r) => keyOf(r) === cur) ? cur : rows[0] ? keyOf(rows[0]) : ''));
      })
      .catch((e: Error) => live && setError(e.message));
    return () => { live = false; };
    // The picked tenant narrows the agents: load again when it changes.
  }, [tenant]);

  useEffect(() => store('ego.activity.agent', subjectKey), [subjectKey]);
  const subject = subjects?.find((r) => keyOf(r) === subjectKey) ?? null;
  useEffect(() => store('ego.activity.minutes', String(minutes)), [minutes]);

  const load = useCallback(async (current: () => boolean) => {
    if (!subject) return;
    try {
      const res = await getActivityTurns(subject.agent, subject.tenant, minutes);
      if (!current()) return;
      setData(res);
      setError(null);
    } catch (e) {
      if (current()) setError(e instanceof Error ? e.message : String(e));
    }
  }, [subject, minutes]);
  // New turns arrive with the usage of their model requests.
  useLiveData('usage', load);


  const stats = useMemo(() => (data ? turnStats(data.turns, data.fromMs, data.toMs) : null), [data]);
  const picked = selected && data?.turns.find((t) => t.session === selected.session && t.rootSpanId === selected.rootSpanId) ? selected : null;
  // Until one is picked, the newest turn is the one shown.
  const shown = picked ?? (data && data.turns.length > 0 ? data.turns[data.turns.length - 1] : null);

  // New turns arrive all the time; the graph only starts its layout again when what it draws has changed.
  const built = useMemo(() => (data ? buildGraph(data.turns, data.agent) : null), [data]);
  const [graph, setGraph] = useState<Graph | null>(null);
  if (built && (!graph || JSON.stringify(graph) !== JSON.stringify(built))) setGraph(built);

  const node = graph?.nodes.find((n) => n.id === nodeId) ?? null;
  const { detail, error: detailError } = useTurnDetail(shown);
  const shownNode = shown ? turnNodeId(shown) : null;
  const path = useMemo(() => (detail && graph ? pathOf(detail, graph, shownNode) : null), [detail, graph, shownNode]);

  if (subjects && subjects.length === 0) return <p css={noteStyle}>no agent with turns yet: an agent shows here once it has sent spans of its prompts</p>;

  return (
    <div>
      <div css={barStyle}>
        <label css={fieldStyle}>
          agent
          <select value={subjectKey} onChange={(e) => { setSubjectKey(e.target.value); setSelected(null); }} css={selectStyle}>
            {subjects === null && <option>loading…</option>}
            {subjects?.map((t) => (
              <option key={keyOf(t)} value={keyOf(t)}>{t.agent}{t.tenant ? ` · ${t.tenant}` : ''}</option>
            ))}
          </select>
        </label>
        <span css={segStyle}>
          {WINDOWS.map((w) => (
            <button key={w.minutes} onClick={() => { setMinutes(w.minutes); setSelected(null); }} aria-pressed={minutes === w.minutes} css={[segBtnStyle, minutes === w.minutes && segOnStyle]}>{w.label}</button>
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
      {!error && !data && subject && <p css={noteStyle}>loading turns…</p>}

      {data && graph && (
        data.turns.length === 0 ? (
          <p css={noteStyle}>no turns in this window</p>
        ) : (
          <div css={layoutStyle}>
            <div css={mainStyle}>
              <div css={graphBoxStyle}>
                <AgentGraph graph={graph} path={path} turnNode={shownNode} selectedId={nodeId} onSelectNode={setNodeId} onPickTurn={setSelected} />
              </div>
              <div css={legendStyle}>
                <span><i style={{ background: '#a79ff0' }} />agent</span>
                <span><i style={{ background: '#6b7280' }} />model, MCP server, tool and, small, a turn</span>
                <span><i style={{ background: '#f59e0b' }} />some failures</span>
                <span><i style={{ background: '#ef4444' }} />many</span>
                <span><i style={{ background: 'transparent', boxShadow: '0 0 0 2px #f97316' }} />the turn on show</span>
                <span>click a turn to see its path · drag, scroll to zoom, double-click to fit</span>
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
                    <button css={linkStyle} onClick={() => onOpenSession(shown.session)} title="Open the session this turn is part of: its conversation, scores and spans">session ›</button>
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
  padding: 0;
  border: none;
  background: none;
  color: var(--accent-primary);
  font: inherit;
  font-size: 12px;
  cursor: pointer;
  white-space: nowrap;
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
  background: var(--bg-primary);
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
