import React, { useEffect, useMemo, useState } from 'react';
import { css } from '@emotion/react';
import { Line } from 'react-chartjs-2';
import { Chart as ChartJS, CategoryScale, Legend, LinearScale, LineElement, PointElement, Tooltip } from 'chart.js';
import { getAdminUsage } from '../../api/client';
import { store, stored } from '../../lib/stored';
import type { AdminTenant, UsageSeries } from '../../lib/types';
import { ChartBox } from '../ui/ChartBox';
import { cardStyle, cardTitleStyle, segActiveStyle, segBtnStyle, segmentStyle } from '../ui/page';
import { KIND_SLOTS, OTHER_COLOR } from '../usage/UsageView';

ChartJS.register(CategoryScale, LinearScale, LineElement, PointElement, Tooltip, Legend);

type Metric = 'calls' | 'inputTokens' | 'outputTokens' | 'errors';

const METRICS: { key: Metric; label: string }[] = [
  { key: 'calls', label: 'calls' },
  { key: 'inputTokens', label: 'tokens in' },
  { key: 'outputTokens', label: 'tokens out' },
  { key: 'errors', label: 'errors' },
];

const RANGES: { label: string; hours: number }[] = [
  { label: '24h', hours: 24 },
  { label: '7d', hours: 24 * 7 },
  { label: '30d', hours: 24 * 30 },
];

// Lines drawn by name; the tenants past this many are summed into one grey line, as the usage page does. The names are
// listed under the chart, so a long list takes no room from the plot.
const NAMED_LINES = 12;
const OTHER = 'Other';

function short(n: number): string {
  if (n >= 1e9) return `${(n / 1e9).toFixed(1)}B`;
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
  if (n >= 1e3) return `${(n / 1e3).toFixed(1)}k`;
  return String(Math.round(n));
}

const pad = (n: number) => String(n).padStart(2, '0');

/** How the tenants trend: one line per tenant over the window, for the figure picked. It follows the table's filters. */
export const TenantTrends: React.FC<{ tenants: AdminTenant[] }> = ({ tenants }) => {
  // The figure and the range stay as they were left in this browser; the first visit shows the last 24 hours.
  const [metric, setMetricState] = useState<Metric>(() => {
    const saved = stored('ego.admin.trend.metric');
    return METRICS.find((m) => m.key === saved)?.key ?? 'calls';
  });
  const [hours, setHoursState] = useState(() => {
    const saved = Number(stored('ego.admin.trend.hours'));
    return RANGES.find((r) => r.hours === saved)?.hours ?? 24;
  });
  const setMetric = (m: Metric) => {
    setMetricState(m);
    store('ego.admin.trend.metric', m);
  };
  const setHours = (h: number) => {
    setHoursState(h);
    store('ego.admin.trend.hours', String(h));
  };
  const [series, setSeries] = useState<UsageSeries | null>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    let cancelled = false;
    setSeries(null);
    getAdminUsage(hours)
      .then((s) => !cancelled && (setSeries(s), setError('')))
      .catch((e) => !cancelled && setError(e instanceof Error ? e.message : String(e)));
    return () => {
      cancelled = true;
    };
  }, [hours]);

  const shown = useMemo(() => new Map(tenants.map((t) => [t.key, t.name || t.label])), [tenants]);

  const chart = useMemo(() => {
    if (!series) return null;
    const width = series.bucketMs;
    const first = Math.floor(series.from / width) * width;
    const starts: number[] = [];
    for (let t = first; t <= series.to; t += width) starts.push(t);
    const perTenant = new Map<string, number[]>();
    for (const b of series.buckets) {
      if (!shown.has(b.tenant)) continue;
      const i = Math.floor((b.start - first) / width);
      if (i < 0 || i >= starts.length) continue;
      const row = perTenant.get(b.tenant) ?? new Array<number>(starts.length).fill(0);
      row[i] += b[metric];
      perTenant.set(b.tenant, row);
    }
    const ranked = [...perTenant.entries()]
      .map(([key, row]) => ({ key, row, total: row.reduce((a, b) => a + b, 0) }))
      .filter((r) => r.total > 0)
      .sort((a, b) => b.total - a.total);
    const named = ranked.slice(0, NAMED_LINES);
    const rest = ranked.slice(NAMED_LINES);
    const datasets = named.map((r, i) => ({
      label: shown.get(r.key) ?? r.key,
      data: r.row,
      borderColor: KIND_SLOTS[i % KIND_SLOTS.length],
      backgroundColor: KIND_SLOTS[i % KIND_SLOTS.length],
      borderWidth: 2,
      pointRadius: 0,
      pointHoverRadius: 4,
      tension: 0.2,
    }));
    if (rest.length > 0) {
      datasets.push({
        label: `${OTHER} (${rest.length})`,
        data: starts.map((_, i) => rest.reduce((a, r) => a + r.row[i], 0)),
        borderColor: OTHER_COLOR,
        backgroundColor: OTHER_COLOR,
        borderWidth: 2,
        pointRadius: 0,
        pointHoverRadius: 4,
        tension: 0.2,
      });
    }
    const labels = starts.map((t) => {
      const d = new Date(t);
      return width >= 24 * 3600 * 1000 ? `${pad(d.getMonth() + 1)}-${pad(d.getDate())}` : `${pad(d.getHours())}:00`;
    });
    return { labels, datasets, hourly: width < 24 * 3600 * 1000 };
  }, [series, shown, metric]);

  const label = METRICS.find((m) => m.key === metric)!.label;
  return (
    <section css={cardStyle}>
      <div css={headStyle}>
        <h2 css={cardTitleStyle}>{label} per {chart?.hourly ? 'hour' : 'day'}, by tenant</h2>
        <div css={controlsStyle}>
          <div css={segmentStyle} role="group" aria-label="Figure">
            {METRICS.map((m) => (
              <button key={m.key} css={[segBtnStyle, metric === m.key && segActiveStyle]} onClick={() => setMetric(m.key)}>
                {m.label}
              </button>
            ))}
          </div>
          <div css={segmentStyle} role="group" aria-label="Time range">
            {RANGES.map((r) => (
              <button key={r.hours} css={[segBtnStyle, hours === r.hours && segActiveStyle]} onClick={() => setHours(r.hours)}>
                {r.label}
              </button>
            ))}
          </div>
        </div>
      </div>
      {error && <p css={noteStyle}>{error}</p>}
      {!error && chart === null && <p css={noteStyle}>Loading…</p>}
      {chart && chart.datasets.length === 0 && <p css={noteStyle}>No {label} in this window for the tenants shown.</p>}
      {chart && chart.datasets.length > 0 && (
        <ChartBox css={boxStyle}>
          <Line
            data={{ labels: chart.labels, datasets: chart.datasets }}
            options={{
              animation: false,
              interaction: { mode: 'index', intersect: false },
              plugins: {
                legend: { display: false },
                tooltip: {
                  filter: (item) => (item.raw as number) > 0,
                  itemSort: (a, b) => (b.raw as number) - (a.raw as number),
                  callbacks: { label: (item) => ` ${item.dataset.label}: ${short(item.raw as number)}` },
                },
              },
              scales: {
                x: { grid: { display: false }, ticks: { color: '#6b7586', maxRotation: 0, autoSkipPadding: 16 }, border: { color: '#3a4052' } },
                y: { beginAtZero: true, grid: { color: 'rgba(58, 64, 82, 0.5)' }, border: { display: false }, ticks: { color: '#6b7586', callback: (v) => short(Number(v)) } },
              },
            }}
          />
        </ChartBox>
      )}
      {chart && chart.datasets.length > 0 && (
        <ul css={legendStyle}>
          {chart.datasets.map((d) => (
            <li key={d.label} css={legendItemStyle} title={d.label}>
              <span css={swatchStyle} style={{ background: d.borderColor }} />
              <span css={legendNameStyle}>{d.label}</span>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
};

const headStyle = css`
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 8px 16px;
  margin-bottom: 4px;
`;

const controlsStyle = css`
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
`;

const boxStyle = css`
  height: 240px;
  position: relative;
`;

const noteStyle = css`
  margin: 8px 0;
  color: var(--text-muted);
  font-size: 12px;
`;

const legendStyle = css`
  display: flex;
  flex-wrap: wrap;
  gap: 4px 18px;
  max-height: 96px;
  margin: 8px 0 0;
  padding: 0;
  overflow-y: auto;
  list-style: none;
`;

const legendItemStyle = css`
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  max-width: 240px;
  color: #a8b2c1;
  font-size: 12px;
`;

const swatchStyle = css`
  flex: none;
  width: 11px;
  height: 11px;
  border-radius: 2px;
`;

const legendNameStyle = css`
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
`;
