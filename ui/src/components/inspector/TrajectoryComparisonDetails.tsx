import React from 'react';
import { css } from '@emotion/react';
import { ArrowRight } from 'lucide-react';
import type { TrajectoryComparison } from '../../lib/types';

interface TrajectoryComparisonDetailsProps {
  comparisons: TrajectoryComparison[];
}

export const TrajectoryComparisonDetails: React.FC<TrajectoryComparisonDetailsProps> = ({
  comparisons,
}) => {
  const mismatches = comparisons.filter(c => !c.matched);

  if (mismatches.length === 0) {
    return null;
  }

  return (
    <div css={containerStyles}>
      <div css={headerStyles}>Tool Trajectory Mismatch</div>
      {mismatches.map((comparison, idx) => {
        // Backend Details may carry a Go nil slice (ToolCalls() returns
        // nil when an invocation has no tool calls), which marshals as
        // JSON null, not []. Normalize defensively so .length/.map never
        // crash.
        const expectedCalls = comparison.expected ?? [];
        const actualCalls = comparison.actual ?? [];
        return (
        <div key={idx} css={comparisonCardStyles}>
          {comparison.invocationId && (
            <div css={invocationIdStyles}>
              Invocation: {comparison.invocationId.substring(0, 8)}...
            </div>
          )}

          <div css={comparisonRowStyles}>
            <div css={columnStyles}>
              <div css={labelStyles}>Expected</div>
              <div css={toolListStyles}>
                {expectedCalls.length > 0 ? (
                  expectedCalls.map((tool, i) => (
                    <div key={i} css={toolCallStyles}>
                      <div css={toolNameStyles}>{tool.name}</div>
                      <div css={argsContainerStyles}>
                        {Object.keys(tool.args ?? {}).length > 0 ? (
                          <pre css={argsStyles}>{JSON.stringify(tool.args, null, 2)}</pre>
                        ) : (
                          <span css={emptyArgsStyles}>{'{ }'}</span>
                        )}
                      </div>
                    </div>
                  ))
                ) : (
                  <div css={emptyListStyles}>(none)</div>
                )}
              </div>
            </div>

            <div css={arrowContainerStyles}>
              <ArrowRight size={16} css={arrowStyles} />
            </div>

            <div css={columnStyles}>
              <div css={labelStyles}>Actual</div>
              <div css={toolListStyles}>
                {actualCalls.length > 0 ? (
                  actualCalls.map((tool, i) => (
                    <div key={i} css={toolCallStyles}>
                      <div css={toolNameStyles}>{tool.name}</div>
                      <div css={argsContainerStyles}>
                        {Object.keys(tool.args ?? {}).length > 0 ? (
                          <pre css={argsStyles}>{JSON.stringify(tool.args, null, 2)}</pre>
                        ) : (
                          <span css={emptyArgsStyles}>{'{ }'}</span>
                        )}
                      </div>
                    </div>
                  ))
                ) : (
                  <div css={emptyListStyles}>(none)</div>
                )}
              </div>
            </div>
          </div>

          {expectedCalls.length > 0 && actualCalls.length > 0 && (
            <div css={differenceHintStyles}>
              {expectedCalls[0].name !== actualCalls[0].name ? (
                <span>Tool names differ</span>
              ) : JSON.stringify(expectedCalls[0].args) !== JSON.stringify(actualCalls[0].args) ? (
                <span>Tool arguments differ</span>
              ) : (
                <span>Tool sequences differ</span>
              )}
            </div>
          )}
        </div>
        );
      })}
    </div>
  );
};

const containerStyles = css`
  margin-top: 12px;
  padding: 12px;
  background: rgba(255, 87, 87, 0.05);
  border-radius: 4px;
  border: 1px solid rgba(255, 87, 87, 0.2);
`;

const headerStyles = css`
  font-size: 0.75rem;
  font-weight: 600;
  color: var(--status-failure);
  margin-bottom: 12px;
  letter-spacing: 0.5px;
`;

const comparisonCardStyles = css`
  background: var(--bg-elevated);
  border-radius: 4px;
  padding: 12px;
  margin-bottom: 12px;

  &:last-child {
    margin-bottom: 0;
  }
`;

const invocationIdStyles = css`
  font-size: 0.688rem;
  color: var(--text-secondary);
  font-family: var(--font-mono);
  margin-bottom: 8px;
`;

const comparisonRowStyles = css`
  display: grid;
  grid-template-columns: 1fr auto 1fr;
  gap: 16px;
  align-items: start;
`;

const columnStyles = css`
  display: flex;
  flex-direction: column;
  gap: 8px;
`;

const labelStyles = css`
  font-size: 0.688rem;
  font-weight: 600;
  color: var(--text-secondary);
  text-transform: uppercase;
  letter-spacing: 0.5px;
`;

const toolListStyles = css`
  display: flex;
  flex-direction: column;
  gap: 8px;
`;

const toolCallStyles = css`
  background: var(--bg-primary);
  border-radius: 3px;
  padding: 8px;
  border: 1px solid var(--border-default);
`;

const toolNameStyles = css`
  font-family: var(--font-mono);
  font-size: 0.813rem;
  font-weight: 600;
  color: var(--text-primary);
  margin-bottom: 6px;
`;

const argsContainerStyles = css`
  margin-top: 4px;
`;

const argsStyles = css`
  font-family: var(--font-mono);
  font-size: 0.75rem;
  color: var(--text-secondary);
  margin: 0;
  padding: 6px;
  background: var(--bg-elevated);
  border-radius: 2px;
  overflow-x: auto;
  line-height: 1.4;
`;

const emptyArgsStyles = css`
  font-family: var(--font-mono);
  font-size: 0.75rem;
  color: var(--text-tertiary);
  font-style: italic;
`;

const emptyListStyles = css`
  font-size: 0.75rem;
  color: var(--text-tertiary);
  font-style: italic;
  padding: 8px;
`;

const arrowContainerStyles = css`
  display: flex;
  align-items: center;
  justify-content: center;
  padding-top: 24px;
`;

const arrowStyles = css`
  color: var(--text-tertiary);
`;

const differenceHintStyles = css`
  margin-top: 8px;
  padding: 6px 8px;
  background: rgba(255, 87, 87, 0.1);
  border-radius: 3px;
  font-size: 0.688rem;
  color: var(--status-failure);
  font-style: italic;
`;
