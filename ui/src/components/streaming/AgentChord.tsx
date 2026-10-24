import { useMemo, useState } from 'react';
import { cluster, hierarchy, type HierarchyPointNode } from 'd3-hierarchy';
import { curveBundle, lineRadial } from 'd3-shape';
import { flowOf, type FlowNode } from '../../lib/agentChord';
import type { Graph } from '../../lib/agentGraph';
import type { TurnRow } from '../../lib/types';

// The call flow of an agent on a circle, drawn like the service graph: a dot for each model and tool, as big as its
// calls and warmer the more of them failed, grouped by MCP server, and bundled links for what followed what. Hover a
// dot to see what led to it (blue) and what came after it (violet); a click selects it.

const NEUTRAL = '#6b7280';
const WARN = '#f59e0b';
const FAILED = '#ef4444';
const LINK = '#374151';
const IN = '#5eb1ef';
const OUT = '#a79ff0';
const R = 300;
const MAX_LABEL = 28;
const shorten = (s: string) => (s.length <= MAX_LABEL ? s : `${s.slice(0, 16)}…${s.slice(-9)}`);

interface Item {
  id: string;
  node?: FlowNode;
  children?: Item[];
}

const tint = (n: FlowNode) => (n.failed === 0 ? NEUTRAL : n.failed / Math.max(1, n.calls) >= 0.3 ? FAILED : WARN);

export function AgentChord({
  rows, graph, selectedId, onSelectNode,
}: {
  rows: TurnRow[];
  graph: Graph;
  selectedId?: string | null;
  onSelectNode?: (id: string | null) => void;
}) {
  const flow = useMemo(() => flowOf(rows, graph), [rows, graph]);
  const [hover, setHover] = useState<string | null>(null);

  const layout = useMemo(() => {
    const groups = new Map<string, Item[]>();
    for (const n of flow.nodes) groups.set(n.group, [...(groups.get(n.group) ?? []), { id: n.id, node: n }]);
    // The models first, then the servers with the most calls.
    const total = (g: string) => (groups.get(g) ?? []).reduce((s, i) => s + (i.node?.calls ?? 0), 0);
    const order = [...groups.keys()].sort((a, b) => (a === '' ? -1 : b === '' ? 1 : total(b) - total(a)));
    const root: Item = { id: 'root', children: order.map((g) => ({ id: `g:${g}`, children: groups.get(g)!.sort((a, b) => (b.node?.calls ?? 0) - (a.node?.calls ?? 0)) })) };
    const tree = cluster<Item>().size([2 * Math.PI, R])(hierarchy(root));
    const leaves = new Map<string, HierarchyPointNode<Item>>();
    for (const l of tree.leaves()) leaves.set(l.data.id, l);
    return { leaves };
  }, [flow]);

  if (flow.edges.length === 0) {
    return <p style={{ color: 'var(--text-muted)', fontSize: 12, padding: 16 }}>no turn in this window made two steps in a row that the graph holds</p>;
  }

  const line = lineRadial<HierarchyPointNode<Item>>().curve(curveBundle.beta(0.85)).angle((d) => d.x).radius((d) => d.y);
  const maxN = Math.max(...flow.edges.map((e) => e.n));
  const into = new Set(flow.edges.filter((e) => e.to === hover).map((e) => e.from));
  const outOf = new Set(flow.edges.filter((e) => e.from === hover).map((e) => e.to));
  const maxCalls = Math.max(...flow.nodes.map((n) => n.calls));

  return (
    <svg viewBox="-470 -440 940 880" style={{ width: '100%', height: '100%' }} role="img" aria-label="what the agent does next">
      <g fill="none" strokeLinecap="round">
        {flow.edges.map((e) => {
          const a = layout.leaves.get(e.from);
          const b = layout.leaves.get(e.to);
          if (!a || !b) return null;
          const path = line(a.path(b));
          const colour = hover === null ? LINK : e.to === hover ? IN : e.from === hover ? OUT : LINK;
          const on = hover === null || e.to === hover || e.from === hover;
          return (
            <path key={`${e.from}>${e.to}`} d={path ?? ''} stroke={colour} strokeWidth={0.8 + 2.2 * (e.n / maxN)} strokeOpacity={on ? (hover === null ? 0.5 : 0.9) : 0.06}>
              <title>{`${e.from.slice(2)} → ${e.to.slice(2)}\n${e.n.toLocaleString()} time${e.n === 1 ? '' : 's'}`}</title>
            </path>
          );
        })}
      </g>
      <g>
        {flow.nodes.map((n) => {
          const p = layout.leaves.get(n.id);
          if (!p) return null;
          const deg = (p.x * 180) / Math.PI - 90;
          const flip = p.x > Math.PI;
          const sel = selectedId === n.id;
          const related = hover === null || hover === n.id || into.has(n.id) || outOf.has(n.id);
          const r = 4 + 9 * Math.sqrt(n.calls / maxCalls);
          return (
            <g
              key={n.id}
              transform={`rotate(${deg}) translate(${p.y})`}
              opacity={related ? 1 : 0.3}
              style={{ cursor: 'pointer' }}
              onMouseEnter={() => setHover(n.id)}
              onMouseLeave={() => setHover(null)}
              onClick={() => onSelectNode?.(sel ? null : n.id)}
            >
              <circle r={r} fill={tint(n)} stroke={sel ? '#f9fafb' : '#1f2937'} strokeWidth={sel ? 2 : 1} />
              <text
                x={flip ? -(r + 6) : r + 6}
                transform={flip ? 'rotate(180)' : undefined}
                textAnchor={flip ? 'end' : 'start'}
                dominantBaseline="middle"
                fontSize={12}
                fill={hover === n.id || sel ? '#f9fafb' : into.has(n.id) ? IN : outOf.has(n.id) ? OUT : '#9aa3b2'}
              >
                {shorten(n.label)}
              </text>
              <title>{`${n.label}${n.group ? ` · tool of ${n.group}` : ' · model'}\n${n.calls.toLocaleString()} call${n.calls === 1 ? '' : 's'}${n.failed ? `, ${n.failed} failed` : ''}${n.repeats ? `\nfollowed itself ${n.repeats.toLocaleString()} time${n.repeats === 1 ? '' : 's'}` : ''}`}</title>
            </g>
          );
        })}
      </g>
    </svg>
  );
}
