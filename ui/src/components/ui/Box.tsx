import React from 'react';
import { css } from '@emotion/react';

interface BoxProps {
  title?: React.ReactNode;
  meta?: React.ReactNode;
  actions?: React.ReactNode;
  children?: React.ReactNode;
  className?: string;
  style?: React.CSSProperties;
}

// A bordered panel with its title sitting on the top border, like a terminal
// box. meta is the dim text after the title; actions sit on the right edge.
export const Box: React.FC<BoxProps> = ({ title, meta, actions, children, className, style }) => (
  <section css={boxStyle} className={className} style={style}>
    {(title || meta || actions) && (
      <header css={legendStyle}>
        {title && <span css={titleStyle}>{title}</span>}
        {meta && <span css={metaStyle}>{meta}</span>}
        {actions && <span css={actionsStyle}>{actions}</span>}
      </header>
    )}
    {children}
  </section>
);

const boxStyle = css`
  position: relative;
  border: 1px solid var(--border-default);
  border-radius: var(--radius);
  padding: 16px 12px 10px;
  margin-top: 10px;
`;

const legendStyle = css`
  position: absolute;
  top: -9px;
  left: 10px;
  right: 10px;
  display: flex;
  align-items: center;
  gap: 10px;
  pointer-events: none;
  line-height: 18px;

  > span {
    background: var(--bg-primary);
    padding: 0 6px;
    white-space: nowrap;
  }
`;

const titleStyle = css`
  color: var(--accent-cyan);
`;

const metaStyle = css`
  color: var(--text-muted);
  font-size: 0.92em;
`;

const actionsStyle = css`
  margin-left: auto;
  pointer-events: auto;
`;
