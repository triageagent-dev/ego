import React from 'react';
import { css } from '@emotion/react';
import type { OnlineSessionScore } from '../../lib/types';

// What stands behind one online score: the judge's reasons, item by item, failing ones first. A person ranking the
// score (the thumbs) needs this in front of them, not only the number.
const call = (c: { tool: string; args?: Record<string, unknown> }) =>
  `${c.tool}(${Object.entries(c.args ?? {}).map(([k, v]) => `${k}=${JSON.stringify(v)}`).join(', ')})`;

const LABEL_COLOR: Record<string, string> = {
  supported: 'var(--status-success)',
  yes: 'var(--status-success)',
  not_applicable: 'var(--text-tertiary)',
  unsupported: 'var(--status-failure)',
  contradictory: 'var(--status-failure)',
  disputed: 'var(--status-warning)',
  no: 'var(--status-failure)',
};

export const ScoreDetails: React.FC<{ s: OnlineSessionScore }> = ({ s }) => {
  const empty = !s.rationale && !s.error && !s.sentences?.length && !s.rubrics?.length && !s.comparisons?.length && !s.decisions?.length;
  return (
    <div css={boxStyle}>
      {s.failing !== undefined && s.total ? (
        <div css={summaryStyle}>{s.failing} of {s.total} item{s.total === 1 ? '' : 's'} failed</div>
      ) : null}
      {s.error && <div css={errorStyle}>{s.error}</div>}
      {s.rationale && <div css={rationaleStyle}>{s.rationale}</div>}

      {s.sentences?.map((x, i) => (
        <div key={i} css={itemStyle}>
          <span css={tagStyle} style={{ color: LABEL_COLOR[x.label] ?? 'var(--text-secondary)' }}>{x.label}</span>
          <div>
            <div>{x.sentence}</div>
            {x.rationale && <div css={dimStyle}>{x.rationale}</div>}
            {x.supporting_excerpt && <div css={quoteStyle}>supports: {x.supporting_excerpt}</div>}
            {x.contradicting_excerpt && <div css={quoteStyle}>contradicts: {x.contradicting_excerpt}</div>}
          </div>
        </div>
      ))}

      {s.rubrics?.map((x, i) => (
        <div key={i} css={itemStyle}>
          <span css={tagStyle} style={{ color: LABEL_COLOR[x.verdict] ?? 'var(--text-secondary)' }}>{x.verdict}</span>
          <div>
            <div>{x.rubric}</div>
            {x.rationale && <div css={dimStyle}>{x.rationale}</div>}
          </div>
        </div>
      ))}

      {s.comparisons?.map((x, i) => (
        <div key={i} css={itemStyle}>
          <span css={tagStyle} style={{ color: x.matched ? 'var(--status-success)' : 'var(--status-failure)' }}>{x.matched ? 'match' : 'differs'}</span>
          <div css={monoStyle}>
            <div>expected: {x.expected.length ? x.expected.map(call).join(' → ') : 'no tools'}</div>
            <div>actual: {x.actual.length ? x.actual.map(call).join(' → ') : 'no tools'}</div>
          </div>
        </div>
      ))}

      {s.decisions?.map((x, i) => (
        <div key={i} css={itemStyle}>
          <span css={tagStyle}>{x.score === null ? '–' : x.score.toFixed(2)}</span>
          <div>
            {x.question && <div>{x.question}</div>}
            {x.answer && <div css={dimStyle}>{x.answer}</div>}
          </div>
        </div>
      ))}

      {s.truncated ? <div css={dimStyle}>{s.truncated} more not shown</div> : null}
      {empty && <div css={dimStyle}>The judge stored no reasons for this score.</div>}
    </div>
  );
};

const boxStyle = css`
  grid-column: 1 / -1;
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 8px 10px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius);
  background: var(--bg-primary);
  font-size: 12.5px;
  line-height: 1.45;
  color: var(--text-secondary);
`;

const summaryStyle = css`
  color: var(--text-primary);
  font-weight: 500;
`;

const rationaleStyle = css`
  color: var(--text-primary);
`;

const errorStyle = css`
  color: var(--status-failure);
`;

const itemStyle = css`
  display: grid;
  grid-template-columns: 82px minmax(0, 1fr);
  gap: 10px;
  padding-top: 6px;
  border-top: 1px solid var(--border-subtle);
`;

const tagStyle = css`
  font-family: var(--font-mono);
  font-size: 11.5px;
  text-transform: lowercase;
`;

const dimStyle = css`
  color: var(--text-muted);
`;

const quoteStyle = css`
  color: var(--text-muted);
  font-style: italic;
`;

const monoStyle = css`
  font-family: var(--font-mono);
  font-size: 11.5px;
  overflow-wrap: anywhere;
`;
