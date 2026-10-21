import React from 'react';
import { css } from '@emotion/react';

export const PAGE_SIZE = 50;

interface PagerProps {
  total: number;
  page: number; // zero-based
  onChange: (page: number) => void;
  pageSize?: number;
}

// Page count for total items; at least one page.
export function pageCount(total: number, pageSize: number = PAGE_SIZE): number {
  return Math.max(1, Math.ceil(total / pageSize));
}

// Keeps page inside what exists, e.g. after a filter shrinks the list.
export function clampPage(page: number, total: number, pageSize: number = PAGE_SIZE): number {
  return Math.min(Math.max(0, page), pageCount(total, pageSize) - 1);
}

// Renders nothing until there is more than one page.
export const Pager: React.FC<PagerProps> = ({ total, page, onChange, pageSize = PAGE_SIZE }) => {
  const pages = pageCount(total, pageSize);
  if (pages <= 1) return null;
  const from = page * pageSize + 1;
  const to = Math.min(total, (page + 1) * pageSize);
  return (
    <nav css={pagerStyle} aria-label="Pagination">
      <button onClick={() => onChange(0)} disabled={page === 0} title="First page">«</button>
      <button onClick={() => onChange(page - 1)} disabled={page === 0} title="Previous page">‹</button>
      <span>
        {from}–{to} of {total} <span css={dimStyle}>· page {page + 1}/{pages}</span>
      </span>
      <button onClick={() => onChange(page + 1)} disabled={page >= pages - 1} title="Next page">›</button>
      <button onClick={() => onChange(pages - 1)} disabled={page >= pages - 1} title="Last page">»</button>
    </nav>
  );
};

interface CursorPagerProps {
  page: number; // zero-based
  hasNext: boolean;
  onFirst: () => void;
  onPrev: () => void;
  onNext: () => void;
  // How many items match, as counted by the server when the first page was fetched; approx when that is a lower bound.
  total?: number;
  approx?: boolean;
  pageSize?: number;
  // Sits in a row of other controls: no padding of its own.
  inline?: boolean;
}

// Pager for a list the server pages with a cursor: pages are walked one at a time, newest first, and the page count
// comes from the total the server counted.
export const CursorPager: React.FC<CursorPagerProps> = ({ page, hasNext, onFirst, onPrev, onNext, total, approx, pageSize = PAGE_SIZE, inline }) => {
  if (page === 0 && !hasNext) return null;
  const more = approx ? '+' : '';
  const pages = total ? Math.max(Math.ceil(total / pageSize), page + 1) : null;
  return (
    <nav css={[pagerStyle, inline && inlinePagerStyle]} aria-label="Pagination">
      <button onClick={onFirst} disabled={page === 0} title="Newest">«</button>
      <button onClick={onPrev} disabled={page === 0} title="Newer">‹</button>
      <span>
        page {page + 1}{pages ? ` of ${pages.toLocaleString()}${more}` : ''}
        {total ? <span css={dimStyle}> · {total.toLocaleString()}{more} sessions</span> : null}
      </span>
      <button onClick={onNext} disabled={!hasNext} title="Older">›</button>
    </nav>
  );
};

const inlinePagerStyle = css`
  padding: 0;
  font-size: 12px;

  button {
    min-width: 0;
    padding: 3px 9px;
    font-size: 12px;
    line-height: normal;
  }
`;

const pagerStyle = css`
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  padding: 8px 0;
  font-size: 11.5px;
  color: var(--text-secondary);

  button {
    min-width: 24px;
    padding: 1px 6px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius);
    background: transparent;
    color: var(--accent-primary);
    font: inherit;
    cursor: pointer;
  }
  button:disabled {
    color: var(--text-muted);
    cursor: default;
    opacity: 0.5;
  }
  button:not(:disabled):hover {
    border-color: var(--accent-primary);
  }
`;

const dimStyle = css`
  color: var(--text-muted);
`;
