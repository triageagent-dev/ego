import React, { useEffect, useState } from 'react';
import { Button, Checkbox, Segmented } from 'antd';
import { css } from '@emotion/react';
import { Play, Pencil, Loader2 } from 'lucide-react';
import { NewGoldenForm } from './NewGoldenForm';
import { MetricGroup, useMetricGroups } from './MetricSelector';
import { SessionPicker } from './SessionPicker';
import { formatStarted } from '../../lib/trace-helpers';
import { ago, blurbFor, groupStats } from './metricInsights';
import { getEvalSet, listEvalSets, listRuns } from '../../api/client';
import { evalSetToWireFormat } from '../../lib/evalset-builder';
import type { EvalSetSummary, Run } from '../../lib/types';
import { SessionEvalSetFile, SessionTraceFile } from '../../lib/sessionFiles';
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

const goldenNoteStyle = css`
  padding: 6px 0;
  color: var(--text-secondary);
  font-size: 12px;
`;

const goldenListStyle = css`
  display: flex;
  flex-direction: column;
  gap: 4px;
`;

const goldenRowStyle = css`
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 5px 10px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius);
  background: var(--card-bg);
  color: var(--text-primary);
  font: inherit;
  font-size: 12px;
  text-align: left;
  cursor: pointer;

  &:hover:not(:disabled) {
    background: var(--bg-elevated);
    border-color: var(--accent-primary);
  }
`;

const goldenNameStyle = css`
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
`;

const goldenMetaStyle = css`
  flex-shrink: 0;
  color: var(--text-muted);
  font-family: var(--font-mono);
  font-size: 11px;
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

const turnsHeadStyle = css`
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 4px;
  color: var(--text-primary);
  font-weight: 600;
`;

const turnsListStyle = css`
  max-height: 260px;
  overflow-y: auto;
`;

const turnRowStyle = css`
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 3px 0;
  cursor: pointer;
`;

const turnTimeStyle = css`
  flex-shrink: 0;
  color: var(--text-muted);
  font-family: monospace;
`;

const turnTextStyle = css`
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--text-secondary);
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

// The open card: the sources (traces and golden set), the eval sets, or a metric category's name.
const SOURCES = 'sources';
const EVALSETS = 'evalsets';


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
  // Where the golden set comes from: one the server keeps, a session of its own, or one written here.
  const [goldenFrom, setGoldenFrom] = useState<'sets' | 'session'>('sets');
  const [evalSets, setEvalSets] = useState<EvalSetSummary[] | null>(null);
  const [goldenBusy, setGoldenBusy] = useState<string | null>(null);
  const [goldenError, setGoldenError] = useState('');
  useEffect(() => {
    let active = true;
    listEvalSets()
      .then((sets) => {
        if (!active) return;
        setEvalSets(sets);
        // Nothing saved: start on the sessions, the next best source.
        if (sets.length === 0) setGoldenFrom('session');
      })
      .catch(() => active && setEvalSets([]));
    return () => {
      active = false;
    };
  }, []);
  const chooseEvalSet = async (id: string) => {
    setGoldenBusy(id);
    setGoldenError('');
    try {
      const set = await getEvalSet(id);
      actions.setEvalSet(new File([JSON.stringify(evalSetToWireFormat(set), null, 2)], `${id}.json`, { type: 'application/json' }));
    } catch (e) {
      setGoldenError(e instanceof Error ? e.message : String(e));
    } finally {
      setGoldenBusy(null);
    }
  };
  const [evalSetDrawerOpen, setEvalSetDrawerOpen] = useState(false);
  const [traceEditorIndex, setTraceEditorIndex] = useState<number | null>(null);

  // How many metrics would start to work with a golden set.
  const goldenMetrics = groups.reduce((n, [, ms]) => n + ms.filter((m) => m.requiresEvalSet).length, 0);
  const canRunEvaluation =
    state.traceFiles.length > 0 &&
    state.selectedEvaluatorNames.length > 0 &&
    !state.isLoadingMetadata &&
    // Something must be left to score once turns were taken out.
    (state.traceMetadata.size === 0 || state.traceMetadata.size > state.excludedTraceIds.size);

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

  // The turns (traces) of the chosen sessions, to score all of them or only some.
  const turns = Array.from(state.traceMetadata.values()).sort((a, b) => (a.startTime ?? 0) - (b.startTime ?? 0));
  const turnPicker = (
    <div css={noteStyle}>
      <div css={turnsHeadStyle}>
        <span>Turns to score: {turns.length - state.excludedTraceIds.size} of {turns.length}</span>
        <span>
          <Button size="small" type="link" onClick={() => actions.setExcludedTraces([])}>all</Button>
          <Button size="small" type="link" onClick={() => actions.setExcludedTraces(turns.map((m) => m.traceId))}>none</Button>
        </span>
      </div>
      <div css={turnsListStyle}>
        {turns.map((m) => (
          <label key={m.traceId} css={turnRowStyle} title={m.userInputPreview}>
            <Checkbox checked={!state.excludedTraceIds.has(m.traceId)} onChange={() => actions.toggleTrace(m.traceId)} />
            <span css={turnTimeStyle}>{m.startTime ? formatStarted(new Date(m.startTime / 1000).toISOString()) : '–'}</span>
            <span css={turnTextStyle}>{m.userInputPreview || m.traceId}</span>
          </label>
        ))}
      </div>
    </div>
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
      {!state.isLoadingMetadata && state.traceMetadata.size > 1 && turnPicker}
    </>
  );

  const tracesBox = (
    <section css={boxStyle}>
          <h2 css={detailsTitleStyle}>traces</h2>
          <p css={detailsHintStyle}>
            The sessions to score. Click one to edit its trace.
          </p>
          {traceCount === 0 && (
            <SessionPicker onUse={(ids) => actions.setTraceFiles(ids.map((id) => new SessionTraceFile(id)))} />
          )}
          {traceCount > 0 && (
            <div css={fileBoxStyle}>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', color: 'var(--text-primary)', fontSize: 12, fontWeight: 600, marginBottom: 6 }}>
                <span>{traceCount} session{traceCount === 1 ? '' : 's'} selected:</span>
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
          {!state.evalSetFile && (
            <>
              <Segmented
                size="small"
                value={goldenFrom}
                onChange={(v) => setGoldenFrom(v as 'sets' | 'session')}
                options={[
                  { value: 'sets', label: `Saved eval sets${evalSets ? ` (${evalSets.length})` : ''}` },
                  { value: 'session', label: 'A session' },
                ]}
              />
              <div style={{ marginTop: 10 }}>
                {goldenFrom === 'sets' && (
                  evalSets === null ? <div css={goldenNoteStyle}>Loading eval sets…</div>
                  : evalSets.length === 0 ? <div css={goldenNoteStyle}>No saved eval sets yet. Build one under sets, or use a session.</div>
                  : (
                    <div css={goldenListStyle}>
                      {evalSets.map((es) => (
                        <button key={es.evalSetId} css={goldenRowStyle} disabled={goldenBusy !== null} onClick={() => chooseEvalSet(es.evalSetId)} title={es.description || es.evalSetId}>
                          <span css={goldenNameStyle}>{es.name || es.evalSetId}</span>
                          <span css={goldenMetaStyle}>{es.numCases} case{es.numCases === 1 ? '' : 's'} · {goldenBusy === es.evalSetId ? 'loading…' : formatStarted(es.updatedAt)}</span>
                        </button>
                      ))}
                    </div>
                  )
                )}
                {goldenFrom === 'session' && (
                  <SessionPicker
                    single
                    title="use one session as the golden reference"
                    useLabel="Use as golden"
                    onUse={(ids) => actions.setEvalSet(new SessionEvalSetFile(ids[0]))}
                  />
                )}
                {goldenError && <div css={goldenNoteStyle} style={{ color: 'var(--status-failure)' }}>{goldenError}</div>}
              </div>
            </>
          )}
          {state.evalSetFile ? (
            <div css={fileBoxStyle}>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', color: 'var(--text-primary)', fontSize: 12, fontWeight: 600, marginBottom: 6 }}>
                <span>Golden set:</span>
                <Button size="small" onClick={() => actions.setEvalSet(null)}>Choose another</Button>
              </div>
              <div css={fileRowStyle} onClick={() => setEvalSetDrawerOpen(true)}>
                {state.evalSetFile.name}
                <Pencil size={10} style={{ opacity: 0.4, flexShrink: 0 }} />
              </div>
            </div>
          ) : null}
        </section>
  );

  const detailsOf = (id: string) => {
    if (id === SOURCES) return <>{goldenBox}{tracesBox}</>;
    if (id === EVALSETS) {
      return (
        <>
          <h2 css={detailsTitleStyle}>eval sets</h2>
          <p css={detailsHintStyle}>Write a golden conversation once and score against it as often as you like. Saved sets are listed under the golden set.</p>
          <NewGoldenForm
            onCreated={(id) => {
              // The new set is saved, in the list from now on, and is the golden set of this evaluation.
              listEvalSets().then(setEvalSets).catch(() => undefined);
              void chooseEvalSet(id);
              setGoldenFrom('sets');
              setPicked(SOURCES);
            }}
          />
        </>
      );
    }
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
    return null;
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
                ? <span css={[chipStyle, chipMutedStyle]}>no sessions yet</span>
                : (
                  <>
                    <span css={chipStyle}>{traceCount} session{traceCount === 1 ? '' : 's'}</span>
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
          {card(
            EVALSETS,
            'eval sets',
            null,
            <span css={chipStyle}>{evalSets === null ? '…' : `${evalSets.length} saved`}</span>,
            null,
            <>
              <span>Golden conversations you write once and reuse.</span>
              <span css={sideWhenStyle}>Create one here; it becomes the golden set of this evaluation.</span>
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
          <div style={{ marginTop: 6 }}>
            <Button
              type="primary"
              size="large"
              className="run-button"
              // While it runs the button is the way to the run's progress.
              onClick={state.isEvaluating ? () => actions.setCurrentView('dashboard') : actions.runEvaluation}
              disabled={!state.isEvaluating && !canRunEvaluation}
              loading={state.isLoadingMetadata}
            >
              {state.isEvaluating ? <Loader2 size={20} className="animate-spin" /> : <Play size={20} />}
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
