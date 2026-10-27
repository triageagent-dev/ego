import { estimateCostUsd, formatCostUsd } from '../../lib/pricing';

interface SessionMetadataProps {
  session: {
    sessionId: string;
    traceId: string;
    metadata: Record<string, any>;
    startedAt: string;
    completedAt?: string | null;
    status: 'active' | 'complete';
    // The parts of the session's input tokens read from and written to the prompt cache, when the server says.
    cacheReadTokens?: number;
    cacheWriteTokens?: number;
    invocations?: Array<{
      modelInfo?: {
        provider?: string;
        cacheCreationTokens?: number;
        cacheReadTokens?: number;
      };
    }>;
  };
  liveStats: {
    totalInputTokens: number;
    totalOutputTokens: number;
  };
  /** Model id used to look up a per-token rate for the cost estimate. */
  modelName?: string;
}

function MetadataItem({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <div style={{
        fontSize: '11px',
        color: 'var(--text-tertiary)',
        marginBottom: '4px',
        fontWeight: 600,
        textTransform: 'uppercase' as const,
      }}>
        {label}
      </div>
      <div style={{
        fontSize: '14px',
        fontWeight: 400,
        color: 'var(--text-primary)',
      }}>
        {children}
      </div>
    </div>
  );
}

function formatDuration(ms: number): string {
  const totalSeconds = Math.max(0, Math.round(ms / 1000));
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  if (minutes === 0) return `${seconds}s`;
  return `${minutes}m ${String(seconds).padStart(2, '0')}s`;
}

/** Up to now while the session has no completedAt yet, as of the last update that redrew it; frozen once it has one. */
function formatElapsed(startedAt: string, completedAt: string | null | undefined): string | null {
  const start = Date.parse(startedAt);
  if (Number.isNaN(start)) return null;
  const end = completedAt ? Date.parse(completedAt) : Date.now();
  if (Number.isNaN(end)) return null;
  return formatDuration(end - start);
}

export function SessionMetadata({ session, liveStats, modelName }: SessionMetadataProps) {
  const totalTokens = liveStats.totalInputTokens + liveStats.totalOutputTokens;
  const provider = session.invocations?.[0]?.modelInfo?.provider;
  const totalCacheCreation = session.invocations?.reduce((sum, inv) => sum + (inv.modelInfo?.cacheCreationTokens || 0), 0) || 0;
  const totalCacheRead = session.invocations?.reduce((sum, inv) => sum + (inv.modelInfo?.cacheReadTokens || 0), 0) || 0;
  // The server's totals cover the whole session, spans without invocations included; the invocations are the fallback.
  const cacheRead = session.cacheReadTokens ?? totalCacheRead;
  const cacheWrite = session.cacheWriteTokens ?? totalCacheCreation;
  // Elapsed time only while active. A completed session's span window is the
  // header chip; wall-clock here would add the 3s completion grace.
  const duration = formatElapsed(session.startedAt, session.completedAt);
  const estimatedCost = totalTokens > 0
    ? estimateCostUsd(
        modelName, liveStats.totalInputTokens, liveStats.totalOutputTokens,
        cacheRead, cacheWrite,
      )
    : null;

  return (
    <div style={{
      padding: '12px 0',
      borderBottom: '1px solid var(--border)',
      marginBottom: '12px',
      display: 'flex',
      gap: '24px',
      alignItems: 'center',
      flexWrap: 'wrap',
    }}>
      {totalTokens > 0 && (
        <MetadataItem label="Tokens">
          <span style={{ color: '#10b981' }}>
            {totalTokens.toLocaleString()}
          </span>
          <span style={{
            fontSize: '11px',
            color: 'var(--text-tertiary)',
            marginLeft: '6px',
          }}>
            (↓{liveStats.totalInputTokens.toLocaleString()} ↑{liveStats.totalOutputTokens.toLocaleString()}{cacheRead > 0 && ` c${cacheRead.toLocaleString()}`})
          </span>
        </MetadataItem>
      )}

      {estimatedCost !== null ? (
        <MetadataItem label="Est. Cost">
          <span style={{ color: '#10b981' }} title="Computed from token counts and public list prices: approximate, it will differ somewhat from the provider's bill">
            {formatCostUsd(estimatedCost)}
          </span>
        </MetadataItem>
      ) : totalTokens > 0 && modelName && (
        <MetadataItem label="Est. Cost">
          <span
            style={{ color: 'var(--text-tertiary)', fontWeight: 400, fontSize: '12px' }}
            title="No list-price entry for this model yet - add one to lib/pricing.ts"
          >
            no rate for {modelName}
          </span>
        </MetadataItem>
      )}

      {provider && (
        <MetadataItem label="Provider">{provider}</MetadataItem>
      )}

      {session.startedAt && (
        <MetadataItem label="Started">
          {new Date(session.startedAt).toLocaleString(undefined, {
            dateStyle: 'medium',
            timeStyle: 'medium',
          })}
        </MetadataItem>
      )}

      {session.status === 'active' && duration && (
        <MetadataItem label="Duration">
          {duration}
          {session.status === 'active' && !session.completedAt && (
            <span style={{ color: '#10b981', marginLeft: '4px' }}>●</span>
          )}
        </MetadataItem>
      )}

      {Object.keys(session.metadata).length > 0 && Object.entries(session.metadata).map(([key, value]) => (
        <MetadataItem key={key} label={key}>{String(value)}</MetadataItem>
      ))}
    </div>
  );
}
