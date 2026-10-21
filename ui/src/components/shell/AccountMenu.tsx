import React, { useEffect, useRef, useState } from 'react';
import { css } from '@emotion/react';
import { ChevronDown, LogIn, LogOut, User } from 'lucide-react';
import { useMe } from '../../lib/me';
import { describe, useConnection } from '../../lib/connection';
import { ConnectionDot } from './ConnectionDot';
import { ModeSwitch } from './ModeSwitch';
import { VersionLink } from './VersionLink';

// The classic header's account menu: who is signed in, with the interface style, the link to the server and the
// version under it, and the way out. It is there without a sign-in too (a local server has none), as the home of
// the style switch and the status.
export const AccountMenu: React.FC = () => {
  const me = useMe();
  const conn = useConnection();
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false);
    };
    document.addEventListener('mousedown', onDown);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onDown);
      document.removeEventListener('keydown', onKey);
    };
  }, [open]);

  const signedIn = !!me?.authenticated;
  const canSignIn = me?.authEnabled !== false && me !== null && !me.authenticated;

  return (
    <div css={rootStyle} ref={rootRef}>
      <button css={triggerStyle} onClick={() => setOpen((o) => !o)} aria-haspopup="menu" aria-expanded={open} title={describe(conn)}>
        <ConnectionDot />
        <User size={14} />
        {signedIn && <span css={nameStyle}>{me?.username}</span>}
        <ChevronDown size={13} />
      </button>

      {open && (
        <div css={panelStyle} role="menu">
          {signedIn && (
            <div css={rowStyle}>
              <span css={mutedStyle}>signed in as</span>
              <span>
                {me?.username}
                {me?.role === 'none' && (
                  <span css={noAccessStyle} title="No role binding covers you: ask an admin to add your GitHub team"> no access</span>
                )}
              </span>
            </div>
          )}
          <div css={rowStyle}>
            <span css={mutedStyle}>interface</span>
            <ModeSwitch />
          </div>
          <div css={rowStyle}>
            <span css={mutedStyle}>server</span>
            <span css={statusStyle}>
              <ConnectionDot />
              <VersionLink />
            </span>
          </div>
          {(signedIn || canSignIn) && <div css={dividerStyle} />}
          {signedIn && (
            // A form post, not a link: logout changes state, and the server accepts it only as POST.
            <form method="post" action="/auth/logout" css={formStyle}>
              <button type="submit" css={itemStyle}>
                <LogOut size={14} />
                log out
              </button>
            </form>
          )}
          {canSignIn && (
            <a href="/auth/login" css={itemStyle}>
              <LogIn size={14} />
              log in
            </a>
          )}
        </div>
      )}
    </div>
  );
};

const rootStyle = css`
  position: relative;
`;

const triggerStyle = css`
  display: flex;
  align-items: center;
  gap: 8px;
  height: 30px;
  padding: 0 10px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius);
  background: transparent;
  color: var(--text-secondary);
  font-family: var(--font-display);
  font-size: 0.8rem;
  cursor: pointer;

  &:hover {
    color: var(--text-primary);
    background: var(--bg-elevated);
  }
`;

const nameStyle = css`
  max-width: 160px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
`;

const panelStyle = css`
  position: absolute;
  right: 0;
  top: calc(100% + 6px);
  z-index: 50;
  min-width: 240px;
  padding: 6px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius);
  background: var(--bg-surface);
  box-shadow: 0 6px 24px rgba(0, 0, 0, 0.35);
  font-family: var(--font-display);
  font-size: 0.8rem;
  color: var(--text-primary);
`;

const rowStyle = css`
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 7px 8px;
`;

const mutedStyle = css`
  color: var(--text-tertiary);
`;

const statusStyle = css`
  display: inline-flex;
  align-items: center;
  gap: 8px;
`;

const noAccessStyle = css`
  color: var(--status-failure);
`;

const dividerStyle = css`
  height: 1px;
  margin: 4px 0;
  background: var(--border-default);
`;

const formStyle = css`
  margin: 0;
`;

const itemStyle = css`
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 7px 8px;
  border: none;
  border-radius: var(--radius);
  background: transparent;
  color: var(--text-secondary);
  font: inherit;
  text-decoration: none;
  text-align: left;
  cursor: pointer;

  &:hover {
    color: var(--text-primary);
    background: var(--bg-elevated);
  }
`;
