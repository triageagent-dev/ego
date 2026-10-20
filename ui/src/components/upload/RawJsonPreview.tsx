import React, { useMemo } from 'react';
import { css } from '@emotion/react';
import { Button, message } from 'antd';
import { Copy } from 'lucide-react';
import { copyToClipboard } from '../../lib/utils';

interface RawJsonPreviewProps {
  content: string;
  title?: string;
}

function prettifyJson(content: string): string {
  try {
    return JSON.stringify(JSON.parse(content), null, 2);
  } catch {
    const lines = content.split('\n').filter(l => l.trim());
    if (lines.length <= 1) return content;
    try {
      return lines.map(line => JSON.stringify(JSON.parse(line), null, 2)).join('\n\n');
    } catch {
      return content;
    }
  }
}

export const RawJsonPreview: React.FC<RawJsonPreviewProps> = ({ content, title = 'JSON Preview' }) => {
  const prettified = useMemo(() => prettifyJson(content), [content]);

  const handleCopy = async () => {
    const success = await copyToClipboard(prettified);
    if (success) {
      message.success('Copied to clipboard!');
    } else {
      message.error('Failed to copy');
    }
  };

  return (
    <div css={containerStyle}>
      <div css={headerStyle}>
        <h3>{title}</h3>
        <Button
          size="small"
          icon={<Copy size={14} />}
          onClick={handleCopy}
        >
          Copy
        </Button>
      </div>

      <div css={jsonContainerStyle}>
        <pre>{prettified}</pre>
      </div>
    </div>
  );
};

const containerStyle = css`
  display: flex;
  flex-direction: column;
  height: 100%;
`;

const headerStyle = css`
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 16px;
  border-bottom: 1px solid var(--border-default);

  h3 {
    font-size: 1rem;
    font-weight: 600;
    color: var(--text-primary);
    margin: 0;
  }
`;

const jsonContainerStyle = css`
  flex: 1;
  overflow: auto;
  padding: 16px;

  pre {
    margin: 0;
    font-size: 0.75rem;
    font-family: monospace;
    color: var(--text-primary);
    line-height: 1.5;
  }
`;
