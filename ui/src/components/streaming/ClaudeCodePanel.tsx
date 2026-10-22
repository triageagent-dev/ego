import { useEffect, useState, type ReactNode } from 'react';
import { getSessionSpans } from '../../api/client';
import { resolveClaudeCode, type CCModelRequest, type CCStep, type CCToolCall, type CCTurn } from '../../lib/claudeCode';
import { formatSpanDuration, formatTurnTime } from '../../lib/trace-helpers';
import { Pager, PAGE_SIZE, clampPage } from '../ui/Pager';
import { PrettyText } from './PrettyText';

const USER = '#a79ff0';
const AGENT = '#10b981';
const FAILED = '#ef4444';

// 43261 -> 43.3k
function short(n: number): string {
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
  if (n >= 1e3) return `${(n / 1e3).toFixed(1)}k`;
  return String(n);
}

const chip = (color: string, background: string) => ({
  fontSize: '10px',
  fontWeight: 600,
  color,
  background,
  padding: '1px 6px',
  borderRadius: 'var(--radius)',
  flexShrink: 0 as const,
});

const muted = { fontSize: '10px', color: 'var(--text-tertiary)', fontFamily: 'monospace', whiteSpace: 'nowrap' as const };

function Prompt({ turn }: { turn: CCTurn }) {
  const { text, length } = turn.prompt;
  return (
    <div style={{ marginBottom: '12px' }}>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: '8px' }}>
        <span style={{ fontSize: '10px', color: USER, fontWeight: 700, textTransform: 'uppercase', letterSpacing: '0.5px' }}>User</span>
        {length != null && <span style={{ fontSize: '10px', color: 'var(--text-tertiary)' }}>{length.toLocaleString()} chars</span>}
      </div>
      {text ? (
        <div style={{ fontSize: '12.5px', color: 'var(--text-primary)', lineHeight: 1.5, marginTop: '6px', paddingLeft: '18px' }}>
          <PrettyText text={text} />
        </div>
      ) : (
        <div style={{ fontSize: '12px', color: 'var(--text-tertiary)', marginTop: '4px', paddingLeft: '18px', fontStyle: 'italic' }}>
          the sender did not send the prompt text
        </div>
      )}
    </div>
  );
}

function ModelRequest({ r }: { r: CCModelRequest }) {
  return (
    <div style={{ display: 'flex', alignItems: 'baseline', flexWrap: 'wrap', gap: '8px', marginBottom: '4px', paddingLeft: '12px' }}>
      <span style={{ fontSize: '12px', color: r.error ? FAILED : AGENT, fontFamily: 'monospace', fontWeight: 500 }}>◆ {r.model}</span>
      <span style={muted}>
        {formatSpanDuration(r.ms)}
        {r.ttftMs != null && ` · first token ${formatSpanDuration(r.ttftMs)}`}
      </span>
      <span style={chip(AGENT, 'rgba(16, 185, 129, 0.08)')} title={r.cacheReadTokens > 0 ? `${r.cacheReadTokens.toLocaleString()} of the input read from the cache` : undefined}>
        ↓{short(r.inputTokens)} ↑{short(r.outputTokens)}
      </span>
      {r.stopReason && <span style={muted}>{r.stopReason === 'tool_use' ? 'then used a tool' : r.stopReason === 'end_turn' ? 'answered' : r.stopReason}</span>}
      {r.error && <span style={chip(FAILED, 'rgba(239, 68, 68, 0.1)')}>{r.error}</span>}
    </div>
  );
}

function ToolCall({ c }: { c: CCToolCall }) {
  const rejected = c.approval?.decision === 'reject';
  const bad = !!c.error || rejected;
  return (
    <div style={{ marginBottom: '4px', paddingLeft: '12px' }}>
      <div style={{ display: 'flex', alignItems: 'baseline', flexWrap: 'wrap', gap: '8px' }}>
        <span style={{ fontSize: '12px', color: bad ? FAILED : '#b5abfc', fontFamily: 'monospace', fontWeight: 500 }}>→ {c.name}</span>
        {c.server && <span style={chip('var(--text-secondary)', 'var(--bg-primary)')} title="MCP server">{c.server}</span>}
        {c.detail && <span style={{ fontSize: '11px', color: 'var(--text-secondary)', fontFamily: 'monospace' }}>{c.detail}</span>}
        <span style={muted}>{formatSpanDuration(c.ms)}</span>
        {c.run && <span style={{ ...muted, color: c.run.ok ? AGENT : FAILED }}>{c.run.ok ? '✓' : '✗'} ran {formatSpanDuration(c.run.ms)}</span>}
        {c.approval && (
          <span style={{ ...muted, color: rejected ? FAILED : 'var(--text-tertiary)' }} title="Time the call waited to be approved">
            {rejected ? 'rejected' : 'approved'}
            {c.approval.source && ` by ${c.approval.source}`} · waited {formatSpanDuration(c.approval.ms)}
          </span>
        )}
      </div>
      {c.error && <div style={{ fontSize: '11px', color: FAILED, fontFamily: 'monospace', paddingLeft: '14px' }}>{c.error}</div>}
    </div>
  );
}

function Step({ s }: { s: CCStep }): ReactNode {
  if (s.kind === 'llm') return <ModelRequest r={s} />;
  if (s.kind === 'tool') return <ToolCall c={s} />;
  return (
    <div style={{ ...muted, marginBottom: '4px', paddingLeft: '12px' }}>
      {s.op.replace('claude_code.', '')} · {formatSpanDuration(s.ms)}
    </div>
  );
}

function Turn({ turn, open, onToggle }: { turn: CCTurn; open: boolean; onToggle: () => void }) {
  const preview = turn.prompt.text ? turn.prompt.text.replace(/\s+/g, ' ').trim().slice(0, 72) : '';
  return (
    <div style={{ marginBottom: '10px' }}>
      <div
        role="button"
        tabIndex={0}
        aria-expanded={open}
        onClick={onToggle}
        onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onToggle(); } }}
        style={{
          display: 'flex', alignItems: 'center', gap: '8px', cursor: 'pointer', userSelect: 'none', padding: '6px 8px',
          borderRadius: 'var(--radius)', background: open ? 'rgba(167, 159, 240, 0.06)' : 'transparent',
        }}
      >
        <span style={{ fontSize: '10px', color: 'var(--text-tertiary)', width: '10px', flexShrink: 0 }}>{open ? '▾' : '▸'}</span>
        <span style={{ fontSize: '10px', fontWeight: 700, color: USER, textTransform: 'uppercase', letterSpacing: '0.5px', flexShrink: 0 }}>Turn {turn.sequence}</span>
        <span title="When this turn started" style={{ fontSize: '10px', color: 'var(--text-muted)', flexShrink: 0 }}>{formatTurnTime(turn.startUs / 1000)}</span>
        {turn.tools > 0 && (
          <span style={chip(turn.failedTools > 0 ? FAILED : 'var(--text-tertiary)', turn.failedTools > 0 ? 'rgba(239, 68, 68, 0.1)' : 'var(--bg-primary)')}>
            {turn.tools} tool{turn.tools !== 1 ? 's' : ''}
            {turn.failedTools > 0 && ` (${turn.failedTools} failed)`}
          </span>
        )}
        {turn.requests > 0 && <span style={chip(AGENT, 'rgba(16, 185, 129, 0.08)')}>↓{short(turn.inputTokens)} ↑{short(turn.outputTokens)}</span>}
        <span style={{ fontSize: '10px', fontWeight: 600, color: 'var(--text-tertiary)', flexShrink: 0 }}>{formatSpanDuration(turn.ms)}</span>
        {!open && preview && (
          <span style={{ fontSize: '12px', color: 'var(--text-secondary)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', flex: 1, minWidth: 0 }}>{preview}</span>
        )}
      </div>
      {open && (
        <div style={{ borderLeft: '2px solid rgba(167, 159, 240, 0.2)', marginLeft: '13px', paddingLeft: '14px', paddingTop: '8px' }}>
          <Prompt turn={turn} />
          {turn.steps.map((s, i) => <Step key={i} s={s} />)}
        </div>
      )}
    </div>
  );
}

// A Claude Code session read from its spans: per prompt, the model requests and the tool calls in the order they ran,
// each with its time, tokens and outcome, a tool call with the wait for its approval and the run after it. Opens when
// the session is Claude Code's; the raw spans stay below it.
export function ClaudeCodePanel({ sessionId }: { sessionId: string }) {
  const [turns, setTurns] = useState<CCTurn[] | null>(null);
  const [open, setOpen] = useState<Record<string, boolean>>({});
  const [page, setPage] = useState(0);

  useEffect(() => {
    let live = true;
    getSessionSpans(sessionId)
      .then((spans) => live && setTurns(resolveClaudeCode(spans)))
      .catch(() => live && setTurns([]));
    return () => { live = false; };
  }, [sessionId]);

  if (!turns || turns.length === 0) return null;
  const at = clampPage(page, turns.length);
  const shown = turns.slice(at * PAGE_SIZE, (at + 1) * PAGE_SIZE);
  const setAll = (value: boolean) => setOpen(Object.fromEntries(turns.map((t) => [t.id, value])));

  return (
    <div style={{ marginTop: '16px' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '8px' }}>
        <div style={{ fontSize: '11px', fontWeight: 600, color: 'var(--text-tertiary)' }}>
          CLAUDE CODE
          {turns.length > 1 && <span style={{ fontWeight: 400 }}> &middot; {turns.length} turns</span>}
        </div>
        {turns.length > 1 && (
          <div style={{ display: 'flex', gap: '8px', fontSize: '11px' }}>
            <button onClick={() => setAll(true)} style={{ background: 'none', border: 'none', color: 'var(--accent-primary)', cursor: 'pointer', padding: 0, fontSize: '11px' }}>Expand all</button>
            <button onClick={() => setAll(false)} style={{ background: 'none', border: 'none', color: 'var(--accent-primary)', cursor: 'pointer', padding: 0, fontSize: '11px' }}>Collapse all</button>
          </div>
        )}
      </div>
      <Pager total={turns.length} page={at} onChange={setPage} />
      {shown.map((t) => (
        <Turn key={t.id} turn={t} open={open[t.id] ?? turns.length === 1} onToggle={() => setOpen((o) => ({ ...o, [t.id]: !(o[t.id] ?? turns.length === 1) }))} />
      ))}
      <Pager total={turns.length} page={at} onChange={setPage} />
    </div>
  );
}
