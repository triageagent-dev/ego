import React from 'react';
import { css } from '@emotion/react';
import { FileJson, Play, Radio } from 'lucide-react';
import { useTraceContext } from '../../context/TraceContext';

export const WelcomeView: React.FC = () => {
  const { actions } = useTraceContext();

  const handleGetStarted = () => {
    actions.setCurrentView('builder');
  };

  const handleExpertMode = () => {
    actions.setCurrentView('upload');
  };

  const handleLiveStreaming = () => {
    actions.setCurrentView('streaming');
  };

  return (
    <div css={containerStyle}>
      <div css={contentStyle}>
        <div css={headerStyle}>
          <img src="/logo.svg" alt="ego" css={logoStyle} />
          <p>Reality testing for AI agents</p>
        </div>

        <div css={optionsGridStyle}>
          <button css={optionCardStyle} onClick={handleLiveStreaming}>
            <div css={iconWrapperStyle}>
              <Radio size={48} />
            </div>
            <h3>Watch live sessions</h3>
            <p>Traces, conversations and scores as they arrive</p>
          </button>

          <button css={optionCardStyle} onClick={handleExpertMode}>
            <div css={iconWrapperStyle}>
              <Play size={48} />
            </div>
            <h3>Evaluate</h3>
            <p>Upload traces and eval sets to run batch evaluations</p>
          </button>

          <button css={optionCardStyle} onClick={handleGetStarted}>
            <div css={iconWrapperStyle}>
              <FileJson size={48} />
            </div>
            <h3>Eval sets</h3>
            <p>Turn traces into golden eval sets</p>
          </button>
        </div>
      </div>
    </div>
  );
};

const containerStyle = css`
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px;
`;

const contentStyle = css`
  max-width: 1000px;
  width: 100%;
`;

const headerStyle = css`
  text-align: center;
  margin-bottom: 48px;
  display: flex;
  flex-direction: column;
  align-items: center;

  p {
    font-size: 1.125rem;
    color: var(--text-secondary);
    margin: 0;
  }
`;

const logoStyle = css`
  width: 240px;
  height: auto;
  margin-bottom: 16px;
`;

const optionsGridStyle = css`
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 24px;

  @media (max-width: 1024px) {
    grid-template-columns: 1fr;
  }
`;

const optionCardStyle = css`
  background: var(--bg-surface);
  border: 2px solid var(--border-default);
  border-radius: 16px;
  padding: 40px 32px;
  cursor: pointer;
  transition: all 0.3s ease;
  text-align: center;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 16px;

  &:hover {
    transform: translateY(-8px);
    border-color: var(--accent-primary);
    box-shadow: 0 16px 48px rgba(168, 85, 247, 0.2);
    background: var(--bg-elevated);
  }

  h3 {
    font-size: 1.25rem;
    font-weight: 600;
    color: var(--text-primary);
    margin: 0;
  }

  p {
    font-size: 1rem;
    color: var(--text-secondary);
    margin: 0;
    line-height: 1.5;
  }
`;

const iconWrapperStyle = css`
  width: 80px;
  height: 80px;
  border-radius: 50%;
  background: linear-gradient(135deg, rgba(168, 85, 247, 0.1) 0%, rgba(109, 40, 217, 0.1) 100%);
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--accent-primary);
  transition: all 0.3s ease;

  button:hover & {
    transform: scale(1.1);
    background: linear-gradient(135deg, rgba(168, 85, 247, 0.2) 0%, rgba(109, 40, 217, 0.2) 100%);
  }
`;
