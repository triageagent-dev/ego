import React, { useEffect, useState } from 'react';
import { css } from '@emotion/react';
import { fetchTenants, setSelectedTenant, useSelectedTenant, type TenantOption } from '../../lib/tenant';

const REFRESH_MS = 60_000;

// Picks which tenant the sessions, usage and quality views show. Admins see every tenant and can switch between
// them; anyone else is offered only the tenants their team grants. Hidden when there is nothing to choose from.
export const TenantSwitch: React.FC = () => {
  const picked = useSelectedTenant();
  const [options, setOptions] = useState<TenantOption[] | null>(null);

  useEffect(() => {
    let active = true;
    const load = () =>
      fetchTenants()
        .then((o) => active && setOptions(o))
        .catch(() => active && setOptions([]));
    load();
    const id = setInterval(load, REFRESH_MS);
    return () => {
      active = false;
      clearInterval(id);
    };
  }, []);

  // A remembered pick that the role no longer covers is dropped rather than shown as an empty view.
  useEffect(() => {
    if (!options || !picked) return;
    const known = options.some((o) => o.key === picked) ||
      (picked.endsWith('/*') && options.some((o) => o.key.startsWith(picked.slice(0, -1))));
    if (!known) setSelectedTenant('');
  }, [options, picked]);

  if (!options || options.length === 0) return null;

  const senders = Array.from(new Set(options.map((o) => o.sender ?? ''))).sort();
  return (
    <label css={wrapStyle} title="Tenant">
      <span>tenant</span>
      <select css={selectStyle} value={picked} onChange={(e) => setSelectedTenant(e.target.value)}>
        <option value="">all</option>
        {senders.map((s) =>
          s ? (
            <optgroup key={s} label={s}>
              <option value={`${s}/*`}>{s} · all</option>
              {options.filter((o) => o.sender === s).map((o) => (
                <option key={o.key} value={o.key}>{o.label}{o.sessions ? ` (${o.sessions})` : ''}</option>
              ))}
            </optgroup>
          ) : (
            options.filter((o) => !o.sender).map((o) => (
              <option key={o.key} value={o.key}>{o.label}{o.sessions ? ` (${o.sessions})` : ''}</option>
            ))
          ),
        )}
      </select>
    </label>
  );
};

const wrapStyle = css`
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: var(--text-tertiary);
  font-size: 11.5px;
`;

const selectStyle = css`
  max-width: 190px;
  padding: 1px 4px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius);
  background: var(--bg-surface);
  color: var(--accent-magenta);
  font: inherit;
`;
