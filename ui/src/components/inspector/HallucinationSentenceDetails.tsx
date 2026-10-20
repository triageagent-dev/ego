import React from 'react';
import { css } from '@emotion/react';
import type { HallucinationSentence } from '../../lib/types';

interface HallucinationSentenceDetailsProps {
  sentences: HallucinationSentence[];
}

// labelColor maps hallucinations_v1's five classification labels to a
// status color, matching the "supported"/"not_applicable" = pass,
// "unsupported"/"contradictory"/"disputed" = fail split the score itself
// uses (see internal/judge/hallucinations_v1.go's positive/negative label
// sets).
function labelColor(label: string): string {
  switch (label) {
    case 'supported':
    case 'not_applicable':
      return 'var(--status-success)';
    case 'unsupported':
    case 'contradictory':
    case 'disputed':
      return 'var(--status-failure)';
    default:
      return 'var(--text-secondary)';
  }
}

// HallucinationSentenceDetails renders the judge model's sentence-by-
// sentence breakdown for one hallucinations_v1 invocation - the "why"
// behind the score: each sentence of the response, whether it was
// grounded in the invocation's context, the judge's rationale, and any
// supporting/contradicting excerpt it cited.
export const HallucinationSentenceDetails: React.FC<HallucinationSentenceDetailsProps> = ({
  sentences,
}) => {
  if (sentences.length === 0) return null;

  return (
    <div css={containerStyles}>
      {sentences.map((s, idx) => (
        <div key={idx} css={sentenceCardStyles(labelColor(s.label))}>
          <div css={sentenceHeaderStyles}>
            <span css={sentenceTextStyles}>&ldquo;{s.sentence}&rdquo;</span>
            <span css={labelBadgeStyles(labelColor(s.label))}>{s.label.replace(/_/g, ' ')}</span>
          </div>
          {s.rationale && <div css={rationaleStyles}>{s.rationale}</div>}
          {s.supporting_excerpt && (
            <div css={excerptStyles}>
              <span css={excerptLabelStyles}>Supporting:</span> {s.supporting_excerpt}
            </div>
          )}
          {s.contradicting_excerpt && (
            <div css={excerptStyles}>
              <span css={excerptLabelStyles}>Contradicting:</span> {s.contradicting_excerpt}
            </div>
          )}
        </div>
      ))}
    </div>
  );
};

const containerStyles = css`
  display: flex;
  flex-direction: column;
  gap: 8px;
`;

const sentenceCardStyles = (color: string) => css`
  border-left: 3px solid ${color};
  background: var(--bg-primary);
  border-radius: 4px;
  padding: 8px 10px;
`;

const sentenceHeaderStyles = css`
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: 8px;
`;

const sentenceTextStyles = css`
  font-size: 0.813rem;
  line-height: 1.4;
  color: var(--text-primary);
  flex: 1;
`;

const labelBadgeStyles = (color: string) => css`
  flex-shrink: 0;
  font-size: 0.625rem;
  font-weight: 700;
  text-transform: uppercase;
  letter-spacing: 0.5px;
  color: ${color};
  border: 1px solid ${color};
  border-radius: 10px;
  padding: 1px 8px;
`;

const rationaleStyles = css`
  margin-top: 6px;
  font-size: 0.75rem;
  color: var(--text-secondary);
  font-style: italic;
  line-height: 1.4;
`;

const excerptStyles = css`
  margin-top: 4px;
  font-size: 0.72rem;
  color: var(--text-secondary);
  line-height: 1.4;
`;

const excerptLabelStyles = css`
  font-weight: 600;
  color: var(--text-primary);
`;
