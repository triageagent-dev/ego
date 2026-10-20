import React from 'react';
import { css } from '@emotion/react';
import { useTraceContext } from '../../context/TraceContext';
import { UserMenu } from './UserMenu';
import { ModeSwitch } from './ModeSwitch';
import { VersionLink } from './VersionLink';
import { TABS, tabForView } from './routes';

export const TabBar: React.FC = () => {
  const { state, actions } = useTraceContext();
  const active = tabForView(state.currentView);

  return (
    <nav css={barStyle}>
      <button css={brandStyle} onClick={() => actions.setCurrentView('streaming')} title="sessions">
        <img src="/favicon.svg" alt="" width={18} height={18} />
        <span>ego</span>
      </button>

      <div css={tabsStyle}>
        {TABS.map((t) => (
          <button
            key={t.view}
            css={[tabStyle, active?.view === t.view && activeTabStyle]}
            onClick={() => actions.setCurrentView(t.view)}
            title={`${t.label} (${t.key})`}
          >
            <kbd>{t.key}</kbd>
            {t.label}
          </button>
        ))}
      </div>

      <div css={rightStyle}>
        <UserMenu />
        <ModeSwitch />
        <VersionLink />
      </div>
    </nav>
  );
};

const barStyle = css`
  height: 36px;
  flex-shrink: 0;
  display: flex;
  align-items: stretch;
  gap: 18px;
  padding: 0 14px;
  background: var(--bg-surface);
  border-bottom: 1px solid var(--border);
  font-size: 12.5px;
`;

const brandStyle = css`
  display: flex;
  align-items: center;
  gap: 8px;
  background: none;
  border: none;
  cursor: pointer;
  color: var(--text-primary);
  font: inherit;
  font-weight: 600;
`;

const tabsStyle = css`
  display: flex;
  align-items: stretch;
  gap: 2px;
  min-width: 0;
  overflow-x: auto;
`;

const tabStyle = css`
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 0 10px;
  background: none;
  border: none;
  border-bottom: 2px solid transparent;
  color: var(--text-tertiary);
  font: inherit;
  cursor: pointer;
  white-space: nowrap;

  kbd {
    font: inherit;
    font-size: 11px;
    color: var(--text-muted);
  }

  &:hover {
    color: var(--text-primary);
    background: var(--bg-elevated);
  }
`;

const activeTabStyle = css`
  color: var(--accent-magenta);
  border-bottom-color: var(--accent-magenta);

  kbd {
    color: var(--accent-magenta);
  }

  &:hover {
    color: var(--accent-magenta);
  }
`;

const rightStyle = css`
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 14px;
`;

