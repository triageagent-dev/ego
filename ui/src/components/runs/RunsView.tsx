import React, { useCallback, useMemo, useState } from 'react';
import { css } from '@emotion/react';
import { pageStyle, headerStyle, titleStyle, subtitleStyle, tilesStyle, tileStyle, tileLabelStyle, tileValueStyle } from '../ui/page';
import { Segmented, Select } from 'antd';
import { History, AlertCircle } from 'lucide-react';
import type { Run } from '../../lib/types';
import { listRuns, StorageUnavailableError } from '../../api/client';
import { useLiveData } from '../../lib/connection';
import {
  groupRuns,
  metricNamesAcross,
  passRate,
  sortByCreatedAsc,
  type GroupBy,
} from './runHistory';
import { PassRateTrendChart } from './PassRateTrendChart';
import { PerMetricTrendChart } from './PerMetricTrendChart';
import { RunsHistoryTable } from './RunsHistoryTable';
import { RunDetailView } from './RunDetailView';
import { PageTitle } from '../shell/PageTitle';

const ALL_GROUPS = '__all__';

export const RunsView: React.FC = () => {
  const [runs, setRuns] = useState<Run[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [storageUnavailable, setStorageUnavailable] = useState(false);
  const [groupBy, setGroupBy] = useState<GroupBy>('evalSet');
  const [selectedGroup, setSelectedGroup] = useState<string>(ALL_GROUPS);
  const [detailRunId, setDetailRunId] = useState<string | null>(null);

  // Group keys differ per dimension, so reset the picker to "all" when the
  // grouping dimension changes to avoid pointing at a now-nonexistent group.
  const changeGroupBy = (next: GroupBy) => {
    setGroupBy(next);
    setSelectedGroup(ALL_GROUPS);
  };

  const load = useCallback(async () => {
    setError(null);
    setStorageUnavailable(false);
    try {
      const fetched = await listRuns({ limit: 200 });
      setRuns(fetched);
    } catch (err) {
      if (err instanceof StorageUnavailableError) {
        setStorageUnavailable(true);
      } else {
        setError(err instanceof Error ? err.message : 'Failed to load runs');
      }
    } finally {
      setLoading(false);
    }
  }, []);

  useLiveData('runs', load);

  const groups = useMemo(() => groupRuns(runs, groupBy), [runs, groupBy]);

  const selectedRuns = useMemo(() => {
    if (selectedGroup === ALL_GROUPS) return runs;
    return groups.find(g => g.group.key === selectedGroup)?.runs ?? [];
  }, [runs, groups, selectedGroup]);

  const trendRuns = useMemo(() => sortByCreatedAsc(selectedRuns), [selectedRuns]);

  const stats = useMemo(() => {
    const rates = trendRuns.map(passRate).filter((r): r is number => r !== null);
    const latest = rates.length ? rates[rates.length - 1] : null;
    const avg = rates.length ? rates.reduce((a, b) => a + b, 0) / rates.length : null;
    return {
      total: selectedRuns.length,
      latest,
      avg,
      metrics: metricNamesAcross(selectedRuns).length,
    };
  }, [selectedRuns, trendRuns]);

  if (detailRunId) {
    return <RunDetailView runId={detailRunId} onBack={() => setDetailRunId(null)} />;
  }

  return (
    <div css={pageStyle}>
      <header css={headerStyle}>
        <PageTitle>
          <h1 css={titleStyle}>runs</h1>
          <p css={subtitleStyle}>every evaluation, newest first</p>
        </PageTitle>
        <div css={controlsStyle}>
          {runs.length > 0 && (
            <Segmented<GroupBy>
              value={groupBy}
              onChange={changeGroupBy}
              options={[
                { value: 'evalSet', label: 'Eval set' },
                { value: 'agent', label: 'Agent' },
              ]}
            />
          )}
          {groups.length > 0 && (
            <Select
              value={selectedGroup}
              onChange={setSelectedGroup}
              css={selectStyle}
              popupMatchSelectWidth={false}
              options={[
                {
                  value: ALL_GROUPS,
                  label: `All ${groupBy === 'agent' ? 'agents' : 'eval sets'} (${runs.length})`,
                },
                ...groups.map(g => ({
                  value: g.group.key,
                  label: `${g.group.label} (${g.runs.length})`,
                })),
              ]}
            />
          )}
        </div>
      </header>

      {storageUnavailable && (
        <div css={noticeStyle}>
          <AlertCircle size={18} />
          <span>
            Runs are unavailable: the run store is not configured or failed to open at server
            startup. Set <code>--session-db</code> (<code>EGO_SESSION_DB_PATH</code>) and check the
            server logs.
          </span>
        </div>
      )}

      {error && (
        <div css={[noticeStyle, errorNoticeStyle]}>
          <AlertCircle size={18} />
          <span>{error}</span>
        </div>
      )}

      {loading && <p css={mutedTextStyle}>Loading runs...</p>}

      {!loading && !storageUnavailable && !error && runs.length === 0 && (
        <div css={emptyStateStyle}>
          <History size={32} />
          <p>No evaluation runs recorded yet.</p>
          <span>Run an evaluation and it will appear here, tracked over time.</span>
        </div>
      )}

      {!loading && !storageUnavailable && runs.length > 0 && (
        <>
          <div css={tilesStyle}>
            <StatCard label="Runs" value={String(stats.total)} />
            <StatCard
              label="Latest pass rate"
              value={stats.latest === null ? '-' : `${Math.round(stats.latest * 100)}%`}
              accent={stats.latest !== null && stats.latest < 0.5 ? 'failure' : 'success'}
            />
            <StatCard
              label="Avg pass rate"
              value={stats.avg === null ? '-' : `${Math.round(stats.avg * 100)}%`}
            />
            <StatCard label="Metrics tracked" value={String(stats.metrics)} />
          </div>

          <div css={chartsGridStyle}>
            <PassRateTrendChart runs={trendRuns} />
            <PerMetricTrendChart runs={trendRuns} />
          </div>

          <RunsHistoryTable runs={selectedRuns} onSelectRun={setDetailRunId} />
        </>
      )}
    </div>
  );
};

interface StatCardProps {
  label: string;
  value: string;
  accent?: 'success' | 'failure';
}

const StatCard: React.FC<StatCardProps> = ({ label, value, accent }) => {
  const color =
    accent === 'failure'
      ? 'var(--status-failure)'
      : accent === 'success'
        ? 'var(--status-success)'
        : 'var(--accent-primary)';
  return (
    <div css={tileStyle}>
      <div css={tileLabelStyle}>{label}</div>
      <div css={tileValueStyle} style={{ color }}>
        {value}
      </div>
    </div>
  );
};




const controlsStyle = css`
  display: flex;
  align-items: center;
  gap: 12px;
`;

const selectStyle = css`
  min-width: 200px;
`;


const noticeStyle = css`
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 14px 18px;
  border-radius: var(--radius);
  margin-bottom: 24px;
  background: linear-gradient(135deg, rgba(59, 130, 246, 0.08) 0%, rgba(139, 92, 246, 0.08) 100%);
  border: 1px solid var(--border-default);
  color: var(--text-secondary);
  font-size: 0.875rem;

  code {
    font-family: var(--font-mono);
    font-size: 0.8125rem;
    color: var(--accent-primary);
    background: var(--bg-elevated);
    padding: 1px 6px;
    border-radius: var(--radius);
  }
`;

const errorNoticeStyle = css`
  background: rgba(255, 87, 87, 0.08);
  color: var(--status-failure);
`;

const mutedTextStyle = css`
  color: var(--text-tertiary);
  font-size: 0.9rem;
`;

const emptyStateStyle = css`
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  padding: 80px 20px;
  color: var(--text-tertiary);
  text-align: center;

  p {
    margin: 8px 0 0;
    font-size: 1.05rem;
    color: var(--text-secondary);
    font-weight: 600;
  }

  span {
    font-size: 0.875rem;
  }
`;





// minmax(0, ...) and min-width: 0: a chart canvas keeps the width it had, and with plain 1fr columns that pushed
// the page wider than the table below it.
const chartsGridStyle = css`
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 16px;
  margin-bottom: 12px;

  > * {
    min-width: 0;
  }

  @media (max-width: 1100px) {
    grid-template-columns: minmax(0, 1fr);
  }
`;
