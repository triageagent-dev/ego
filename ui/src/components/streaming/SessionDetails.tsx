import { OnlineScoresPanel } from './OnlineScoresPanel';
import { SessionMetadata } from './SessionMetadata';
import { SpansPanel } from './SpansPanel';
import { ClaudeCodePanel } from './ClaudeCodePanel';
import { LiveConversationPanel } from './LiveConversationPanel';
import { sessionFigures, type SessionCardProps } from './SessionCard';
import { formatTurnTime } from '../../lib/trace-helpers';

// Everything about one session: its identity and metadata, the conversation, the online scores and the spans.
// Drawn in the side panel of the sessions view when a row is opened.
export function SessionDetails({ session }: { session: SessionCardProps['session'] }) {
  const { conversationElements, liveStats, totalTokens, modelName } = sessionFigures(session);

  return (
    <div>
      {session.startedAt && (totalTokens > 0 || Object.keys(session.metadata).length > 0) && (
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

      {conversationElements.length > 0 && (
        <LiveConversationPanel
          elements={conversationElements}
          isActive={session.status === 'active'}
          invocations={session.invocations}
        />
      )}

      {session.invocations && session.invocations.length > 0 && conversationElements.length === 0 && (
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

      {session.rootSpanName?.startsWith('claude_code.') && <ClaudeCodePanel sessionId={session.sessionId} />}

      {session.status === 'complete' && <OnlineScoresPanel sessionId={session.sessionId} />}
      {<SpansPanel sessionId={session.sessionId} />}
    </div>
  );
}
