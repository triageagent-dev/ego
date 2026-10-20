// A session's spans with
// their attributes and errors. Loaded on first open from
// GET /api/streaming/session-spans; spans with otel.status_code ERROR are
// marked, and their status message / exception fields are shown first.
import { useState } from 'react';
import { getSessionSpans, type SessionSpan } from '../../api/client';
import { PrettyText } from './PrettyText';

const ERROR_KEYS = ['otel.status_description', 'exception.type', 'exception.message', 'exception.stacktrace'];
const LONG_VALUE = 160;

function fmtDuration(us: number): string {
  if (us < 1000) return `${us}µs`;
  if (us < 1_000_000) return `${Math.round(us / 1000)}ms`;
  return `${(us / 1_000_000).toFixed(1)}s`;
}

function valueText(v: unknown): string {
  if (typeof v === 'string') return v;
  return JSON.stringify(v);
}

const isError = (s: SessionSpan) => s.tags['otel.status_code'] === 'ERROR';

function AttrValue({ value }: { value: string }) {
  const [open, setOpen] = useState(false);
  const long = value.length > LONG_VALUE || value.includes('\n');
  if (!long) return <span style={{ wordBreak: 'break-word' }}>{value}</span>;
  if (!open) {
    return (
      <span onClick={() => setOpen(true)} style={{ cursor: 'pointer', wordBreak: 'break-word' }} title="Click to expand">
        {value.replace(/\s+/g, ' ').slice(0, LONG_VALUE)}…{' '}
        <span style={{ color: 'var(--text-tertiary)', fontSize: '11px' }}>({value.length.toLocaleString()} chars)</span>
      </span>
    );
  }
  return (
    <div>
      <div onClick={() => setOpen(false)} style={{ cursor: 'pointer', color: 'var(--text-tertiary)', fontSize: '11px', marginBottom: '4px' }}>collapse</div>
      <PrettyText text={value} />
    </div>
  );
}

function AttrTable({ tags, keys }: { tags: Record<string, unknown>; keys: string[] }) {
  return (
    <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: '12px' }}>
      <tbody>
        {keys.map(k => (
          <tr key={k} style={{ borderTop: '1px solid var(--border)' }}>
            <td style={{ padding: '4px 10px 4px 0', fontFamily: 'monospace', color: 'var(--text-secondary)', verticalAlign: 'top', whiteSpace: 'nowrap', width: '1%' }}>{k}</td>
            <td style={{ padding: '4px 0', color: 'var(--text-primary)', verticalAlign: 'top' }}><AttrValue value={valueText(tags[k])} /></td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function SpanRow({ span }: { span: SessionSpan }) {
  const [open, setOpen] = useState(false);
  const err = isError(span);
  const errKeys = ERROR_KEYS.filter(k => span.tags[k] != null && span.tags[k] !== '');
  const keys = Object.keys(span.tags).filter(k => !errKeys.includes(k)).sort();
  return (
    <div>
      <div
        role="button"
        tabIndex={0}
        aria-expanded={open}
        onClick={() => setOpen(o => !o)}
        onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); setOpen(o => !o); } }}
        style={{
          display: 'flex', alignItems: 'center', gap: '8px', cursor: 'pointer', userSelect: 'none',
          padding: '4px 8px', paddingLeft: `${8 + span.depth * 16}px`, borderRadius: 'var(--radius)',
          background: open ? 'var(--bg-primary)' : 'transparent', fontSize: '12px',
        }}
      >
        <span style={{ fontSize: '10px', color: 'var(--text-tertiary)', width: '10px' }}>{open ? '▾' : '▸'}</span>
        <span style={{ fontFamily: 'monospace', color: err ? '#ef4444' : 'var(--text-primary)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', flex: 1, minWidth: 0 }}>
          {span.operation_name || span.span_id}
        </span>
        {err && (
          <span style={{ fontSize: '10px', fontWeight: 700, color: '#ef4444', background: 'rgba(239, 68, 68, 0.12)', padding: '1px 6px', borderRadius: 'var(--radius)' }}>ERROR</span>
        )}
        <span style={{ fontFamily: 'monospace', color: 'var(--text-tertiary)', flexShrink: 0 }}>{fmtDuration(span.duration)}</span>
      </div>
      {open && (
        <div style={{ padding: '6px 8px 10px', paddingLeft: `${26 + span.depth * 16}px` }}>
          {errKeys.length > 0 && (
            <div style={{ border: '1px solid rgba(239, 68, 68, 0.5)', background: 'rgba(239, 68, 68, 0.06)', borderRadius: 'var(--radius)', padding: '6px 10px', marginBottom: '8px' }}>
              <AttrTable tags={span.tags} keys={errKeys} />
            </div>
          )}
          {keys.length > 0
            ? <AttrTable tags={span.tags} keys={keys} />
            : <span style={{ color: 'var(--text-tertiary)', fontSize: '12px' }}>no attributes</span>}
        </div>
      )}
    </div>
  );
}

export function SpansPanel({ sessionId, spanCount }: { sessionId: string; spanCount?: number }) {
  const [open, setOpen] = useState(false);
  const [spans, setSpans] = useState<SessionSpan[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [errorsOnly, setErrorsOnly] = useState(false);

  const toggle = () => {
    const next = !open;
    setOpen(next);
    if (next && spans === null) {
      setError(null);
      getSessionSpans(sessionId).then(setSpans).catch((e: Error) => setError(e.message));
    }
  };

  const errCount = spans?.filter(isError).length ?? 0;
  const shown = spans && errorsOnly ? spans.filter(isError) : spans;

  return (
    <div style={{ borderTop: '1px solid var(--border)', paddingTop: '12px', marginTop: '8px' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: '10px' }}>
        <span
          role="button"
          tabIndex={0}
          onClick={toggle}
          onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); toggle(); } }}
          style={{ cursor: 'pointer', userSelect: 'none', fontSize: '12px', fontWeight: 600, color: 'var(--text-secondary)', textTransform: 'uppercase', letterSpacing: '0.5px' }}
        >
          {open ? '▾' : '▸'} Spans{spans ? ` (${spans.length})` : spanCount ? ` (${spanCount})` : ''}
        </span>
        {open && errCount > 0 && (
          <label style={{ fontSize: '12px', color: '#ef4444', display: 'flex', alignItems: 'center', gap: '4px', cursor: 'pointer' }}>
            <input type="checkbox" checked={errorsOnly} onChange={e => setErrorsOnly(e.target.checked)} />
            errors only ({errCount})
          </label>
        )}
      </div>
      {open && (
        <div style={{ marginTop: '8px' }}>
          {error && <div style={{ color: '#ef4444', fontSize: '12px' }}>{error}</div>}
          {!error && !spans && <div style={{ color: 'var(--text-tertiary)', fontSize: '12px' }}>loading…</div>}
          {shown?.map(s => <SpanRow key={s.span_id} span={s} />)}
        </div>
      )}
    </div>
  );
}
