import React from 'react';
import { css } from '@emotion/react';
import { useUiMode, type UiMode } from '../../lib/mode';

const MODES: UiMode[] = ['tui', 'classic'];

export const ModeSwitch: React.FC = () => {
  const [mode, setMode] = useUiMode();
  return (
    <div css={groupStyle} role="group" aria-label="Interface style" title="Interface style (t)">
      {MODES.map((m) => (
        <button key={m} css={[btnStyle, mode === m && activeStyle]} onClick={() => setMode(m)} aria-pressed={mode === m}>
          {m}
        </button>
      ))}
    </div>
  );
};

const groupStyle = css`
  display: inline-flex;
  border: 1px solid var(--border-default);
  border-radius: var(--radius);
  padding: 1px;
`;

const btnStyle = css`
  padding: 1px 8px;
  border: none;
  border-radius: var(--radius);
  background: transparent;
  color: var(--text-tertiary);
  font: inherit;
  font-size: 11.5px;
  cursor: pointer;

  &:hover {
    color: var(--text-primary);
  }
`;

const activeStyle = css`
  background: var(--bg-selected);
  color: var(--accent-magenta);
`;
