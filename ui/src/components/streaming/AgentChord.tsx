import { useMemo, useState } from 'react';
import { chord, ribbon } from 'd3-chord';
import { arc } from 'd3-shape';
import { chordOf } from '../../lib/agentChord';
import type { Graph } from '../../lib/agentGraph';
import type { TurnRow } from '../../lib/types';

// The chord diagram of what an agent uses together: an arc for each model and tool, grouped by MCP server, a ribbon
// between two of them as wide as the turns that used both. Hover lights the ribbons of one arc, a click selects it.

const PALETTE = ['#a79ff0', '#5eb1ef', '#4ec9a0', '#e5a550', '#e07a9b', '#9fbf5f', '#c58af0', '#7f93a8'];
const FAILED = '#ef4444';
const WARN = '#f59e0b';
const R = 280;
const BAND = 14;
const MAX_LABEL = 24;
const shorten = (s: string) => (s.length <= MAX_LABEL ? s : `${s.slice(0, 14)}…${s.slice(-8)}`);

export function AgentChord({
  rows, graph, selectedId, onSelectNode,
}: {
  rows: TurnRow[];
  graph: Graph;
  selectedId?: string | null;
  onSelectNode?: (id: string | null) => void;
}) {
  const { items, matrix } = useMemo(() => chordOf(rows, graph), [rows, graph]);
  const [hover, setHover] = useState<number | null>(null);
  const layout = useMemo(() => chord().padAngle(0.05).sortSubgroups((a, b) => b - a)(matrix), [matrix]);

  const groups = useMemo(() => [...new Set(items.map((i) => i.group))], [items]);
  const color = (g: string) => PALETTE[groups.indexOf(g) % PALETTE.length];

  if (items.length < 2 || layout.every((c) => c.source.value === 0)) {
    return <p style={{ color: 'var(--text-muted)', fontSize: 12, padding: 16 }}>no turn used two things together in this window</p>;
  }

  const arcPath = arc<{ startAngle: number; endAngle: number }>().innerRadius(R).outerRadius(R + BAND);
  const ribbonPath = ribbon().radius(R - 2);
  const lit = (i: number) => hover === null || hover === i;

  return (
    <svg viewBox="-420 -420 840 840" style={{ width: '100%', height: '100%' }} role="img" aria-label="what the agent uses together">
      <g>
        {layout.map((c) => {
          const s = c.source.index;
          const t = c.target.index;
          if (s === t) return null;
          const on = hover === null || hover === s || hover === t;
          const it = items[s];
          return (
            <path
              key={`${s}-${t}`}
              d={ribbonPath(c as never) as unknown as string}
              fill={color(it.group)}
              fillOpacity={on ? 0.55 : 0.07}
              stroke={color(it.group)}
              strokeOpacity={on ? 0.8 : 0.1}
              strokeWidth={0.5}
            >
              <title>{`${items[s].label} + ${items[t].label}\n${c.source.value} turn${c.source.value === 1 ? '' : 's'} used both`}</title>
            </path>
          );
        })}
      </g>
      <g>
        {layout.groups.map((g) => {
          const it = items[g.index];
          const mid = (g.startAngle + g.endAngle) / 2;
          const deg = (mid * 180) / Math.PI - 90;
          const flip = mid > Math.PI;
          const share = it.calls > 0 ? it.failed / it.calls : 0;
          const ring = it.failed === 0 ? null : share >= 0.3 ? FAILED : WARN;
          return (
            <g
              key={it.id}
              opacity={lit(g.index) ? 1 : 0.35}
              style={{ cursor: 'pointer' }}
              onMouseEnter={() => setHover(g.index)}
              onMouseLeave={() => setHover(null)}
              onClick={() => onSelectNode?.(selectedId === it.id ? null : it.id)}
            >
              <path d={arcPath(g) as string} fill={color(it.group)} stroke={ring ?? (selectedId === it.id ? '#f9fafb' : '#1f2937')} strokeWidth={ring || selectedId === it.id ? 2 : 1} />
              <text
                transform={`rotate(${deg}) translate(${R + BAND + 8}) ${flip ? 'rotate(180)' : ''}`}
                textAnchor={flip ? 'end' : 'start'}
                dominantBaseline="middle"
                fontSize={12}
                fill={selectedId === it.id ? '#f9fafb' : '#c4cad4'}
              >
                {shorten(it.label)}
              </text>
              <title>{`${it.label}${it.group ? ` · tool of ${it.group}` : ' · model'}\n${it.calls.toLocaleString()} call${it.calls === 1 ? '' : 's'}${it.failed ? `, ${it.failed} failed` : ''}`}</title>
            </g>
          );
        })}
      </g>
    </svg>
  );
}
