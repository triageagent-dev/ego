import React, { useEffect, useState } from 'react';
import { Button, Select, Slider } from 'antd';
import { css } from '@emotion/react';
import { Play, Pencil } from 'lucide-react';
import { FileDropZone } from './FileDropZone';
import { MetricGroup, useMetricGroups } from './MetricSelector';
import { SessionPicker } from './SessionPicker';
import { ago, blurbFor, groupStats } from './metricInsights';
import { listRuns } from '../../api/client';
import type { Run } from '../../lib/types';
import { SessionTraceFile } from '../../lib/sessionFiles';
import { EvalSetEditorDrawer } from './EvalSetEditorDrawer';
import { TraceEditorDrawer } from './TraceEditorDrawer';
import { useTraceContext } from '../../context/TraceContext';
import { PageTitle } from '../shell/PageTitle';
import { subtitleStyle, titleStyle } from '../ui/page';

const uploadViewStyle = css`
  padding: 12px 18px 24px;
  min-height: 100%;

  .header {
    margin-bottom: 8px;
    position: relative;
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 6px 14px;
  }

  .spinner {
    width: 12px;
    height: 12px;
    border: 2px solid var(--bg-elevated);
    border-top-color: var(--accent-primary);
    border-radius: 50%;
    animation: spin 0.6s linear infinite;
  }

  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }

  .run-button {
    width: 100%;
    height: 34px;
    font-size: 13px;
    font-weight: 600;
    padding: 0 22px;
    border-radius: var(--radius);
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 10px;
    background-color: var(--accent-primary) !important;
    border-color: var(--accent-primary) !important;
    color: #000 !important;
    transition: all 0.15s ease;

    &:hover:not(:disabled) {
      filter: brightness(1.1);
      background-color: var(--accent-primary) !important;
      border-color: var(--accent-primary) !important;
    }

    &:disabled {
      opacity: 0.4;
      box-shadow: none;
      transform: none;
      background-color: var(--bg-surface) !important;
      border-color: var(--border-default) !important;
      color: var(--text-secondary) !important;
    }
  }

  .setting-item {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .setting-label {
    font-size: 12px;
    font-weight: 500;
    color: var(--text-primary);
  }

  .setting-hint {
    font-size: 10px;
    color: var(--text-secondary);
  }
`;

// The page is a list and its details, as the sessions page is: each step of an evaluation is a card on the left with
// what it holds, the open one shows its controls on the right.
// The traces and the golden set share one details panel, one under the other.
const boxStyle = css`
  & + & {
    margin-top: 18px;
    padding-top: 14px;
    border-top: 1px solid var(--border-default);
  }
`;

const splitStyle = css`
  display: flex;
  align-items: flex-start;
  gap: 14px;
`;

const listStyle = css`
  flex: 0 0 540px;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
`;

const cardStyle = css`
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  gap: 6px 14px;
  padding: 8px 12px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius);
  background: var(--card-bg);
  color: inherit;
  font: inherit;
  text-align: left;
  cursor: pointer;
  transition: all 0.2s;
  min-width: 0;

  &:hover {
    background: var(--bg-elevated);
  }
`;

const cardMainStyle = css`
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 0;
`;

// What the group is for and what past runs say about it, beside the metrics.
const cardSideStyle = css`
  display: flex;
  flex-direction: column;
  gap: 5px;
  min-width: 0;
  padding-left: 12px;
  border-left: 1px solid var(--border-default);
  font-size: 12px;
  line-height: 1.45;
  color: var(--text-secondary);
`;

const sideWhenStyle = css`
  color: var(--text-muted);
`;

const sideWarnStyle = css`
  color: #fb923c;
`;

const sideStatsStyle = css`
  margin-top: auto;
  font-family: var(--font-mono);
  font-size: 11.5px;
  color: var(--text-tertiary);
`;

const cardOpenStyle = css`
  background: var(--bg-elevated);
  border-color: var(--accent-primary);
`;

const cardTitleStyle = css`
  display: flex;
  align-items: center;
  gap: 8px;
  color: var(--accent-cyan);
  font-size: 13px;
  text-transform: lowercase;
`;

// What is behind the card, in words: the line under its chips.
const descStyle = css`
  color: var(--text-muted);
  font-size: 12px;
  line-height: 1.45;
  overflow-wrap: anywhere;
`;

const descOnStyle = css`
  color: var(--status-success);
`;

const chipsStyle = css`
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  min-width: 0;
`;

const chipStyle = css`
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  padding: 1px 8px;
  border-radius: var(--radius);
  background: var(--bg-elevated);
  color: var(--text-secondary);
  font-size: 11.5px;
  line-height: 18px;
`;

const chipGoodStyle = css`
  color: var(--status-success);
`;

const chipMutedStyle = css`
  background: transparent;
  color: var(--text-muted);
  padding-left: 0;
`;

const detailsStyle = css`
  flex: 1;
  min-width: 420px;
  position: sticky;
  top: 0;
  max-height: calc(var(--view-h, 100vh) - 70px);
  overflow-y: auto;
  padding: 12px 14px;
  background: var(--card-bg);
  border: 1px solid var(--border-default);
  border-radius: var(--radius);
`;

const detailsTitleStyle = css`
  margin: 0 0 4px;
  color: var(--accent-cyan);
  font-size: 13px;
  font-weight: 400;
  text-transform: lowercase;
`;

const detailsHintStyle = css`
  margin: 0 0 12px;
  color: var(--text-muted);
  font-size: 12px;
`;

const fileBoxStyle = css`
  margin-top: 12px;
  padding: 10px;
  background: var(--bg-elevated);
  border-radius: var(--radius);
  border: 1px solid var(--border-default);
`;

const fileRowStyle = css`
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 3px 6px;
  background: var(--bg-surface);
  border-radius: var(--radius);
  color: var(--text-secondary);
  font-family: monospace;
  font-size: 11px;
  cursor: pointer;
  transition: background 0.15s ease;

  & + & {
    margin-top: 3px;
  }

  &:hover {
    background: var(--bg-elevated);
  }
`;

const noteStyle = css`
  margin-top: 8px;
  padding: 6px;
  background: var(--bg-surface);
  border-radius: var(--radius);
  font-size: 11px;
`;

// The open card: the sources (traces and golden set), a metric category's name, or the settings.
const SOURCES = 'sources';
const SETTINGS = 'settings';

const JUDGE_MODELS = [
  { value: 'gemini-3.8-flash', provider: 'google' },
  { value: 'gemini-3.5-flash', provider: 'google' },
  { value: 'gemini-3.1-pro-preview', provider: 'google' },
  { value: 'gemini-3-pro-preview', provider: 'google' },
  { value: 'gemini-3-flash-preview', provider: 'google' },
  { value: 'gemini-2.5-flash', provider: 'google' },
  { value: 'gemini-2.0-flash', provider: 'google' },
  { value: 'anthropic/claude-3.5-sonnet', provider: 'anthropic' },
  { value: 'openai/gpt-4o', provider: 'openai' },
] as const;

type JudgeModelProvider = typeof JUDGE_MODELS[number]['provider'];

export const UploadView: React.FC = () => {
  const { state, actions } = useTraceContext();
  const groups = useMetricGroups();
  // The finished runs, for what they already say about each metric group.
  const [runs, setRuns] = useState<Run[]>([]);
  useEffect(() => {
    let active = true;
    listRuns({ limit: 200 }).then((r) => active && setRuns(r)).catch(() => undefined);
    return () => {
      active = false;
    };
  }, []);
  const [picked, setPicked] = useState('');
  const section = picked || SOURCES;
  const [evalSetDrawerOpen, setEvalSetDrawerOpen] = useState(false);
  const [traceEditorIndex, setTraceEditorIndex] = useState<number | null>(null);

  const fromSessions = state.evaluationOrigin === 'streaming';
  // How many metrics would start to work with a golden set.
  const goldenMetrics = groups.reduce((n, [, ms]) => n + ms.filter((m) => m.requiresEvalSet).length, 0);
  const canRunEvaluation =
    state.traceFiles.length > 0 &&
    state.selectedEvaluatorNames.length > 0 &&
    !state.isLoadingMetadata;

  const traceCount = state.traceFiles.length;

  const card = (id: string, title: string, extra: React.ReactNode, chips: React.ReactNode, desc?: React.ReactNode, side?: React.ReactNode) => (
    <button
      key={id}
      css={[cardStyle, section === id && cardOpenStyle]}
      onClick={() => setPicked(id)}
      aria-pressed={section === id}
    >
      <span css={cardMainStyle} style={side ? undefined : { gridColumn: '1 / -1' }}>
        <span css={cardTitleStyle}>{title}{extra}</span>
        <span css={chipsStyle}>{chips}</span>
        {desc && <span css={descStyle}>{desc}</span>}
      </span>
      {side && <span css={cardSideStyle}>{side}</span>}
    </button>
  );

  const traceStatus = (
    <>
      {state.isLoadingMetadata && (
        <div css={[noteStyle]} style={{ display: 'flex', alignItems: 'center', gap: 8, color: 'var(--accent-primary)' }}>
          <div className="spinner" />
          Extracting trace metadata...
        </div>
      )}
      {!state.isLoadingMetadata && state.traceMetadata.size > 0 && (
        <div css={noteStyle} style={{ color: 'var(--status-success)' }}>
          ✓ {state.traceMetadata.size} trace(s) ready
        </div>
      )}
    </>
  );

  const tracesBox = (
    <section css={boxStyle}>
          <h2 css={detailsTitleStyle}>traces</h2>
          <p css={detailsHintStyle}>
            {fromSessions ? 'Loaded from the sessions you picked.' : 'The traces to score. Click a file to edit it.'}
          </p>
          {!fromSessions && (
            <FileDropZone
              title="Drop trace files here"
              description="Upload one or more trace files (.json or .jsonl)"
              multiple
              accept=".json,.jsonl"
              onChange={actions.setTraceFiles}
            />
          )}
          {traceCount === 0 && (
            <SessionPicker onUse={(ids) => actions.setTraceFiles(ids.map((id) => new SessionTraceFile(id)))} />
          )}
          {traceCount > 0 && (
            <div css={fileBoxStyle}>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', color: 'var(--text-primary)', fontSize: 12, fontWeight: 600, marginBottom: 6 }}>
                <span>{traceCount} file(s){fromSessions ? ' loaded from live sessions' : ' selected'}:</span>
                <Button size="small" onClick={() => actions.setTraceFiles([])}>Choose others</Button>
              </div>
              {state.traceFiles.map((file, idx) => (
                <div key={idx} css={fileRowStyle} onClick={() => setTraceEditorIndex(idx)}>
                  {file.name}
                  <Pencil size={10} style={{ opacity: 0.4, flexShrink: 0 }} />
                </div>
              ))}
              {traceStatus}
            </div>
          )}
        </section>
  );

  const goldenBox = (
    <section css={boxStyle}>
          <h2 css={detailsTitleStyle}>golden set · optional</h2>
          <p css={detailsHintStyle}>A reference conversation the traces are compared with.</p>
          {!fromSessions && (
            <FileDropZone
              title="Drop eval set file here"
              description="Upload golden eval set JSON for comparison"
              onChange={(files) => actions.setEvalSet(files[0] || null)}
            />
          )}
          {state.evalSetFile ? (
            <div css={fileBoxStyle}>
              <div style={{ color: 'var(--text-primary)', fontSize: 12, fontWeight: 600, marginBottom: 6 }}>Eval set file:</div>
              <div css={fileRowStyle} onClick={() => setEvalSetDrawerOpen(true)}>
                {state.evalSetFile.name}
                <Pencil size={10} style={{ opacity: 0.4, flexShrink: 0 }} />
              </div>
            </div>
          ) : (
            fromSessions && <div style={{ color: 'var(--text-secondary)', fontSize: 12 }}>No golden eval set loaded.</div>
          )}
        </section>
  );

  const detailsOf = (id: string) => {
    if (id === SOURCES) return <>{tracesBox}{goldenBox}</>;
    const group = groups.find(([name]) => name === id);
    if (group) {
      return (
        <>
          <h2 css={detailsTitleStyle}>{group[0]}</h2>
          <p css={detailsHintStyle}>Tick the metrics each trace is scored on.</p>
          <MetricGroup
            metrics={group[1]}
            selectedEvaluatorNames={state.selectedEvaluatorNames}
            onToggleEvaluatorName={actions.toggleEvaluatorName}
          />
        </>
      );
    }
    return (
      <>
        <h2 css={detailsTitleStyle}>settings</h2>
        <p css={detailsHintStyle}>The judge that scores and the score a trace needs to pass.</p>

        <div className="setting-item">
          <label className="setting-label">Judge Model</label>
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

        <div className="setting-item" style={{ marginTop: 16 }}>
          <label className="setting-label">Pass Threshold: {state.threshold}</label>
          <Slider min={0} max={1} step={0.05} value={state.threshold} onChange={actions.setThreshold} />
          <span className="setting-hint">Minimum score to pass</span>
        </div>

        {state.selectedEvaluatorNames.includes('tool_trajectory_avg_score') && (
          <div className="setting-item" style={{ marginTop: 16 }}>
            <label className="setting-label">Trajectory Match Type</label>
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
            <span className="setting-hint">How to compare tool call sequences for tool_trajectory_avg_score</span>
          </div>
        )}
      </>
    );
  };

  return (
    <div css={uploadViewStyle}>
      <PageTitle className="header">
        <h1 css={titleStyle}>evaluate</h1>
        <p css={subtitleStyle}>
          traces + a golden set, scored by the metrics you pick
        </p>
      </PageTitle>

      {state.errors.length > 0 && (
        <div role="alert" style={{ margin: '0 0 12px', fontSize: '14px', color: 'var(--status-failure)' }}>
          {state.errors.map((e, i) => <div key={i}>{e}</div>)}
        </div>
      )}

      <div css={splitStyle}>
        <div css={listStyle}>
          {card(
            SOURCES,
            'traces & golden set',
            null,
            <>
              {traceCount === 0
                ? <span css={[chipStyle, chipMutedStyle]}>no traces yet</span>
                : (
                  <>
                    <span css={chipStyle}>{traceCount} trace file{traceCount === 1 ? '' : 's'}</span>
                    {state.isLoadingMetadata
                      ? <span css={[chipStyle, chipMutedStyle]}>reading…</span>
                      : state.traceMetadata.size > 0 && <span css={[chipStyle, chipGoodStyle]}>{state.traceMetadata.size} ready</span>}
                  </>
                )}
              <span css={[chipStyle, !state.evalSetFile && chipMutedStyle]}>
                {state.evalSetFile ? `golden: ${state.evalSetFile.name}` : 'no golden set'}
              </span>
            </>,
            null,
            <>
              <span>The traces to score, and a golden conversation to compare them with.</span>
              <span css={sideWhenStyle}>
                {goldenMetrics > 0 && !state.evalSetFile
                  ? `A golden set unlocks ${goldenMetrics} metric${goldenMetrics === 1 ? '' : 's'} that compare with one.`
                  : 'Score a few dozen recent sessions rather than one, so a single odd run does not decide.'}
              </span>
            </>,
          )}
          {groups.map(([name, metrics]) => {
            const picked = new Set(state.selectedEvaluatorNames);
            const on = metrics.filter((m) => picked.has(m.name)).length;
            const stats = groupStats(metrics, runs);
            const blurb = blurbFor(name, metrics);
            const missingGolden = !state.evalSetFile && metrics.some((m) => picked.has(m.name) && m.requiresEvalSet);
            return card(
              name,
              name,
              null,
              <span css={[chipStyle, on > 0 && chipGoodStyle]}>{on} of {metrics.length}</span>,
              // Every metric of the group, the ticked ones lit.
              metrics.map((m, i) => (
                <React.Fragment key={m.name}>
                  {i > 0 && ' · '}
                  <span css={picked.has(m.name) ? descOnStyle : undefined}>{m.name}</span>
                </React.Fragment>
              )),
              <>
                <span>{blurb.what}</span>
                <span css={sideWhenStyle}>{blurb.when}</span>
                {missingGolden && <span css={sideWarnStyle}>Ticked here, but no golden set is chosen.</span>}
                {stats.errored > 0.2 && (
                  <span css={sideWarnStyle}>{Math.round(stats.errored * 100)}% of its past results errored; check keys and access.</span>
                )}
                <span css={sideStatsStyle}>
                  {stats.runs === 0
                    ? 'not run yet'
                    : [
                        `${stats.runs} run${stats.runs === 1 ? '' : 's'}`,
                        stats.pass !== null ? `pass ${Math.round(stats.pass * 100)}%` : null,
                        stats.avg !== null ? `avg ${stats.avg.toFixed(2)}` : null,
                        stats.lastAt ? `${ago(stats.lastAt)} ago` : null,
                      ].filter(Boolean).join(' · ')}
                </span>
              </>,
            );
          })}
          {card(
            SETTINGS,
            'settings',
            null,
            <>
              <span css={chipStyle}>{state.judgeModel}</span>
              <span css={chipStyle}>pass ≥ {state.threshold}</span>
            </>,
            null,
            <>
              <span>The model that judges, and the score a trace needs to pass.</span>
              <span css={sideWhenStyle}>Keep both the same from run to run, so the scores stay comparable.</span>
            </>,
          )}

          <div style={{ marginTop: 6 }}>
            <Button
              type="primary"
              size="large"
              className="run-button"
              onClick={actions.runEvaluation}
              disabled={!canRunEvaluation}
              loading={state.isEvaluating || state.isLoadingMetadata}
            >
              <Play size={20} />
              {state.isEvaluating
                ? (state.progressMessage || 'Running Evaluation...')
                : state.isLoadingMetadata
                ? 'Loading Traces...'
                : 'Run Evaluation'}
            </Button>
          </div>
        </div>

        <aside css={detailsStyle}>{detailsOf(section)}</aside>
      </div>

      {state.evalSetFile && (
        <EvalSetEditorDrawer
          file={state.evalSetFile}
          open={evalSetDrawerOpen}
          onClose={() => setEvalSetDrawerOpen(false)}
        />
      )}

      {traceEditorIndex !== null && state.traceFiles[traceEditorIndex] && (
        <TraceEditorDrawer
          file={state.traceFiles[traceEditorIndex]}
          fileIndex={traceEditorIndex}
          open={true}
          onClose={() => setTraceEditorIndex(null)}
        />
      )}
    </div>
  );
};
