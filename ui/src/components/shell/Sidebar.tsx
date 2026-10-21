import React, { useState } from 'react';
import { css } from '@emotion/react';
import { Radio, Play, FileJson, History, Coins, Gauge, PanelLeftClose, PanelLeftOpen } from 'lucide-react';
import { useTraceContext } from '../../context/TraceContext';
import type { ViewType } from '../../lib/types';
import { SidebarFailed } from './SidebarFailed';
import { SidebarFlags } from './SidebarFlags';
import { SidebarReview } from './SidebarReview';
import { SidebarUsage } from './SidebarUsage';
import { TABS, tabForView } from './routes';

const ICONS: Partial<Record<ViewType, React.ReactNode>> = {
  streaming: <Radio size={18} />,
  upload: <Play size={18} />,
  runs: <History size={18} />,
  usage: <Coins size={18} />,
  quality: <Gauge size={18} />,
  builder: <FileJson size={18} />,
};

const COLLAPSED_KEY = 'ego.sidebar.collapsed';

const readCollapsed = (): boolean => {
  try {
    // Collapsed until the user opens it: only an explicit '0' keeps it open.
    return localStorage.getItem(COLLAPSED_KEY) !== '0';
  } catch {
    return true;
  }
};

// The classic shell: the same six views as the tab bar, in a left sidebar.
export const Sidebar: React.FC = () => {
  const { actions, state } = useTraceContext();
  const active = tabForView(state.currentView);
  const [collapsed, setCollapsed] = useState(readCollapsed);
  const toggle = () => {
    setCollapsed((c) => {
      try {
        localStorage.setItem(COLLAPSED_KEY, c ? '0' : '1');
      } catch {
        // the choice just does not survive a reload
      }
      return !c;
    });
  };

  return (
    <nav css={[sidebarStyle, collapsed && collapsedStyle]}>
      <div css={[brandStyle, collapsed && collapsedBrandStyle]}>
        <div css={brandLinkStyle} onClick={() => actions.setCurrentView('streaming')}>
          <img src="/favicon.svg" alt="" css={brandMarkStyle} />
          {!collapsed && <span css={brandNameStyle}>ego</span>}
        </div>
      </div>

      <div css={navListStyle}>
        {TABS.map((t) => (
          <button
            key={t.view}
            css={[navItemStyle, collapsed && collapsedItemStyle, active?.view === t.view && activeItemStyle]}
            onClick={() => actions.setCurrentView(t.view)}
            title={collapsed ? t.label : undefined}
          >
            {ICONS[t.view]}
            {!collapsed && t.label}
          </button>
        ))}
      </div>

      <div css={flagsStyle}>
        <SidebarFlags collapsed={collapsed} />
        <SidebarFailed collapsed={collapsed} />
        <SidebarReview collapsed={collapsed} />
      </div>

      <SidebarUsage collapsed={collapsed} />

      <div css={[bottomStyle, collapsed && collapsedBottomStyle]}>
        <button css={toggleStyle} onClick={toggle} title={collapsed ? 'Show menu' : 'Hide menu'} aria-label={collapsed ? 'Show menu' : 'Hide menu'}>
          {collapsed ? <PanelLeftOpen size={16} /> : <PanelLeftClose size={16} />}
        </button>
      </div>
    </nav>
  );
};

const sidebarStyle = css`
  width: 150px;
  flex-shrink: 0;
  height: 100%;
  background: var(--bg-surface);
  border-right: 1px solid var(--border-default);
  display: flex;
  flex-direction: column;
  padding: 20px 0 0;
  overflow: hidden;
`;

const brandStyle = css`
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 0 16px 14px;
  border-bottom: 1px solid var(--border-default);
  margin-bottom: 12px;
`;

const brandLinkStyle = css`
  display: flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
`;

const toggleStyle = css`
  display: inline-flex;
  padding: 6px;
  border: none;
  border-radius: var(--radius);
  background: transparent;
  color: var(--text-tertiary);
  cursor: pointer;

  &:hover {
    color: var(--text-primary);
    background: var(--bg-elevated);
  }
`;

// Icon rail: the views stay one click away, the labels and the footer are hidden.
const collapsedStyle = css`
  width: 56px;
`;

const collapsedBrandStyle = css`
  justify-content: center;
  padding: 0 0 12px;
`;

const collapsedItemStyle = css`
  justify-content: center;
  padding: 10px 0;
`;
const brandMarkStyle = css`
  display: block;
  width: 20px;
  height: 20px;
`;

const brandNameStyle = css`
  font-family: var(--font-mono);
  font-size: 0.9rem;
  font-weight: 500;
  color: var(--text-primary);
`;

// The flags, set apart from the views by a rule; it goes with them when there are none.
const flagsStyle = css`
  display: flex;
  flex-direction: column;
  gap: 2px;
  margin: 10px 8px 0;
  padding-top: 8px;
  border-top: 1px solid var(--border-default);

  &:empty {
    display: none;
  }
`;

const navListStyle = css`
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 0 8px;
`;

const navItemStyle = css`
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 12px;
  border-radius: var(--radius);
  border: none;
  border-left: 3px solid transparent;
  background: transparent;
  color: var(--text-secondary);
  font-size: 0.875rem;
  font-weight: 500;
  font-family: var(--font-display);
  text-transform: capitalize;
  cursor: pointer;
  width: 100%;
  text-align: left;
  transition: all 0.15s ease;

  &:hover {
    color: var(--text-primary);
    background: var(--bg-elevated);
  }
`;

const activeItemStyle = css`
  color: var(--accent-primary);
  background: var(--bg-selected);
  border-left-color: var(--accent-primary);
  font-weight: 600;

  &:hover {
    color: var(--accent-primary);
  }
`;

// The show/hide toggle sits at the bottom, away from the logo.
const bottomStyle = css`
  padding: 10px 8px;
  border-top: 1px solid var(--border-default);
  display: flex;
`;

const collapsedBottomStyle = css`
  justify-content: center;
`;
