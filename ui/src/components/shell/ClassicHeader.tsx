import React from 'react';
import { css } from '@emotion/react';
import { AccountMenu } from './AccountMenu';
import { TenantSwitch } from './TenantSwitch';
import { setTitleSlot } from './PageTitle';

// The top bar of the classic layout: the title of the view on the left (drawn by the view, see PageTitle), the
// tenant the views show and the account menu on the right.
export const ClassicHeader: React.FC = () => (
  <header css={headerStyle}>
    <div ref={setTitleSlot} css={titleSlotStyle} />
    <TenantSwitch />
    <AccountMenu />
  </header>
);

const headerStyle = css`
  flex-shrink: 0;
  display: flex;
  align-items: center;
  gap: 14px;
  height: 46px;
  padding: 0 18px;
  background: var(--bg-surface);
  border-bottom: 1px solid var(--border-default);
`;

const titleSlotStyle = css`
  display: flex;
  align-items: baseline;
  gap: 14px;
  flex: 1;
  min-width: 0;
  overflow: hidden;

  p {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
`;
