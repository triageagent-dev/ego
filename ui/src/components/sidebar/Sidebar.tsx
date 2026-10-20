import React from 'react';
import { css } from '@emotion/react';
import { Radio, Play, FileJson, History, Coins, Gauge } from 'lucide-react';
import { useTraceContext } from '../../context/TraceContext';
import type { ViewType } from '../../lib/types';
import { REPO_URL } from '../../config';
import { UserMenu } from './UserMenu';

type SidebarSection = 'streaming' | 'offline' | 'builder' | 'runs' | 'usage' | 'quality';

function getActiveSection(currentView: ViewType): SidebarSection | null {
  switch (currentView) {
    case 'streaming': return 'streaming';
    case 'upload':
    case 'dashboard':
    case 'inspector':
    case 'comparison':
      return 'offline';
    case 'builder': return 'builder';
    case 'runs': return 'runs';
    case 'usage': return 'usage';
    case 'quality': return 'quality';
    default: return null;
  }
}

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

export const Sidebar: React.FC = () => {
  const { state, actions } = useTraceContext();
  const activeSection = getActiveSection(state.currentView);
  return (
    <>
      <nav css={sidebarStyle}>
        <div css={brandStyle} onClick={() => actions.setCurrentView('welcome')}>
          <img src="/favicon.svg" alt="" css={brandMarkStyle} />
          <span css={brandNameStyle}>ego</span>
        </div>

        <div css={navListStyle}>
          <button
            css={[navItemStyle, activeSection === 'streaming' && activeItemStyle]}
            onClick={() => actions.setCurrentView('streaming')}
          >
            <Radio size={18} />
            Live
          </button>

          <button
            css={[navItemStyle, activeSection === 'offline' && activeItemStyle]}
            onClick={() => actions.setCurrentView('upload')}
          >
            <Play size={18} />
            Evaluate
          </button>

          <button
            css={[navItemStyle, activeSection === 'runs' && activeItemStyle]}
            onClick={() => actions.setCurrentView('runs')}
          >
            <History size={18} />
            Runs
          </button>

          <button
            css={[navItemStyle, activeSection === 'usage' && activeItemStyle]}
            onClick={() => actions.setCurrentView('usage')}
          >
            <Coins size={18} />
            Usage
          </button>

          <button
            css={[navItemStyle, activeSection === 'quality' && activeItemStyle]}
            onClick={() => actions.setCurrentView('quality')}
          >
            <Gauge size={18} />
            Quality
          </button>

          <button
            css={[navItemStyle, activeSection === 'builder' && activeItemStyle]}
            onClick={() => actions.setCurrentView('builder')}
          >
            <FileJson size={18} />
            Eval sets
          </button>
        </div>

        <div css={footerStyle}>
          <UserMenu />
          {state.version && (
            <a
              href={versionUrl(state.version)}
              target="_blank"
              rel="noreferrer"
              css={versionLinkStyle}
              title="ego on GitHub"
            >
              <GithubMark />
              {/^\d/.test(state.version) ? `v${state.version}` : state.version}
            </a>
          )}
        </div>
      </nav>
    </>
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
  border-radius: 6px;
  border: none;
  border-left: 3px solid transparent;
  background: transparent;
  color: var(--text-secondary);
  font-size: 0.875rem;
  font-weight: 500;
  font-family: var(--font-display);
  cursor: pointer;
  width: 100%;
  text-align: left;
  transition: all 0.15s ease;

  &:hover {
    color: var(--text-primary);
    background: var(--bg-elevated);
  }
`;

const footerStyle = css`
  margin-top: auto;
  padding: 12px 16px;
  border-top: 1px solid var(--border-default);
  font-size: 0.75rem;
  color: var(--text-tertiary);
  font-family: var(--font-mono);
  display: flex;
  flex-direction: column;
  gap: 8px;
`;

const versionLinkStyle = css`
  display: inline-flex;
  align-items: center;
  gap: 6px;
  align-self: flex-start;
  color: var(--text-tertiary);
  font-size: 0.7rem;
  text-decoration: none;
  transition: color 0.15s ease;

  &:hover {
    color: var(--accent-primary);
  }
`;

const activeItemStyle = css`
  color: var(--accent-primary);
  background: rgba(168, 85, 247, 0.06);
  border-left-color: var(--accent-primary);
  font-weight: 600;

  &:hover {
    color: var(--accent-primary);
    background: rgba(168, 85, 247, 0.1);
  }
`;
