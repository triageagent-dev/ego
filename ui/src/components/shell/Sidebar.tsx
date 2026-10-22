import React, { useState } from 'react';
import { css } from '@emotion/react';
import { Radio, Play, FileJson, History, Coins, PanelLeftClose, PanelLeftOpen } from 'lucide-react';
import { useTraceContext } from '../../context/TraceContext';
import type { ViewType } from '../../lib/types';
import { SidebarFailed } from './SidebarFailed';
import { SidebarFlags } from './SidebarFlags';
import { SidebarReview } from './SidebarReview';
import { SidebarUsage } from './SidebarUsage';
import { TABS, tabForView } from './routes';

// The ego mark in the colour of the text, so it reads like the other icons (the favicon has its own gradient).
const EgoMark: React.FC = () => (
  <svg width={18} height={18} viewBox="60 60 106 106" fill="currentColor" aria-hidden="true" style={{ display: 'block', flexShrink: 0 }}>
    <path d="M113.141,161.832 c13.312,0,25.826-5.184,35.236-14.596c1.367-1.367,1.367-3.583,0-4.949c-1.367-1.367-3.584-1.367-4.949,0 c-8.089,8.09-18.846,12.545-30.287,12.545c-23.618,0-42.833-19.215-42.833-42.833c0-23.618,19.215-42.833,42.833-42.833 c22.441,0,40.905,17.345,42.691,39.333h-5.313c-1.771-19.064-17.859-34.04-37.379-34.04c-20.699,0-37.54,16.841-37.54,37.54 s16.841,37.54,37.54,37.54c10.028,0,19.455-3.905,26.544-10.996c1.366-1.367,1.366-3.583-0.001-4.949l-8.295-8.295 c-1.367-1.367-3.582-1.367-4.949,0c-3.552,3.552-8.275,5.508-13.299,5.508c-10.37,0-18.808-8.437-18.808-18.808 s8.438-18.808,18.808-18.808c5.024,0,9.747,1.956,13.299,5.509c2.713,2.713,4.494,6.108,5.186,9.799h-28.708 c-1.933,0-3.5,1.567-3.5,3.5s1.567,3.5,3.5,3.5h32.53c1.933,0,3.5-1.566,3.5-3.5c0.001-6.894-2.684-13.374-7.558-18.249 c-4.875-4.874-11.355-7.559-18.249-7.559c-14.23,0-25.808,11.577-25.808,25.808c0,14.23,11.577,25.808,25.808,25.808 c5.713,0,11.142-1.844,15.608-5.249l3.372,3.372c-5.38,4.284-12.002,6.609-18.98,6.609c-16.84,0-30.54-13.7-30.54-30.54 c0-16.84,13.7-30.54,30.54-30.54c16.84,0,30.54,13.7,30.541,30.54c0,1.933,1.567,3.5,3.5,3.5h12.292c1.933,0,3.5-1.567,3.5-3.5 c0-27.478-22.355-49.833-49.833-49.833c-27.477,0-49.833,22.355-49.833,49.833C63.308,139.477,85.663,161.832,113.141,161.832z" />
  </svg>
);

const ICONS: Partial<Record<ViewType, React.ReactNode>> = {
  streaming: <Radio size={18} />,
  upload: <Play size={18} />,
  runs: <History size={18} />,
  usage: <Coins size={18} />,
  // The ego mark: this tab is ego's own view of the agents (scores, findings, corrections).
  quality: <EgoMark />,
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

// The views in a left sidebar, with the flags and the day's usage under them.
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
