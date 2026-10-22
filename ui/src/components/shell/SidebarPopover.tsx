import React, { useEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { css } from '@emotion/react';
import { Flag } from 'lucide-react';

/** The flag every sidebar flag is drawn with: an outline in the flag's colour over a faint fill of it. */
export const FlagIcon: React.FC<{ color: string }> = ({ color }) => (
  <Flag size={18} strokeWidth={1.75} color={color} fill={color} fillOpacity={0.18} />
);

interface Props {
  collapsed: boolean;
  icon: React.ReactNode;
  label: string;
  // The count on the icon's corner; none when null.
  badge: number | null;
  badgeColor: string;
  title: string;
  // The panel's content; close() shuts it, for a row that navigates.
  children: (close: () => void) => React.ReactNode;
}

// A sidebar button that opens a small panel beside the sidebar (drawn outside its clipped box). Esc or a click
// outside shuts it.
export const SidebarPopover: React.FC<Props> = ({ collapsed, icon, label, badge, badgeColor, title, children }) => {
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
      if (e.key === 'Escape') setOpen(false);
    };
    document.addEventListener('mousedown', onDown);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onDown);
      document.removeEventListener('keydown', onKey);
    };
  }, [open]);

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
        title={title}
        aria-haspopup="menu"
        aria-expanded={open}
      >
        {/* In the rail the flag and its badge are centred together: the badge hangs off the flag's right. */}
        <span css={iconWrapStyle} style={collapsed && badge !== null ? { transform: 'translateX(-4px)' } : undefined}>
          {icon}
          {badge !== null && <span css={badgeStyle} style={{ background: badgeColor }}>{badge > 9 ? '9+' : badge}</span>}
        </span>
        {!collapsed && label}
      </button>
      {open &&
        createPortal(
          <div ref={panelRef} css={panelStyle} style={{ left: at.left, top: at.top }} role="menu">
            {children(() => setOpen(false))}
          </div>,
          document.body,
        )}
    </div>
  );
};

export const popoverRowStyle = css`
  display: flex;
  align-items: center;
  border-radius: var(--radius);

  &:hover {
    background: var(--bg-elevated);
  }
`;

export const popoverItemStyle = css`
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

export const popoverNoteStyle = css`
  padding: 8px;
  color: var(--text-secondary);
`;

const boxStyle = css`
  margin: 0;
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

// The ring in the sidebar's colour keeps the badge apart from the flag it sits on.
const badgeStyle = css`
  position: absolute;
  top: -7px;
  right: -10px;
  min-width: 15px;
  height: 15px;
  padding: 0 4px;
  box-sizing: border-box;
  border-radius: 8px;
  box-shadow: 0 0 0 2px var(--bg-surface);
  color: #1c1917;
  font-size: 10px;
  font-weight: 700;
  line-height: 15px;
  text-align: center;
`;

const panelStyle = css`
  position: fixed;
  z-index: 60;
  width: 280px;
  padding: 4px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius);
  background: var(--bg-surface);
  box-shadow: 0 6px 24px rgba(0, 0, 0, 0.35);
  font-family: var(--font-display);
  font-size: 0.76rem;
`;
