import React, { useEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { css } from '@emotion/react';
import { Flag, X } from 'lucide-react';
import { toggleFlag, useFlagged } from '../../lib/flagged';
import { sessionLink } from './routes';

// The sessions flagged on the sessions page, behind one flag with their count; a click lists them, newest first, and
// a click on one opens it. The list is drawn beside the sidebar, outside its clipped box.
export const SidebarFlags: React.FC<{ collapsed: boolean }> = ({ collapsed }) => {
  const flags = useFlagged();
  const [open, setOpen] = useState(false);
  const [at, setAt] = useState({ left: 0, top: 0 });
  const buttonRef = useRef<HTMLButtonElement>(null);
  const panelRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      const t = e.target as Node;
      if (!panelRef.current?.contains(t) && !buttonRef.current?.contains(t)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return;
      // Marks the key as taken, so the panel behind this popup stays open.
      e.preventDefault();
      setOpen(false);
    };
    document.addEventListener('mousedown', onDown);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onDown);
      document.removeEventListener('keydown', onKey);
    };
  }, [open]);

  // The last flag removed leaves nothing to list.
  useEffect(() => {
    if (flags.length === 0) setOpen(false);
  }, [flags.length]);

  if (flags.length === 0) return null;

  const toggle = () => {
    const r = buttonRef.current?.getBoundingClientRect();
    if (r) setAt({ left: r.right + 8, top: r.top });
    setOpen((o) => !o);
  };

  return (
    <div css={boxStyle}>
      <button
        ref={buttonRef}
        css={[buttonStyle, collapsed && collapsedButtonStyle, open && openStyle]}
        onClick={toggle}
        title={`${flags.length} flagged session${flags.length === 1 ? '' : 's'}`}
        aria-haspopup="menu"
        aria-expanded={open}
      >
        <span css={iconWrapStyle}>
          <Flag size={18} css={flagIconStyle} />
          <span css={badgeStyle}>{flags.length}</span>
        </span>
        {!collapsed && 'flagged'}
      </button>
      {open &&
        createPortal(
          <div ref={panelRef} css={panelStyle} style={{ left: at.left, top: at.top }} role="menu">
            {[...flags].reverse().map((f) => (
              <div key={f.id} css={rowStyle}>
                <button
                  css={itemStyle}
                  onClick={() => {
                    location.hash = sessionLink(f.id);
                    setOpen(false);
                  }}
                  title={f.id}
                >
                  <span css={labelStyle}>{f.label}</span>
                </button>
                <button css={removeStyle} onClick={() => toggleFlag(f.id, f.label)} title="Remove the flag" aria-label="Remove the flag">
                  <X size={12} />
                </button>
              </div>
            ))}
          </div>,
          document.body,
        )}
    </div>
  );
};

const boxStyle = css`
  margin: 10px 8px 0;
`;

// Looks like a view button of the sidebar, so it lines up with them.
const buttonStyle = css`
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  padding: 10px 12px;
  border: none;
  border-left: 3px solid transparent;
  border-radius: var(--radius);
  background: transparent;
  color: var(--text-secondary);
  font-family: var(--font-display);
  font-size: 0.875rem;
  font-weight: 500;
  text-align: left;
  cursor: pointer;

  &:hover {
    color: var(--text-primary);
    background: var(--bg-elevated);
  }
`;

const collapsedButtonStyle = css`
  justify-content: center;
  padding: 10px 0;
  border-left: none;
`;

const openStyle = css`
  color: var(--text-primary);
  background: var(--bg-elevated);
`;

const iconWrapStyle = css`
  position: relative;
  display: inline-flex;
`;

const flagIconStyle = css`
  color: #fbbf24;
`;

const badgeStyle = css`
  position: absolute;
  top: -6px;
  right: -9px;
  min-width: 15px;
  height: 15px;
  padding: 0 4px;
  box-sizing: border-box;
  border-radius: 8px;
  background: #fbbf24;
  color: #1c1917;
  font-size: 10px;
  font-weight: 700;
  line-height: 15px;
  text-align: center;
`;

const panelStyle = css`
  position: fixed;
  z-index: 60;
  width: 260px;
  padding: 4px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius);
  background: var(--bg-surface);
  box-shadow: 0 6px 24px rgba(0, 0, 0, 0.35);
  font-family: var(--font-display);
  font-size: 0.76rem;
`;

const rowStyle = css`
  display: flex;
  align-items: center;
  border-radius: var(--radius);

  &:hover {
    background: var(--bg-elevated);
  }
`;

const itemStyle = css`
  flex: 1;
  min-width: 0;
  padding: 6px 8px;
  border: none;
  background: transparent;
  color: var(--text-primary);
  font: inherit;
  text-align: left;
  cursor: pointer;
`;

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
