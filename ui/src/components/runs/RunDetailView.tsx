import React, { useEffect, useMemo, useState } from 'react';
import { css } from '@emotion/react';
import {
  ChevronLeft,
  CheckCircle2,
  XCircle,
  AlertCircle,
  MinusCircle,
  ChevronRight,
  ChevronDown,
} from 'lucide-react';
import type { Run, RunResultRow, ResultStatus2, ToolCallComparison, HallucinationSentence, OnlineLabel, OnlineSessionScore } from '../../lib/types';
import { getOnlineLabels, getOnlineSessionScores, getRun, getRunResults, StorageUnavailableError } from '../../api/client';
import { LabelButtons } from '../streaming/OnlineScoresPanel';
import { STATUS_COLORS, formatDuration, formatTimestamp, passRate, runDurationMs } from './runHistory';
import { isJevMetric, jevPassingAnswer, jevTurns, jevVerdict } from '../../lib/jev';

interface RunDetailViewProps {
  runId: string;
  onBack: () => void;
}

const RESULT_STATUS: Record<ResultStatus2, { color: string; Icon: typeof CheckCircle2 }> = {
  passed: { color: 'var(--status-success)', Icon: CheckCircle2 },
  failed: { color: 'var(--status-failure)', Icon: XCircle },
  errored: { color: 'var(--status-warning)', Icon: AlertCircle },
  skipped: { color: 'var(--text-tertiary)', Icon: MinusCircle },
};

function formatToolCall(tc: ToolCallComparison): string {
  const args = tc.args && Object.keys(tc.args).length ? JSON.stringify(tc.args) : '';
  return `${tc.name ?? '?'}(${args})`;
}

const HALLUCINATION_LABEL_COLOR: Record<string, string> = {
  supported: 'var(--status-success)',
  not_applicable: 'var(--text-tertiary)',
  unsupported: 'var(--status-warning)',
  contradictory: 'var(--status-failure)',
  disputed: 'var(--status-failure)',
};

function hallucinationLabelColor(label: string): string {
  return HALLUCINATION_LABEL_COLOR[label.toLowerCase()] ?? 'var(--text-secondary)';
}

export const RunDetailView: React.FC<RunDetailViewProps> = ({ runId, onBack }) => {
  const [run, setRun] = useState<Run | null>(null);
  const [results, setResults] = useState<RunResultRow[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const [showGolden, setShowGolden] = useState(false);

  useEffect(() => {
    let active = true;
    (async () => {
      setLoading(true);
      setError(null);
      try {
        const [r, rows] = await Promise.all([getRun(runId), getRunResults(runId)]);
        if (!active) return;
        setRun(r);
        setResults(rows);
      } catch (err) {
        if (!active) return;
        setError(
          err instanceof StorageUnavailableError
            ? err.message
            : err instanceof Error
              ? err.message
              : 'Failed to load run',
        );
      } finally {
        if (active) setLoading(false);
      }
    })();
    return () => {
      active = false;
    };
  }, [runId]);

  const groupedResults = useMemo(() => {
    const byCase = new Map<string, RunResultRow[]>();
    for (const row of results) {
      const list = byCase.get(row.evalSetItemName) ?? [];
      list.push(row);
      byCase.set(row.evalSetItemName, list);
    }
    return [...byCase.entries()];
  }, [results]);

  // An online run's case is a session. When online evaluation scored it, each of its metric rows takes the thumbs,
  // so a person can judge the score while reading the rationale under it. Other runs' cases have no scores and
  // show nothing extra.
  const [online, setOnline] = useState<Record<string, { scores: OnlineSessionScore[]; labels: OnlineLabel[] }>>({});
  useEffect(() => {
    let cancelled = false;
    (async () => {
      const next: Record<string, { scores: OnlineSessionScore[]; labels: OnlineLabel[] }> = {};
      for (const [caseName] of groupedResults.slice(0, 50)) {
        const scores = (await getOnlineSessionScores(caseName).catch(() => null)) ?? [];
        if (scores.some(sc => sc.score !== null)) {
          next[caseName] = { scores, labels: await getOnlineLabels(caseName).catch(() => []) };
        }
      }
      if (!cancelled) setOnline(next);
    })();
    return () => {
      cancelled = true;
    };
  }, [groupedResults]);
  const reloadLabels = async (caseName: string) => {
    const labels = await getOnlineLabels(caseName);
    setOnline(o => (o[caseName] ? { ...o, [caseName]: { ...o[caseName], labels } } : o));
  };

  const toggle = (id: string) =>
    setExpanded(prev => {
      const next = new Set(prev);
      next.has(id) ? next.delete(id) : next.add(id);
      return next;
    });

  const evaluators = run?.spec?.evalConfig?.evaluators ?? [];
  const goldenCases = (run?.spec?.evalSet?.eval_cases ?? []) as unknown[];
  const rate = run ? passRate(run) : null;
  const counts = run?.summary?.result_counts;

  return (
    <div css={pageStyle}>
      <button css={backStyle} onClick={onBack}>
        <ChevronLeft size={16} />
        Run history
      </button>

      {loading && <p css={mutedStyle}>Loading run...</p>}
      {error && (
        <div css={errorStyle}>
          <AlertCircle size={18} />
          <span>{error}</span>
        </div>
      )}

      {run && !loading && (
        <>
          <header css={headerStyle}>
            <h1>{run.spec?.evalSet?.name || run.spec?.evalSet?.eval_set_id || `Run ${run.runId.slice(0, 8)}`}</h1>
            <div css={metaRowStyle}>
              <span css={badgeStyle} style={{ color: STATUS_COLORS[run.status] }}>
                <span css={dotStyle} style={{ background: STATUS_COLORS[run.status] }} />
                {run.status}
              </span>
              {run.summary?.agents?.length ? (
                <span css={metaItemStyle}>
                  agents: <strong>{run.summary.agents.join(', ')}</strong>
                </span>
              ) : null}
              <span css={metaItemStyle}>{formatTimestamp(run.createdAt)}</span>
              <span css={metaItemStyle}>duration: {formatDuration(runDurationMs(run))}</span>
              <span css={metaItemStyle}>
                target: {run.spec?.target?.kind ?? '-'}
                {run.summary?.trace_count != null ? ` (${run.summary.trace_count} traces)` : ''}
              </span>
              {rate !== null && (
                <span css={metaItemStyle} style={{ color: rate >= 0.5 ? 'var(--status-success)' : 'var(--status-failure)' }}>
                  {Math.round(rate * 100)}% pass
                </span>
              )}
            </div>
            {counts && (
              <div css={countsRowStyle}>
                <span style={{ color: 'var(--status-success)' }}>{counts.passed} passed</span>
                <span style={{ color: 'var(--status-failure)' }}>{counts.failed} failed</span>
                {counts.errored > 0 && <span style={{ color: 'var(--status-warning)' }}>{counts.errored} errored</span>}
                {counts.skipped > 0 && <span style={{ color: 'var(--text-tertiary)' }}>{counts.skipped} skipped</span>}
              </div>
            )}
            {run.error && <div css={errorStyle}><AlertCircle size={16} /><span>{run.error}</span></div>}
          </header>

          <section css={cardStyle}>
            <h2>Configuration</h2>
            {evaluators.length === 0 ? (
              <p css={mutedStyle}>No evaluator configuration recorded.</p>
            ) : (
              <table css={configTableStyle}>
                <thead>
                  <tr>
                    <th>Evaluator</th>
                    <th>Type</th>
                    <th>Threshold</th>
                    <th>Judge model</th>
                    <th>Match</th>
                  </tr>
                </thead>
                <tbody>
                  {evaluators.map((e, i) => (
                    <tr key={i}>
                      <td css={monoStyle}>
                        {e.name ?? '-'}
                        {(e.rubrics ?? []).length > 0 && (
                          <ul css={rubricListStyle}>
                            {e.rubrics!.map((r, j) => (
                              <li key={j}>{r}</li>
                            ))}
                          </ul>
                        )}
                      </td>
                      <td>{e.type ?? '-'}</td>
                      <td css={monoStyle}>{e.threshold ?? '-'}</td>
                      <td css={monoStyle}>{e.judge_model ?? '-'}</td>
                      <td css={monoStyle}>{e.trajectory_match_type ?? '-'}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </section>

          <section css={cardStyle}>
            <h2>Results</h2>
            {groupedResults.length === 0 ? (
              <p css={mutedStyle}>No per-result rows persisted for this run.</p>
            ) : (
              groupedResults.map(([caseName, rows]) => (
                <div key={caseName} css={caseBlockStyle}>
                  <div css={caseHeaderStyle}>
                    <span css={monoStyle}>{caseName}</span>
                    {rows[0]?.traceId && rows[0].traceId !== caseName && (
                      <span css={subtleMonoStyle}>trace {rows[0].traceId.slice(0, 12)}</span>
                    )}
                  </div>
                  {rows.map(row => {
                    const meta = RESULT_STATUS[row.status];
                    const Icon = meta.Icon;
                    const comparisons = row.details?.comparisons ?? [];
                    const isJev = isJevMetric(row.evaluatorName);
                    // jev_* rows: the first jev release stored its per-turn
                    // detail under per_invocation too, in a different shape.
                    const hallucinationInvocations = isJev ? [] : row.details?.per_invocation ?? [];
                    const turns = isJev ? jevTurns(row.details) : [];
                    const rubricInvocations = row.details?.rubric_invocations ?? [];
                    const isExpandable =
                      comparisons.length > 0 || hallucinationInvocations.length > 0 || turns.length > 0 || rubricInvocations.length > 0;
                    const isOpen = expanded.has(row.resultId);
                    return (
                      <div key={row.resultId} css={resultRowStyle}>
                        <div
                          css={resultHeadStyle}
                          onClick={() => isExpandable && toggle(row.resultId)}
                          style={{ cursor: isExpandable ? 'pointer' : 'default' }}
                        >
                          {isExpandable ? (
                            isOpen ? <ChevronDown size={14} /> : <ChevronRight size={14} />
                          ) : (
                            <span style={{ width: 14, display: 'inline-block' }} />
                          )}
                          <Icon size={15} style={{ color: meta.color, flexShrink: 0 }} />
                          <span css={evaluatorNameStyle}>{row.evaluatorName}</span>
                          {row.score !== null && (
                            <span css={monoStyle} style={{ color: meta.color }}>{row.score.toFixed(3)}</span>
                          )}
                          {row.perInvocationScores.length > 0 && (
                            <span css={subtleMonoStyle}>
                              [{row.perInvocationScores.map(s => (s === null ? '-' : s.toFixed(2))).join(', ')}]
                            </span>
                          )}
                          {row.latencyMs != null && <span css={subtleMonoStyle}>{row.latencyMs}ms</span>}
                          {online[caseName]?.scores.some(sc => sc.metric === row.evaluatorName && sc.score !== null) && (
                            <LabelButtons sessionId={caseName} metric={row.evaluatorName}
                              latest={online[caseName].labels.find(l => l.metric === row.evaluatorName)}
                              onLabeled={() => reloadLabels(caseName)} />
                          )}
                        </div>
                        {row.errorText && <div css={resultErrorStyle}>{row.errorText}</div>}
                        {isOpen && comparisons.length > 0 && (
                          <div css={comparisonsStyle}>
                            {comparisons.map((c, idx) => (
                              <div key={c.invocation_id ?? idx} css={invocationStyle}>
                                <div css={invocationHeadStyle}>
                                  {c.matched ? (
                                    <CheckCircle2 size={13} style={{ color: 'var(--status-success)' }} />
                                  ) : (
                                    <XCircle size={13} style={{ color: 'var(--status-failure)' }} />
                                  )}
                                  invocation {idx + 1}
                                </div>
                                <div css={diffGridStyle}>
                                  <div>
                                    <div css={diffLabelStyle}>Expected</div>
                                    {(c.expected ?? []).length === 0 ? (
                                      <div css={subtleMonoStyle}>(no tool calls)</div>
                                    ) : (
                                      (c.expected ?? []).map((tc, i) => (
                                        <div key={i} css={toolCallStyle}>{formatToolCall(tc)}</div>
                                      ))
                                    )}
                                  </div>
                                  <div>
                                    <div css={diffLabelStyle}>Actual</div>
                                    {(c.actual ?? []).length === 0 ? (
                                      <div css={subtleMonoStyle}>(no tool calls)</div>
                                    ) : (
                                      (c.actual ?? []).map((tc, i) => (
                                        <div
                                          key={i}
                                          css={toolCallStyle}
                                          style={{ color: c.matched ? 'var(--text-secondary)' : 'var(--status-failure)' }}
                                        >
                                          {formatToolCall(tc)}
                                        </div>
                                      ))
                                    )}
                                  </div>
                                </div>
                              </div>
                            ))}
                          </div>
                        )}
                        {isOpen && turns.length > 0 && (
                          <div css={comparisonsStyle}>
                            {row.details?.question && (
                              <div css={sentenceRationaleStyle}>
                                {row.details.question}
                                {row.details.question_type && ` Passing answer: ${jevPassingAnswer(row.details.question_type, row.details.expect)}.`}
                              </div>
                            )}
                            {turns.map((t, idx) => (
                              <div key={t.invocation_id || idx} css={invocationStyle}>
                                <div css={invocationHeadStyle}>
                                  invocation {idx + 1}
                                  {t.score !== null && (
                                    <span css={subtleMonoStyle}>score {t.score.toFixed(2)}</span>
                                  )}
                                </div>
                                {(t.user_input || t.final_response) && (
                                  <div css={diffGridStyle}>
                                    <div>
                                      <div css={diffLabelStyle}>User</div>
                                      <div css={monoStyle}>{t.user_input || '(no text)'}</div>
                                    </div>
                                    <div>
                                      <div css={diffLabelStyle}>Agent</div>
                                      <div css={monoStyle}>{t.final_response || '(no response)'}</div>
                                    </div>
                                  </div>
                                )}
                                <div css={sentenceRationaleStyle}>{jevVerdict(t.answer)}</div>
                              </div>
                            ))}
                          </div>
                        )}
                        {isOpen && rubricInvocations.length > 0 && (
                          <div css={comparisonsStyle}>
                            {rubricInvocations.map((inv, idx) => (
                              <div key={inv.invocation_id ?? idx} css={invocationStyle}>
                                <div css={invocationHeadStyle}>
                                  invocation {idx + 1}
                                  {inv.score !== null && (
                                    <span css={subtleMonoStyle}>score {inv.score.toFixed(2)}</span>
                                  )}
                                </div>
                                <div style={{ display: 'flex', flexDirection: 'column', gap: '8px' }}>
                                  {inv.rubrics.map((r, i) => (
                                    <div key={r.id ?? i} css={sentenceBlockStyle}>
                                      <div style={{ display: 'flex', alignItems: 'baseline', gap: '8px' }}>
                                        <span css={sentenceLabelBadgeStyle} style={{ color: rubricVerdictColor(r.verdict) }}>
                                          {r.verdict ?? 'unread'}
                                        </span>
                                        <span css={monoStyle} style={{ color: 'var(--text-primary)' }}>
                                          {r.rubric}
                                        </span>
                                      </div>
                                      {r.rationale && (
                                        <div css={sentenceRationaleStyle} style={{ whiteSpace: 'pre-wrap' }}>
                                          {r.rationale}
                                        </div>
                                      )}
                                    </div>
                                  ))}
                                </div>
                              </div>
                            ))}
                          </div>
                        )}
                        {isOpen && hallucinationInvocations.length > 0 && (
                          <div css={comparisonsStyle}>
                            {hallucinationInvocations.map((inv, idx) => (
                              <div key={inv.invocation_id ?? idx} css={invocationStyle}>
                                <div css={invocationHeadStyle}>
                                  invocation {idx + 1}
                                  {inv.score !== null && (
                                    <span css={subtleMonoStyle}>score {inv.score.toFixed(2)}</span>
                                  )}
                                </div>
                                {(inv.sentences ?? []).length === 0 ? (
                                  <div css={subtleMonoStyle}>(no sentence-level detail from the judge)</div>
                                ) : (
                                  <div style={{ display: 'flex', flexDirection: 'column', gap: '8px' }}>
                                    {inv.sentences.map((s: HallucinationSentence, i: number) => (
                                      <div key={i} css={sentenceBlockStyle}>
                                        <div style={{ display: 'flex', alignItems: 'baseline', gap: '8px' }}>
                                          <span
                                            css={sentenceLabelBadgeStyle}
                                            style={{ color: hallucinationLabelColor(s.label) }}
                                          >
                                            {s.label}
                                          </span>
                                          <span css={monoStyle} style={{ color: 'var(--text-primary)' }}>
                                            {s.sentence}
                                          </span>
                                        </div>
                                        {s.rationale && (
                                          <div css={sentenceRationaleStyle}>{s.rationale}</div>
                                        )}
                                        {s.supporting_excerpt && (
                                          <div css={sentenceExcerptStyle}>
                                            <span style={{ color: 'var(--status-success)' }}>supports: </span>
                                            {s.supporting_excerpt}
                                          </div>
                                        )}
                                        {s.contradicting_excerpt && (
                                          <div css={sentenceExcerptStyle}>
                                            <span style={{ color: 'var(--status-failure)' }}>contradicts: </span>
                                            {s.contradicting_excerpt}
                                          </div>
                                        )}
                                      </div>
                                    ))}
                                  </div>
                                )}
                              </div>
                            ))}
                          </div>
                        )}
                      </div>
                    );
                  })}
                </div>
              ))
            )}
          </section>

          {goldenCases.length > 0 && (
            <section css={cardStyle}>
              <h2
                css={collapsibleHeadingStyle}
                onClick={() => setShowGolden(v => !v)}
              >
                {showGolden ? <ChevronDown size={16} /> : <ChevronRight size={16} />}
                Reference: golden eval set ({goldenCases.length} case{goldenCases.length === 1 ? '' : 's'})
              </h2>
              {showGolden && (
                <pre css={jsonStyle}>{JSON.stringify(run.spec?.evalSet?.eval_cases, null, 2)}</pre>
              )}
            </section>
          )}
        </>
      )}
    </div>
  );
};

const pageStyle = css`
  padding: 32px;
  max-width: 1100px;
  margin: 0 auto;
`;

const backStyle = css`
  display: inline-flex;
  align-items: center;
  gap: 4px;
  background: none;
  border: none;
  color: var(--text-secondary);
  font-size: 0.875rem;
  font-weight: 600;
  cursor: pointer;
  padding: 4px 0;
  margin-bottom: 16px;

  &:hover {
    color: var(--accent-primary);
  }
`;

const headerStyle = css`
  margin-bottom: 24px;

  h1 {
    margin: 0 0 12px;
    font-size: 1.4rem;
    font-weight: 700;
    color: var(--text-primary);
    font-family: var(--font-display);
  }
`;

const metaRowStyle = css`
  display: flex;
  flex-wrap: wrap;
  gap: 16px;
  align-items: center;
`;

const metaItemStyle = css`
  font-size: 0.8125rem;
  color: var(--text-secondary);

  strong {
    color: var(--accent-primary);
    font-family: var(--font-mono);
  }
`;

const badgeStyle = css`
  display: inline-flex;
  align-items: center;
  gap: 6px;
  text-transform: capitalize;
  font-size: 0.8125rem;
  font-weight: 600;
`;

const dotStyle = css`
  width: 8px;
  height: 8px;
  border-radius: 50%;
`;

const countsRowStyle = css`
  display: flex;
  gap: 16px;
  margin-top: 10px;
  font-family: var(--font-mono);
  font-size: 0.8125rem;
  font-weight: 600;
`;

const cardStyle = css`
  background: var(--bg-surface);
  border: 1px solid var(--border-default);
  border-radius: var(--radius);
  padding: 20px;
  margin-bottom: 20px;

  h2 {
    margin: 0 0 16px;
    font-size: 1rem;
    font-weight: 600;
    color: var(--text-primary);
  }
`;

const collapsibleHeadingStyle = css`
  display: flex;
  align-items: center;
  gap: 6px;
  cursor: pointer;
  user-select: none;
  margin: 0 !important;
`;

const configTableStyle = css`
  width: 100%;
  border-collapse: collapse;
  font-size: 0.8125rem;

  th {
    text-align: left;
    color: var(--text-tertiary);
    font-weight: 600;
    text-transform: uppercase;
    font-size: 0.6875rem;
    letter-spacing: 0.5px;
    padding: 6px 12px 6px 0;
  }

  td {
    padding: 6px 12px 6px 0;
    color: var(--text-secondary);
    border-top: 1px solid var(--border-subtle);
  }
`;

const caseBlockStyle = css`
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius);
  margin-bottom: 12px;
  overflow: hidden;
`;

const caseHeaderStyle = css`
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 14px;
  background: var(--bg-elevated);
  border-bottom: 1px solid var(--border-subtle);
`;

const resultRowStyle = css`
  padding: 8px 14px;
  border-bottom: 1px solid var(--border-subtle);

  &:last-child {
    border-bottom: none;
  }
`;

const resultHeadStyle = css`
  display: flex;
  align-items: center;
  gap: 10px;
`;

const evaluatorNameStyle = css`
  flex: 1;
  font-size: 0.875rem;
  color: var(--text-primary);
`;

const monoStyle = css`
  font-family: var(--font-mono);
  font-size: 0.8125rem;
  color: var(--text-secondary);
`;

const subtleMonoStyle = css`
  font-family: var(--font-mono);
  font-size: 0.75rem;
  color: var(--text-tertiary);
`;

const resultErrorStyle = css`
  margin: 6px 0 0 24px;
  font-size: 0.8125rem;
  color: var(--status-failure);
  font-family: var(--font-mono);
`;

const comparisonsStyle = css`
  margin: 10px 0 4px 24px;
  display: flex;
  flex-direction: column;
  gap: 10px;
`;

const invocationStyle = css`
  border-left: 2px solid var(--border-default);
  padding-left: 12px;
`;

const invocationHeadStyle = css`
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 0.75rem;
  color: var(--text-tertiary);
  margin-bottom: 6px;
`;

const diffGridStyle = css`
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 16px;
`;

const diffLabelStyle = css`
  font-size: 0.6875rem;
  text-transform: uppercase;
  letter-spacing: 0.5px;
  color: var(--text-tertiary);
  margin-bottom: 4px;
`;

const toolCallStyle = css`
  font-family: var(--font-mono);
  font-size: 0.8125rem;
  color: var(--text-secondary);
  word-break: break-all;
`;

const rubricListStyle = css`
  margin: 4px 0 0;
  padding-left: 16px;
  font-family: var(--font-display);
  font-size: 0.75rem;
  color: var(--text-secondary);
`;

function rubricVerdictColor(verdict: 'yes' | 'no' | null): string {
  if (verdict === 'yes') return 'var(--status-success)';
  if (verdict === 'no') return 'var(--status-failure)';
  return 'var(--text-secondary)';
}

const sentenceBlockStyle = css`
  padding: 8px 10px;
  background: var(--bg-primary);
  border-radius: var(--radius);
`;

const sentenceLabelBadgeStyle = css`
  font-family: var(--font-mono);
  font-size: 0.6875rem;
  font-weight: 700;
  text-transform: uppercase;
  letter-spacing: 0.5px;
  flex-shrink: 0;
`;

const sentenceRationaleStyle = css`
  margin-top: 4px;
  font-size: 0.8125rem;
  color: var(--text-secondary);
`;

const sentenceExcerptStyle = css`
  margin-top: 4px;
  font-size: 0.75rem;
  color: var(--text-tertiary);
  font-style: italic;
`;

const jsonStyle = css`
  background: var(--bg-primary);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius);
  padding: 12px;
  font-family: var(--font-mono);
  font-size: 0.75rem;
  color: var(--text-secondary);
  overflow-x: auto;
  max-height: 400px;
`;

const mutedStyle = css`
  color: var(--text-tertiary);
  font-size: 0.9rem;
`;

const errorStyle = css`
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 12px 16px;
  border-radius: var(--radius);
  margin-top: 12px;
  background: rgba(255, 87, 87, 0.08);
  color: var(--status-failure);
  font-size: 0.875rem;
`;
