import { useEffect, useState } from 'react';
import { estimateCostUsd, formatCostUsd } from '../../lib/pricing';

interface SessionMetadataProps {
  session: {
    sessionId: string;
    traceId: string;
    metadata: Record<string, any>;
    startedAt: string;
    completedAt?: string | null;
    status: 'active' | 'complete';
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
        fontSize: '10px',
        color: 'var(--text-tertiary)',
        marginBottom: '4px',
        fontWeight: 600,
        textTransform: 'uppercase' as const,
      }}>
        {label}
      </div>
      <div style={{
        fontSize: '14px',
        fontWeight: 600,
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

/** Live-ticking while the session is still active and has no completedAt yet; frozen once it does. */
function useDuration(startedAt: string, completedAt: string | null | undefined, isActive: boolean): string | null {
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    if (!isActive || completedAt) return;
    const id = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(id);
  }, [isActive, completedAt]);

  const start = Date.parse(startedAt);
  if (Number.isNaN(start)) return null;
  const end = completedAt ? Date.parse(completedAt) : now;
  if (Number.isNaN(end)) return null;
  return formatDuration(end - start);
}

export function SessionMetadata({ session, liveStats, modelName }: SessionMetadataProps) {
  const totalTokens = liveStats.totalInputTokens + liveStats.totalOutputTokens;
  const provider = session.invocations?.[0]?.modelInfo?.provider;
  const totalCacheCreation = session.invocations?.reduce((sum, inv) => sum + (inv.modelInfo?.cacheCreationTokens || 0), 0) || 0;
  const totalCacheRead = session.invocations?.reduce((sum, inv) => sum + (inv.modelInfo?.cacheReadTokens || 0), 0) || 0;
  // Live ticker only while active. A completed session's span window is the
  // header chip; wall-clock here would add the 3s completion grace.
  const duration = useDuration(session.startedAt, session.completedAt, session.status === 'active');
  const estimatedCost = totalTokens > 0
    ? estimateCostUsd(modelName, liveStats.totalInputTokens, liveStats.totalOutputTokens)
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
            (↓{liveStats.totalInputTokens.toLocaleString()} ↑{liveStats.totalOutputTokens.toLocaleString()})
          </span>
        </MetadataItem>
      )}

      {estimatedCost !== null ? (
        <MetadataItem label="Est. Cost">
          <span style={{ color: '#10b981' }} title="Based on public list pricing for this model - not committed spend">
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

      {(totalCacheCreation > 0 || totalCacheRead > 0) && (
        <MetadataItem label="Cache Tokens">
          <span style={{ color: '#f59e0b' }}>
            {totalCacheRead > 0 && `${totalCacheRead.toLocaleString()} read`}
            {totalCacheCreation > 0 && totalCacheRead > 0 && ' / '}
            {totalCacheCreation > 0 && `${totalCacheCreation.toLocaleString()} created`}
          </span>
        </MetadataItem>
      )}

      {Object.keys(session.metadata).length > 0 && Object.entries(session.metadata).map(([key, value]) => (
        <MetadataItem key={key} label={key}>{String(value)}</MetadataItem>
      ))}
    </div>
  );
}
