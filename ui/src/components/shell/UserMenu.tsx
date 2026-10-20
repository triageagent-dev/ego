import { useEffect, useState } from 'react';
import { css } from '@emotion/react';
import { User, LogOut } from 'lucide-react';
import { config } from '../../config';

interface MeState {
  authenticated: boolean;
  // false when the server runs without auth: no login to offer.
  authEnabled?: boolean;
  username?: string;
}

export function UserMenu() {
  const [me, setMe] = useState<MeState | null>(null);

  useEffect(() => {
    let active = true;
    fetch(config.api.endpoints.authMe)
      .then(res => (res.ok ? res.json() : { authenticated: false }))
      .then(data => {
        if (active) setMe(data);
      })
      .catch(() => {
        if (active) setMe({ authenticated: false });
      });
    return () => {
      active = false;
    };
  }, []);

  // Render nothing while loading rather than flashing a "logged out" state
  // that the successful check would immediately replace.
  if (!me || me.authEnabled === false) return null;

  if (!me.authenticated) {
    return (
      <a href="/auth/login" css={loginLinkStyle}>
        <User size={14} />
        Log in
      </a>
    );
  }

  return (
    <div css={userRowStyle}>
      <span css={usernameStyle} title={me.username}>
        <User size={14} />
        {me.username}
      </span>
      {/* Real navigation, not a fetch: /auth/logout redirects back to /evals
       * after clearing the session cookie, which a plain click handles
       * cleanly without going through the global 401-redirect interceptor. */}
      <a href="/auth/logout" css={logoutButtonStyle} title="Log out">
        <LogOut size={13} />
      </a>
    </div>
  );
}

const userRowStyle = css`
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 6px 10px;
  border-radius: var(--radius);
  border: 1px solid var(--border-default);
  font-size: 0.75rem;
  font-family: var(--font-display);
`;

const usernameStyle = css`
  display: flex;
  align-items: center;
  gap: 6px;
  color: var(--text-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
`;

const logoutButtonStyle = css`
  display: flex;
  align-items: center;
  color: var(--text-tertiary);
  cursor: pointer;
  transition: color 0.15s ease;
  flex-shrink: 0;

  &:hover {
    color: var(--status-failure);
  }
`;

const loginLinkStyle = css`
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 6px 10px;
  border-radius: var(--radius);
  border: 1px solid var(--border-default);
  background: transparent;
  color: var(--accent-primary);
  font-size: 0.75rem;
  font-weight: 600;
  font-family: var(--font-display);
  text-decoration: none;
  transition: all 0.15s ease;

  &:hover {
    background: var(--bg-elevated);
  }
`;
