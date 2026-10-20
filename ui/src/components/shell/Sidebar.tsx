import React from 'react';
import { css } from '@emotion/react';
import { Radio, Play, FileJson, History, Coins, Gauge } from 'lucide-react';
import { useTraceContext } from '../../context/TraceContext';
import type { ViewType } from '../../lib/types';
import { UserMenu } from './UserMenu';
import { ModeSwitch } from './ModeSwitch';
import { VersionLink } from './VersionLink';
import { TABS, tabForView } from './routes';

const ICONS: Partial<Record<ViewType, React.ReactNode>> = {
  streaming: <Radio size={18} />,
  upload: <Play size={18} />,
  runs: <History size={18} />,
  usage: <Coins size={18} />,
  quality: <Gauge size={18} />,
  builder: <FileJson size={18} />,
};

// The classic shell: the same six views as the tab bar, in a left sidebar.
export const Sidebar: React.FC = () => {
  const { actions, state } = useTraceContext();
  const active = tabForView(state.currentView);

  return (
    <nav css={sidebarStyle}>
      <div css={brandStyle} onClick={() => actions.setCurrentView('streaming')}>
        <img src="/favicon.svg" alt="" css={brandMarkStyle} />
        <span css={brandNameStyle}>ego</span>
      </div>

      <div css={navListStyle}>
        {TABS.map((t) => (
          <button
            key={t.view}
            css={[navItemStyle, active?.view === t.view && activeItemStyle]}
            onClick={() => actions.setCurrentView(t.view)}
          >
            {ICONS[t.view]}
            {t.label}
          </button>
        ))}
      </div>

      <div css={footerStyle}>
        <UserMenu />
        <ModeSwitch />
        <VersionLink />
      </div>
    </nav>
  );
};

const sidebarStyle = css`
  width: 220px;
  flex-shrink: 0;
  height: 100vh;
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
  gap: 8px;
  padding: 0 16px 14px;
  border-bottom: 1px solid var(--border-default);
  margin-bottom: 12px;
  cursor: pointer;
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

const footerStyle = css`
  margin-top: auto;
  padding: 12px 16px;
  border-top: 1px solid var(--border-default);
  font-family: var(--font-mono);
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 10px;
`;
