import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { css } from '@emotion/react';
import { pageStyle, headerStyle, titleStyle, subtitleStyle, refreshStyle, filterRowStyle, segmentStyle, segBtnStyle, segActiveStyle, errorStyle, noteStyle, tilesStyle, tileStyle, tileLabelStyle, tileValueStyle, tileSubStyle, cardStyle, cardTitleStyle } from '../ui/page';
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
import { RefreshCw } from 'lucide-react';
import { getOnlineAlerts, getOnlineCorrections, getOnlineFindings, getOnlineSeries, getOnlineStatus, StorageUnavailableError } from '../../api/client';
import { sessionLink } from '../shell/routes';
import { useSelectedTenant } from '../../lib/tenant';
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
const REFRESH_MS = 60_000;

const FINDING_STATES: OnlineFindingState[] = ['open', 'corrected', 'resolved', 'stale'];

function scopeOf(p: { tenant: string; kind: string }): string {
  return p.tenant ? `${p.tenant} · ${p.kind}` : p.kind;
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
  const [loading, setLoading] = useState(false);
  const tenant = useSelectedTenant();

  const load = useCallback(async () => {
    setLoading(true);
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
      setStatus(st);
      setSeries(se);
      setAlerts(al);
      setFindings(fi);
      setCorrections(co);
      setError(null);
      setNotConfigured(false);
    } catch (e) {
      const off = e instanceof StorageUnavailableError;
      setNotConfigured(off);
      setError(off ? null : (e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [hours, tenant]);

  useEffect(() => {
    load();
    const t = setInterval(load, REFRESH_MS);
    return () => clearInterval(t);
  }, [load]);

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
  const agreement = status?.labels ? Object.entries(status.labels.perMetric).sort((a, b) => a[0].localeCompare(b[0])) : [];

  return (
    <div css={pageStyle}>
      <header css={headerStyle}>
        <PageTitle>
          <h1 css={titleStyle}>quality</h1>
          <p css={subtitleStyle}>
            online evaluation of sampled live sessions · hourly scores are also runs
          </p>
        </PageTitle>
        <button css={refreshStyle} onClick={load} disabled={loading} title="Refresh">
          <RefreshCw size={14} css={loading ? spinStyle : undefined} /> Refresh
        </button>
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
                      <th>Suspect</th><th>Lowest sessions</th><th>Opened</th>
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
              <div css={tableWrapStyle}>
                <table css={tableStyle}>
                  <thead>
                    <tr>
                      <th>Kind</th><th>Metric</th><th>Failure mode</th><th>Confidence</th><th>Created</th><th css={numStyle}>Sessions</th><th css={numStyle} title="evidence sessions a person checked">Checked</th>
                    </tr>
                  </thead>
                  <tbody>
                    {shownFindings.map(f => (
                      <React.Fragment key={f.finding_id}>
                        <tr css={clickRowStyle} onClick={() => setOpenFinding(openFinding === f.finding_id ? null : f.finding_id)}>
                          <td>{scopeOf(f)}</td>
                          <td css={monoStyle}>{f.metric}</td>
                          <td css={wrapCellStyle}>{f.diagnosis.failure_mode}</td>
                          <td>{f.diagnosis.confidence}</td>
                          <td>{new Date(f.created_at).toLocaleString()}</td>
                          <td css={numStyle}>{f.sessions.length}</td>
                          <td css={numStyle}>{f.confirmed ?? 0}</td>
                        </tr>
                        {openFinding === f.finding_id && (
                          <tr>
                            <td colSpan={7} css={detailStyle}>
                              <p>{f.diagnosis.summary}</p>
                              {f.diagnosis.suggested_correction && (
                                <p><b>Suggested correction.</b> {f.diagnosis.suggested_correction}</p>
                              )}
                              <p css={dimStyle}>
                                window mean {f.window.mean.toFixed(2)} over {f.window.n} sessions
                                {f.model ? ` · diagnosed by ${f.model}` : ' · written from the evidence alone'}
                              </p>
                              <div>
                                <b>Evidence sessions.</b>{' '}
                                {f.sessions.map(id => (
                                  <a key={id} css={sessionLinkStyle} href={sessionLink(id)} title={f.diagnosis.evidence.find(e => e.session_id === id)?.why}>{id}</a>
                                ))}
                              </div>
                              {f.history.length > 0 && (
                                <div css={dimStyle}>
                                  Tried: {f.history.map(h => `${h.what} (${h.state})`).join('; ')}
                                </div>
                              )}
                            </td>
                          </tr>
                        )}
                      </React.Fragment>
                    ))}
                  </tbody>
                </table>
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

const spinStyle = css`
  animation: quality-spin 1s linear infinite;
  @keyframes quality-spin { to { transform: rotate(360deg); } }
`;

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

const clickRowStyle = css`
  cursor: pointer;
`;

const wrapCellStyle = css`
  white-space: normal !important;
  min-width: 220px;
  max-width: 480px;
`;

const detailStyle = css`
  white-space: normal !important;
  background: var(--bg-elevated);
  p { margin: 0 0 6px; }
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
