import { useEffect, useMemo, useRef, useState } from 'react';
import type { TurnRow, TurnSegment } from '../../lib/types';
import { fmtDuration, shortCount } from '../../lib/turns';

const OK = '#10b981';
const FAILED = '#ef4444';
const CACHED = 'rgba(167, 159, 240, 0.35)';
const FRESH = '#a79ff0';

const LEFT = 46;
const RIGHT = 10;
const TOP = 8;
const LANE_H = 96;
const GAP = 14;
const TOKEN_H = 40;
const AXIS = 22;
const HEIGHT = TOP + LANE_H + GAP + TOKEN_H + AXIS;

// Where the ticks go on the time axis, from fine to coarse; the finest step that leaves room is used.
const STEPS_MS = [10, 30, 60, 180, 360, 720, 1440, 2880, 10080].map((m) => m * 60_000);
const DURATION_TICKS = [1_000, 10_000, 60_000, 600_000, 3_600_000];

function tickLabel(ms: number, stepMs: number): string {
  const d = new Date(ms);
  if (stepMs >= 1440 * 60_000) return d.toLocaleDateString(undefined, { day: 'numeric', month: 'short' });
  const time = d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', hour12: false });
  return d.getHours() === 0 && d.getMinutes() === 0 ? `${d.toLocaleDateString(undefined, { day: 'numeric', month: 'short' })} ${time}` : time;
}

const bad = (t: TurnRow) => !!t.failed || !!t.failedTools;

// An always-on agent over a window, one stem per turn. A stem stands where the turn began and is as tall as it was long
// (on a log scale: a 2 s turn and a 20 min one both show); red marks a turn with a failure. Below it the tokens of each
// turn, the part read from the cache dim. Empty stretches are the agent asleep. A dotted line is where a new part of
// the thread (a segment ego scores) begins.
export function TurnTimeline({
  turns, segments, fromMs, toMs, selected, onSelect,
}: {
  turns: TurnRow[];
  segments: TurnSegment[];
  fromMs: number;
  toMs: number;
  selected: TurnRow | null;
  onSelect: (t: TurnRow) => void;
}) {
  const wrapRef = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(0);
  const [hover, setHover] = useState<number | null>(null);

  useEffect(() => {
    const el = wrapRef.current;
    if (!el) return;
    const measure = () => setWidth(el.clientWidth);
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    return () => observer.disconnect();
  }, []);

  const plotW = Math.max(10, width - LEFT - RIGHT);
  const x = (ms: number) => LEFT + ((ms - fromMs) / Math.max(1, toMs - fromMs)) * plotW;

  const scale = useMemo(() => {
    const maxDur = Math.max(1000, ...turns.map((t) => t.durationMs));
    const maxTok = Math.max(1, ...turns.map((t) => t.inputTokens + t.outputTokens));
    return { maxDur, maxTok };
  }, [turns]);
  const stemH = (ms: number) => (LANE_H * Math.log1p(Math.max(0, ms) / 1000)) / Math.log1p(scale.maxDur / 1000);
  const tokenH = (n: number) => TOKEN_H * Math.sqrt(n / scale.maxTok);

  const xs = useMemo(() => turns.map((t) => x(t.startMs)), [turns, fromMs, toMs, plotW]);
  // The pointer in the svg's own pixels: the page may be zoomed, which scales the client coordinates but not the svg's.
  const localX = (e: React.MouseEvent<SVGSVGElement>) => {
    const rect = e.currentTarget.getBoundingClientRect();
    return (e.clientX - rect.left) / (rect.width / Math.max(1, width));
  };
  const nearest = (px: number): number | null => {
    let best: number | null = null;
    let bestD = 6;
    for (let i = 0; i < xs.length; i++) {
      const d = Math.abs(xs[i] - px);
      if (d <= bestD) {
        best = i;
        bestD = d;
      }
    }
    return best;
  };
  const selectedIdx = selected ? turns.findIndex((t) => t.rootSpanId === selected.rootSpanId && t.session === selected.session) : -1;

  const step = STEPS_MS.find((s) => plotW / ((toMs - fromMs) / s) >= 70) ?? STEPS_MS[STEPS_MS.length - 1];
  const ticks: number[] = [];
  for (let t = Math.ceil(fromMs / step) * step; t <= toMs; t += step) ticks.push(t);

  const onKey = (e: React.KeyboardEvent) => {
    if (turns.length === 0 || (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight')) return;
    e.preventDefault();
    const at = selectedIdx < 0 ? turns.length - 1 : Math.max(0, Math.min(turns.length - 1, selectedIdx + (e.key === 'ArrowRight' ? 1 : -1)));
    onSelect(turns[at]);
  };

  const h = hover != null ? turns[hover] : null;
  const tipLeft = h ? Math.min(Math.max(xs[hover!], 120), width - 120) : 0;
  const base = TOP + LANE_H;
  const tokBase = base + GAP + TOKEN_H;

  return (
    <div ref={wrapRef} style={{ position: 'relative', width: '100%' }} tabIndex={0} onKeyDown={onKey} aria-label="Turns over time; arrow keys move between turns">
      {width > 0 && (
        <svg
          width={width}
          height={HEIGHT}
          style={{ display: 'block', cursor: hover != null ? 'pointer' : 'default', outline: 'none' }}
          onMouseMove={(e) => setHover(nearest(localX(e)))}
          onMouseLeave={() => setHover(null)}
          onClick={(e) => {
            const i = nearest(localX(e));
            if (i != null) onSelect(turns[i]);
          }}
        >
          {DURATION_TICKS.filter((d) => d <= scale.maxDur).map((d) => (
            <g key={d}>
              <line x1={LEFT} x2={LEFT + plotW} y1={base - stemH(d)} y2={base - stemH(d)} stroke="var(--border-default)" strokeWidth={1} opacity={0.5} />
              <text x={LEFT - 6} y={base - stemH(d) + 3} textAnchor="end" fontSize={10} fill="var(--text-muted)">{fmtDuration(d)}</text>
            </g>
          ))}
          <line x1={LEFT} x2={LEFT + plotW} y1={base} y2={base} stroke="var(--border-default)" />
          <line x1={LEFT} x2={LEFT + plotW} y1={tokBase} y2={tokBase} stroke="var(--border-default)" />
          <text x={LEFT - 6} y={tokBase - 2} textAnchor="end" fontSize={10} fill="var(--text-muted)">tokens</text>

          {ticks.map((t) => (
            <g key={t}>
              <line x1={x(t)} x2={x(t)} y1={tokBase} y2={tokBase + 4} stroke="var(--text-muted)" />
              <text x={x(t)} y={tokBase + 16} textAnchor="middle" fontSize={10} fill="var(--text-muted)">{tickLabel(t, step)}</text>
            </g>
          ))}

          {segments.map((s) => {
            const sx = x(Math.max(s.startMs, fromMs));
            return (
              <g key={s.session}>
                <line x1={sx} x2={sx} y1={TOP} y2={tokBase} stroke="var(--text-muted)" strokeDasharray="2 3" opacity={0.55} />
                {(s.part ?? 1) > 1 && <text x={sx + 3} y={TOP + 9} fontSize={9} fill="var(--text-muted)">~{s.part}</text>}
              </g>
            );
          })}

          {turns.map((t, i) => {
            const tx = xs[i];
            const color = bad(t) ? FAILED : OK;
            const cached = t.cacheReadTokens ?? 0;
            const total = t.inputTokens + t.outputTokens;
            const hCached = tokenH(cached);
            const hTotal = tokenH(total);
            return (
              <g key={`${t.session}:${t.rootSpanId}`} opacity={hover != null && hover !== i && selectedIdx !== i ? 0.55 : 1}>
                <line x1={tx} x2={tx} y1={base} y2={base - Math.max(2, stemH(t.durationMs))} stroke={color} strokeWidth={2} strokeLinecap="round" />
                <line x1={tx} x2={tx} y1={tokBase} y2={tokBase - hCached} stroke={CACHED} strokeWidth={2} />
                <line x1={tx} x2={tx} y1={tokBase - hCached} y2={tokBase - hTotal} stroke={FRESH} strokeWidth={2} />
              </g>
            );
          })}

          {selectedIdx >= 0 && (
            <g pointerEvents="none">
              <line x1={xs[selectedIdx]} x2={xs[selectedIdx]} y1={TOP} y2={tokBase} stroke="var(--text-primary)" strokeWidth={1} opacity={0.5} />
              <circle cx={xs[selectedIdx]} cy={base - Math.max(2, stemH(turns[selectedIdx].durationMs))} r={4} fill="none" stroke="var(--text-primary)" strokeWidth={1.5} />
            </g>
          )}
        </svg>
      )}
      {h && (
        <div style={{
          position: 'absolute', top: 0, left: tipLeft, transform: 'translateX(-50%)', pointerEvents: 'none', zIndex: 3,
          background: 'rgba(0, 0, 0, 0.92)', color: '#fff', padding: '8px 10px', borderRadius: 6, fontSize: 12, lineHeight: 1.5, whiteSpace: 'nowrap',
        }}>
          <div style={{ fontWeight: 600 }}>
            {new Date(h.startMs).toLocaleString(undefined, { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false })}
            {(h.part ?? 1) > 1 && <span style={{ fontWeight: 400, opacity: 0.7 }}> · part {h.part}</span>}
          </div>
          <div>{fmtDuration(h.durationMs)} · {h.requests} model request{h.requests === 1 ? '' : 's'} · {h.tools} tool call{h.tools === 1 ? '' : 's'}</div>
          <div>↓{shortCount(h.inputTokens)} ↑{shortCount(h.outputTokens)}{h.cacheReadTokens ? ` · ${Math.round((h.cacheReadTokens / Math.max(1, h.inputTokens)) * 100)}% cached` : ''}</div>
          {bad(h) && (
            <div style={{ color: '#fca5a5' }}>
              {Object.entries(h.toolCalls ?? {}).filter(([, c]) => c.failed).map(([n, c]) => `${n} failed${c.failed! > 1 ? ` ×${c.failed}` : ''}`).join(', ') || 'a span failed'}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
