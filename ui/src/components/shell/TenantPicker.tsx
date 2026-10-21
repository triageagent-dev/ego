import React, { useMemo, useRef, useState } from 'react';
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

// The A-Z rail only earns its width on a long list.
const RAIL_MIN = 12;

// Picks the tenant the views show: a search box over the list, the current pick checked, and a letter rail to jump
// down a long list. '' is every tenant the role covers; "<sender>/*" is all of one sender's.
export const TenantPicker: React.FC<{ options: TenantOption[]; picked: string; onPick: (key: string) => void }> = ({ options, picked, onPick }) => {
  const [query, setQuery] = useState('');
  const listRef = useRef<HTMLDivElement>(null);
  const rowRefs = useRef(new Map<string, HTMLElement>());

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

  const q = query.trim().toLowerCase();
  const shown = q ? rows.filter((r) => `${r.label} ${r.hint ?? ''} ${r.key}`.toLowerCase().includes(q)) : rows;

  const letterOf = (r: Row) => {
    const c = r.label.charAt(0).toUpperCase();
    return /[A-Z]/.test(c) ? c : '#';
  };
  const letters = Array.from(new Set(tenantRows.map(letterOf)));
  const firstOfLetter = (l: string) => tenantRows.find((r) => letterOf(r) === l);
  const jump = (l: string) => {
    const first = firstOfLetter(l);
    const el = first && rowRefs.current.get(first.key);
    if (el && listRef.current) listRef.current.scrollTop = el.offsetTop - listRef.current.offsetTop;
  };

  return (
    <div css={wrapStyle}>
      <div css={headingStyle}>Tenant ({options.length})</div>
      <label css={searchStyle}>
        <Search size={16} />
        <input
          autoFocus
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && shown[0]) onPick(shown[0].key);
          }}
          placeholder={`Search ${options.length} tenants`}
          aria-label="Search tenants"
        />
      </label>
      <div css={bodyStyle}>
        <div css={listStyle} ref={listRef} role="listbox">
          {shown.map((r) => (
            <button
              key={r.key || 'all'}
              ref={(el) => {
                if (el) rowRefs.current.set(r.key, el);
                else rowRefs.current.delete(r.key);
              }}
              css={[rowStyle, r.key === picked && pickedStyle]}
              role="option"
              aria-selected={r.key === picked}
              title={r.title}
              onClick={() => onPick(r.key)}
            >
              <span css={checkStyle}>{r.key === picked && <Check size={16} />}</span>
              <span css={labelStyle}>{r.label}</span>
              {r.hint && <span css={hintStyle}>{r.hint}</span>}
            </button>
          ))}
          {shown.length === 0 && <div css={emptyStyle}>no tenant matches</div>}
        </div>
        {!q && options.length >= RAIL_MIN && (
          <div css={railStyle} aria-hidden>
            {letters.map((l) => (
              <button key={l} css={letterStyle} onClick={() => jump(l)} tabIndex={-1}>{l}</button>
            ))}
          </div>
        )}
      </div>
    </div>
  );
};

const wrapStyle = css`
  padding: 4px 4px 2px;
`;

const headingStyle = css`
  padding: 4px 6px 8px;
  color: var(--text-tertiary);
`;

const searchStyle = css`
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 0 10px;
  height: 34px;
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
  margin-top: 6px;
`;

const listStyle = css`
  max-height: 300px;
  overflow-y: auto;
  position: relative;
  padding-right: 22px;
`;

const rowStyle = css`
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  padding: 8px 8px;
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
  width: 16px;
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
  font-size: 0.72rem;
`;

const emptyStyle = css`
  padding: 10px 8px;
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
  padding: 0 4px;
  border: none;
  background: transparent;
  color: var(--text-tertiary);
  font: inherit;
  font-size: 0.68rem;
  line-height: 1.15;
  cursor: pointer;

  &:hover {
    color: var(--accent-primary);
  }
`;
