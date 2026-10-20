import { useEffect, useState } from 'react';
import { css } from '@emotion/react';
import { AlertCircle } from 'lucide-react';
import { onSessionExpired } from '../lib/network-capture';

export function SessionExpiredBanner() {
  const [expired, setExpired] = useState(false);

  useEffect(() => onSessionExpired(() => setExpired(true)), []);

  if (!expired) return null;

  return (
    <div css={bannerStyle}>
      <AlertCircle size={16} />
      <span>Your session has expired.</span>
      {/* A real anchor, clicked by the person - unlike a script-triggered
       * window.location redirect, this carries a genuine user gesture and
       * won't get blocked following the server's redirect to GitHub. */}
      <a href="/auth/login" css={linkStyle}>Log in again</a>
    </div>
  );
}

const bannerStyle = css`
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  z-index: 1000;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  padding: 10px 16px;
  background: var(--status-failure);
  color: #fff;
  font-size: 0.8125rem;
  font-weight: 600;
`;

const linkStyle = css`
  color: #fff;
  text-decoration: underline;
  font-weight: 700;

  &:hover {
    opacity: 0.85;
  }
`;
