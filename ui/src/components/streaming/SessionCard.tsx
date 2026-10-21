import { Fragment } from 'react';
import { Eye } from 'lucide-react';
import type { ConversationElement } from './LiveConversationPanel';
import { formatSpanDuration, formatStarted, modelsLabel } from '../../lib/trace-helpers';
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

export interface SessionCardProps {
  /** The session whose details are open in the side panel. */
  isOpen?: boolean;
  /** Open (or close) its details: a double click on the row, or the arrow. */
  onOpen?: () => void;
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
  /** When set, renders a "Compare" checkbox for opting this session into the
   * evaluation run alongside the golden session. Absent for the golden
   * session itself and for sessions that aren't complete yet. */
  compareOption?: { checked: boolean; onToggle: () => void };
}

// A count as a small round badge; what it counts is the tooltip.
function RoundBadge({ n, title, color, background }: { n: number; title: string; color: string; background: string }) {
  return (
    <span title={title} style={{
      minWidth: '19px',
      height: '19px',
      padding: '0 5px',
      boxSizing: 'border-box',
      borderRadius: '10px',
      display: 'inline-flex',
      alignItems: 'center',
      justifyContent: 'center',
      background,
      color,
      fontSize: '11.5px',
      fontWeight: 600,
      fontVariantNumeric: 'tabular-nums',
    }}>{n}</span>
  );
}

// The date badge and the text after the badges have colours of their own, none shared with another badge.
const WHEN_COLOR = '#d6c28f';
const KIND_COLOR = '#5eead4';
const TURNS_TONE = { color: '#fb923c', background: 'rgba(251, 146, 60, 0.16)' };

// What a session shows in its row and in its details: the conversation so far, the live token counts, the total
// and the model it ran.
export function sessionFigures(session: SessionCardProps['session']) {
  const conversationElements = session.liveElements || [];
  const liveStats = session.liveStats || {
    totalInputTokens: 0,
    totalOutputTokens: 0,
  };
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
  return { conversationElements, liveStats, totalTokens, modelName };
}

export function SessionCard({ session, isSelected, isOpen, onOpen, onSelect, onRemove, evaluationResult, compareOption }: SessionCardProps) {
  const { totalTokens, modelName } = sessionFigures(session);
  // Every model the session used, not the first and a count: `a / b`.
  const allModels = (session.models?.length ?? 0) > 1 ? session.models! : modelName ? [modelName] : [];
  const modelLabel = modelsLabel(allModels);
  const agentLabel = session.agents?.join(' · ') ?? '';
  useTenantNames();
  const tenantLabel = session.tenants?.map(tenantName).join(' · ') ?? '';

  const providerName = session.invocations?.[0]?.modelInfo?.provider || null;

  const totalCacheCreation = session.invocations?.reduce((sum, inv) => sum + (inv.modelInfo?.cacheCreationTokens || 0), 0) || 0;
  const totalCacheRead = session.invocations?.reduce((sum, inv) => sum + (inv.modelInfo?.cacheReadTokens || 0), 0) || 0;

  // The text after the badges: what ran, and errors last.
  const details: Array<{ key: string; text: string; color: string }> = [];
  if (session.rootSpanName) details.push({ key: 'root', text: session.rootSpanName, color: KIND_COLOR });
  if ((session.errors ?? 0) > 0) {
    details.push({ key: 'errors', text: `${session.errors} error${session.errors === 1 ? '' : 's'}`, color: '#ef4444' });
  }

  return (
    <div
      style={{
        background: isOpen ? 'var(--bg-elevated)' : 'var(--card-bg)',
        borderRadius: 'var(--radius)',
        border: isSelected ? '1px solid var(--accent-magenta)' : isOpen ? '1px solid var(--accent-primary)' : '1px solid var(--border-default)',
        padding: '8px 12px',
        minWidth: 0,
        transition: 'all 0.2s',
        boxShadow: 'none',
      }}
    >
      <div
        onDoubleClick={(e) => {
          // A double-click on the row opens its details; one on a control inside it does its own thing.
          if ((e.target as HTMLElement).closest('button, a, input, select, textarea, label')) return;
          window.getSelection()?.removeAllRanges();
          onOpen?.();
        }}
        style={{
        display: 'flex',
        justifyContent: 'space-between',
        alignItems: 'center',
        gap: '12px',
        marginBottom: '0',
      }}>
        <div style={{ flex: 1 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: '8px', flexWrap: 'wrap' }}>
            {session.startedAt && (
              <span
                title={new Date(session.startedAt).toLocaleString()}
                style={{
                  fontSize: '12.5px',
                  fontWeight: 500,
                  color: WHEN_COLOR,
                  background: 'rgba(214, 194, 143, 0.1)',
                  padding: '1px 7px',
                  borderRadius: 'var(--radius)',
                  fontVariantNumeric: 'tabular-nums',
                }}
              >
                {formatStarted(session.startedAt)}
              </span>
            )}

            {tenantLabel && (
              <span
                title="tenant"
                style={{
                  fontSize: '11.5px',
                  fontWeight: 500,
                  color: 'var(--accent-magenta)',
                  background: 'rgba(255, 106, 198, 0.08)',
                  padding: '0 6px',
                  lineHeight: '19px',
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
            <span title={allModels.join('\n')} style={{
              fontSize: '12.5px',
              fontWeight: 500,
              color: '#b5abfc',
              background: 'rgba(181, 171, 252, 0.1)',
              padding: '1px 7px',
              borderRadius: 'var(--radius)',
              textTransform: 'uppercase',
              letterSpacing: '0.5px',
            }}>
              {modelLabel}
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
              <RoundBadge
                n={session.invocations.length}
                title={`${session.invocations.length} turn${session.invocations.length === 1 ? '' : 's'}`}
                {...TURNS_TONE}
              />
            )}

            {totalTokens > 0 && (
              <span title={`${totalTokens.toLocaleString()} tokens`} style={{
                fontSize: '12.5px',
                fontWeight: 500,
                color: '#10b981',
                background: 'rgba(16, 185, 129, 0.1)',
                padding: '1px 7px',
                borderRadius: 'var(--radius)',
              }}>
                {totalTokens.toLocaleString()} t
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

            {details.length > 0 && (
              <span style={{ display: 'inline-flex', alignItems: 'center', columnGap: '6px', fontSize: '12px', color: 'var(--text-tertiary)', marginLeft: '2px' }}>
                {details.map((d, i) => (
                  <Fragment key={d.key}>
                    {i > 0 && <span aria-hidden>·</span>}
                    <span style={{ color: d.color, fontWeight: d.key === 'errors' ? 600 : 400 }}>{d.text}</span>
                  </Fragment>
                ))}
              </span>
            )}
          </div>

        </div>

        <div style={{ display: 'flex', gap: '8px', alignItems: 'center', flexShrink: 0 }}>
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
            title={isSelected ? 'The golden session: click to unset' : 'Set as the golden session'}
            aria-pressed={isSelected}
            style={{
              display: 'inline-flex',
              alignItems: 'center',
              padding: '5px 8px',
              borderRadius: 'var(--radius)',
              background: isSelected ? 'rgba(167, 159, 240, 0.18)' : 'transparent',
              border: `1px solid ${isSelected ? 'var(--accent-primary)' : 'var(--border)'}`,
              cursor: 'pointer',
              transition: 'all 0.2s',
            }}
          >
            <img src="/favicon.svg" alt="" width={16} height={16} style={{ display: 'block', opacity: isSelected ? 1 : 0.4 }} />
          </button>

          {session.status === 'complete' && onRemove && (
            <button
              onClick={(e) => {
                e.stopPropagation();
                onRemove();
              }}
              title="Hide this session (it is not deleted and comes back after a reload)"
              aria-label="Hide session"
              style={{
                display: 'inline-flex',
                alignItems: 'center',
                padding: '5px 8px',
                borderRadius: 'var(--radius)',
                background: 'transparent',
                border: '1px solid var(--border)',
                color: 'var(--text-tertiary)',
                cursor: 'pointer',
                transition: 'all 0.2s',
              }}
            >
              <Eye size={15} />
            </button>
          )}

          <button
            onClick={(e) => {
              e.stopPropagation();
              onOpen?.();
            }}
            title={isOpen ? 'Close details' : 'Open details'}
            aria-pressed={isOpen}
            style={{
              padding: '8px 12px',
              borderRadius: 'var(--radius)',
              background: isOpen ? 'rgba(167, 159, 240, 0.18)' : 'transparent',
              border: '1px solid var(--border)',
              color: 'var(--text-primary)',
              fontSize: '13px',
              fontWeight: 600,
              cursor: 'pointer',
              transition: 'all 0.2s',
            }}
          >
            ▸
          </button>
        </div>
      </div>
    </div>
  );
}
