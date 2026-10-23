import { useCallback, useState } from 'react';
import { css } from '@emotion/react';
import { getThreads } from '../../api/client';
import { useLiveData } from '../../lib/connection';
import { useSelectedTenant } from '../../lib/tenant';
import { formatStarted } from '../../lib/trace-helpers';
import type { ThreadRow } from '../../lib/types';

// 3,960,702 -> 3.9M, 132,391 -> 132.3k: cut, not rounded, to one decimal.
function shortTokens(n: number): string {
  if (n >= 1e6) return `${(Math.floor(n / 1e5) / 10).toFixed(1)}M`;
  if (n >= 1e3) return `${(Math.floor(n / 100) / 10).toFixed(1)}k`;
  return String(n);
}

/** The threads the server holds, one row each: a thread is the id an agent keeps for as long as it runs, and its
 * sessions are the parts ego scores. Picking a row lists those parts in the sessions view. */
export function ThreadsPanel({ onPick, onActivity }: { onPick: (thread: string) => void; onActivity?: (thread: string) => void }) {
  const [rows, setRows] = useState<ThreadRow[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const tenant = useSelectedTenant();
  const load = useCallback(async (current: () => boolean) => {
    try {
      const data = await getThreads();
      if (!current()) return;
      setRows(data);
      setError(null);
    } catch (e) {
      if (current()) setError(e instanceof Error ? e.message : String(e));
    }
    // getThreads reads the picked tenant; listing it as a dependency loads again when the pick changes.
  }, [tenant]);
  useLiveData('usage', load);

  if (error) return <p css={noteStyle} role="alert">{error}</p>;
  if (rows === null) return <p css={noteStyle}>loading threads…</p>;
  if (rows.length === 0) return <p css={noteStyle}>no threads</p>;
  return (
    <div css={wrapStyle}>
      <table css={tableStyle}>
        <thead>
          <tr>
            <th>Thread</th>
            <th css={numStyle}>Parts</th>
            <th>First seen</th>
            <th>Last seen</th>
            <th css={numStyle}>Tokens</th>
            <th css={numStyle}>Last score</th>
            {onActivity && <th />}
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.thread}>
              <td>
                <button css={linkStyle} onClick={() => onPick(r.thread)} title="List the parts of this thread">{r.thread}</button>
              </td>
              <td css={numStyle}>
                {r.segments}
                {r.open && <span css={openStyle} title="The newest part still takes new traces">open</span>}
              </td>
              <td>{formatStarted(r.firstSeen)}</td>
              <td>{formatStarted(r.lastSeen)}</td>
              <td css={numStyle} title={`${r.totalTokens.toLocaleString()} tokens`}>{r.totalTokens ? shortTokens(r.totalTokens) : '–'}</td>
              <td css={numStyle}>{r.lastScore != null ? r.lastScore.toFixed(2) : '–'}</td>
              {onActivity && (
                <td>
                  <button css={linkStyle} onClick={() => onActivity(r.thread)} title="Turns of this thread over time">activity ›</button>
                </td>
              )}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

const noteStyle = css`
  color: var(--text-tertiary);
  font-size: 0.875rem;
  margin: 8px 0;
`;

const wrapStyle = css`
  overflow-x: auto;
  background: var(--card-bg);
  border: 1px solid var(--border-default);
  border-radius: var(--radius);
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

const linkStyle = css`
  padding: 0;
  border: none;
  background: none;
  color: var(--accent-primary);
  font-family: var(--font-mono);
  font-size: inherit;
  cursor: pointer;
  max-width: 420px;
  overflow: hidden;
  text-overflow: ellipsis;
  &:hover { text-decoration: underline; }
`;

const openStyle = css`
  margin-left: 8px;
  color: var(--accent-cyan);
  font-size: 0.72rem;
`;
