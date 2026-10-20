import React from 'react';
import { css, keyframes } from '@emotion/react';
import { connectionLevel, describe, useConnection } from '../../lib/connection';

const COLORS = { ok: 'var(--status-success)', warn: 'var(--status-warning)', down: 'var(--status-failure)' } as const;

// A dot for the link to the server: green when connected, amber while the live feed reconnects, red when the
// server does not answer.
export const ConnectionDot: React.FC = () => {
  const conn = useConnection();
  const level = connectionLevel(conn);
  return (
    <span
      css={[dotStyle, level === 'warn' && pulseStyle]}
      style={{ background: COLORS[level] }}
      title={describe(conn)}
      role="status"
      aria-label={describe(conn)}
    />
  );
};

const pulse = keyframes`
  0%, 100% { opacity: 1; }
  50% { opacity: 0.35; }
`;

const dotStyle = css`
  display: inline-block;
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex-shrink: 0;
`;

const pulseStyle = css`
  animation: ${pulse} 1.4s ease-in-out infinite;
`;
