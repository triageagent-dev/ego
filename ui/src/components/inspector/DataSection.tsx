import React, { useState } from 'react';
import { css } from '@emotion/react';
import { ChevronDown, ChevronRight } from 'lucide-react';

interface DataSectionProps {
  title: string;
  icon: React.ReactNode;
  color: string;
  children: React.ReactNode;
  dataPath: string;
  isHighlighted?: boolean;
  onClick?: (dataPath: string) => void;
  defaultExpanded?: boolean;
}

export const DataSection: React.FC<DataSectionProps> = ({
  title,
  icon,
  color,
  children,
  dataPath,
  isHighlighted = false,
  onClick,
  defaultExpanded = true,
}) => {
  const [isExpanded, setIsExpanded] = useState(defaultExpanded);

  const handleHeaderClick = () => {
    setIsExpanded(!isExpanded);
  };

  const handleSectionClick = (e: React.MouseEvent) => {
    // Only trigger onClick if clicking on the content area, not the header
    if (onClick && e.currentTarget === e.target) {
      onClick(dataPath);
    }
  };

  return (
    <div
      css={sectionContainerStyles(color, isHighlighted)}
      data-path={dataPath}
    >
      <div css={sectionHeaderStyles} onClick={handleHeaderClick}>
        <div css={headerLeftStyles}>
          {isExpanded ? (
            <ChevronDown size={16} />
          ) : (
            <ChevronRight size={16} />
          )}
          <div css={iconContainerStyles(color)}>{icon}</div>
          <span css={titleStyles}>{title}</span>
        </div>
      </div>

      {isExpanded && (
        <div
          css={sectionContentStyles}
          onClick={handleSectionClick}
        >
          {children}
        </div>
      )}
    </div>
  );
};

const sectionContainerStyles = (color: string, isHighlighted: boolean) => css`
  background: var(--bg-elevated);
  border-radius: 6px;
  border: 1px solid var(--border-default);
  overflow: hidden;
  transition: all 0.2s ease;

  ${isHighlighted && `
    border-color: ${color};
    box-shadow: 0 0 16px ${color}33;
  `}

  &:hover {
    border-color: ${color}66;
  }
`;

const sectionHeaderStyles = css`
  padding: 12px 16px;
  cursor: pointer;
  user-select: none;
  transition: background 0.2s ease;

  &:hover {
    background: var(--bg-surface);
  }
`;

const headerLeftStyles = css`
  display: flex;
  align-items: center;
  gap: 10px;
  color: var(--text-primary);
`;

const iconContainerStyles = (color: string) => css`
  display: flex;
  align-items: center;
  justify-content: center;
  color: ${color};
`;

const titleStyles = css`
  font-size: 0.875rem;
  font-weight: 600;
  letter-spacing: 0.3px;
`;

const sectionContentStyles = css`
  padding: 16px;
  border-top: 1px solid var(--border-default);
  cursor: pointer;
  transition: background 0.15s ease;

  &:hover {
    background: var(--bg-surface);
  }
`;
