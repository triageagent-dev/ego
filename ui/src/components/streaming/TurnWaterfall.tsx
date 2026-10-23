import { useEffect, useState } from 'react';
import { getSessionSpans, type SessionSpan } from '../../api/client';
import { resolveTurn, type TurnDetail, type TurnStep } from '../../lib/turnSteps';
import { fmtDuration, shortCount } from '../../lib/turns';
import type { TurnRow } from '../../lib/types';

const MODEL = '#10b981';
const TOOL = '#a79ff0';
const WAIT = '#fbbf24';
const FAILED = '#ef4444';
const AGENT = 'var(--border-default)';

// Spans of a session are read once for all the turns picked in it.
const cache = new Map<string, { at: number; spans: Promise<SessionSpan[]> }>();
const CACHE_MS = 15_000;
function spansOf(session: string): Promise<SessionSpan[]> {
  const hit = cache.get(session);
  if (hit && Date.now() - hit.at < CACHE_MS) return hit.spans;
  const spans = getSessionSpans(session);
  cache.set(session, { at: Date.now(), spans });
  spans.catch(() => cache.delete(session));
  return spans;
}

const us = (v: number) => v / 1000;

function Row({ step, totalUs }: { step: TurnStep; totalUs: number }) {
  const left = Math.min(99.6, (step.offsetUs / totalUs) * 100);
  const width = Math.max(0.4, Math.min(100 - left, (step.durationUs / totalUs) * 100));
  const approvalW = step.approvalUs ? Math.min(100, (step.approvalUs / Math.max(1, step.durationUs)) * 100) : 0;
  const color = step.failed ? FAILED : step.kind === 'model' ? MODEL : TOOL;
  return (
    <div style={{ marginBottom: 3 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, minHeight: 20 }}>
        <div style={{ width: 210, flexShrink: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', fontSize: 12, fontFamily: 'monospace', color: step.failed ? FAILED : step.kind === 'model' ? MODEL : '#b5abfc' }} title={[step.label, step.detail].filter(Boolean).join(' · ')}>
          {step.kind === 'model' ? '◆' : '→'} {step.label}
          {step.detail && <span style={{ color: 'var(--text-tertiary)' }}> {step.detail}</span>}
        </div>
        <div style={{ position: 'relative', flex: 1, height: 12, background: 'rgba(255,255,255,0.03)', borderRadius: 2 }}>
          <div style={{ position: 'absolute', left: `${left}%`, width: `${width}%`, top: 0, bottom: 0, borderRadius: 2, background: color, overflow: 'hidden' }}>
            {approvalW > 0 && <div style={{ width: `${approvalW}%`, height: '100%', background: step.failed ? 'rgba(251,191,36,0.9)' : WAIT }} title="Waiting to be approved" />}
          </div>
        </div>
        <div style={{ width: 118, flexShrink: 0, textAlign: 'right', fontSize: 11, fontFamily: 'monospace', color: 'var(--text-tertiary)', whiteSpace: 'nowrap' }}>
          {fmtDuration(us(step.durationUs))}
          {step.kind === 'model' && (step.tokensIn || step.tokensOut) ? ` · ↓${shortCount(step.tokensIn ?? 0)} ↑${shortCount(step.tokensOut ?? 0)}` : ''}
        </div>
      </div>
      {step.failed && step.message && <div style={{ marginLeft: 220, fontSize: 11, color: FAILED, fontFamily: 'monospace' }}>{step.message}</div>}
    </div>
  );
}

// Where one turn's time went: a bar of the whole turn split into the model, the tools, the wait for approval and the
// agent itself, then every model request and tool call on the turn's own time axis, a tool call with its wait first.
export function TurnWaterfall({ turn }: { turn: TurnRow }) {
  const [detail, setDetail] = useState<TurnDetail | null | undefined>(undefined);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    setDetail(undefined);
    setError(null);
    spansOf(turn.session)
      .then((spans) => live && setDetail(resolveTurn(spans, turn.rootSpanId) ?? null))
      .catch((e: Error) => live && setError(e.message));
    return () => { live = false; };
  }, [turn.session, turn.rootSpanId]);

  const total = Math.max(1, turn.durationMs);
  const agent = Math.max(0, total - turn.modelMs - turn.toolMs - turn.approvalMs);
  const parts = [
    { label: 'model', ms: turn.modelMs, color: MODEL },
    { label: 'tools', ms: turn.toolMs, color: TOOL },
    { label: 'approval wait', ms: turn.approvalMs, color: WAIT },
    { label: 'agent', ms: agent, color: AGENT },
  ];
  const pct = (ms: number) => `${Math.round((ms / total) * 100)}%`;

  return (
    <div>
      <div style={{ display: 'flex', height: 10, borderRadius: 3, overflow: 'hidden', marginBottom: 6 }}>
        {parts.filter((p) => p.ms > 0).map((p) => (
          <div key={p.label} style={{ width: `${(p.ms / total) * 100}%`, background: p.color }} title={`${p.label} ${fmtDuration(p.ms)}`} />
        ))}
      </div>
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: '4px 16px', fontSize: 11.5, color: 'var(--text-tertiary)', marginBottom: 12 }}>
        {parts.map((p) => (
          <span key={p.label}>
            <span style={{ display: 'inline-block', width: 8, height: 8, borderRadius: 2, background: p.color, marginRight: 5 }} />
            {p.label} {fmtDuration(p.ms)} <span style={{ opacity: 0.7 }}>{pct(p.ms)}</span>
          </span>
        ))}
      </div>
      {error && <div style={{ color: FAILED, fontSize: 12 }}>{error}</div>}
      {!error && detail === undefined && <div style={{ color: 'var(--text-tertiary)', fontSize: 12 }}>loading the turn…</div>}
      {detail === null && <div style={{ color: 'var(--text-tertiary)', fontSize: 12 }}>the spans of this turn are no longer held</div>}
      {detail && detail.steps.length === 0 && <div style={{ color: 'var(--text-tertiary)', fontSize: 12 }}>no model request or tool call in this turn</div>}
      {detail && detail.steps.length > 0 && (
        <div style={{ maxHeight: 420, overflowY: 'auto', paddingRight: 4 }}>
          {detail.steps.map((s, i) => <Row key={i} step={s} totalUs={Math.max(1, detail.durationUs)} />)}
        </div>
      )}
    </div>
  );
}
