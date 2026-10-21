import React from 'react';
import { css } from '@emotion/react';
import { Flag, X } from 'lucide-react';
import { toggleFlag, useFlagged } from '../../lib/flagged';
import { sessionLink } from './routes';

// The sessions flagged on the sessions page, newest first, for a click back to one. In the icon rail each is a flag
// with its name as the tooltip.
export const SidebarFlags: React.FC<{ collapsed: boolean }> = ({ collapsed }) => {
  const flags = useFlagged();
  if (flags.length === 0) return null;
  const open = (id: string) => {
    location.hash = sessionLink(id);
  };
  return (
    <div css={boxStyle}>
      {!collapsed && <span css={headingStyle}>flagged</span>}
      {[...flags].reverse().map((f) => (
        <div key={f.id} css={[rowStyle, collapsed && collapsedRowStyle]}>
          <button css={itemStyle} onClick={() => open(f.id)} title={`${f.label}\n${f.id}`}>
            <Flag size={13} css={flagIconStyle} />
            {!collapsed && <span css={labelStyle}>{f.label}</span>}
          </button>
          {!collapsed && (
            <button css={removeStyle} onClick={() => toggleFlag(f.id, f.label)} title="Remove the flag" aria-label="Remove the flag">
              <X size={12} />
            </button>
          )}
        </div>
      ))}
    </div>
  );
};

const boxStyle = css`
  margin: 14px 8px 0;
  display: flex;
  flex-direction: column;
  gap: 1px;
  font-family: var(--font-display);
`;

const headingStyle = css`
  padding: 0 6px 4px;
  color: var(--text-tertiary);
  font-size: 0.68rem;
  text-transform: uppercase;
  letter-spacing: 0.06em;
`;

const rowStyle = css`
  display: flex;
  align-items: center;
  border-radius: var(--radius);

  &:hover {
    background: var(--bg-elevated);
  }

  &:not(:hover) button:last-of-type:not(:first-of-type) {
    visibility: hidden;
  }
`;

const collapsedRowStyle = css`
  justify-content: center;
`;

const itemStyle = css`
  display: flex;
  align-items: center;
  gap: 7px;
  flex: 1;
  min-width: 0;
  padding: 5px 6px;
  border: none;
  background: transparent;
  color: var(--text-secondary);
  font: inherit;
  font-size: 0.75rem;
  text-align: left;
  cursor: pointer;

  &:hover {
    color: var(--text-primary);
  }
`;

const flagIconStyle = css`
  flex-shrink: 0;
  color: #fbbf24;
`;

const labelStyle = css`
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
`;

const removeStyle = css`
  display: inline-flex;
  padding: 4px;
  border: none;
  background: transparent;
  color: var(--text-tertiary);
  cursor: pointer;

  &:hover {
    color: var(--text-primary);
  }
`;
