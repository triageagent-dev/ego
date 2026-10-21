import { css } from '@emotion/react';

// Shared look of the data views (runs, tokens, quality): a one-line header, a
// stat strip, and titled boxes. Dense on purpose; the data is the interface.

export const pageStyle = css`
  padding: var(--page-pad);
  font-family: var(--font-body);
  color: var(--text-secondary);
`;

export const headerStyle = css`
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
  margin-bottom: 10px;
  flex-wrap: wrap;

  > div:first-of-type {
    display: flex;
    align-items: baseline;
    gap: 14px;
    flex-wrap: wrap;
    min-width: 0;
  }

  /* With the title drawn in the app's header bar, the controls are alone here. */
  > :last-child:not(:first-child) {
    margin-left: auto;
  }
`;

export const titleStyle = css`
  margin: 0;
  font-size: var(--title-size);
  font-weight: 600;
  color: var(--accent-cyan);
  text-transform: var(--title-case);
`;

export const subtitleStyle = css`
  margin: 0;
  color: var(--text-muted);
  font-size: 12px;
`;

export const refreshStyle = css`
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 3px 9px;
  border-radius: var(--radius);
  border: 1px solid var(--border-default);
  background: transparent;
  color: var(--text-tertiary);
  font-family: var(--font-mono);
  font-size: 12px;
  cursor: pointer;

  &:hover {
    color: var(--text-primary);
    border-color: var(--accent-primary);
  }
`;

export const filterRowStyle = css`
  display: flex;
  gap: 10px;
  flex-wrap: wrap;
  align-items: center;
  margin-bottom: 10px;
`;

export const segmentStyle = css`
  display: inline-flex;
  align-items: center;
  gap: 2px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius);
  padding: 1px;
  max-width: 100%;
  overflow-x: auto;
`;

export const segLabelStyle = css`
  padding: 0 8px;
  font-size: 11px;
  color: var(--text-muted);
`;

export const segBtnStyle = css`
  padding: 2px 9px;
  border: none;
  border-radius: var(--radius);
  background: transparent;
  color: var(--text-tertiary);
  font-family: var(--font-mono);
  font-size: 12px;
  cursor: pointer;
  white-space: nowrap;

  &:hover {
    color: var(--text-primary);
  }
`;

export const segActiveStyle = css`
  background: var(--bg-selected);
  color: var(--accent-magenta);
`;

export const errorStyle = css`
  padding: 8px 12px;
  margin-bottom: 10px;
  border: 1px solid var(--status-failure);
  border-radius: var(--radius);
  color: var(--status-failure);
  font-size: 12.5px;
`;


// A feature that is switched off is not a failure: same box as errorStyle, neutral colours.
export const noteStyle = css`
  padding: 8px 12px;
  margin-bottom: 10px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius);
  color: var(--text-secondary);
  font-size: 12.5px;
`;

// The stat strip: one bordered row, one cell per stat.
export const tilesStyle = css`
  display: flex;
  flex-wrap: wrap;
  border: 1px solid var(--border-default);
  border-radius: var(--radius);
  margin-bottom: 12px;
  overflow: hidden;
`;

export const tileStyle = css`
  flex: 1 1 150px;
  padding: var(--tile-pad);
  border-right: 1px solid var(--border);
  min-width: 0;

  &:last-of-type {
    border-right: none;
  }
`;

export const tileLabelStyle = css`
  font-size: 10.5px;
  color: var(--text-muted);
  text-transform: uppercase;
  letter-spacing: 0.04em;
`;

export const tileValueStyle = css`
  font-size: var(--tile-value);
  font-weight: 500;
  color: var(--text-primary);
  line-height: 1.3;
`;

export const tileSubStyle = css`
  font-size: 11px;
  color: var(--text-muted);
`;

// A box with its title inside, above the content. (The title used to sit on the top border, over a patch of the
// page colour, which showed as a dark block on a card of another colour.)
export const cardStyle = css`
  position: relative;
  border: 1px solid var(--border-default);
  border-radius: var(--radius);
  background: var(--card-surface);
  padding: 12px 14px 10px;
  margin: 0 0 12px;
`;

export const cardTitleStyle = css`
  margin: 0 0 8px;
  font-size: 13px;
  font-weight: 500;
  color: var(--accent-cyan);
  text-transform: lowercase;
  line-height: 18px;
`;
