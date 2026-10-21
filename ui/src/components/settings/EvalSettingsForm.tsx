import React from 'react';
import { css } from '@emotion/react';
import { Select, Slider } from 'antd';
import { useTraceContext } from '../../context/TraceContext';
import { JUDGE_MODELS, type JudgeModelProvider } from '../../lib/judgeModels';

/** The judge model, the pass threshold and the trajectory match type: the evaluation defaults, kept per browser. The
 * evaluate page shows the match type only while that metric is ticked; the settings page always does. */
export const EvalSettingsForm: React.FC<{ showTrajectory: boolean }> = ({ showTrajectory }) => {
  const { state, actions } = useTraceContext();
  return (
    <div>
    <div css={itemStyle}>
      <label css={labelStyle}>Judge Model</label>
      <Select
        value={state.judgeModel}
        onChange={actions.setJudgeModel}
        options={JUDGE_MODELS.map(({ value }) => ({ label: value, value }))}
        style={{ width: '100%' }}
        size="small"
      />
      {state.apiKeyStatus && (
        <div style={{ display: 'flex', gap: 10, marginTop: 4, flexWrap: 'wrap' }}>
          {([
            { label: 'GOOGLE_API_KEY', provider: 'google' as const },
            { label: 'ANTHROPIC_API_KEY', provider: 'anthropic' as const },
            { label: 'OPENAI_API_KEY', provider: 'openai' as const },
          ] as const).map(({ label, provider }) => {
            const ok = state.apiKeyStatus![provider];
            return (
              <span key={provider} style={{ display: 'flex', alignItems: 'center', gap: 4, fontSize: 10, color: ok ? 'var(--status-success)' : 'var(--status-failure)' }}>
                <span style={{ width: 6, height: 6, borderRadius: '50%', backgroundColor: ok ? 'var(--status-success)' : 'var(--status-failure)', flexShrink: 0 }} />
                {label}
              </span>
            );
          })}
        </div>
      )}
      {(() => {
        const selected = JUDGE_MODELS.find((m) => m.value === state.judgeModel);
        const provider = selected?.provider as JudgeModelProvider | undefined;
        if (!provider || !state.apiKeyStatus || state.apiKeyStatus[provider]) return null;
        const keyName = provider === 'google' ? 'GOOGLE_API_KEY / GEMINI_API_KEY'
          : provider === 'anthropic' ? 'ANTHROPIC_API_KEY'
          : 'OPENAI_API_KEY';
        return (
          <span style={{ fontSize: '11px', color: 'var(--status-failure)', marginTop: 2 }}>
            {keyName} is not set - this model will fail
          </span>
        );
      })()}
    </div>

    <div css={itemStyle}>
      <label css={labelStyle}>Pass Threshold: {state.threshold}</label>
      <Slider min={0} max={1} step={0.05} value={state.threshold} onChange={actions.setThreshold} />
      <span css={hintStyle}>Minimum score to pass</span>
    </div>

    {showTrajectory && (
      <div css={itemStyle}>
        <label css={labelStyle}>Trajectory Match Type</label>
        <Select
          value={state.trajectoryMatchType}
          onChange={actions.setTrajectoryMatchType}
          options={[
            { value: 'EXACT', label: 'EXACT - tools must match in exact order' },
            { value: 'IN_ORDER', label: 'IN_ORDER - expected tools appear in order (extras allowed)' },
            { value: 'ANY_ORDER', label: 'ANY_ORDER - expected tools appear in any order' },
          ]}
          style={{ width: '100%' }}
          size="small"
        />
        <span css={hintStyle}>How to compare tool call sequences for tool_trajectory_avg_score</span>
      </div>
    )}

    </div>
  );
};

const itemStyle = css`
  display: flex;
  flex-direction: column;
  gap: 4px;

  & + & {
    margin-top: 16px;
  }
`;

const labelStyle = css`
  font-size: 12px;
  font-weight: 500;
  color: var(--text-primary);
`;

const hintStyle = css`
  font-size: 10px;
  color: var(--text-secondary);
`;
