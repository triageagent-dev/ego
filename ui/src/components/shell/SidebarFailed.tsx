import React, { useCallback, useState } from 'react';
import { css } from '@emotion/react';
import { Flag } from 'lucide-react';
import { getFailedSessions, type FailedSession } from '../../api/client';
import { useLiveData } from '../../lib/connection';
import { tenantName, useTenantNames } from '../../lib/tenant';
import { formatStarted } from '../../lib/trace-helpers';
import { sessionLink } from './routes';
import { popoverItemStyle, popoverNoteStyle, popoverRowStyle, SidebarPopover } from './SidebarPopover';

const RED = '#ef4444';
const GREEN = '#4ade80';

// A red flag with the five sessions whose LLM calls failed last (24 hours), or a green one when none did. It
// follows the live feed: a failed call lands in the usage ledger, which tells the feed it changed.
export const SidebarFailed: React.FC<{ collapsed: boolean }> = ({ collapsed }) => {
  const [failed, setFailed] = useState<FailedSession[] | null>(null);
  useTenantNames();
  const load = useCallback(async () => {
    try {
      setFailed(await getFailedSessions(5));
    } catch {
      // The server keeps no usage ledger, or this role has no usage: no flag.
      setFailed(null);
    }
  }, []);
  useLiveData('usage', load);

  if (failed === null) return null;
  const any = failed.length > 0;
  return (
    <SidebarPopover
      collapsed={collapsed}
      icon={<Flag size={18} fill={any ? RED : 'none'} css={css`color: ${any ? RED : GREEN};`} />}
      label={any ? 'failed' : 'no failures'}
      badge={any ? failed.length : null}
      badgeColor={RED}
      title={any ? `${failed.length} session${failed.length === 1 ? '' : 's'} with failed LLM calls in the last 24 hours` : 'No failed LLM calls in the last 24 hours'}
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
                  {f.tenants && <span css={tenantStyle}>{f.tenants.split(',').map((t) => tenantName(t.trim())).join(' · ')}</span>}
                  <span css={kindStyle}>{f.kinds.split(',').join(' · ')}</span>
                  <span css={countStyle}>{f.failed}/{f.calls}</span>
                </span>
              </button>
            </div>
          ))
        ) : (
          <div css={popoverNoteStyle}>No failed LLM calls in the last 24 hours.</div>
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
