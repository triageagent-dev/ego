import React, { useState, useEffect } from 'react';
import { Checkbox, Button } from 'antd';
import { css } from '@emotion/react';
import { AVAILABLE_METRICS, type MetricMetadata } from '../../lib/types';
import { listMetrics } from '../../api/client';

export const metricStyle = css`
  .metric-list {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }

  .metric-item {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }

  .metric-row {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 6px 10px;
  }

  .metric-name {
    color: var(--text-primary);
    font-family: var(--font-mono);
    font-size: 12px;
  }

  .metric-description {
    color: var(--text-secondary);
    font-size: 11px;
    margin-left: 24px;
    line-height: 1.4;
    overflow-wrap: break-word;
    word-break: break-word;
  }

  .metric-badges {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    flex-shrink: 0;
  }

  .metric-badge {
    font-size: 10px;
    padding: 2px 6px;
    border-radius: var(--radius);
    font-weight: 500;
    line-height: 1.4;
  }

  .badge-eval-set {
    background-color: rgba(124, 255, 107, 0.1);
    color: var(--status-success);
    border: 1px solid rgba(124, 255, 107, 0.3);
  }

  .badge-llm {
    background-color: rgba(181, 171, 252, 0.1);
    color: var(--accent-primary);
    border: 1px solid rgba(181, 171, 252, 0.3);
  }

  .badge-gcp {
    background-color: rgba(167, 159, 240, 0.1);
    color: #60A5FA;
    border: 1px solid rgba(167, 159, 240, 0.3);
  }

  .badge-rubrics {
    background-color: rgba(251, 146, 60, 0.1);
    color: #FB923C;
    border: 1px solid rgba(251, 146, 60, 0.3);
  }

  .badge-incomplete {
    background-color: rgba(239, 68, 68, 0.1);
    color: #EF4444;
    border: 1px solid rgba(239, 68, 68, 0.3);
  }

  .selector-actions {
    display: flex;
    gap: 8px;
    margin-top: 12px;
    padding-top: 12px;
    border-top: 1px solid var(--border-default);
  }

  .selector-note {
    margin-top: 10px;
    font-size: 11px;
    color: var(--text-secondary);
  }

  .ant-checkbox-wrapper {
    color: var(--text-primary);
  }

  .ant-checkbox-checked .ant-checkbox-inner {
    background-color: var(--accent-primary);
    border-color: var(--accent-primary);
  }
`;

let cachedMetrics: MetricMetadata[] | null = null;

/** The metrics grouped by category, in the order the server lists them; loads them once per page view. */
export function useMetricGroups(loadFromAPI = true): Array<[category: string, metrics: MetricMetadata[]]> {
  const [metrics, setMetrics] = useState<MetricMetadata[]>(cachedMetrics ?? AVAILABLE_METRICS);

  useEffect(() => {
    if (!loadFromAPI || cachedMetrics) return;
    let cancelled = false;
    listMetrics()
      .then((apiMetrics) => {
        if (!cancelled) {
          cachedMetrics = apiMetrics;
          setMetrics(apiMetrics);
        }
      })
      .catch((error) => {
        console.error('Failed to load metrics from API, using fallback:', error);
      });
    return () => { cancelled = true; };
  }, [loadFromAPI]);

  const groups = new Map<string, MetricMetadata[]>();
  for (const m of metrics) groups.set(m.category, [...(groups.get(m.category) ?? []), m]);
  return Array.from(groups.entries());
}

interface MetricGroupProps {
  metrics: MetricMetadata[];
  selectedEvaluatorNames: string[];
  onToggleEvaluatorName: (evaluatorName: string) => void;
}

/** One category's metrics with their badges and descriptions, and select-all / clear for just that category. */
export const MetricGroup: React.FC<MetricGroupProps> = ({ metrics, selectedEvaluatorNames, onToggleEvaluatorName }) => {
  const caveated = metrics.some((m) => m.requiresRubrics === true || m.working === false);
  const setGroup = (on: boolean) => {
    metrics.forEach((m) => {
      if (selectedEvaluatorNames.includes(m.name) !== on) onToggleEvaluatorName(m.name);
    });
  };

  return (
    <div css={metricStyle}>
      <div className="metric-list">
        {metrics.map((metric) => (
          <div key={metric.name} className="metric-item">
            <div className="metric-row">
              <Checkbox
                checked={selectedEvaluatorNames.includes(metric.name)}
                onChange={() => onToggleEvaluatorName(metric.name)}
              >
                <span className="metric-name">{metric.name}</span>
              </Checkbox>
              <div className="metric-badges">
                {metric.requiresEvalSet && <span className="metric-badge badge-eval-set">Requires Eval Set</span>}
                {metric.requiresLLM && <span className="metric-badge badge-llm">Uses LLM</span>}
                {metric.requiresGCP && <span className="metric-badge badge-gcp">Requires GCP</span>}
                {metric.requiresRubrics && <span className="metric-badge badge-rubrics">Requires Rubrics</span>}
                {metric.working === false && <span className="metric-badge badge-incomplete">Incomplete</span>}
              </div>
            </div>
            <div className="metric-description">{metric.description}</div>
          </div>
        ))}
      </div>

      <div className="selector-actions">
        <Button size="small" onClick={() => setGroup(true)}>Select all</Button>
        <Button size="small" onClick={() => setGroup(false)}>Clear</Button>
      </div>
      {caveated && (
        <div className="selector-note">
          Some evaluators here require rubric configuration or are work-in-progress; see badges.
        </div>
      )}
    </div>
  );
};
