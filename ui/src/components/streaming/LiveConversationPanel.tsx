import { useRef, useEffect, useState, useMemo } from 'react';
import { UserMessage, ToolCallMessage, ToolResultMessage, AgentMessage } from './LiveMessage';
import type { ConversationElement } from '../../lib/types';

export type { ConversationElement };

/** Minimal shape LiveConversationPanel needs per invocation - declared
 * structurally rather than importing StreamingInvocation or SessionCard's
 * local Invocation type, so either one satisfies it without coupling. */
interface TurnInvocationInfo {
  invocationId: string;
  modelInfo?: {
    inputTokens?: number;
    outputTokens?: number;
    turnLatencyMs?: number;
  };
}

interface LiveConversationPanelProps {
  elements: ConversationElement[];
  isActive: boolean;
  /** Per-invocation tokens/latency, when available (populated once a session
   * completes - a still-streaming turn won't have this yet). */
  invocations?: TurnInvocationInfo[];
}

interface Turn {
  invocationId: string;
  elements: ConversationElement[];
}

function formatLatency(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)}ms`;
  return `${(ms / 1000).toFixed(1)}s`;
}

/** Groups elements by invocationId, preserving the order each invocation first appears in. */
function groupIntoTurns(elements: ConversationElement[]): Turn[] {
  const turns: Turn[] = [];
  const indexByInvocation = new Map<string, number>();

  for (const element of elements) {
    const key = element.invocationId || '__no_invocation__';
    let i = indexByInvocation.get(key);
    if (i === undefined) {
      i = turns.length;
      indexByInvocation.set(key, i);
      turns.push({ invocationId: key, elements: [] });
    }
    turns[i].elements.push(element);
  }

  return turns;
}

function turnSummary(turn: Turn) {
  const userEl = turn.elements.find(e => e.type === 'user_input');
  const agentEl = turn.elements.find(e => e.type === 'agent_response' && e.data.text);
  const toolCallEls = turn.elements.filter(e => e.type === 'tool_call');
  const toolErrorCount = turn.elements.filter(e => e.type === 'tool_result' && e.data.isError).length;
  const toolLatencies = toolCallEls
    .map(e => e.data.toolCall?.latencyMs)
    .filter((ms): ms is number => typeof ms === 'number');
  const raw = userEl?.data?.text || agentEl?.data?.text || '';
  const preview = raw.length > 72 ? `${raw.slice(0, 72)}…` : raw;
  return {
    preview,
    toolCount: toolCallEls.length,
    toolErrorCount,
    toolLatencyMs: toolLatencies.length > 0 ? toolLatencies.reduce((a, b) => a + b, 0) : null,
    hasAgentResponse: !!agentEl,
  };
}

function renderElement(element: ConversationElement, key: number) {
  switch (element.type) {
    case 'user_input':
      return <UserMessage key={key} text={element.data.text} timestamp={element.timestamp} />;
    case 'tool_call':
      return <ToolCallMessage
        key={key}
        name={element.data.toolCall.name}
        args={element.data.toolCall.args}
        timestamp={element.timestamp}
        latencyMs={element.data.toolCall.latencyMs}
        resultBytes={element.data.toolCall.resultBytes}
      />;
    case 'tool_result':
      return <ToolResultMessage
        key={key}
        response={element.data.response}
        isError={element.data.isError}
        timestamp={element.timestamp}
      />;
    case 'agent_response':
      return <AgentMessage
        key={key}
        text={element.data.text}
        timestamp={element.timestamp}
        isStreaming={false}
      />;
    default:
      return null;
  }
}

export function LiveConversationPanel({ elements, isActive, invocations }: LiveConversationPanelProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const [autoScroll, setAutoScroll] = useState(true);
  const invocationsById = useMemo(
    () => new Map((invocations || []).map(inv => [inv.invocationId, inv])),
    [invocations],
  );
  // Manual expand/collapse overrides, keyed by invocationId. Absent = default
  // (every turn starts collapsed, the last one too: a single turn's prompt
  // can be a wall of text) - this is what keeps a long trace navigable.
  const [expandedOverrides, setExpandedOverrides] = useState<Record<string, boolean>>({});

  const sortedElements = useMemo(
    () => [...elements].sort((a, b) => (a.timestamp || 0) - (b.timestamp || 0)),
    [elements],
  );

  const turns = useMemo(() => groupIntoTurns(sortedElements), [sortedElements]);
  const lastTurnId = turns.length > 0 ? turns[turns.length - 1].invocationId : null;

  useEffect(() => {
    if (autoScroll && containerRef.current) {
      containerRef.current.scrollTop = containerRef.current.scrollHeight;
    }
  }, [sortedElements, autoScroll]);

  const toggleTurn = (invocationId: string, currentlyExpanded: boolean) => {
    setExpandedOverrides(prev => ({ ...prev, [invocationId]: !currentlyExpanded }));
  };

  const setAllExpanded = (value: boolean) => {
    setExpandedOverrides(Object.fromEntries(turns.map(t => [t.invocationId, value])));
  };

  return (
    <div>
      {sortedElements.length > 0 && (
        <div style={{
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
          marginBottom: '8px',
        }}>
          <div style={{
            fontSize: '11px',
            fontWeight: 600,
            color: 'var(--text-tertiary)',
            display: 'flex',
            alignItems: 'center',
            gap: '6px',
          }}>
            {isActive && <span style={{
              display: 'inline-block',
              width: '6px',
              height: '6px',
              borderRadius: '50%',
              background: '#10b981',
              animation: 'pulse 2s ease-in-out infinite',
            }} />}
            CONVERSATION
            {turns.length > 1 && (
              <span style={{ color: 'var(--text-tertiary)', fontWeight: 400 }}>
                &middot; {turns.length} turns
              </span>
            )}
          </div>

          <div style={{ display: 'flex', alignItems: 'center', gap: '12px' }}>
            {turns.length > 1 && (
              <div style={{ display: 'flex', gap: '8px', fontSize: '11px' }}>
                <button
                  onClick={() => setAllExpanded(true)}
                  style={{ background: 'none', border: 'none', color: 'var(--accent-primary)', cursor: 'pointer', padding: 0, fontSize: '11px' }}
                >
                  Expand all
                </button>
                <button
                  onClick={() => setAllExpanded(false)}
                  style={{ background: 'none', border: 'none', color: 'var(--accent-primary)', cursor: 'pointer', padding: 0, fontSize: '11px' }}
                >
                  Collapse all
                </button>
              </div>
            )}

            <label style={{
              fontSize: '11px',
              color: 'var(--text-tertiary)',
              display: 'flex',
              alignItems: 'center',
              gap: '4px',
              cursor: 'pointer',
            }}>
              <input
                type="checkbox"
                checked={autoScroll}
                onChange={(e) => setAutoScroll(e.target.checked)}
              />
              Auto-scroll
            </label>
          </div>
        </div>
      )}

      <div
        ref={containerRef}
        style={{
          maxHeight: '600px',
          overflowY: 'auto',
        }}
      >
        {sortedElements.length === 0 && (
          <div style={{
            fontSize: '13px',
            color: 'var(--text-tertiary)',
            textAlign: 'center',
            padding: '24px',
          }}>
            Waiting for agent activity...
          </div>
        )}

        {turns.map((turn, i) => {
          const isLast = turn.invocationId === lastTurnId;
          const isExpanded = expandedOverrides[turn.invocationId] ?? false;
          const { preview, toolCount, toolErrorCount, toolLatencyMs, hasAgentResponse } = turnSummary(turn);
          const modelInfo = invocationsById.get(turn.invocationId)?.modelInfo;
          const tokens = (modelInfo?.inputTokens || modelInfo?.outputTokens)
            ? (modelInfo.inputTokens || 0) + (modelInfo.outputTokens || 0)
            : null;

          return (
            <div key={turn.invocationId} style={{ marginBottom: '10px' }}>
              <div
                onClick={() => toggleTurn(turn.invocationId, isExpanded)}
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  gap: '8px',
                  cursor: 'pointer',
                  padding: '6px 8px',
                  borderRadius: '6px',
                  background: isExpanded ? 'rgba(124, 58, 237, 0.06)' : 'transparent',
                  userSelect: 'none',
                }}
              >
                <span style={{ fontSize: '10px', color: 'var(--text-tertiary)', width: '10px', flexShrink: 0 }}>
                  {isExpanded ? '▾' : '▸'}
                </span>
                <span style={{
                  fontSize: '10px',
                  fontWeight: 700,
                  color: '#7C3AED',
                  textTransform: 'uppercase' as const,
                  letterSpacing: '0.5px',
                  flexShrink: 0,
                }}>
                  Turn {i + 1}
                </span>
                {toolCount > 0 && (
                  <span style={{
                    fontSize: '10px',
                    fontWeight: 600,
                    color: toolErrorCount > 0 ? '#ef4444' : 'var(--text-tertiary)',
                    background: toolErrorCount > 0 ? 'rgba(239, 68, 68, 0.1)' : 'var(--bg-primary)',
                    padding: '1px 6px',
                    borderRadius: '4px',
                    flexShrink: 0,
                  }}>
                    {toolCount} tool{toolCount !== 1 ? 's' : ''}
                    {toolErrorCount > 0 && ` (${toolErrorCount} failed)`}
                    {toolLatencyMs != null && ` · ${formatLatency(toolLatencyMs)}`}
                  </span>
                )}
                {tokens != null && (
                  <span style={{
                    fontSize: '10px',
                    fontWeight: 600,
                    color: '#10b981',
                    background: 'rgba(16, 185, 129, 0.08)',
                    padding: '1px 6px',
                    borderRadius: '4px',
                    flexShrink: 0,
                  }}>
                    ↓{(modelInfo?.inputTokens || 0).toLocaleString()} ↑{(modelInfo?.outputTokens || 0).toLocaleString()}
                  </span>
                )}
                {modelInfo?.turnLatencyMs != null && (
                  <span style={{
                    fontSize: '10px',
                    fontWeight: 600,
                    color: 'var(--text-tertiary)',
                    flexShrink: 0,
                  }}>
                    {formatLatency(modelInfo.turnLatencyMs)}
                  </span>
                )}
                {!isExpanded && preview && (
                  <span style={{
                    fontSize: '12px',
                    color: 'var(--text-secondary)',
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                    whiteSpace: 'nowrap',
                    flex: 1,
                    minWidth: 0,
                  }}>
                    {preview}
                  </span>
                )}
                {isLast && isActive && !hasAgentResponse && (
                  <span style={{ fontSize: '10px', color: '#10b981', fontWeight: 600, marginLeft: 'auto', flexShrink: 0 }}>
                    in progress
                  </span>
                )}
              </div>

              {isExpanded && (
                <div style={{
                  borderLeft: '2px solid rgba(124, 58, 237, 0.2)',
                  marginLeft: '13px',
                  paddingLeft: '14px',
                  paddingTop: '8px',
                }}>
                  {turn.elements.map((element, idx) => renderElement(element, idx))}
                </div>
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
}
