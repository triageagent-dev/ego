import React from 'react';
import { css } from '@emotion/react';
import { AccountMenu } from './AccountMenu';
import { TenantSwitch } from './TenantSwitch';

// The top bar of the classic layout: the tenant the views show, and the account menu, on the right.
export const ClassicHeader: React.FC = () => (
  <header css={headerStyle}>
    <TenantSwitch />
    <AccountMenu />
  </header>
);

const headerStyle = css`
  flex-shrink: 0;
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 14px;
  height: 46px;
  padding: 0 18px;
  background: var(--bg-surface);
  border-bottom: 1px solid var(--border-default);
`;
