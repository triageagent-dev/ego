import React from 'react';
import { css } from '@emotion/react';
import { Button } from 'antd';
import { Save } from 'lucide-react';
import { PageTitle } from '../shell/PageTitle';
import { titleStyle as pageTitleStyle } from '../ui/page';

interface BuilderHeaderProps {
  onSave: () => void;
  evalSetId: string;
}

export const BuilderHeader: React.FC<BuilderHeaderProps> = ({
  onSave,
  evalSetId,
}) => {
  return (
    <div css={headerStyle}>
      <PageTitle style={{ display: 'flex', alignItems: 'baseline', gap: '14px' }}>
        <h1 css={pageTitleStyle}>eval sets</h1>
        <span css={evalSetIdStyle}>{evalSetId}</span>
      </PageTitle>

      <div css={rightSectionStyle}>
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



const evalSetIdStyle = css`
  font-size: 0.75rem;
  color: var(--text-secondary);
  font-family: monospace;
`;

const rightSectionStyle = css`
  display: flex;
  gap: 12px;
`;
