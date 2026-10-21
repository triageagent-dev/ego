import React, { useMemo, useState } from 'react';
import { css } from '@emotion/react';
import { Check, Search } from 'lucide-react';
import type { TenantOption } from '../../lib/tenant';

interface Row {
  key: string;
  label: string;
  // The sender, when tenants of more than one sender are listed.
  hint?: string;
  title?: string;
}

// Rows on screen at once. A longer list is not scrolled: the letter rail moves the window and typing narrows it.
// A list that overflows by only a row or two is shown whole, a hint for one hidden row is worse than the row.
const WINDOW = 8;
const SLACK = 2;

// Picks the tenant the views show: a search box over a fixed window of rows, the current pick checked, and a letter
// rail that moves the window. '' is every tenant the role covers; "<sender>/*" is all of one sender's.
export const TenantPicker: React.FC<{ options: TenantOption[]; picked: string; onPick: (key: string) => void }> = ({ options, picked, onPick }) => {
  const [query, setQuery] = useState('');

  const { rows, tenantRows } = useMemo(() => {
    const senders = Array.from(new Set(options.map((o) => o.sender ?? '').filter(Boolean))).sort();
    const named = (o: TenantOption) => o.name || o.label;
    const tenants: Row[] = [...options]
      .sort((a, b) => named(a).localeCompare(named(b), undefined, { sensitivity: 'base' }))
      .map((o) => ({
        key: o.key,
        label: named(o),
        hint: senders.length > 1 ? o.sender : undefined,
        title: `${o.key} · ${o.sessions} session${o.sessions === 1 ? '' : 's'}`,
      }));
    const lead: Row[] = [
      { key: '', label: 'all tenants' },
      ...senders.map((s) => ({ key: `${s}/*`, label: `${s} · all` })),
    ];
    return { rows: [...lead, ...tenants], tenantRows: tenants };
  }, [options]);

  // The window opens on the current pick, so it is never out of sight.
  const [start, setStart] = useState(() => Math.max(0, rows.findIndex((r) => r.key === picked) - WINDOW + 1));

  const q = query.trim().toLowerCase();
  const shown = q ? rows.filter((r) => `${r.label} ${r.hint ?? ''} ${r.key}`.toLowerCase().includes(q)) : rows;

  const letterOf = (r: Row) => {
    const c = r.label.charAt(0).toUpperCase();
    return /[A-Z]/.test(c) ? c : '#';
  };
  const letters = Array.from(new Set(tenantRows.map(letterOf)));
  const size = shown.length <= WINDOW + SLACK ? shown.length : WINDOW;
  const maxStart = Math.max(0, shown.length - size);
  const from = Math.min(start, maxStart);
  const visible = shown.slice(from, from + size);
  const below = shown.length - (from + visible.length);
  const jump = (l: string) => {
    const i = shown.findIndex((r) => r.key !== '' && !r.key.endsWith('/*') && letterOf(r) === l);
    if (i >= 0) setStart(Math.min(i, maxStart));
  };

  return (
    <div css={wrapStyle}>
      <div css={headingStyle}>Tenant ({options.length})</div>
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
            if (e.key === 'Enter' && shown[0]) onPick(shown[0].key);
          }}
          placeholder={`Search ${options.length} tenants`}
          aria-label="Search tenants"
        />
      </label>
      <div css={bodyStyle}>
        <div css={listStyle} role="listbox">
          {visible.map((r) => (
            <button
              key={r.key || 'all'}
              css={[rowStyle, r.key === picked && pickedStyle]}
              role="option"
              aria-selected={r.key === picked}
              title={r.title}
              onClick={() => onPick(r.key)}
            >
              <span css={checkStyle}>{r.key === picked && <Check size={13} />}</span>
              <span css={labelStyle}>{r.label}</span>
              {r.hint && <span css={hintStyle}>{r.hint}</span>}
            </button>
          ))}
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
      {below > 0 && <div css={moreStyle}>↓ {below} below — type to narrow</div>}
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

const hintStyle = css`
  color: var(--text-tertiary);
  font-size: 0.68rem;
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
  padding: 5px 6px 2px;
  color: var(--text-tertiary);
  font-size: 0.68rem;
`;
