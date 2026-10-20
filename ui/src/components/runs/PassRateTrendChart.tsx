import React from 'react';
import { css } from '@emotion/react';
import { Line } from 'react-chartjs-2';
import {
  Chart as ChartJS,
  CategoryScale,
  LinearScale,
  PointElement,
  LineElement,
  Filler,
  Tooltip,
  Legend,
} from 'chart.js';
import type { ChartOptions, TooltipItem } from 'chart.js';
import type { Run } from '../../lib/types';
import {
  CHART_COLORS,
  METRIC_PALETTE,
  agentNamesAcross,
  formatTimestamp,
  passRate,
  runAgents,
} from './runHistory';

ChartJS.register(CategoryScale, LinearScale, PointElement, LineElement, Filler, Tooltip, Legend);

interface PassRateTrendChartProps {
  runs: Run[];
}

const toPercent = (rate: number) => Math.round(rate * 1000) / 10;

export const PassRateTrendChart: React.FC<PassRateTrendChartProps> = ({ runs }) => {
  const hasDecided = runs.some(run => passRate(run) !== null);
  if (!hasDecided) {
    return (
      <div css={cardStyle}>
        <h3>Pass rate over time</h3>
        <p css={emptyStyle}>No decided (pass/fail) results yet.</p>
      </div>
    );
  }

  const agentNames = agentNamesAcross(runs);
  const labels = runs.map(run => formatTimestamp(run.createdAt));

  // One line per agent (service.name) on a shared run-ordered time axis; a run
  // contributes a point only to the agent(s) it ran, null elsewhere so lines
  // break across gaps. Runs with no agent identity fall back to one aggregate
  // line so older runs still chart.
  const datasets = agentNames.length
    ? agentNames.map((name, index) => {
        const color = METRIC_PALETTE[index % METRIC_PALETTE.length];
        return {
          label: name,
          data: runs.map(run => {
            const rate = passRate(run);
            return runAgents(run).includes(name) && rate !== null ? toPercent(rate) : null;
          }),
          borderColor: color,
          backgroundColor: color,
          spanGaps: false,
          tension: 0.25,
          pointRadius: 3,
          pointHoverRadius: 5,
          borderWidth: 2,
        };
      })
    : [
        {
          label: 'Pass rate',
          data: runs.map(run => {
            const rate = passRate(run);
            return rate === null ? null : toPercent(rate);
          }),
          borderColor: CHART_COLORS.passRate,
          backgroundColor: CHART_COLORS.passRateFill,
          fill: true,
          spanGaps: false,
          tension: 0.25,
          pointRadius: 3,
          pointHoverRadius: 5,
          borderWidth: 2,
        },
      ];

  const multiAgent = agentNames.length > 0;

  const options: ChartOptions<'line'> = {
    responsive: true,
    maintainAspectRatio: false,
    interaction: { mode: 'index' as const, intersect: false },
    plugins: {
      legend: {
        display: multiAgent,
        position: 'bottom' as const,
        labels: { color: CHART_COLORS.text, font: { size: 12 }, padding: 14, usePointStyle: true },
      },
      tooltip: {
        backgroundColor: 'rgba(0, 0, 0, 0.9)',
        titleColor: '#fff',
        bodyColor: '#fff',
        padding: 12,
        cornerRadius: 6,
        callbacks: {
          label: (ctx: TooltipItem<'line'>) => `${ctx.dataset.label}: ${ctx.parsed.y}%`,
        },
      },
    },
    scales: {
      y: {
        min: 0,
        max: 100,
        ticks: {
          color: CHART_COLORS.text,
          font: { size: 12 },
          callback: (value: string | number) => `${value}%`,
        },
        grid: { color: CHART_COLORS.grid },
      },
      x: {
        ticks: { color: CHART_COLORS.text, font: { size: 11 }, maxRotation: 0, autoSkip: true },
        grid: { color: CHART_COLORS.grid },
      },
    },
  };

  return (
    <div css={cardStyle}>
      <h3>Pass rate over time</h3>
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
