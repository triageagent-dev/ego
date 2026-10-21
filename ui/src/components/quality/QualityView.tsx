import React, { useCallback, useMemo, useState } from 'react';
import { css } from '@emotion/react';
import { pageStyle, headerStyle, titleStyle, subtitleStyle, filterRowStyle, segmentStyle, segBtnStyle, segActiveStyle, errorStyle, noteStyle, tilesStyle, tileStyle, tileLabelStyle, tileValueStyle, tileSubStyle, cardStyle, cardTitleStyle } from '../ui/page';
import { Line } from 'react-chartjs-2';
import {
  Chart as ChartJS,
  CategoryScale,
  LinearScale,
  LineElement,
  PointElement,
  Tooltip,
  Legend,
} from 'chart.js';
import { useLiveData } from '../../lib/connection';
import { getOnlineAlerts, getOnlineCorrections, getOnlineFindings, getOnlineSeries, getOnlineStatus, StorageUnavailableError } from '../../api/client';
import { onlineRunsLink } from '../../lib/runLinks';
import { sessionLink } from '../shell/routes';
import { tenantName, useSelectedTenant, useTenantNames } from '../../lib/tenant';
import type { OnlineAlert, OnlineCorrection, OnlineFinding, OnlineFindingState, OnlineSeriesPoint, OnlineStatus } from '../../lib/types';
import { PageTitle } from '../shell/PageTitle';

ChartJS.register(CategoryScale, LinearScale, LineElement, PointElement, Tooltip, Legend);

// Quality view. Online evaluation's state
// (GET /api/online/status, /series, /alerts): judge spend against the daily
// caps, what the sampler did, open regressions, and hourly mean scores per
// call kind. The per-run detail lives in Runs ("online · <kind>").

const RANGES: { label: string; hours: number }[] = [
  { label: '24h', hours: 24 },
  { label: '7d', hours: 24 * 7 },
  { label: '30d', hours: 24 * 30 },
];

// Same categorical slots as the Usage view.
const SLOTS = ['#3987e5', '#d95926', '#199e70', '#c98500', '#d55181', '#008300', '#9085e9'];

const FINDING_STATES: OnlineFindingState[] = ['open', 'corrected', 'resolved', 'stale'];

// A scope reads by the tenant's display name, not its key ("<sender>/<id>").
function scopeOf(p: { tenant: string; kind: string }): string {
  return p.tenant ? `${tenantName(p.tenant)} · ${p.kind}` : p.kind;
}

function formatTokens(n: number): string {
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
  if (n >= 1e3) return `${Math.round(n / 1e3)}k`;
  return String(n);
}

function hourLabel(ms: number): string {
  const d = new Date(ms);
  return `${d.getMonth() + 1}/${d.getDate()} ${String(d.getHours()).padStart(2, '0')}:00`;
}

export const QualityView: React.FC = () => {
  const [hours, setHours] = useState(24 * 7);
  const [status, setStatus] = useState<OnlineStatus | null>(null);
  const [series, setSeries] = useState<OnlineSeriesPoint[]>([]);
  const [alerts, setAlerts] = useState<OnlineAlert[]>([]);
  const [findings, setFindings] = useState<OnlineFinding[]>([]);
  const [corrections, setCorrections] = useState<OnlineCorrection[]>([]);
  const [findingState, setFindingState] = useState<OnlineFindingState>('open');
  const [openFinding, setOpenFinding] = useState<string | null>(null);
  const [scope, setScope] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [notConfigured, setNotConfigured] = useState(false);
  const tenant = useSelectedTenant();
  useTenantNames();

  const load = useCallback(async (current: () => boolean) => {
    try {
      // The judge budget is not per tenant, so a tenant-limited user is refused it; the rest of the view still loads.
      // Findings and corrections are an extra: a server without them, or a role that cannot read them, still
      // gets the rest of the view.
      const [st, se, al, fi, co] = await Promise.all([
        getOnlineStatus().catch((e) => (e instanceof StorageUnavailableError ? Promise.reject(e) : null)),
        getOnlineSeries(hours),
        getOnlineAlerts('all'),
        getOnlineFindings().catch(() => [] as OnlineFinding[]),
        getOnlineCorrections().catch(() => [] as OnlineCorrection[]),
      ]);
      if (!current()) return;
      setStatus(st);
      setSeries(se);
      setAlerts(al);
      setFindings(fi);
      setCorrections(co);
      setError(null);
      setNotConfigured(false);
    } catch (e) {
      if (!current()) return;
      const off = e instanceof StorageUnavailableError;
      setNotConfigured(off);
      setError(off ? null : (e as Error).message);
    }
  }, [hours, tenant]);

  useLiveData('quality', load);

  const scopes = useMemo(() => [...new Set(series.map(scopeOf))].sort(), [series]);
  const active = scope && scopes.includes(scope) ? scope : scopes[0] ?? null;

  const chart = useMemo(() => {
    if (!active) return null;
    const points = series.filter(p => scopeOf(p) === active);
    const buckets = [...new Set(points.map(p => p.bucketMs))].sort((a, b) => a - b);
    const metrics = [...new Set(points.map(p => p.metric))].sort();
    const datasets = metrics.map((m, i) => {
      const byBucket = new Map(points.filter(p => p.metric === m).map(p => [p.bucketMs, p]));
      return {
        label: m,
        data: buckets.map(b => byBucket.get(b)?.mean ?? null),
        counts: buckets.map(b => byBucket.get(b)?.n ?? 0),
        borderColor: SLOTS[i % SLOTS.length],
        backgroundColor: SLOTS[i % SLOTS.length],
        pointRadius: 2,
        spanGaps: true,
        tension: 0.2,
      };
    });
    return { labels: buckets.map(hourLabel), datasets };
  }, [series, active]);

  const counts = status ? Object.entries(status.counts).sort((a, b) => b[1] - a[1]) : [];
  const sampled = counts.filter(([k]) => k.startsWith('sampled_')).reduce((s, [, v]) => s + v, 0);
  const openAlerts = alerts.filter(a => a.status === 'open');
  const shownFindings = findings.filter(f => f.state === findingState);
  // The finding whose details are open: the one picked, else the first listed.
  const chosenFinding = shownFindings.find(f => f.finding_id === openFinding) ?? shownFindings[0];
  const agreement = status?.labels ? Object.entries(status.labels.perMetric).sort((a, b) => a[0].localeCompare(b[0])) : [];

  return (
    <div css={pageStyle}>
      <header css={headerStyle}>
        <PageTitle>
          <h1 css={titleStyle}>quality</h1>
          <p css={subtitleStyle}>
            how sampled live sessions score on the metrics you chose
          </p>
        </PageTitle>
      </header>

      {error && <div css={errorStyle}>{error}</div>}
      {notConfigured && (
        <div css={noteStyle}>
          Online evaluation is off. Start the server with --session-db and --online-config &lt;file&gt; to turn it on;
          a minimal file is in the <a href="https://github.com/triageagent-dev/ego#online-evaluation" target="_blank" rel="noreferrer">README</a>.
        </div>
      )}

      {status && (
        <>
          <div css={tilesStyle}>
            <Tile label="Judge tokens today" value={formatTokens(status.budget.spent.judgeTokens)}
              sub={status.budget.caps.judgeTokensPerDay ? `of ${formatTokens(status.budget.caps.judgeTokensPerDay)} cap` : 'no Gemini judges'} />
            <Tile label="Jev calls today" value={String(status.budget.spent.jevCalls)}
              sub={status.budget.caps.jevCallsPerDay ? `of ${status.budget.caps.jevCallsPerDay} cap` : 'no Jev judges'} />
            <Tile label="Sessions scored" value={(status.sessionsScored24h ?? sampled).toLocaleString()}
              sub={status.sessionsScored24h != null ? 'in the last 24 hours' : 'since the server started'} />
            <Tile label="Open alerts" value={String(openAlerts.length)}
              sub={`watching ${status.profiles.length} profile${status.profiles.length === 1 ? '' : 's'} · ${status.sinks.length ? `alerts go to ${status.sinks.join(', ')}` : 'no alert channel'}`} />
          </div>

          <section css={cardStyle}>
            <h2 css={cardTitleStyle}>Alerts</h2>
            {alerts.length === 0 ? (
              <p css={emptyStyle}>No regressions detected.</p>
            ) : (
              <div css={tableWrapStyle}>
                <table css={tableStyle}>
                  <thead>
                    <tr>
                      <th>Status</th><th>Scope</th><th>Metric</th><th css={numStyle}>Baseline</th><th css={numStyle}>Now</th>
                      <th>Suspect</th><th>Lowest sessions</th><th>Opened</th><th></th>
                    </tr>
                  </thead>
                  <tbody>
                    {alerts.map(a => (
                      <tr key={a.id}>
                        <td><span css={[tagStyle, a.status === 'open' && openTagStyle]}>{a.status}</span></td>
                        <td>{scopeOf(a.key)}</td>
                        <td css={monoStyle}>{a.key.metric}</td>
                        <td css={numStyle}>{a.last.baseMean.toFixed(2)} <span css={dimStyle}>n={a.last.nBase}</span></td>
                        <td css={numStyle}>{a.last.curMean.toFixed(2)} <span css={dimStyle}>n={a.last.nCur}</span></td>
                        <td>{a.last.suspect || '–'}</td>
                        <td css={monoStyle} title={(a.last.worst ?? []).join(', ')}>{(a.last.worst ?? []).join(', ') || '–'}</td>
                        <td>{new Date(a.openedAt).toLocaleString()}</td>
                        <td><a css={runsLinkStyle} href={onlineRunsLink(a.key.tenant, a.key.kind)} title="The hourly runs of this scope, and how each one ran">runs ›</a></td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </section>

          <section css={cardStyle}>
            <h2 css={cardTitleStyle}>Findings</h2>
            <div css={[filterRowStyle, findingFilterStyle]}>
              <div css={segmentStyle} role="group" aria-label="Finding state">
                {FINDING_STATES.map(st => (
                  <button key={st} css={[segBtnStyle, findingState === st && segActiveStyle]} onClick={() => setFindingState(st)}>
                    {st} {findings.filter(f => f.state === st).length}
                  </button>
                ))}
              </div>
            </div>
            {shownFindings.length === 0 ? (
              <p css={emptyStyle}>No {findingState} findings.</p>
            ) : (
              <div css={splitStyle}>
                <div css={findingListStyle}>
                  {shownFindings.map(f => (
                    <button key={f.finding_id} css={[findingCardStyle, chosenFinding?.finding_id === f.finding_id && findingCardOnStyle]}
                      onClick={() => setOpenFinding(f.finding_id)}>
                      <span css={findingHeadStyle}>
                        <span>{scopeOf(f)}</span>
                        <span css={monoStyle}>{f.metric}</span>
                      </span>
                      <span css={findingModeStyle}>{f.diagnosis.failure_mode}</span>
                      <span css={dimStyle}>{f.diagnosis.confidence} confidence · {f.sessions.length} sessions · {new Date(f.created_at).toLocaleDateString()}</span>
                    </button>
                  ))}
                </div>
                {chosenFinding && (
                  <aside css={findingDetailStyle}>
                    <div css={findingHeadStyle}>
                      <span>{scopeOf(chosenFinding)}</span>
                      <span css={monoStyle}>{chosenFinding.metric}</span>
                      <a css={runsLinkStyle} href={onlineRunsLink(chosenFinding.tenant, chosenFinding.kind)} title="The hourly runs of this scope, and how each one ran">runs ›</a>
                    </div>
                    <p>{chosenFinding.diagnosis.summary}</p>
                    {chosenFinding.diagnosis.suggested_correction && (
                      <p><b>Suggested correction.</b> {chosenFinding.diagnosis.suggested_correction}</p>
                    )}
                    <p css={dimStyle}>
                      window mean {chosenFinding.window.mean.toFixed(2)} over {chosenFinding.window.n} sessions
                      {chosenFinding.model ? ` · diagnosed by ${chosenFinding.model}` : ' · written from the evidence alone'}
                      {' · '}{chosenFinding.confirmed ?? 0} checked by a person
                    </p>
                    <div>
                      <b>Evidence sessions.</b>
                      <div css={evidenceListStyle}>
                        {chosenFinding.sessions.map(id => (
                          <a key={id} css={sessionLinkStyle} href={sessionLink(id)} title={chosenFinding.diagnosis.evidence.find(e => e.session_id === id)?.why}>{id}</a>
                        ))}
                      </div>
                    </div>
                    {chosenFinding.history.length > 0 && (
                      <div css={dimStyle}>
                        Tried: {chosenFinding.history.map(h => `${h.what} (${h.state})`).join('; ')}
                      </div>
                    )}
                  </aside>
                )}
              </div>
            )}
          </section>

          <section css={cardStyle}>
            <h2 css={cardTitleStyle}>Corrections</h2>
            {corrections.length === 0 ? (
              <p css={emptyStyle}>No corrections registered.</p>
            ) : (
              <div css={tableWrapStyle}>
                <table css={tableStyle}>
                  <thead>
                    <tr>
                      <th>Kind</th><th>What</th><th>Version</th><th>State</th><th css={numStyle}>Before</th><th css={numStyle}>After</th><th>Created</th>
                    </tr>
                  </thead>
                  <tbody>
                    {corrections.map(c => (
                      <tr key={c.correction_id}>
                        <td>{scopeOf(c)}</td>
                        <td css={wrapCellStyle} title={c.verification.reason}>{c.what}</td>
                        <td css={monoStyle}>{c.version || '–'}</td>
                        <td><span css={[tagStyle, c.state === 'failed' && openTagStyle]}>{c.state}</span></td>
                        <td css={numStyle}>{c.verification.before.mean.toFixed(2)} <span css={dimStyle}>n={c.verification.before.n}</span></td>
                        <td css={numStyle}>{c.verification.after.n > 0 ? c.verification.after.mean.toFixed(2) : '–'} <span css={dimStyle}>n={c.verification.after.n}</span></td>
                        <td>{new Date(c.created_at).toLocaleString()}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </section>

          <section css={cardStyle}>
            <h2 css={cardTitleStyle}>Judge agreement</h2>
            {agreement.length === 0 ? (
              <p css={emptyStyle}>No labels in the last 7 days. Agree or disagree with a score on a session's card.</p>
            ) : (
              <div css={[tilesStyle, agreementTilesStyle]}>
                {agreement.map(([metric, m]) => (
                  <Tile key={metric} label={metric}
                    value={m.agreement === null ? '–' : `${Math.round(m.agreement * 100)}%`}
                    sub={`${m.agree} agree · ${m.disagree} disagree${m.drift !== undefined ? ` · drift ${m.drift.toFixed(2)} (n=${m.driftN ?? 0})` : ''}`}
                    badge={m.gated ? 'gated' : undefined} />
                ))}
              </div>
            )}
          </section>

          <div css={filterRowStyle}>
            <div css={segmentStyle} role="group" aria-label="Time range">
              {RANGES.map(r => (
                <button key={r.hours} css={[segBtnStyle, hours === r.hours && segActiveStyle]} onClick={() => setHours(r.hours)}>
                  {r.label}
                </button>
              ))}
            </div>
            {scopes.length > 0 && (
              <div css={segmentStyle} role="group" aria-label="Call kind">
                {scopes.map(s => (
                  <button key={s} css={[segBtnStyle, active === s && segActiveStyle]} onClick={() => setScope(s)}>
                    {s}
                  </button>
                ))}
              </div>
            )}
          </div>

          <section css={cardStyle}>
            <h2 css={cardTitleStyle}>Mean score per hour{active ? `, ${active}` : ''}</h2>
            {!chart ? (
              <p css={emptyStyle}>No scored sessions in this window.</p>
            ) : (
              <div css={chartBoxStyle}>
                <Line
                  data={chart}
                  options={{
                    responsive: true,
                    maintainAspectRatio: false,
                    animation: false,
                    interaction: { mode: 'index', intersect: false },
                    plugins: {
                      legend: {
                        position: 'top',
                        align: 'start',
                        labels: { color: '#a8b2c1', boxWidth: 12, boxHeight: 12, useBorderRadius: true, borderRadius: 2 },
                      },
                      tooltip: {
                        callbacks: {
                          label: item => {
                            const n = (item.dataset as unknown as { counts: number[] }).counts[item.dataIndex];
                            return ` ${item.dataset.label}: ${(item.raw as number)?.toFixed(2)} (n=${n})`;
                          },
                        },
                      },
                    },
                    scales: {
                      x: {
                        grid: { display: false },
                        ticks: { color: '#6b7586', maxRotation: 0, autoSkipPadding: 16 },
                        border: { color: '#3a4052' },
                      },
                      y: {
                        min: 0,
                        max: 1,
                        grid: { color: 'rgba(58, 64, 82, 0.5)' },
                        border: { display: false },
                        ticks: { color: '#6b7586' },
                      },
                    },
                  }}
                />
              </div>
            )}
          </section>

          <section css={cardStyle}>
            <h2 css={cardTitleStyle}>Sampler decisions since start</h2>
            {counts.length === 0 ? (
              <p css={emptyStyle}>No completed sessions seen yet.</p>
            ) : (
              <div css={chipRowStyle}>
                {counts.map(([k, v]) => (
                  <span key={k} css={chipStyle}>{k.replace(/_/g, ' ')} <b>{v.toLocaleString()}</b></span>
                ))}
              </div>
            )}
          </section>
        </>
      )}
    </div>
  );
};

const Tile: React.FC<{ label: string; value: string; sub: string; badge?: string }> = ({ label, value, sub, badge }) => (
  <div css={tileStyle}>
    <div css={tileLabelStyle}>{label}</div>
    <div css={tileValueStyle}>{value} {badge && <span css={[tagStyle, openTagStyle]} title="People disagree with this judge too often for it to write findings">{badge}</span>}</div>
    <div css={tileSubStyle}>{sub}</div>
  </div>
);

const chartBoxStyle = css`
  height: 240px;
  position: relative;
`;

const emptyStyle = css`
  color: var(--text-tertiary);
  font-size: 0.875rem;
  margin: 8px 0;
`;

const tableWrapStyle = css`
  overflow-x: auto;
`;

const tableStyle = css`
  width: 100%;
  border-collapse: collapse;
  font-size: 0.82rem;
  th {
    text-align: left;
    font-weight: 600;
    font-size: 0.72rem;
    color: var(--text-tertiary);
    text-transform: uppercase;
    letter-spacing: 0.03em;
    padding: 8px 10px;
    border-bottom: 1px solid var(--border-default);
    white-space: nowrap;
  }
  td {
    padding: 8px 10px;
    border-bottom: 1px solid rgba(58, 64, 82, 0.5);
    white-space: nowrap;
  }
  tbody tr:hover { background: var(--bg-elevated); }
  tbody tr:last-child td { border-bottom: none; }
`;

const numStyle = css`
  text-align: right !important;
  font-family: var(--font-mono);
  font-variant-numeric: tabular-nums;
`;

const monoStyle = css`
  font-family: var(--font-mono);
  font-size: 0.78rem;
  color: var(--text-secondary);
  max-width: 260px;
  overflow: hidden;
  text-overflow: ellipsis;
`;

const dimStyle = css`
  color: var(--text-tertiary);
  font-size: 0.72rem;
`;

const tagStyle = css`
  font-size: 0.7rem;
  padding: 1px 6px;
  border-radius: var(--radius);
  border: 1px solid var(--border-default);
  color: var(--text-secondary);
`;

const openTagStyle = css`
  border-color: var(--status-failure);
  color: var(--text-primary);
`;

const chipRowStyle = css`
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
`;

const chipStyle = css`
  font-size: 0.78rem;
  padding: 4px 10px;
  border-radius: var(--radius);
  background: var(--bg-elevated);
  color: var(--text-secondary);
  b { color: var(--text-primary); font-family: var(--font-mono); font-weight: 500; margin-left: 4px; }
`;

const findingFilterStyle = css`
  margin-bottom: 6px;
`;

const wrapCellStyle = css`
  white-space: normal !important;
  min-width: 220px;
  max-width: 480px;
`;

const sessionLinkStyle = css`
  font-family: var(--font-mono);
  font-size: 0.78rem;
  margin-right: 10px;
  white-space: nowrap;
`;

// The agreement tiles sit inside their card, which already has the border.
const agreementTilesStyle = css`
  border: none;
  margin-bottom: 0;
`;

const runsLinkStyle = css`
  color: var(--accent-primary);
  white-space: nowrap;
  text-decoration: none;

  &:hover {
    text-decoration: underline;
  }
`;

// Findings are a list and the details of the open one, as on the sessions page: a long diagnosis does not push
// the list off the screen.
const splitStyle = css`
  display: flex;
  align-items: flex-start;
  gap: 14px;
`;

const findingListStyle = css`
  flex: 0 0 420px;
  min-width: 0;
  max-height: 520px;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: 6px;
`;

const findingCardStyle = css`
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 8px 12px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius);
  background: var(--card-bg);
  color: inherit;
  font: inherit;
  font-size: 12.5px;
  text-align: left;
  cursor: pointer;

  &:hover {
    background: var(--bg-elevated);
  }
`;

const findingCardOnStyle = css`
  background: var(--bg-elevated);
  border-color: var(--accent-primary);
`;

const findingHeadStyle = css`
  display: flex;
  align-items: baseline;
  flex-wrap: wrap;
  gap: 4px 12px;
  color: var(--text-primary);
`;

const findingModeStyle = css`
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
  color: var(--text-secondary);
  line-height: 1.4;
`;

const findingDetailStyle = css`
  flex: 1;
  min-width: 360px;
  max-height: 520px;
  overflow-y: auto;
  padding: 12px 14px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius);
  background: var(--card-bg);
  font-size: 13px;
  line-height: 1.5;

  p {
    margin: 0 0 10px;
  }
`;

const evidenceListStyle = css`
  display: flex;
  flex-direction: column;
  gap: 2px;
  margin-top: 4px;
`;
