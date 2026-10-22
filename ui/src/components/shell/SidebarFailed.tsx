import React, { useCallback, useState } from 'react';
import { css } from '@emotion/react';
import { getFailedCallSessions, getFailedSessions, getOnlineAlerts, getOnlineFindings, getUsageLimits, getUsageSessions, listRuns } from '../../api/client';
import { useLiveData } from '../../lib/connection';
import { tenantName, useSelectedTenant, useTenantNames } from '../../lib/tenant';
import { formatStarted } from '../../lib/trace-helpers';
import { budgetAlerts, isBurst } from '../../lib/usageAlerts';
import { passRate } from '../runs/runHistory';
import { sessionLink } from './routes';
import { FlagIcon, popoverItemStyle, popoverNoteStyle, popoverRowStyle, SidebarPopover } from './SidebarPopover';

const RED = '#ef4444';
const GREEN = '#4ade80';

// The failed metric, or the first of several: `safety_v1 +2`.
function metricsLabel(metrics: string): string {
  const names = [...new Set(metrics.split(',').filter(Boolean))];
  return names.length > 1 ? `${names[0]} +${names.length - 1}` : (names[0] ?? '');
}

// One alert per tab: sessions, usage, runs and ego.
type Category = 'sessions' | 'usage' | 'runs' | 'ego';

interface Entry {
  category: Category;
  // When it happened, if it is an event rather than a state.
  at?: number;
  scope: string;
  text: string;
  value: string;
  title: string;
  // Where a click goes.
  hash: string;
}

const scopeOf = (tenants: string) => tenants.split(',').map((t) => t.trim()).filter(Boolean).map(tenantName).join(' · ');

// A run counts as failed when it did not finish, or when fewer than half of its decided results passed.
const LOW_PASS = 0.5;

// A red flag with what needs a look, or a green one when nothing does: the latest alert of each tab. Sessions: the
// last to have a failed LLM call. Usage: a daily budget nearly spent, else the latest session that ran away. Runs: the
// latest that failed or mostly failed. Ego: the latest of a session that failed an evaluation and an open alert. It
// follows the live feed: all of it lands in data that tells the feed it changed.
export const SidebarFailed: React.FC<{ collapsed: boolean }> = ({ collapsed }) => {
  const [entries, setEntries] = useState<Entry[] | null>(null);
  useTenantNames();
  // The lists are of the tenant picked in the account menu, so they are read again when it changes.
  const picked = useSelectedTenant();
  const load = useCallback(async () => {
    const [calls, limits, bursts, runs, evals, alerts, findings] = await Promise.allSettled([
      getFailedCallSessions(1), getUsageLimits(), getUsageSessions(24, 25), listRuns({ limit: 100 }), getFailedSessions(1), getOnlineAlerts('open'), getOnlineFindings(),
    ]);
    // Each source can be missing (not configured, or not for this role); with all of them missing there is nothing to flag.
    if ([calls, limits, bursts, runs, evals, alerts, findings].every((r) => r.status === 'rejected')) {
      setEntries(null);
      return;
    }
    const out: Entry[] = [];

    if (calls.status === 'fulfilled' && calls.value[0]) {
      const f = calls.value[0];
      out.push({
        category: 'sessions', at: f.lastAt, scope: [scopeOf(f.tenants), f.kinds.split(',')[0]].filter(Boolean).join(' · '),
        text: `${f.failed} of ${f.calls} LLM calls failed`, value: `${f.failed}`, title: `A session with failed LLM calls\n${f.sessionId}`, hash: sessionLink(f.sessionId),
      });
    }

    const budget = limits.status === 'fulfilled' ? budgetAlerts(limits.value)[0] : undefined;
    const runaway = bursts.status === 'fulfilled' ? bursts.value.filter(isBurst).sort((a, b) => b.end - a.end)[0] : undefined;
    if (budget) {
      out.push({
        category: 'usage', scope: budget.group.toLowerCase(), text: budget.label, value: `${Math.round(budget.fraction * 100)}%`,
        title: `${budget.used.toLocaleString()} of ${budget.limit.toLocaleString()} ${budget.unit} today`, hash: '#usage',
      });
    } else if (runaway) {
      out.push({
        category: 'usage', at: runaway.end, scope: scopeOf(runaway.tenants),
        text: `ran away: ${runaway.calls} calls in ${Math.max(1, Math.round((runaway.end - runaway.start) / 60000))} min`, value: '',
        title: `A session that ran away\n${runaway.sessionId}`, hash: sessionLink(runaway.sessionId),
      });
    }

    if (runs.status === 'fulfilled') {
      const bad = runs.value
        .filter((r) => r.status === 'failed' || (passRate(r) ?? 1) < LOW_PASS)
        .sort((a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt))[0];
      if (bad) {
        const rate = passRate(bad);
        out.push({
          category: 'runs', at: Date.parse(bad.createdAt), scope: String((bad.spec?.evalSet as { name?: string } | undefined)?.name ?? 'run'),
          text: bad.status === 'failed' ? 'did not finish' : 'mostly failed', value: rate === null ? '' : `${Math.round(rate * 100)}%`,
          title: `${bad.status === 'failed' ? 'A run that failed' : 'A run where most results failed'}\n${bad.runId}`, hash: `#runs?run=${encodeURIComponent(bad.runId)}`,
        });
      }
    }

    const failedEval = evals.status === 'fulfilled' ? evals.value[0] : undefined;
    const alert = alerts.status === 'fulfilled' ? [...alerts.value].sort((a, b) => Date.parse(b.openedAt) - Date.parse(a.openedAt))[0] : undefined;
    const openFindings = findings.status === 'fulfilled' ? findings.value.filter((f) => f.state === 'open') : [];
    if (failedEval && (!alert || failedEval.lastAt >= Date.parse(alert.openedAt))) {
      // A click goes to the ego page, to what ego made of it: the finding of the same scope, else its alert, else
      // the page itself. The session is in the tooltip and on the page's own lists.
      const finding = openFindings.find((f) => f.tenant === failedEval.tenant && f.kind === failedEval.kind);
      const sameScope = alerts.status === 'fulfilled' ? alerts.value.find((a) => a.key.tenant === failedEval.tenant && a.key.kind === failedEval.kind) : undefined;
      out.push({
        category: 'ego', at: failedEval.lastAt, scope: [scopeOf(failedEval.tenant), failedEval.kind].filter(Boolean).join(' · '),
        text: metricsLabel(failedEval.metrics), value: `${Math.round(failedEval.lowest * 100)}%`,
        title: `A session that failed an evaluation; lowest score ${Math.round(failedEval.lowest * 100)}%\n${failedEval.sessionId}`,
        hash: finding ? `#ego?finding=${encodeURIComponent(finding.finding_id)}` : sameScope ? `#ego?alert=${encodeURIComponent(sameScope.id)}` : '#ego',
      });
    } else if (alert) {
      out.push({
        category: 'ego', at: Date.parse(alert.openedAt), scope: [scopeOf(alert.key.tenant), alert.key.kind].filter(Boolean).join(' · '),
        text: alert.key.metric, value: `${Math.round(alert.last.baseMean * 100)}→${Math.round(alert.last.curMean * 100)}%`,
        title: 'An open alert: the score of this metric dropped', hash: `#ego?alert=${encodeURIComponent(alert.id)}`,
      });
    }

    setEntries(out);
  }, [picked]);
  useLiveData(['quality', 'usage', 'runs'], load);

  if (entries === null) return null;
  const any = entries.length > 0;
  return (
    <SidebarPopover
      collapsed={collapsed}
      icon={<FlagIcon color={any ? RED : GREEN} />}
      label={any ? 'alerts' : 'all clear'}
      badge={any ? entries.length : null}
      badgeColor={RED}
      title={any ? `${entries.length} tab${entries.length === 1 ? '' : 's'} with something to look at: ${entries.map((e) => e.category).join(', ')}` : 'Nothing to look at in the last 24 hours'}
    >
      {(close) =>
        any ? (
          entries.map((e) => (
            <div key={e.category} css={popoverRowStyle}>
              <button
                css={popoverItemStyle}
                onClick={() => {
                  location.hash = e.hash;
                  close();
                }}
                title={e.title}
              >
                <span css={lineStyle}>
                  <span css={tagStyle(e.category)}>{e.category}</span>
                  {e.at !== undefined && <span css={whenStyle}>{formatStarted(new Date(e.at).toISOString())}</span>}
                  {e.scope && <span css={scopeStyle}>{e.scope}</span>}
                  <span css={textStyle}>{e.text}</span>
                  {e.value && <span css={valueStyle}>{e.value}</span>}
                </span>
              </button>
            </div>
          ))
        ) : (
          <div css={popoverNoteStyle}>All clear in the last 24 hours: no failed calls, no budget nearly spent, no failed run, no failed evaluation.</div>
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

const TAG_COLOR: Record<Category, string> = { sessions: '#ef4444', usage: '#f59e0b', runs: '#fb923c', ego: '#b5abfc' };

const tagStyle = (c: Category) => css`
  flex-shrink: 0;
  padding: 0 6px;
  border-radius: var(--radius);
  background: ${TAG_COLOR[c]}26;
  color: ${TAG_COLOR[c]};
  font-size: 10.5px;
  line-height: 16px;
`;

const whenStyle = css`
  flex-shrink: 0;
  color: #d6c28f;
  font-family: var(--font-mono);
`;

const scopeStyle = css`
  flex-shrink: 1;
  min-width: 0;
  max-width: 90px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: #b5abfc;
`;

const textStyle = css`
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--text-secondary);
`;

const valueStyle = css`
  flex-shrink: 0;
  max-width: 80px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: ${RED};
  font-family: var(--font-mono);
`;
