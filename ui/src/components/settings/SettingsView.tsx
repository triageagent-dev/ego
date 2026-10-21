import React, { useCallback, useState } from 'react';
import { css } from '@emotion/react';
import { getUsageLimits } from '../../api/client';
import { useLiveData } from '../../lib/connection';
import { useMe } from '../../lib/me';
import type { LimitItem } from '../../lib/types';
import { PageTitle } from '../shell/PageTitle';
import { ModeSwitch } from '../shell/ModeSwitch';
import { VersionLink } from '../shell/VersionLink';
import { cardStyle, cardTitleStyle, errorStyle, pageStyle, subtitleStyle, titleStyle } from '../ui/page';
import { EvalSettingsForm } from './EvalSettingsForm';

// 1,234,567 -> 1.2M
function short(n: number): string {
  if (n >= 1e9) return `${(n / 1e9).toFixed(1)}B`;
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
  if (n >= 1e3) return `${(n / 1e3).toFixed(1)}k`;
  return String(n);
}

function amount(n: number, unit: LimitItem['unit']): string {
  if (unit === 'days') return `${n} day${n === 1 ? '' : 's'}`;
  return short(n);
}

const LimitRow: React.FC<{ item: LimitItem }> = ({ item }) => {
  const unlimited = item.limit === 0;
  const pct = item.used !== undefined && !unlimited ? Math.min(100, (item.used / item.limit) * 100) : null;
  return (
    <div css={rowStyle} title={item.note}>
      <div css={rowHeadStyle}>
        <span css={rowLabelStyle}>{item.label}</span>
        <span css={rowValueStyle}>
          {item.used !== undefined && <>{amount(item.used, item.unit)} of </>}
          {unlimited ? 'no limit' : amount(item.limit, item.unit)}
        </span>
      </div>
      {pct !== null && (
        <div css={trackStyle}>
          <div css={fillStyle} style={{ width: `${pct}%`, background: pct >= 90 ? 'var(--status-failure)' : pct >= 70 ? 'var(--status-warning)' : 'var(--accent-primary)' }} />
        </div>
      )}
      {item.note && <div css={noteStyle}>{item.note}</div>}
    </div>
  );
};

export const SettingsView: React.FC = () => {
  const me = useMe();
  const [limits, setLimits] = useState<LimitItem[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setLimits(await getUsageLimits());
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, []);
  useLiveData('usage', load);

  const groups = new Map<string, LimitItem[]>();
  for (const item of limits ?? []) groups.set(item.group, [...(groups.get(item.group) ?? []), item]);

  return (
    <div css={pageStyle}>
      <header css={headerRowStyle}>
        <PageTitle>
          <h1 css={titleStyle}>settings</h1>
          <p css={subtitleStyle}>the evaluation defaults, the limits this server runs with, and the interface</p>
        </PageTitle>
      </header>

      <div css={columnsStyle}>
        <div>
          <section css={cardStyle}>
            <h2 css={cardTitleStyle}>evaluation</h2>
            <p css={hintStyle}>Used by every evaluation you start; kept in this browser.</p>
            <EvalSettingsForm showTrajectory />
          </section>

          <section css={cardStyle}>
            <h2 css={cardTitleStyle}>interface</h2>
            <div css={lineStyle}>
              <span css={rowLabelStyle}>Style</span>
              <ModeSwitch />
            </div>
          </section>

          <section css={cardStyle}>
            <h2 css={cardTitleStyle}>account</h2>
            <div css={lineStyle}>
              <span css={rowLabelStyle}>Signed in as</span>
              <span css={rowValueStyle}>{me?.authenticated ? me.username : 'no sign-in on this server'}</span>
            </div>
            {me?.authenticated && me.role && (
              <div css={lineStyle}>
                <span css={rowLabelStyle}>Role</span>
                <span css={rowValueStyle}>{me.role}</span>
              </div>
            )}
            <div css={lineStyle}>
              <span css={rowLabelStyle}>Server</span>
              <VersionLink />
            </div>
          </section>
        </div>

        <div>
          <section css={cardStyle}>
            <h2 css={cardTitleStyle}>caps and limits · read only</h2>
            <p css={hintStyle}>Set when the server starts or in its online configuration; not changed here.</p>
            {error && <div css={errorStyle}>{error}</div>}
            {limits === null && !error && <div css={noteStyle}>Loading…</div>}
            {[...groups.entries()].map(([group, items]) => (
              <div key={group} css={groupStyle}>
                <div css={groupTitleStyle}>{group}</div>
                {items.map((item) => <LimitRow key={item.key} item={item} />)}
              </div>
            ))}
          </section>
        </div>
      </div>
    </div>
  );
};

const headerRowStyle = css`
  margin-bottom: 4px;
`;

const columnsStyle = css`
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0 24px;
  align-items: start;
  max-width: 1100px;

  @media (max-width: 1000px) {
    grid-template-columns: minmax(0, 1fr);
  }
`;

const hintStyle = css`
  margin: 0 0 12px;
  color: var(--text-muted);
  font-size: 12px;
`;

const lineStyle = css`
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 5px 0;
  font-size: 12.5px;
`;

const groupStyle = css`
  & + & {
    margin-top: 14px;
  }
`;

const groupTitleStyle = css`
  margin-bottom: 4px;
  color: var(--accent-magenta);
  font-size: 11.5px;
  text-transform: lowercase;
`;

const rowStyle = css`
  padding: 5px 0;
  border-bottom: 1px solid var(--border-subtle);

  &:last-child {
    border-bottom: none;
  }
`;

const rowHeadStyle = css`
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 16px;
`;

const rowLabelStyle = css`
  color: var(--text-secondary);
  font-size: 12.5px;
`;

const rowValueStyle = css`
  color: var(--text-primary);
  font-family: var(--font-mono);
  font-size: 12px;
  white-space: nowrap;
`;

const trackStyle = css`
  height: 4px;
  margin-top: 4px;
  border-radius: var(--radius);
  background: var(--bg-elevated);
  overflow: hidden;
`;

const fillStyle = css`
  height: 100%;
  border-radius: var(--radius);
`;

const noteStyle = css`
  margin-top: 3px;
  color: var(--text-muted);
  font-size: 11px;
`;
