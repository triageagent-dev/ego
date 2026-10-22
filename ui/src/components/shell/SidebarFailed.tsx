import React, { useCallback, useState } from 'react';
import { css } from '@emotion/react';
import { getFailedSessions, type FailedSession } from '../../api/client';
import { useLiveData } from '../../lib/connection';
import { tenantName, useSelectedTenant, useTenantNames } from '../../lib/tenant';
import { formatStarted } from '../../lib/trace-helpers';
import { sessionLink } from './routes';
import { FlagIcon, popoverItemStyle, popoverNoteStyle, popoverRowStyle, SidebarPopover } from './SidebarPopover';

// The failed metric, or the first of several: `safety_v1 +2`.
function metricsLabel(metrics: string): string {
  const names = [...new Set(metrics.split(',').filter(Boolean))];
  return names.length > 1 ? `${names[0]} +${names.length - 1}` : (names[0] ?? '');
}

const RED = '#ef4444';
const GREEN = '#4ade80';

// A red flag with the five sessions that failed an online evaluation last (a score below its threshold, in the last 24
// hours), or a green one when none did. It follows the live feed: scores land in the quality data, which tells the
// feed it changed.
export const SidebarFailed: React.FC<{ collapsed: boolean }> = ({ collapsed }) => {
  const [failed, setFailed] = useState<FailedSession[] | null>(null);
  useTenantNames();
  // The lists are of the tenant picked in the account menu, so they are read again when it changes.
  const picked = useSelectedTenant();
  const load = useCallback(async () => {
    try {
      setFailed(await getFailedSessions(5));
    } catch {
      // Online evaluation is not configured, or this role has none: no flag.
      setFailed(null);
    }
  }, [picked]);
  useLiveData('quality', load);

  if (failed === null) return null;
  const any = failed.length > 0;
  return (
    <SidebarPopover
      collapsed={collapsed}
      icon={<FlagIcon color={any ? RED : GREEN} />}
      label={any ? 'failed' : 'no failures'}
      badge={any ? failed.length : null}
      badgeColor={RED}
      title={any ? `${failed.length} session${failed.length === 1 ? '' : 's'} failed an evaluation in the last 24 hours` : 'No session failed an evaluation in the last 24 hours'}
    >
      {(close) =>
        any ? (
          failed.map((f) => (
            <div key={f.sessionId} css={popoverRowStyle}>
              <button
                css={popoverItemStyle}
                onClick={() => {
                  location.hash = sessionLink(f.sessionId);
                  close();
                }}
                title={f.sessionId}
              >
                <span css={lineStyle}>
                  <span css={whenStyle}>{formatStarted(new Date(f.lastAt).toISOString())}</span>
                  {f.tenant && <span css={tenantStyle}>{tenantName(f.tenant)}</span>}
                  <span css={kindStyle} title={f.metrics.split(',').join('\n')}>{metricsLabel(f.metrics)}</span>
                  <span css={countStyle} title="Lowest failing score">{Math.round(f.lowest * 100)}%</span>
                </span>
              </button>
            </div>
          ))
        ) : (
          <div css={popoverNoteStyle}>No session failed an evaluation in the last 24 hours.</div>
        )
      }
    </SidebarPopover>
  );
};

const lineStyle = css`
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
`;

const whenStyle = css`
  flex-shrink: 0;
  color: #d6c28f;
  font-family: var(--font-mono);
`;

const tenantStyle = css`
  flex-shrink: 1;
  min-width: 0;
  max-width: 90px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: #b5abfc;
`;

const kindStyle = css`
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: #5eead4;
`;

const countStyle = css`
  flex-shrink: 0;
  color: ${RED};
  font-family: var(--font-mono);
`;
