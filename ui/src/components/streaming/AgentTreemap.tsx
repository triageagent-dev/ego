import { useEffect, useMemo, useRef, useState } from 'react';
import { hierarchy, treemap, treemapSquarify, type HierarchyRectangularNode } from 'd3-hierarchy';
import { BUILTIN, type Graph } from '../../lib/agentGraph';
import { fmtDuration } from '../../lib/turns';

// The treemap of what an agent uses: the models, and each MCP server with its tools, as tiles as big as the calls
// they took. A tile warms from grey to amber and red with its share of failed calls. A click selects it.

const NEUTRAL = '#6b7280';
const WARN = '#f59e0b';
const FAILED = '#ef4444';
const FILL = '#2a3140';

interface Tile {
  id: string;
  name: string;
  calls: number;
  failed: number;
  ms: number;
  children?: Tile[];
}

const tint = (t: { calls: number; failed: number }) => {
  const share = t.calls > 0 ? t.failed / t.calls : 0;
  return t.failed === 0 ? NEUTRAL : share >= 0.3 ? FAILED : WARN;
};

function tilesOf(graph: Graph): Tile {
  const toTile = (n: Graph['nodes'][number]): Tile => ({ id: n.id, name: n.label, calls: n.calls, failed: n.failed, ms: n.ms });
  const models = graph.nodes.filter((n) => n.kind === 'model').map(toTile);
  const tools = graph.nodes.filter((n) => n.kind === 'tool');
  const servers = graph.nodes.filter((n) => n.kind === 'server').map((s) => ({
    ...toTile(s),
    children: tools.filter((t) => (t.server ?? BUILTIN) === s.label).map(toTile),
  }));
  const children: Tile[] = [];
  if (models.length > 0) children.push({ id: 'models', name: 'models', calls: 0, failed: 0, ms: 0, children: models });
  children.push(...servers.filter((s) => s.children.length > 0));
  return { id: 'agent', name: 'agent', calls: 0, failed: 0, ms: 0, children };
}

export function AgentTreemap({
  graph, selectedId, onSelectNode,
}: {
  graph: Graph;
  selectedId?: string | null;
  onSelectNode?: (id: string | null) => void;
}) {
  const boxRef = useRef<HTMLDivElement>(null);
  const [size, setSize] = useState({ w: 0, h: 0 });
  useEffect(() => {
    const el = boxRef.current;
    if (!el) return;
    const measure = () => setSize((cur) => (cur.w === el.clientWidth && cur.h === el.clientHeight ? cur : { w: el.clientWidth, h: el.clientHeight }));
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    return () => observer.disconnect();
  }, []);

  const { w, h } = size;
  const leaves = useMemo(() => {
    if (w === 0 || h === 0) return null;
    const root = hierarchy(tilesOf(graph))
      .sum((d) => (d.children ? 0 : Math.max(1, d.calls)))
      .sort((a, b) => (b.value ?? 0) - (a.value ?? 0));
    treemap<Tile>().tile(treemapSquarify).size([w, h]).paddingInner(2).paddingOuter(2).paddingTop((d) => (d.depth === 1 ? 18 : 2))(root);
    return root as HierarchyRectangularNode<Tile>;
  }, [graph, w, h]);

  return (
    <div ref={boxRef} style={{ width: '100%', height: '100%' }}>
      {leaves && (
        <svg width={w} height={h} role="img" aria-label="what the agent uses, by calls">
          {leaves.descendants().filter((d) => d.depth > 0).map((d) => {
            const width = d.x1 - d.x0;
            const height = d.y1 - d.y0;
            const t = d.data;
            const group = !!d.children;
            const sel = selectedId === t.id;
            const clip = `ego-tile-${t.id.replace(/[^a-zA-Z0-9]/g, '_')}`;
            const pick = t.id === 'models' ? undefined : () => onSelectNode?.(sel ? null : t.id);
            return (
              <g key={t.id} transform={`translate(${d.x0},${d.y0})`} style={{ cursor: pick ? 'pointer' : 'default' }} onClick={pick}>
                <clipPath id={clip}><rect width={width} height={height} /></clipPath>
                <rect
                  width={width}
                  height={height}
                  fill={group ? 'none' : FILL}
                  stroke={sel ? '#f9fafb' : group ? '#3b4252' : tint(t)}
                  strokeWidth={sel ? 2 : group ? 1 : t.failed > 0 ? 2 : 1}
                  strokeOpacity={group || sel ? 1 : t.failed > 0 ? 0.9 : 0.35}
                />
                <g clipPath={`url(#${clip})`}>
                  {(group || (width > 46 && height > 22)) && (
                    <text x={6} y={group ? 13 : 15} fontSize={group ? 11 : 12} fill={group ? '#9aa3b2' : '#e5e7eb'} fontWeight={group ? 600 : 400}>
                      {t.name}
                      {!group && width > 90 && height > 36 && <tspan x={6} dy={14} fill="#8b93a1" fontSize={11}>{t.calls.toLocaleString()} call{t.calls === 1 ? '' : 's'}</tspan>}
                    </text>
                  )}
                </g>
                <title>{group ? t.name : `${t.name}\n${t.calls.toLocaleString()} call${t.calls === 1 ? '' : 's'}${t.failed ? `, ${t.failed} failed` : ''}${t.ms > 0 ? `\n${fmtDuration(t.ms / Math.max(1, t.calls))} on average` : ''}`}</title>
              </g>
            );
          })}
        </svg>
      )}
    </div>
  );
}
