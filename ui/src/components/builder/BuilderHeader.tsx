import React from 'react';
import { css } from '@emotion/react';
import { Button } from 'antd';
import { Save } from 'lucide-react';
import { useTraceContext } from '../../context/TraceContext';

interface BuilderHeaderProps {
  onSave: () => void;
  evalSetId: string;
}

export const BuilderHeader: React.FC<BuilderHeaderProps> = ({
  onSave,
  evalSetId,
}) => {
  const { state, actions } = useTraceContext();
  const hasQueuedItems = state.annotationQueues.some(q => q.items.length > 0);

  return (
    <div css={headerStyle}>
      <div css={leftSectionStyle}>
        <div css={titleStyle}>
          <h1>Eval sets</h1>
          <span css={evalSetIdStyle}>{evalSetId}</span>
        </div>
      </div>

      <div css={rightSectionStyle}>
        {hasQueuedItems && (
          <button
            onClick={() => actions.setCurrentView('annotation-queue')}
            css={css`
              display: flex; align-items: center; gap: 8px;
              padding: 8px 14px; border-radius: var(--radius);
              border: 1px solid rgba(181, 171, 252, 0.4);
              background: transparent; color: #b5abfc;
              font-size: 0.875rem; font-weight: 500; cursor: pointer;
              transition: all 0.2s ease;
              &:hover { background: rgba(181, 171, 252, 0.08); }
            `}
          >
            Annotation Queues
          </button>
        )}
        <Button
          type="primary"
          icon={<Save size={16} />}
          onClick={onSave}
        >
          Save EvalSet
        </Button>
      </div>
    </div>
  );
};

const headerStyle = css`
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 16px 24px;
  border-bottom: 1px solid var(--border-default);
  background: var(--bg-surface);
`;

const leftSectionStyle = css`
  display: flex;
  align-items: center;
  gap: 16px;
`;

const titleStyle = css`
  display: flex;
  flex-direction: column;
  gap: 4px;

  h1 {
    margin: 0;
    font-size: 1.25rem;
    font-weight: 600;
    color: var(--text-primary);
  }
`;

const evalSetIdStyle = css`
  font-size: 0.75rem;
  color: var(--text-secondary);
  font-family: monospace;
`;

const rightSectionStyle = css`
  display: flex;
  gap: 12px;
`;
