import React from 'react';
import { css } from '@emotion/react';
import { MessageCircle, Wrench, MessageSquare } from 'lucide-react';
import { DataSection } from './DataSection';
import { ToolCallList } from './ToolCallList';
import type { Invocation } from '../../lib/types';

interface InvocationCardProps {
  invocation: Invocation;
  index: number;
  isSelected?: boolean;
  highlightedPaths?: Set<string>;
  onSelectData?: (dataPath: string) => void;
  onSelectInvocation?: (invocationId: string) => void;
}

export const InvocationCard: React.FC<InvocationCardProps> = ({
  invocation,
  index,
  isSelected = false,
  highlightedPaths = new Set(),
  onSelectData,
  onSelectInvocation,
}) => {
  const userInputPath = `inv.${invocation.invocationId}.userContent`;
  const toolsPath = `inv.${invocation.invocationId}.tools`;
  const responsePath = `inv.${invocation.invocationId}.finalResponse`;

  // Extract text from content parts
  const getUserInputText = () => {
    return invocation.userContent.parts
      .filter(p => p.text)
      .map(p => p.text)
      .join('\n');
  };

  const getFinalResponseText = () => {
    return invocation.finalResponse.parts
      .filter(p => p.text)
      .map(p => p.text)
      .join('\n');
  };

  return (
    <div css={cardStyles(isSelected)}>
      <div
        css={cardHeaderStyles}
        onClick={() => onSelectInvocation?.(invocation.invocationId)}
      >
        <div css={headerLeftStyles}>
          <span css={invocationNumberStyles}>Invocation #{index + 1}</span>
          <span css={invocationIdStyles}>{invocation.invocationId}</span>
        </div>
      </div>

      <div css={cardBodyStyles}>
        {/* User Input Section */}
        <DataSection
          title="User Input"
          icon={<MessageCircle size={16} />}
          color="var(--accent-purple)"
          dataPath={userInputPath}
          isHighlighted={highlightedPaths.has(userInputPath)}
          onClick={onSelectData}
          defaultExpanded={true}
        >
          <div css={textContentStyles}>
            {getUserInputText() || <span css={emptyTextStyles}>No user input</span>}
          </div>
        </DataSection>

        {/* Tool Trajectory Section */}
        <DataSection
          title="Tool Trajectory"
          icon={<Wrench size={16} />}
          color="var(--accent-lime)"
          dataPath={toolsPath}
          isHighlighted={highlightedPaths.has(toolsPath)}
          onClick={onSelectData}
          defaultExpanded={invocation.intermediateData.toolUses.length > 0}
        >
          <ToolCallList
            toolUses={invocation.intermediateData.toolUses}
            toolResponses={invocation.intermediateData.toolResponses}
            onSelectTool={(idx, type) => {
              const toolPath = `${toolsPath}.${idx}.${type}`;
              onSelectData?.(toolPath);
            }}
          />
        </DataSection>

        {/* Final Response Section */}
        <DataSection
          title="Final Response"
          icon={<MessageSquare size={16} />}
          color="var(--accent-primary)"
          dataPath={responsePath}
          isHighlighted={highlightedPaths.has(responsePath)}
          onClick={onSelectData}
          defaultExpanded={true}
        >
          <div css={textContentStyles}>
            {getFinalResponseText() || <span css={emptyTextStyles}>No response</span>}
          </div>
        </DataSection>
      </div>
    </div>
  );
};

const cardStyles = (isSelected: boolean) => css`
  background: var(--bg-surface);
  border-radius: 8px;
  border: 1px solid var(--border-default);
  overflow: hidden;
  transition: all 0.2s ease;

  ${isSelected && `
    border-color: var(--accent-primary);
    box-shadow: 0 0 20px rgba(168, 85, 247, 0.2);
  `}

  &:hover {
    border-color: var(--accent-primary);
  }
`;

const cardHeaderStyles = css`
  padding: 16px;
  background: var(--bg-elevated);
  border-bottom: 1px solid var(--border-default);
  cursor: pointer;
  transition: background 0.2s ease;

  &:hover {
    background: var(--bg-surface);
  }
`;

const headerLeftStyles = css`
  display: flex;
  align-items: center;
  gap: 12px;
`;

const invocationNumberStyles = css`
  font-size: 0.875rem;
  font-weight: 600;
  color: var(--accent-primary);
  letter-spacing: 0.3px;
`;

const invocationIdStyles = css`
  font-family: var(--font-mono);
  font-size: 0.75rem;
  color: var(--text-secondary);
  background: var(--bg-primary);
  padding: 4px 8px;
  border-radius: 4px;
`;

const cardBodyStyles = css`
  padding: 16px;
  display: flex;
  flex-direction: column;
  gap: 12px;
`;

const textContentStyles = css`
  font-size: 0.875rem;
  line-height: 1.6;
  color: var(--text-primary);
  white-space: pre-wrap;
  word-wrap: break-word;
`;

const emptyTextStyles = css`
  color: var(--text-secondary);
  font-style: italic;
`;
