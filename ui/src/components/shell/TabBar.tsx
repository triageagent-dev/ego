import React from 'react';
import { css } from '@emotion/react';
import { useTraceContext } from '../../context/TraceContext';
import { REPO_URL } from '../../config';
import { UserMenu } from './UserMenu';
import { TABS, tabForView } from './routes';

// A release version links to its tag, a build "x.y.z-sha" to its commit, anything else to the repo.
function versionUrl(v: string): string {
  const sha = v.match(/-([0-9a-f]{7,40})$/)?.[1];
  if (sha) return `${REPO_URL}/commit/${sha}`;
  if (/^\d+\.\d+\.\d+/.test(v)) return `${REPO_URL}/releases/tag/v${v}`;
  return REPO_URL;
}

const GithubMark: React.FC = () => (
  <svg width="14" height="14" viewBox="0 0 16 16" fill="currentColor" aria-hidden="true">
    <path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.013 8.013 0 0016 8c0-4.42-3.58-8-8-8z" />
  </svg>
);

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
        {state.version && (
          <a href={versionUrl(state.version)} target="_blank" rel="noreferrer" css={versionStyle} title="ego on GitHub">
            <GithubMark />
            {/^\d/.test(state.version) ? `v${state.version}` : state.version}
          </a>
        )}
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

const versionStyle = css`
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: var(--text-muted);
  font-size: 11.5px;

  &:hover {
    color: var(--accent-primary);
  }
`;
