import React, { useCallback, useState } from 'react';
import { css } from '@emotion/react';
import { getOnlineAlerts, getOnlineFindings } from '../../api/client';
import { useLiveData } from '../../lib/connection';
import { tenantName, useSelectedTenant, useTenantNames } from '../../lib/tenant';
import type { OnlineAlert, OnlineFinding } from '../../lib/types';
import { FlagIcon, popoverItemStyle, popoverRowStyle, SidebarPopover } from './SidebarPopover';

const ORANGE = '#fb923c';
const SHOWN = 8;

type Item = { id: string; type: 'alert' | 'finding'; scope: string; metric: string; text: string };

// An orange flag with the open alerts and open findings to look at, behind one count; a click lists them and opens the
// quality page, where each has its runs. Nothing open: no flag.
export const SidebarReview: React.FC<{ collapsed: boolean }> = ({ collapsed }) => {
  const [items, setItems] = useState<Item[]>([]);
  useTenantNames();
  // The lists are of the tenant picked in the account menu, so they are read again when it changes.
  const picked = useSelectedTenant();
  const load = useCallback(async () => {
    try {
      const [alerts, findings] = await Promise.all([
        getOnlineAlerts('open'),
        getOnlineFindings().catch(() => [] as OnlineFinding[]),
      ]);
      const scope = (tenant: string, kind: string) => [tenant && tenantName(tenant), kind].filter(Boolean).join(' · ');
      setItems([
        ...alerts.filter((a: OnlineAlert) => a.status === 'open').map((a): Item => ({
          id: a.id, type: 'alert', scope: scope(a.key.tenant, a.key.kind), metric: a.key.metric,
          text: `${Math.round(a.last.baseMean * 100)}% → ${Math.round(a.last.curMean * 100)}%`,
        })),
        ...findings.filter((f) => f.state === 'open').map((f): Item => ({
          id: f.finding_id, type: 'finding', scope: scope(f.tenant, f.kind), metric: f.metric, text: f.diagnosis.failure_mode,
        })),
      ]);
    } catch {
      // Online evaluation is not configured, or this role has none: no flag.
      setItems([]);
    }
  }, [picked]);
  useLiveData('quality', load);

  if (items.length === 0) return null;
  const alerts = items.filter((i) => i.type === 'alert').length;
  const findings = items.length - alerts;
  return (
    <SidebarPopover
      collapsed={collapsed}
      icon={<FlagIcon color={ORANGE} />}
      label="to review"
      badge={items.length}
      badgeColor={ORANGE}
      title={`${alerts} open alert${alerts === 1 ? '' : 's'} and ${findings} open finding${findings === 1 ? '' : 's'}`}
    >
      {(close) => (
        <>
          {items.slice(0, SHOWN).map((i) => (
            <div key={i.id} css={popoverRowStyle}>
              <button css={popoverItemStyle} onClick={() => { location.hash = '#ego'; close(); }} title={i.text}>
                <span css={lineStyle}>
                  <span css={i.type === 'alert' ? alertTagStyle : findingTagStyle}>{i.type}</span>
                  <span css={scopeStyle}>{i.scope}</span>
                  <span css={metricStyle}>{i.metric}</span>
                </span>
              </button>
            </div>
          ))}
          {items.length > SHOWN && (
            <div css={popoverRowStyle}>
              <button css={popoverItemStyle} onClick={() => { location.hash = '#ego'; close(); }}>
                <span css={moreStyle}>+{items.length - SHOWN} more on the ego page</span>
              </button>
            </div>
          )}
        </>
      )}
    </SidebarPopover>
  );
};

const lineStyle = css`
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
`;

const tagBase = css`
  flex-shrink: 0;
  padding: 0 6px;
  border-radius: var(--radius);
  font-size: 10.5px;
  line-height: 16px;
`;

const alertTagStyle = css`
  ${tagBase};
  background: rgba(239, 68, 68, 0.15);
  color: #ef4444;
`;

const findingTagStyle = css`
  ${tagBase};
  background: rgba(251, 146, 60, 0.15);
  color: ${ORANGE};
`;

const scopeStyle = css`
  flex-shrink: 0;
  max-width: 110px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: #b5abfc;
`;

const metricStyle = css`
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--text-secondary);
  font-family: var(--font-mono);
`;

const moreStyle = css`
  color: var(--text-muted);
`;
