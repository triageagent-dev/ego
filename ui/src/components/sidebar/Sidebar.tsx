import React, { useRef, useState } from 'react';
import { css } from '@emotion/react';
import { Radio, Play, FileJson, Bug, History, Coins, Gauge } from 'lucide-react';
import { useTraceContext } from '../../context/TraceContext';
import type { ViewType } from '../../lib/types';
import { BugReportModal } from '../bug-report/BugReportModal';
import { loadBugReport } from '../../api/client';
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

export const Sidebar: React.FC = () => {
  const { state, actions } = useTraceContext();
  const activeSection = getActiveSection(state.currentView);
  const [showBugReport, setShowBugReport] = useState(false);
  const [loadStatus, setLoadStatus] = useState<string | null>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const clickTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const handleLoadBugReport = async (file: File) => {
    setLoadStatus('Loading...');
    try {
      const result = await loadBugReport(file);
      setLoadStatus(`Loaded ${result.count} session${result.count !== 1 ? 's' : ''}`);
      actions.setCurrentView('streaming');
      setTimeout(() => setLoadStatus(null), 3000);
    } catch (err) {
      setLoadStatus('Load failed');
      console.error('Debug bundle load failed:', err);
      setTimeout(() => setLoadStatus(null), 3000);
    }
  };

  return (
    <>
      <nav css={sidebarStyle}>
        <div css={brandStyle} onClick={() => actions.setCurrentView('welcome')}>
          <img src="/logo.svg" alt="ego" css={brandLogoStyle} />
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
          <button
            onClick={() => {
              if (clickTimer.current) return;
              clickTimer.current = setTimeout(() => {
                clickTimer.current = null;
                setShowBugReport(true);
              }, 250);
            }}
            onDoubleClick={() => {
              if (clickTimer.current) {
                clearTimeout(clickTimer.current);
                clickTimer.current = null;
              }
              fileInputRef.current?.click();
            }}
            css={bugReportButtonStyle}
            title="Generate a debug bundle"
          >
            <Bug size={14} />
            {loadStatus ?? 'Debug bundle'}
          </button>
          <input
            ref={fileInputRef}
            type="file"
            accept=".zip"
            style={{ display: 'none' }}
            onChange={(e) => {
              const file = e.target.files?.[0];
              if (file) handleLoadBugReport(file);
              e.target.value = '';
            }}
          />
          {state.version && (
            <span style={{ fontSize: '0.5rem' }}>v{state.version}</span>
          )}
        </div>
      </nav>

      {showBugReport && (
        <BugReportModal onClose={() => setShowBugReport(false)} />
      )}
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
  padding: 0 16px 16px;
  border-bottom: 1px solid var(--border-default);
  margin-bottom: 12px;
  cursor: pointer;
`;

const brandLogoStyle = css`
  display: block;
  width: 160px;
  height: auto;
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

const bugReportButtonStyle = css`
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 6px 10px;
  border-radius: 6px;
  border: 1px solid var(--border-default);
  background: transparent;
  color: var(--text-tertiary);
  font-size: 0.75rem;
  font-weight: 500;
  cursor: pointer;
  transition: all 0.15s ease;
  font-family: var(--font-display);
  width: 100%;

  &:hover {
    color: var(--text-secondary);
    background: var(--bg-elevated);
    border-color: var(--accent-primary);
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
