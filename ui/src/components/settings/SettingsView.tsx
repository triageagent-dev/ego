import React, { useCallback, useState } from 'react';
import { css } from '@emotion/react';
import { getUsageLimits } from '../../api/client';
import { useLiveData } from '../../lib/connection';
import { useMe } from '../../lib/me';
import type { LimitItem } from '../../lib/types';
import { PageTitle } from '../shell/PageTitle';
import { VersionLink } from '../shell/VersionLink';
import { errorStyle, pageStyle, subtitleStyle, titleStyle } from '../ui/page';
import { EvalSettingsForm } from './EvalSettingsForm';
import { ScoringJobsPanel } from './ScoringJobsPanel';

// 1,234,567 -> 1.2M
function short(n: number): string {
  if (n >= 1e9) return `${(n / 1e9).toFixed(1)}B`;
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
  if (n >= 1e3) return `${(n / 1e3).toFixed(1)}k`;
  return String(n);
}

// 1073741824 -> 1.0 GiB
function size(n: number): string {
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${i === 0 ? v : v.toFixed(1)} ${units[i]}`;
}

function amount(n: number, unit: LimitItem['unit']): string {
  if (unit === 'bytes') return size(n);
  if (unit === 'days') return `${n} day${n === 1 ? '' : 's'}`;
  return short(n);
}

// One limit as a row: its name, and at the end the figure in use (or the limit, for one that is not counted) with what it
// is out of, and a thin bar under a running total. Red and amber are for budgets that stop work; a full cache is normal.
const LimitRow: React.FC<{ item: LimitItem }> = ({ item }) => {
  const unlimited = item.limit === 0;
  const counted = item.used !== undefined;
  const pct = counted && !unlimited ? Math.min(100, (item.used! / item.limit) * 100) : null;
  const budget = item.unit === 'tokens' || item.unit === 'calls';
  const disk = item.unit === 'bytes';
  const color = !budget && !disk ? 'var(--accent-primary)' : pct !== null && pct >= 90 ? 'var(--status-failure)' : pct !== null && pct >= 70 ? 'var(--status-warning)' : 'var(--accent-primary)';
  const daily = budget ? ' a day' : '';
  return (
    <div css={limitRowStyle} title={item.note}>
      <span css={rowLabelStyle}>{item.label}</span>
      <span css={rowValueStyle}>
        {counted ? (item.unit === 'bytes' ? size(item.used!) : short(item.used!)) : amount(item.limit, item.unit)}
        <span css={ofStyle}>
          {counted ? ` of ${unlimited ? 'no limit' : amount(item.limit, item.unit) + daily}` : item.unit === 'days' ? ' then deleted' : ' at most'}
        </span>
      </span>
      {pct !== null && (
        <div css={trackStyle}>
          <div css={fillStyle} style={{ width: `${pct}%`, background: color }} />
        </div>
      )}
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
          <p css={subtitleStyle}>the evaluation defaults and the limits this server runs with</p>
        </PageTitle>
      </header>

      <div css={columnsStyle}>
        <div>
          <section css={sectionStyle}>
            <h2 css={sectionTitleStyle}>caps and limits</h2>
            <p css={hintStyle}>Read only: set when the server starts or in its online configuration. Daily budgets reset at 00:00 UTC.</p>
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

        <div>
          <section css={sectionStyle}>
            <h2 css={sectionTitleStyle}>scoring jobs</h2>
            <p css={hintStyle}>
              Each job scores a sample of the sessions an agent finishes, as they come: the kind of session it covers, the metrics that score
              it, how many an hour and how many of the last turns. The scores feed quality, alerts and findings.
            </p>
            <ScoringJobsPanel canEdit={!me?.authenticated || me.role === 'admin'} />
          </section>

          <section css={sectionStyle}>
            <h2 css={sectionTitleStyle}>evaluation</h2>
            <p css={hintStyle}>Used by every evaluation you start; kept in this browser.</p>
            <EvalSettingsForm showTrajectory />
          </section>

          <section css={sectionStyle}>
            <h2 css={sectionTitleStyle}>account</h2>
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
      </div>
    </div>
  );
};

// A section is a plain box with its heading inside, above the content.
const sectionStyle = css`
  margin: 0 0 14px;
  padding: 12px 14px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius);
  background: var(--card-bg);
`;

const sectionTitleStyle = css`
  margin: 0 0 8px;
  color: var(--accent-cyan);
  font-size: 13px;
  font-weight: 500;
  text-transform: lowercase;
`;

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
  margin-bottom: 2px;
  color: var(--text-muted);
  font-size: 11.5px;
  text-transform: lowercase;
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

const limitRowStyle = css`
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  justify-content: space-between;
  gap: 0 16px;
  padding: 5px 0;
  border-bottom: 1px solid var(--border);

  &:last-of-type {
    border-bottom: none;
  }
`;

const ofStyle = css`
  color: var(--text-muted);
  font-family: var(--font-display);
`;

const trackStyle = css`
  flex-basis: 100%;
  height: 3px;
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
