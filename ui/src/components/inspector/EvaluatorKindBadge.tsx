import React from 'react';
import { css } from '@emotion/react';
import { Cpu, Sparkles, Cloud } from 'lucide-react';
import type { EvaluatorKind } from '../../lib/types';

interface EvaluatorKindBadgeProps {
  kind: EvaluatorKind;
  judgeModel?: string;
}

// EvaluatorKindBadge answers "how was this score produced?": a
// deterministic algorithm running locally, a live LLM-as-judge call (and
// which model), or Vertex AI's Managed Eval Service.
export const EvaluatorKindBadge: React.FC<EvaluatorKindBadgeProps> = ({ kind, judgeModel }) => {
  if (kind === 'llm_judge') {
    return (
      <span css={badgeStyles} title="Scored by a live LLM-as-judge model call">
        <Sparkles size={11} />
        LLM Judge{judgeModel ? ` · ${judgeModel}` : ''}
      </span>
    );
  }
  if (kind === 'vertex_judge') {
    return (
      <span css={badgeStyles} title="Scored by Vertex AI's Managed Eval Service">
        <Cloud size={11} />
        Vertex AI Judge
      </span>
    );
  }
  return (
    <span css={badgeStyles} title="Scored by a deterministic, non-LLM algorithm">
      <Cpu size={11} />
      Algorithm
    </span>
  );
};

const badgeStyles = css`
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-size: 0.688rem;
  font-weight: 600;
  color: var(--text-secondary);
  background: var(--bg-primary);
  border: 1px solid var(--border-default);
  border-radius: 10px;
  padding: 2px 8px;
  white-space: nowrap;
`;
