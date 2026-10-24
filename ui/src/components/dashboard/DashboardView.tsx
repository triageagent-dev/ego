import React, { useState } from 'react';
import { css } from '@emotion/react';
import { Button } from 'antd';
import { Loader2 } from 'lucide-react';
import { TraceTable } from './TraceTable';
import { SummaryStats } from './SummaryStats';
import { PerformanceCharts } from './PerformanceCharts';
import { useTraceContext } from '../../context/TraceContext';
import { PageTitle } from '../shell/PageTitle';
import { titleStyle } from '../ui/page';

const dashboardStyle = css`
  max-width: 2400px;
  margin: 0 auto;
  padding: 24px;

  .header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 32px;
  }

  .title {
    font-size: 2rem;
    font-weight: 600;
    color: var(--text-primary);
  }

  .filters {
    display: flex;
    gap: 16px;
    margin-bottom: 24px;
    padding: 16px;
    background-color: var(--bg-surface);
    border: 1px solid var(--border-default);
    border-radius: var(--radius);
  }

  .results-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(400px, 1fr));
    gap: 24px;
  }

  .empty-state {
    text-align: center;
    padding: 64px 24px;
    color: var(--text-secondary);
  }

  .empty-title {
    font-size: 1.5rem;
    margin-bottom: 12px;
  }

  .errors {
    background-color: rgba(255, 87, 87, 0.1);
    border: 1px solid var(--status-failure);
    border-radius: var(--radius);
    padding: 16px;
    margin-bottom: 24px;
  }

  .error-title {
    color: var(--status-failure);
    font-weight: 600;
    margin-bottom: 8px;
  }

  .error-list {
    list-style: none;
    padding-left: 0;
  }

  .error-item {
    color: var(--text-secondary);
    font-size: 14px;
    margin-bottom: 4px;
  }

  .progress-banner {
    padding: 16px;
    background: var(--bg-elevated);
    border: 1px solid var(--accent-primary);
    border-radius: var(--radius);
    margin-bottom: 24px;
    display: flex;
    align-items: center;
    gap: 12px;
  }

  .progress-text {
    color: var(--text-primary);
    font-weight: 600;
    flex: 1;
    min-width: 0;
    overflow-wrap: anywhere;
  }

  @keyframes spin {
    from {
      transform: rotate(0deg);
    }
    to {
      transform: rotate(360deg);
    }
  }

  .animate-spin {
    animation: spin 1s linear infinite;
  }

  @media (max-width: 968px) {
    .results-grid {
      grid-template-columns: 1fr;
    }

    .filters {
      flex-direction: column;
    }
  }
`;

export const DashboardView: React.FC = () => {
  const { state, actions } = useTraceContext();
  const [hoveredTraceId, setHoveredTraceId] = useState<string | null>(null);

  const handleTraceClick = (traceId: string) => {
    actions.selectTrace(traceId);
    actions.setCurrentView('inspector');
  };

  const handleRowHover = (traceId: string | null) => {
    setHoveredTraceId(traceId);
  };

  // The golden/reference trace is plotted in the performance charts (as a
  // baseline) but excluded from pass/fail scoring - summary stats and the
  // results table. Charts get the full set plus the golden id for labeling.
  const goldenTraceId = state.goldenTraceId;
  const scoredResults = goldenTraceId
    ? state.results.filter((r) => r.traceId !== goldenTraceId)
    : state.results;
  const scoredRows = Array.from(state.tableRows.values()).filter(
    (r) => !goldenTraceId || r.traceId !== goldenTraceId,
  );

  return (
    <div css={dashboardStyle}>
      <PageTitle className="header">
        <h1 css={titleStyle}>evaluation results</h1>
      </PageTitle>

      {state.errors.length > 0 && (
        <div className="errors">
          <div className="error-title">Errors</div>
          <ul className="error-list">
            {state.errors.map((error, idx) => (
              <li key={idx} className="error-item">
                • {error}
              </li>
            ))}
          </ul>
        </div>
      )}

      {state.tableRows.size > 0 && (
        <>
          {scoredResults.length > 0 && <SummaryStats traceResults={scoredResults} />}

          <PerformanceCharts
            traceResults={state.results}
            hoveredTraceId={hoveredTraceId}
            goldenTraceId={goldenTraceId}
          />

          {state.isEvaluating && (
            <div className="progress-banner">
              <Loader2 size={20} className="animate-spin" style={{ color: 'var(--accent-primary)' }} />
              <span className="progress-text">
                {state.progressMessage || 'Evaluating traces...'}
              </span>
              <Button size="small" danger onClick={actions.stopEvaluation}>Stop</Button>
            </div>
          )}

          <TraceTable
            rows={scoredRows}
            selectedEvaluatorNames={state.selectedEvaluatorNames}
            threshold={state.threshold}
            onRowClick={handleTraceClick}
            onRowHover={handleRowHover}
            isEvaluating={state.isEvaluating}
          />
        </>
      )}

      {state.tableRows.size === 0 && state.errors.length === 0 && (
        <div className="empty-state">
          <div className="empty-title">No results yet</div>
          <p>Upload trace files and run an evaluation to see results here</p>
        </div>
      )}
    </div>
  );
};
