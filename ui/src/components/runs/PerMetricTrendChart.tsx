import React from 'react';
import { css } from '@emotion/react';
import { Line } from 'react-chartjs-2';
import {
  Chart as ChartJS,
  CategoryScale,
  LinearScale,
  PointElement,
  LineElement,
  Tooltip,
  Legend,
} from 'chart.js';
import type { Run } from '../../lib/types';
import { CHART_COLORS, METRIC_PALETTE, formatTimestamp, metricNamesAcross } from './runHistory';

ChartJS.register(CategoryScale, LinearScale, PointElement, LineElement, Tooltip, Legend);

interface PerMetricTrendChartProps {
  runs: Run[];
}

export const PerMetricTrendChart: React.FC<PerMetricTrendChartProps> = ({ runs }) => {
  const metricNames = metricNamesAcross(runs);

  if (metricNames.length === 0) {
    return (
      <div css={cardStyle}>
        <h3>Per-metric score trend</h3>
        <p css={emptyStyle}>
          No per-metric scores recorded. Runs created before this feature won't have them.
        </p>
      </div>
    );
  }

  const labels = runs.map(run => formatTimestamp(run.createdAt));

  const datasets = metricNames.map((name, index) => {
    const color = METRIC_PALETTE[index % METRIC_PALETTE.length];
    return {
      label: name,
      // null where this metric is absent or produced no numeric score in a run,
      // so the line breaks instead of implying a real drop to zero.
      data: runs.map(run => {
        const score = run.summary?.per_metric?.[name]?.avg_score;
        return score == null ? null : Math.round(score * 1000) / 1000;
      }),
      borderColor: color,
      backgroundColor: color,
      spanGaps: false,
      tension: 0.25,
      pointRadius: 3,
      pointHoverRadius: 5,
      borderWidth: 2,
    };
  });

  const options = {
    responsive: true,
    maintainAspectRatio: false,
    interaction: { mode: 'index' as const, intersect: false },
    plugins: {
      legend: {
        position: 'bottom' as const,
        labels: { color: CHART_COLORS.text, font: { size: 12 }, padding: 14, usePointStyle: true },
      },
      tooltip: {
        backgroundColor: 'rgba(0, 0, 0, 0.9)',
        titleColor: '#fff',
        bodyColor: '#fff',
        padding: 12,
        cornerRadius: 6,
      },
    },
    scales: {
      y: {
        min: 0,
        max: 1,
        ticks: { color: CHART_COLORS.text, font: { size: 12 } },
        grid: { color: CHART_COLORS.grid },
        title: { display: true, text: 'Avg score', color: CHART_COLORS.text, font: { size: 12 } },
      },
      x: {
        ticks: { color: CHART_COLORS.text, font: { size: 11 }, maxRotation: 0, autoSkip: true },
        grid: { color: CHART_COLORS.grid },
      },
    },
  };

  return (
    <div css={cardStyle}>
      <h3>Per-metric score trend</h3>
      <div css={chartWrapperStyle}>
        <Line data={{ labels, datasets }} options={options} />
      </div>
    </div>
  );
};

const cardStyle = css`
  background: var(--bg-surface);
  border: 1px solid var(--border-default);
  border-radius: 8px;
  padding: 20px;

  h3 {
    margin: 0 0 16px 0;
    font-size: 1rem;
    font-weight: 600;
    color: var(--text-primary);
  }
`;

const chartWrapperStyle = css`
  height: 300px;
  position: relative;
`;

const emptyStyle = css`
  color: var(--text-tertiary);
  font-size: 0.875rem;
  margin: 0;
`;
