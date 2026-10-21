import React from 'react';
import { css } from '@emotion/react';
import { setSelectedTenant, useSelectedTenant, useTenantOptions, type TenantOption } from '../../lib/tenant';

const shown = (o: TenantOption) => `${o.name || o.label}${o.sessions ? ` (${o.sessions})` : ''}`;

// Picks which tenant the sessions, usage and quality views show. Admins see every tenant and can switch between
// them; anyone else is offered only the tenants their team grants. Hidden when there is nothing to choose from.
export const TenantSwitch: React.FC<{ stacked?: boolean }> = ({ stacked }) => {
  const picked = useSelectedTenant();
  const options = useTenantOptions();

  if (!options || options.length === 0) return null;

  const senders = Array.from(new Set(options.map((o) => o.sender ?? ''))).sort();
  return (
    <label css={[wrapStyle, stacked && stackedStyle]} title="Tenant">
      <span>tenant</span>
      <select css={[selectStyle, stacked && stackedSelectStyle]} value={picked} onChange={(e) => setSelectedTenant(e.target.value)}>
        <option value="">all</option>
        {senders.map((s) =>
          s ? (
            <optgroup key={s} label={s}>
              <option value={`${s}/*`}>{s} · all</option>
              {options.filter((o) => o.sender === s).map((o) => (
                <option key={o.key} value={o.key} title={o.key}>{shown(o)}</option>
              ))}
            </optgroup>
          ) : (
            options.filter((o) => !o.sender).map((o) => (
              <option key={o.key} value={o.key} title={o.key}>{shown(o)}</option>
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

// In a narrow column the label sits above the select and the select fills the width.
const stackedStyle = css`
  display: flex;
  flex-direction: column;
  align-items: stretch;
  gap: 4px;
  width: 100%;
`;

const stackedSelectStyle = css`
  max-width: none;
  width: 100%;
`;
