import React, { useEffect } from 'react';
import { css } from '@emotion/react';
import { X } from 'lucide-react';
import { toggleFlag, useFlagged } from '../../lib/flagged';
import { sessionLink } from './routes';
import { FlagIcon, popoverItemStyle, popoverRowStyle, SidebarPopover } from './SidebarPopover';

// The sessions flagged on the sessions page, behind one flag with their count; a click lists them, newest first, and
// a click on one opens it.
export const SidebarFlags: React.FC<{ collapsed: boolean }> = ({ collapsed }) => {
  const flags = useFlagged();
  // Nothing left to list: the button goes away with its last flag.
  useEffect(() => undefined, [flags.length]);
  if (flags.length === 0) return null;
  return (
    <SidebarPopover
      collapsed={collapsed}
      icon={<FlagIcon color="#fbbf24" />}
      label="flagged"
      badge={flags.length}
      badgeColor="#fbbf24"
      title={`${flags.length} flagged session${flags.length === 1 ? '' : 's'}`}
    >
      {(close) =>
        [...flags].reverse().map((f) => (
          <div key={f.id} css={popoverRowStyle}>
            <button
              css={popoverItemStyle}
              onClick={() => {
                location.hash = sessionLink(f.id);
                close();
              }}
              title={f.id}
            >
              <span css={labelStyle}>{f.label}</span>
            </button>
            <button css={removeStyle} onClick={() => toggleFlag(f.id, f.label)} title="Remove the flag" aria-label="Remove the flag">
              <X size={12} />
            </button>
          </div>
        ))
      }
    </SidebarPopover>
  );
};

const labelStyle = css`
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
`;

const removeStyle = css`
  display: inline-flex;
  padding: 6px;
  border: none;
  background: transparent;
  color: var(--text-tertiary);
  cursor: pointer;

  &:hover {
    color: var(--text-primary);
  }
`;
