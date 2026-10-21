import { useState, useEffect, useRef } from 'react';
import { Modal } from 'antd';
import { SpansPanel } from './SpansPanel';
import { LiveConversationPanel } from './LiveConversationPanel';
import type { ConversationElement } from './LiveConversationPanel';
import { SessionMetadata } from './SessionMetadata';
import { OnlineScoresPanel } from './OnlineScoresPanel';
import { formatSpanDuration, formatTurnTime } from '../../lib/trace-helpers';
import type { AnnotationQueue } from '../../lib/types';
import { tenantName, useTenantNames } from '../../lib/tenant';

interface Invocation {
  invocationId: string;
  userText: string;
  agentText: string;
  toolCalls: Array<{ name: string; args: any; latencyMs?: number; resultBytes?: number }>;
  modelInfo?: {
    models?: string[];
    inputTokens?: number;
    outputTokens?: number;
    turnLatencyMs?: number;
    turnStartedAt?: number;
    provider?: string;
    responseModels?: string[];
    finishReasons?: string[];
    cacheCreationTokens?: number;
    cacheReadTokens?: number;
    temperature?: number;
    maxTokens?: number;
    errorTypes?: string[];
  };
}

interface SessionCardProps {
  /** Open expanded, for a session linked to directly. */
  defaultExpanded?: boolean;
  session: {
    sessionId: string;
    traceId: string;
    evalSetId: string | null;
    spans: any[];
    status: 'active' | 'complete';
    metadata: Record<string, any>;
    invocations?: Invocation[];
    liveElements?: ConversationElement[];
    liveStats?: {
      totalInputTokens: number;
      totalOutputTokens: number;
      model?: string;
    };
    startedAt?: string;
    agents?: string[];
    tenants?: string[];
    models?: string[];
    completedAt?: string | null;
    rootSpanName?: string;
    llmCalls?: number;
    errors?: number;
    durationMs?: number;
  };
  isSelected: boolean;
  onSelect: () => void;
  onRemove?: () => void;
  evaluationResult?: {
    metricResults: Array<{
      metricName: string;
      score: number;
      evalStatus: string;
    }>;
  };
  annotationQueues?: AnnotationQueue[];
  onAddToQueue?: (queueId: string) => void;
  onCreateAndAddToQueue?: (name: string) => void;
  queueNames?: string[];
  /** When set, renders a "Compare" checkbox for opting this session into the
   * evaluation run alongside the golden session. Absent for the golden
   * session itself and for sessions that aren't complete yet. */
  compareOption?: { checked: boolean; onToggle: () => void };
}

export function SessionCard({ defaultExpanded, session, isSelected, onSelect, onRemove, evaluationResult, annotationQueues, onAddToQueue, onCreateAndAddToQueue, queueNames, compareOption }: SessionCardProps) {
  const [expanded, setExpanded] = useState(!!defaultExpanded);
  const [showQueuePicker, setShowQueuePicker] = useState(false);
  const [newQueueName, setNewQueueName] = useState('');
  const [showNewQueueInput, setShowNewQueueInput] = useState(false);
  const queuePickerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const handleClickOutside = (e: MouseEvent) => {
      if (queuePickerRef.current && !queuePickerRef.current.contains(e.target as Node)) {
        setShowQueuePicker(false);
        setShowNewQueueInput(false);
        setNewQueueName('');
      }
    };
    if (showQueuePicker) document.addEventListener('mousedown', handleClickOutside);
    return () => document.removeEventListener('mousedown', handleClickOutside);
  }, [showQueuePicker]);

  const conversationElements = session.liveElements || [];
  const liveStats = session.liveStats || {
    totalInputTokens: 0,
    totalOutputTokens: 0,
  };

  useEffect(() => {
    if (session.status === 'active' && conversationElements.length > 0 && !expanded) {
      setExpanded(true);
    }
  }, [conversationElements.length, session.status, expanded]);

  const totalTokens = liveStats.totalInputTokens + liveStats.totalOutputTokens ||
    session.invocations?.reduce((sum, inv) => {
      const input = inv.modelInfo?.inputTokens || 0;
      const output = inv.modelInfo?.outputTokens || 0;
      return sum + input + output;
    }, 0) || 0;

  const modelName = session.metadata?.model ||
    session.liveStats?.model ||
    session.invocations?.[0]?.modelInfo?.models?.[0] ||
    session.models?.[0] ||
    '';
  const extraModels = Math.max((session.models?.length ?? 0) - 1, 0);
  const agentLabel = session.agents?.join(' · ') ?? '';
  useTenantNames();
  const tenantLabel = session.tenants?.map(tenantName).join(' · ') ?? '';

  const providerName = session.invocations?.[0]?.modelInfo?.provider || null;

  const totalCacheCreation = session.invocations?.reduce((sum, inv) => sum + (inv.modelInfo?.cacheCreationTokens || 0), 0) || 0;
  const totalCacheRead = session.invocations?.reduce((sum, inv) => sum + (inv.modelInfo?.cacheReadTokens || 0), 0) || 0;

  return (
    <div
      style={{
        background: 'var(--card-bg)',
        borderRadius: 'var(--radius)',
        border: isSelected ? '1px solid var(--accent-magenta)' : '1px solid var(--border-default)',
        padding: '8px 12px',
        minWidth: 0,
        transition: 'all 0.2s',
        boxShadow: 'none',
      }}
    >
      <div
        onDoubleClick={(e) => {
          // A double-click on the header toggles the card; one on a control inside it does its own thing.
          if ((e.target as HTMLElement).closest('button, a, input, select, textarea, label')) return;
          window.getSelection()?.removeAllRanges();
          setExpanded((v) => !v);
        }}
        style={{
        display: 'flex',
        justifyContent: 'space-between',
        alignItems: 'center',
        gap: '12px',
        marginBottom: '0',
      }}>
        <div style={{ flex: 1 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: '8px', marginBottom: '4px', flexWrap: 'wrap' }}>
            {tenantLabel && (
              <span
                title="tenant"
                style={{
                  fontSize: '12.5px',
                  fontWeight: 500,
                  color: 'var(--accent-magenta)',
                  background: 'rgba(255, 106, 198, 0.1)',
                  padding: '1px 7px',
                  borderRadius: 'var(--radius)',
                }}
              >
                {tenantLabel}
              </span>
            )}

            {agentLabel && (
              <span
                title="gen_ai.agent.name"
                style={{
                  fontSize: '12.5px',
                  fontWeight: 500,
                  color: 'var(--accent-cyan)',
                  background: 'rgba(103, 232, 249, 0.1)',
                  padding: '1px 7px',
                  borderRadius: 'var(--radius)',
                }}
              >
                {agentLabel}
              </span>
            )}

            {modelName && (
            <span style={{
              fontSize: '12.5px',
              fontWeight: 500,
              color: '#b5abfc',
              background: 'rgba(181, 171, 252, 0.1)',
              padding: '1px 7px',
              borderRadius: 'var(--radius)',
              textTransform: 'uppercase',
              letterSpacing: '0.5px',
            }}>
              {modelName}{extraModels > 0 && ` +${extraModels}`}
            </span>
            )}

            {providerName && (
              <span style={{
                fontSize: '12.5px',
                fontWeight: 500,
                color: '#3b82f6',
                background: 'rgba(59, 130, 246, 0.1)',
                padding: '1px 7px',
                borderRadius: 'var(--radius)',
              }}>
                {providerName}
              </span>
            )}

            {session.invocations && session.invocations.length > 0 && (
              <span style={{
                fontSize: '12.5px',
                fontWeight: 500,
                color: 'var(--text-primary)',
                background: 'rgba(148, 163, 184, 0.18)',
                padding: '1px 7px',
                borderRadius: 'var(--radius)',
              }}>
                {session.invocations.length} turns
              </span>
            )}

            {totalTokens > 0 && (
              <span style={{
                fontSize: '12.5px',
                fontWeight: 500,
                color: '#10b981',
                background: 'rgba(16, 185, 129, 0.1)',
                padding: '1px 7px',
                borderRadius: 'var(--radius)',
              }}>
                {totalTokens.toLocaleString()} tokens
              </span>
            )}

            {session.status === 'complete' && session.durationMs != null && (
              <span title="Span window: earliest span start to latest span end" style={{
                fontSize: '12.5px',
                fontWeight: 500,
                color: '#fff',
                background: 'var(--accent-primary)',
                padding: '1px 7px',
                borderRadius: 'var(--radius)',
                fontVariantNumeric: 'tabular-nums',
              }}>
                {formatSpanDuration(session.durationMs)}
              </span>
            )}

            {session.startedAt && (
              <span style={{
                fontSize: '12.5px',
                fontWeight: 500,
                color: 'var(--text-primary)',
                background: 'rgba(148, 163, 184, 0.18)',
                padding: '1px 7px',
                borderRadius: 'var(--radius)',
              }}>
                {new Date(session.startedAt).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })}
              </span>
            )}

            {(totalCacheCreation > 0 || totalCacheRead > 0) && (
              <span style={{
                fontSize: '12.5px',
                fontWeight: 500,
                color: '#f59e0b',
                background: 'rgba(245, 158, 11, 0.1)',
                padding: '1px 7px',
                borderRadius: 'var(--radius)',
              }}>
                cache {totalCacheRead > 0 ? `${totalCacheRead.toLocaleString()} read` : ''}
                {totalCacheCreation > 0 && totalCacheRead > 0 ? ' / ' : ''}
                {totalCacheCreation > 0 ? `${totalCacheCreation.toLocaleString()} created` : ''}
              </span>
            )}

            {queueNames && queueNames.length > 0 && queueNames.map(name => (
              <span key={name} style={{
                fontSize: '12.5px',
                fontWeight: 500,
                color: '#b5abfc',
                background: 'rgba(181, 171, 252, 0.12)',
                border: '1px solid rgba(181, 171, 252, 0.3)',
                padding: '1px 6px',
                borderRadius: 'var(--radius)',
              }}>
                📋 {name}
              </span>
            ))}
          </div>

          <h3 style={{
            fontSize: '14px',
            fontWeight: 400,
            color: 'var(--text-primary)',
            margin: '0 0 2px 0',
          }}>
            {session.sessionId}
          </h3>

          <p style={{
            fontSize: '13px',
            color: 'var(--text-tertiary)',
            fontFamily: 'monospace',
            margin: 0,
          }}>
            {session.traceId.slice(0, 16)}...
            {session.rootSpanName && (
              <span style={{ marginLeft: '10px', color: 'var(--text-secondary)' }}>
                {session.rootSpanName}
              </span>
            )}
            {session.status === 'complete' && session.llmCalls != null && (
              <span style={{ marginLeft: '10px', color: 'var(--text-secondary)' }}>
                {session.llmCalls === 0
                  ? 'no LLM call'
                  : `${session.llmCalls} LLM call${session.llmCalls === 1 ? '' : 's'}`}
              </span>
            )}
            {(session.errors ?? 0) > 0 && (
              <span style={{ marginLeft: '10px', color: '#ef4444', fontWeight: 600 }}>
                {session.errors} error{session.errors === 1 ? '' : 's'}
              </span>
            )}
          </p>
        </div>

        <div style={{ display: 'flex', gap: '8px', alignItems: 'center', flexShrink: 0 }}>
          {session.status === 'complete' && onAddToQueue && (
            <div style={{ position: 'relative' }} ref={queuePickerRef}>
              <button
                onClick={(e) => {
                  e.stopPropagation();
                  setShowQueuePicker(!showQueuePicker);
                }}
                style={{
                  padding: '2px 9px', borderRadius: 'var(--radius)',
                  background: showQueuePicker ? 'rgba(181, 171, 252, 0.15)' : 'transparent',
                  border: '1px solid rgba(181, 171, 252, 0.4)',
                  color: '#b5abfc', fontSize: '12px', fontWeight: 500,
                  cursor: 'pointer', transition: 'all 0.2s', whiteSpace: 'nowrap',
                }}
              >
                + queue
              </button>

              {showQueuePicker && (
                <div style={{
                  position: 'absolute', right: 0, top: 'calc(100% + 6px)', zIndex: 100,
                  background: 'var(--bg-elevated)', border: '1px solid var(--border-default)',
                  borderRadius: 'var(--radius)', padding: '8px', minWidth: '200px',
                  boxShadow: '0 8px 24px rgba(0,0,0,0.3)',
                }}>
                  {(annotationQueues || []).length > 0 && (
                    <div>
                      {(annotationQueues || []).map(q => (
                        <button
                          key={q.id}
                          onClick={(e) => {
                            e.stopPropagation();
                            onAddToQueue(q.id);
                            setShowQueuePicker(false);
                          }}
                          style={{
                            display: 'block', width: '100%', padding: '8px 10px',
                            borderRadius: 'var(--radius)', background: 'transparent', border: 'none',
                            color: 'var(--text-primary)', fontSize: '13px', fontWeight: 500,
                            cursor: 'pointer', textAlign: 'left', transition: 'background 0.15s',
                          }}
                          onMouseEnter={e => (e.currentTarget.style.background = 'var(--bg-primary)')}
                          onMouseLeave={e => (e.currentTarget.style.background = 'transparent')}
                        >
                          {q.name} <span style={{ color: 'var(--text-tertiary)', fontSize: '11px' }}>({q.items.length})</span>
                        </button>
                      ))}
                      <div style={{ borderTop: '1px solid var(--border-subtle)', margin: '6px 0' }} />
                    </div>
                  )}

                  {showNewQueueInput ? (
                    <div style={{ padding: '4px 2px', display: 'flex', flexDirection: 'column', gap: '6px' }}>
                      <input
                        autoFocus
                        value={newQueueName}
                        onChange={(e) => setNewQueueName(e.target.value)}
                        onKeyDown={(e) => {
                          e.stopPropagation();
                          if (e.key === 'Enter' && newQueueName.trim() && onCreateAndAddToQueue) {
                            onCreateAndAddToQueue(newQueueName.trim());
                            setNewQueueName('');
                            setShowNewQueueInput(false);
                            setShowQueuePicker(false);
                          }
                          if (e.key === 'Escape') setShowNewQueueInput(false);
                        }}
                        placeholder="Queue name"
                        style={{
                          padding: '6px 10px', borderRadius: 'var(--radius)',
                          border: '1px solid var(--border-default)',
                          background: 'var(--bg-primary)', color: 'var(--text-primary)',
                          fontSize: '12px', outline: 'none', width: '100%', boxSizing: 'border-box',
                        }}
                      />
                      <div style={{ display: 'flex', gap: '4px' }}>
                        <button
                          onClick={(e) => {
                            e.stopPropagation();
                            if (newQueueName.trim() && onCreateAndAddToQueue) {
                              onCreateAndAddToQueue(newQueueName.trim());
                              setNewQueueName('');
                              setShowNewQueueInput(false);
                              setShowQueuePicker(false);
                            }
                          }}
                          style={{
                            flex: 1, padding: '6px', borderRadius: 'var(--radius)',
                            background: 'var(--accent-primary)', border: 'none',
                            color: '#fff', fontSize: '12px', fontWeight: 600, cursor: 'pointer',
                          }}
                        >
                          Create
                        </button>
                        <button
                          onClick={(e) => { e.stopPropagation(); setShowNewQueueInput(false); setNewQueueName(''); }}
                          style={{
                            padding: '6px 8px', borderRadius: 'var(--radius)', background: 'transparent',
                            border: '1px solid var(--border)', color: 'var(--text-secondary)',
                            fontSize: '12px', cursor: 'pointer',
                          }}
                        >
                          ×
                        </button>
                      </div>
                    </div>
                  ) : (
                    <button
                      onClick={(e) => { e.stopPropagation(); setShowNewQueueInput(true); }}
                      style={{
                        display: 'block', width: '100%', padding: '8px 10px',
                        borderRadius: 'var(--radius)', background: 'transparent', border: 'none',
                        color: '#b5abfc', fontSize: '13px', fontWeight: 600,
                        cursor: 'pointer', textAlign: 'left', transition: 'background 0.15s',
                      }}
                      onMouseEnter={e => (e.currentTarget.style.background = 'rgba(181, 171, 252, 0.08)')}
                      onMouseLeave={e => (e.currentTarget.style.background = 'transparent')}
                    >
                      + New queue
                    </button>
                  )}
                </div>
              )}
            </div>
          )}

          {evaluationResult && (
            <div style={{ display: 'flex', gap: '4px' }}>
              {evaluationResult.metricResults.map((mr) => (
                <div
                  key={mr.metricName}
                  style={{
                    padding: '1px 7px',
                    borderRadius: 'var(--radius)',
                    background: mr.evalStatus === 'PASSED'
                      ? 'rgba(34, 197, 94, 0.15)'
                      : 'rgba(239, 68, 68, 0.15)',
                    color: mr.evalStatus === 'PASSED' ? '#22c55e' : '#ef4444',
                    fontSize: '12px',
                    fontWeight: 700,
                  }}
                >
                  {mr.score != null ? mr.score.toFixed(2) : 'N/A'}
                </div>
              ))}
            </div>
          )}

          {compareOption && (
            <label
              onClick={(e) => e.stopPropagation()}
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: '6px',
                fontSize: '12px',
                fontWeight: 600,
                color: compareOption.checked ? '#a79ff0' : 'var(--text-secondary)',
                cursor: 'pointer',
                whiteSpace: 'nowrap',
                padding: '2px 8px',
                borderRadius: 'var(--radius)',
                background: compareOption.checked ? 'rgba(167, 159, 240, 0.1)' : 'transparent',
                border: `1px solid ${compareOption.checked ? 'rgba(167, 159, 240, 0.4)' : 'var(--border)'}`,
              }}
            >
              <input
                type="checkbox"
                checked={compareOption.checked}
                onChange={compareOption.onToggle}
              />
              compare
            </label>
          )}

          <button
            onClick={(e) => {
              e.stopPropagation();
              onSelect();
            }}
            style={{
              padding: '2px 10px',
              borderRadius: 'var(--radius)',
              background: isSelected ? '#a79ff0' : 'transparent',
              border: isSelected ? 'none' : '1px solid var(--border)',
              color: isSelected ? '#14161f' : 'var(--text-primary)',
              fontSize: '12px',
              fontWeight: 500,
              cursor: 'pointer',
              transition: 'all 0.2s',
              whiteSpace: 'nowrap',
            }}
          >
            {isSelected ? 'golden ✓' : 'set golden'}
          </button>

          {session.status === 'complete' && onRemove && (
            <button
              onClick={(e) => {
                e.stopPropagation();
                Modal.confirm({
                  title: 'Remove this session?',
                  content: 'It is hidden from this view only. The stored session is not deleted and comes back after a reload.',
                  okText: 'Remove',
                  okType: 'danger',
                  onOk: onRemove,
                });
              }}
              title="Remove session"
              style={{
                padding: '2px 8px',
                borderRadius: 'var(--radius)',
                background: 'transparent',
                border: '1px solid rgba(239, 68, 68, 0.3)',
                color: '#ef4444',
                fontSize: '13px',
                fontWeight: 600,
                cursor: 'pointer',
                transition: 'all 0.2s',
                lineHeight: 1,
              }}
            >
              ×
            </button>
          )}

          <button
            onClick={(e) => {
              e.stopPropagation();
              setExpanded(!expanded);
            }}
            style={{
              padding: '8px 12px',
              borderRadius: 'var(--radius)',
              background: expanded ? 'rgba(167, 159, 240, 0.1)' : 'transparent',
              border: '1px solid var(--border)',
              color: 'var(--text-primary)',
              fontSize: '13px',
              fontWeight: 600,
              cursor: 'pointer',
              transition: 'all 0.2s',
            }}
          >
            {expanded ? '▲' : '▼'}
          </button>
        </div>
      </div>

      {expanded && session.startedAt && (totalTokens > 0 || Object.keys(session.metadata).length > 0) && (
        <SessionMetadata
          session={{
            sessionId: session.sessionId,
            traceId: session.traceId,
            metadata: session.metadata,
            startedAt: session.startedAt,
            completedAt: session.completedAt,
            status: session.status,
            invocations: session.invocations,
          }}
          liveStats={liveStats}
          modelName={modelName}
        />
      )}

      {expanded && conversationElements.length > 0 && (
        <LiveConversationPanel
          elements={conversationElements}
          isActive={session.status === 'active'}
          invocations={session.invocations}
        />
      )}

      {expanded && session.invocations && session.invocations.length > 0 && conversationElements.length === 0 && (
        <details style={{ marginTop: '16px' }}>
          <summary style={{
            fontSize: '11px',
            color: 'var(--text-tertiary)',
            cursor: 'pointer',
            fontWeight: 600,
            padding: '8px 0',
          }}>
            Show Invocations ({session.invocations.length})
          </summary>
        <div style={{
          borderTop: '1px solid var(--border)',
          paddingTop: '20px',
          marginTop: '4px',
        }}>
          {session.invocations.map((inv, idx) => (
            <div
              key={inv.invocationId}
              style={{
                marginBottom: '16px',
                padding: '16px',
                background: 'var(--bg-primary)',
                borderRadius: 'var(--radius)',
                borderLeft: '3px solid #a79ff0',
              }}
            >
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '12px' }}>
                <span style={{
                  fontSize: '11px',
                  color: '#a79ff0',
                  fontWeight: 700,
                  textTransform: 'uppercase',
                  letterSpacing: '0.5px',
                }}>
                  Turn {idx + 1}
                  {inv.modelInfo?.turnStartedAt != null && (
                    <span style={{ marginLeft: '10px', fontWeight: 400, color: 'var(--text-muted)', textTransform: 'none', letterSpacing: 0 }}>
                      {formatTurnTime(inv.modelInfo.turnStartedAt)}
                    </span>
                  )}
                </span>

                {inv.modelInfo && (inv.modelInfo.inputTokens || inv.modelInfo.outputTokens) && (
                  <div style={{ display: 'flex', gap: '6px' }}>
                    {inv.modelInfo.inputTokens && (
                      <span style={{
                        fontSize: '10px',
                        color: 'var(--text-tertiary)',
                        background: 'var(--card-bg)',
                        padding: '1px 6px',
                        borderRadius: 'var(--radius)',
                        fontWeight: 600,
                      }}>
                        ↓ {inv.modelInfo.inputTokens}
                      </span>
                    )}
                    {inv.modelInfo.outputTokens && (
                      <span style={{
                        fontSize: '10px',
                        color: 'var(--text-tertiary)',
                        background: 'var(--card-bg)',
                        padding: '1px 6px',
                        borderRadius: 'var(--radius)',
                        fontWeight: 600,
                      }}>
                        ↑ {inv.modelInfo.outputTokens}
                      </span>
                    )}
                  </div>
                )}
              </div>

              <div style={{ marginBottom: '12px' }}>
                <div style={{
                  fontSize: '10px',
                  color: '#6b7280',
                  marginBottom: '6px',
                  fontWeight: 600,
                  textTransform: 'uppercase',
                  letterSpacing: '0.3px',
                }}>
                  User
                </div>
                <div style={{
                  fontSize: '14px',
                  color: 'var(--text-primary)',
                  lineHeight: '1.6',
                }}>
                  {inv.userText || '(no text)'}
                </div>
              </div>

              {inv.toolCalls && inv.toolCalls.length > 0 && (
                <div style={{ marginBottom: '12px' }}>
                  <div style={{
                    fontSize: '10px',
                    color: '#6b7280',
                    marginBottom: '6px',
                    fontWeight: 600,
                    textTransform: 'uppercase',
                    letterSpacing: '0.3px',
                  }}>
                    Tools
                  </div>
                  <div style={{ display: 'flex', flexDirection: 'column', gap: '4px' }}>
                    {inv.toolCalls.map((tc, i) => (
                      <div
                        key={i}
                        style={{
                          fontSize: '12px',
                          color: '#a79ff0',
                          fontFamily: 'monospace',
                          background: 'rgba(167, 159, 240, 0.08)',
                          padding: '6px 10px',
                          borderRadius: 'var(--radius)',
                          fontWeight: 500,
                        }}
                      >
                        {tc.name}({Object.keys(tc.args).length > 0 ? Object.keys(tc.args).map(k => `${k}=${JSON.stringify(tc.args[k])}`).join(', ') : ''})
                      </div>
                    ))}
                  </div>
                </div>
              )}

              {inv.agentText && (
                <div>
                  <div style={{
                    fontSize: '10px',
                    color: '#6b7280',
                    marginBottom: '6px',
                    fontWeight: 600,
                    textTransform: 'uppercase',
                    letterSpacing: '0.3px',
                  }}>
                    Agent
                  </div>
                  <div style={{
                    fontSize: '14px',
                    color: 'var(--text-primary)',
                    lineHeight: '1.6',
                  }}>
                    {inv.agentText}
                  </div>
                </div>
              )}
            </div>
          ))}
        </div>
        </details>
      )}

      {expanded && session.status === 'complete' && <OnlineScoresPanel sessionId={session.sessionId} />}
      {expanded && <SpansPanel sessionId={session.sessionId} />}
    </div>
  );
}
