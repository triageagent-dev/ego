import React, { useMemo, useState } from 'react';
import { css } from '@emotion/react';
import { Check, Search } from 'lucide-react';
import type { TenantOption } from '../../lib/tenant';

interface Row {
  key: string;
  label: string;
  title?: string;
  // How many sessions the tenant has, shown beside its name.
  sessions?: number;
  // A group heading (the sender), not something to pick.
  head?: boolean;
}

// Rows on screen at once. A longer list is not scrolled: the letter rail moves the window and typing narrows it.
// A list that overflows by only a row or two is shown whole, a hint for one hidden row is worse than the row.
const WINDOW = 10;
const SLACK = 2;

const ALL: Row = { key: '', label: 'All tenants' };

// Picks the tenant the views show. "All tenants" is always on top; below it the tenants, grouped under the sender
// (the core or agent that reports them) when there is more than one, so two tenants of the same name read as what
// they are: the same tenant on two senders. A fixed window of rows, the pick checked, a letter rail that moves the
// window. "All of <sender>" is every tenant of one sender.
export const TenantPicker: React.FC<{ options: TenantOption[]; picked: string; onPick: (key: string) => void }> = ({ options, picked, onPick }) => {
  const [query, setQuery] = useState('');

  const rows = useMemo(() => {
    const named = (o: TenantOption) => o.name || o.label;
    // The groups: each sender, then the tenants reported without one (the shared ingest token), as "other".
    const senders = Array.from(new Set(options.map((o) => o.sender ?? '').filter(Boolean))).sort();
    const groups = options.some((o) => !o.sender) ? [...senders, ''] : senders;
    const tenantsOf = (sender: string): Row[] =>
      options
        .filter((o) => (o.sender ?? '') === sender)
        .sort((a, b) => named(a).localeCompare(named(b), undefined, { sensitivity: 'base' }))
                .map((o) => ({ key: o.key, label: named(o), sessions: o.sessions, title: `${o.key} · ${o.sessions} session${o.sessions === 1 ? '' : 's'}` }));
    if (groups.length <= 1) return tenantsOf(groups[0] ?? '');
    return groups.flatMap((sender): Row[] => [
      { key: `head:${sender}`, label: sender || 'other', head: true },
      ...(sender ? [{ key: `${sender}/*`, label: `All of ${sender}` }] : []),
      ...tenantsOf(sender),
    ]);
  }, [options]);

  const q = query.trim().toLowerCase();
  // A search keeps the tenants that match, each under its sender's heading; the "All of" rows are for browsing.
  const shown = useMemo(() => {
    if (!q) return rows;
    const out: Row[] = [];
    let head: Row | null = null;
    for (const r of rows) {
      if (r.head) {
        head = r;
        continue;
      }
      if (r.key.endsWith('/*')) continue;
      if (`${r.label} ${r.key}`.toLowerCase().includes(q)) {
        if (head) {
          out.push(head);
          head = null;
        }
        out.push(r);
      }
    }
    return out;
  }, [rows, q]);

  // The window opens so that the current pick is in it.
  const [start, setStart] = useState(() => Math.max(0, rows.findIndex((r) => r.key === picked) - WINDOW + 1));

  const isTenant = (r: Row) => !r.head && !r.key.endsWith('/*');
  const letterOf = (r: Row) => {
    const c = r.label.charAt(0).toUpperCase();
    return /[A-Z]/.test(c) ? c : '#';
  };
  const letters = Array.from(new Set(rows.filter(isTenant).map(letterOf))).sort();
  const size = shown.length <= WINDOW + SLACK ? shown.length : WINDOW;
  const maxStart = Math.max(0, shown.length - size);
  const from = Math.min(start, maxStart);
  const visible = shown.slice(from, from + size);
  const below = shown.length - (from + visible.length);
  // The window moves by a page from the arrows under and over the list, and by a row from the arrow keys.
  const move = (rows: number) => setStart(Math.max(0, Math.min(maxStart, from + rows)));
  const jump = (l: string) => {
    const i = shown.findIndex((r) => isTenant(r) && letterOf(r) === l);
    if (i >= 0) setStart(Math.min(i, maxStart));
  };

  const pickable = (r: Row) => (
    <button
      key={r.key}
      css={[rowStyle, r.key === picked && pickedStyle]}
      role="option"
      aria-selected={r.key === picked}
      title={r.title}
      onClick={() => onPick(r.key)}
    >
      <span css={checkStyle}>{r.key === picked && <Check size={13} />}</span>
      <span css={labelStyle}>{r.label}</span>
      {r.sessions !== undefined && <span css={countStyle}>{r.sessions > 0 ? r.sessions.toLocaleString() : '–'}</span>}
    </button>
  );

  return (
    <div css={wrapStyle}>
      <div css={headingStyle}>Tenants ({options.length})</div>
      <label css={searchStyle}>
        <Search size={13} />
        <input
          autoFocus
          value={query}
          onChange={(e) => {
            setQuery(e.target.value);
            setStart(0);
          }}
          onKeyDown={(e) => {
            const first = shown.find((r) => !r.head);
            if (e.key === 'Enter' && first) onPick(first.key);
            else if (e.key === 'ArrowDown') {
              e.preventDefault();
              move(1);
            } else if (e.key === 'ArrowUp') {
              e.preventDefault();
              move(-1);
            }
          }}
          placeholder={`Search ${options.length} tenants`}
          aria-label="Search tenants"
        />
      </label>
      <div css={pinnedStyle}>{pickable(ALL)}</div>
      {from > 0 && (
        <button css={moreStyle} onClick={() => move(-size)}>↑ {from} above</button>
      )}
      <div css={bodyStyle}>
        <div css={listStyle} role="listbox">
          {visible.map((r) => (r.head ? <div key={r.key} css={groupStyle}>{r.label}</div> : pickable(r)))}
          {shown.length === 0 && <div css={emptyStyle}>no tenant matches</div>}
        </div>
        {!q && letters.length > 1 && (
          <div css={railStyle} aria-hidden>
            {letters.map((l) => (
              <button key={l} css={letterStyle} onClick={() => jump(l)} tabIndex={-1}>{l}</button>
            ))}
          </div>
        )}
      </div>
      {below > 0 && <button css={moreStyle} onClick={() => move(size)}>↓ {below} more · or type to narrow</button>}
    </div>
  );
};

const wrapStyle = css`
  padding: 4px 4px 2px;
`;

const headingStyle = css`
  padding: 2px 4px 5px;
  color: var(--text-tertiary);
  font-size: 0.72rem;
`;

const searchStyle = css`
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 0 8px;
  height: 26px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius);
  background: var(--bg-primary);
  color: var(--text-tertiary);

  &:focus-within {
    border-color: var(--accent-primary);
  }

  input {
    flex: 1;
    min-width: 0;
    border: none;
    outline: none;
    background: transparent;
    color: var(--text-primary);
    font: inherit;
  }
`;

const bodyStyle = css`
  position: relative;
  margin-top: 4px;
`;

const listStyle = css`
  padding-right: 18px;
`;

const rowStyle = css`
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
  padding: 4px 6px;
  border: none;
  border-radius: var(--radius);
  background: transparent;
  color: var(--text-primary);
  font: inherit;
  text-align: left;
  cursor: pointer;

  &:hover {
    background: var(--bg-elevated);
  }
`;

const pickedStyle = css`
  background: var(--bg-selected);
  color: var(--accent-primary);
`;

const checkStyle = css`
  display: inline-flex;
  width: 13px;
  flex-shrink: 0;
`;

const labelStyle = css`
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
`;

const emptyStyle = css`
  padding: 6px;
  color: var(--text-tertiary);
`;

const railStyle = css`
  position: absolute;
  top: 0;
  right: 2px;
  bottom: 0;
  display: flex;
  flex-direction: column;
  justify-content: center;
`;

const letterStyle = css`
  padding: 0 3px;
  border: none;
  background: transparent;
  color: var(--text-tertiary);
  font: inherit;
  font-size: 0.62rem;
  line-height: 1;
  cursor: pointer;

  &:hover {
    color: var(--accent-primary);
  }
`;

const moreStyle = css`
  display: block;
  width: 100%;
  padding: 5px 6px 2px;
  border: none;
  background: transparent;
  color: var(--text-muted);
  font: inherit;
  font-size: 0.68rem;
  text-align: left;
  cursor: pointer;

  &:hover {
    color: var(--accent-primary);
  }
`;

// "All tenants" stays on top however the list below is moved or narrowed.
const pinnedStyle = css`
  margin-top: 6px;
  padding-bottom: 4px;
  border-bottom: 1px solid var(--border-default);
`;

// The sender a group of tenants belongs to.
const groupStyle = css`
  padding: 6px 6px 2px;
  color: var(--text-muted);
  font-size: 0.66rem;
  text-transform: uppercase;
  letter-spacing: 0.06em;
`;

// Sessions of the tenant, at the end of its row.
const countStyle = css`
  flex-shrink: 0;
  color: var(--text-muted);
  font-family: var(--font-mono);
  font-size: 0.68rem;
`;
