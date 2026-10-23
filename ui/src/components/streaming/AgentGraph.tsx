import { useEffect, useRef, useState } from 'react';
import { drag } from 'd3-drag';
import { forceCenter, forceCollide, forceLink, forceManyBody, forceSimulation, type Simulation } from 'd3-force';
import { select, type Selection } from 'd3-selection';
import { zoom, zoomIdentity } from 'd3-zoom';
import 'd3-transition';
import type { GLink, GNode, Graph, PathStep } from '../../lib/agentGraph';
import { fmtDuration, shortCount } from '../../lib/turns';
import type { TurnRow } from '../../lib/types';

// The force-directed graph of d3's own example (observablehq.com/@d3/force-directed-graph-component), drawn the way the
// service graph of the triage UI is: hairline links, grey nodes that turn orange and red with failures, a label only
// on the nodes that matter, a dark ring. What is added is what an agent needs: zoom, the click of a node or a turn,
// and the path of a turn lit over the links.

const NEUTRAL = '#6b7280';
const WARN = '#f59e0b';
const FAILED = '#ef4444';
const AGENT = '#a79ff0';
const RING = '#f97316';
const LINK = '#374151';
const NODE_STROKE = '#1f2937';

const failShare = (n: { calls: number; failed: number }) => (n.calls > 0 ? n.failed / n.calls : 0);
const health = (n: GNode) => (n.failed === 0 ? (n.kind === 'agent' ? AGENT : NEUTRAL) : failShare(n) >= 0.3 ? FAILED : WARN);
const radius = (n: GNode) => {
  switch (n.kind) {
    case 'agent': return 17;
    case 'server': case 'model': return 10;
    case 'tool': return Math.min(16, 6 + 1.5 * Math.sqrt(Math.max(1, n.calls)));
    default: return n.failed ? 6 : 5;
  }
};
const endId = (v: string | GNode) => (typeof v === 'string' ? v : v.id);
const linkWidth = (l: GLink) => (endId(l.source).startsWith('turn:') ? 1 : Math.min(2.4, 1 + 0.6 * Math.log10(1 + l.calls)));

const MAX_LABEL = 26;
const shorten = (label: string) => (label.length <= MAX_LABEL ? label : `${label.slice(0, 15)}…${label.slice(-10)}`);

function titleOf(n: GNode): string {
  if (n.kind === 'turn' && n.row) {
    const r = n.row;
    return `${n.label} · ${fmtDuration(r.durationMs)} · ${r.requests} model request${r.requests === 1 ? '' : 's'}, ${r.tools} tool call${r.tools === 1 ? '' : 's'} · ↓${shortCount(r.inputTokens)} ↑${shortCount(r.outputTokens)}${n.failed ? ' · a failure' : ''}`;
  }
  if (n.kind === 'agent') return `${n.label}\n${n.calls.toLocaleString()} turns${n.failed ? `, ${n.failed} with a failure` : ''}`;
  const calls = `${n.calls.toLocaleString()} call${n.calls === 1 ? '' : 's'}${n.failed ? `, ${n.failed} failed (${Math.round(failShare(n) * 100)}%)` : ''}`;
  const time = n.ms > 0 ? `\n${fmtDuration(n.ms / Math.max(1, n.calls))} on average, ${fmtDuration(n.ms)} in all` : '';
  return `${n.label} · ${n.kind === 'tool' && n.server ? `tool of ${n.server}` : n.kind}\n${calls}${time}`;
}

export function AgentGraph({
  graph, path, turnNode, selectedId, onSelectNode, onPickTurn,
}: {
  graph: Graph;
  path: PathStep[] | null;
  // The node of the turn on show, ringed.
  turnNode?: string | null;
  selectedId?: string | null;
  onSelectNode?: (id: string | null) => void;
  onPickTurn?: (row: TurnRow) => void;
}) {
  const boxRef = useRef<HTMLDivElement>(null);
  const svgRef = useRef<SVGSVGElement>(null);
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

  // Handlers are read through refs, so a new one does not redraw the graph.
  const handlers = useRef({ onSelectNode, onPickTurn });
  useEffect(() => {
    handlers.current = { onSelectNode, onPickTurn };
  }, [onSelectNode, onPickTurn]);

  const dataRef = useRef<GNode[]>([]);
  const nodesSel = useRef<Selection<SVGGElement, GNode, SVGGElement, unknown> | null>(null);
  const linksSel = useRef<Selection<SVGLineElement, GLink, SVGGElement, unknown> | null>(null);
  const badgeLayer = useRef<Selection<SVGGElement, unknown, null, undefined> | null>(null);

  const { w: W, h: H } = size;
  useEffect(() => {
    if (W === 0 || H === 0) return;
    const svg = select(svgRef.current!);
    svg.selectAll('*').remove();
    // The simulation writes positions into its nodes and replaces the ends of its links with them: it gets copies.
    const nodes: GNode[] = graph.nodes.map((n) => ({ ...n }));
    const links: GLink[] = graph.links.map((l) => ({ ...l }));
    dataRef.current = nodes;

    svg.append('defs').append('style').text(`
      .ego-path { stroke-dasharray: 5 3; }
    `);
    const layer = svg.append('g');
    const link = layer.append('g').attr('stroke-linecap', 'round')
      .selectAll<SVGLineElement, GLink>('line').data(links, (d) => d.id).join('line')
      .attr('stroke', LINK).attr('stroke-opacity', 0.5).attr('stroke-width', linkWidth);
    linksSel.current = link;

    const node = layer.append('g').selectAll<SVGGElement, GNode>('g').data(nodes, (d) => d.id).join('g').attr('cursor', 'pointer');
    node.append('circle').attr('r', radius).attr('fill', health).attr('stroke', NODE_STROKE).attr('stroke-width', 1);
    node.append('title').text(titleOf);
    node.filter((d) => d.kind !== 'turn').append('text')
      .text((d) => shorten(d.label))
      .attr('dy', (d) => -radius(d) - 4)
      .attr('text-anchor', 'middle')
      .attr('font-family', 'var(--font-body)')
      .attr('font-size', 10)
      .attr('fill', '#d1d5db')
      .attr('pointer-events', 'none');
    nodesSel.current = node;
    badgeLayer.current = layer.append('g').attr('pointer-events', 'none');

    const placeBadges = () => {
      badgeLayer.current?.selectAll<SVGGElement, unknown>('g.ego-badge').attr('transform', function () {
        const n = nodes.find((x) => x.id === (this as SVGGElement).dataset.node);
        const r = n ? radius(n) : 0;
        return `translate(${(n?.x ?? 0) + r * 0.8},${(n?.y ?? 0) - r * 0.8})`;
      });
    };
    const ticked = () => {
      link
        .attr('x1', (d) => (d.source as GNode).x ?? 0).attr('y1', (d) => (d.source as GNode).y ?? 0)
        .attr('x2', (d) => (d.target as GNode).x ?? 0).attr('y2', (d) => (d.target as GNode).y ?? 0);
      node.attr('transform', (d) => `translate(${d.x ?? 0},${d.y ?? 0})`);
      placeBadges();
    };

    const sim: Simulation<GNode, GLink> = forceSimulation<GNode>(nodes)
      .force('link', forceLink<GNode, GLink>(links).id((d) => d.id).distance(60))
      // The service graph's charge of -120; a turn is one of hundreds, so each pushes less.
      .force('charge', forceManyBody<GNode>().strength((d) => (d.kind === 'turn' ? -25 : -120)))
      .force('collide', forceCollide<GNode>((d) => radius(d) + 3))
      .force('center', forceCenter())
      .on('tick', ticked);

    // The official example has no zoom; this one is zoomable, and shows the whole graph once it has settled.
    const z = zoom<SVGSVGElement, unknown>().scaleExtent([0.1, 4]).on('zoom', (event) => layer.attr('transform', event.transform.toString()));
    const fit = () => {
      let x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
      for (const n of nodes) {
        const r = radius(n);
        const half = n.kind === 'turn' ? r : Math.max(r, shorten(n.label).length * 3.4);
        x0 = Math.min(x0, (n.x ?? 0) - half); x1 = Math.max(x1, (n.x ?? 0) + half);
        y0 = Math.min(y0, (n.y ?? 0) - r - 16); y1 = Math.max(y1, (n.y ?? 0) + r);
      }
      const pad = 30;
      const k = Math.min(2, (W - 2 * pad) / Math.max(1, x1 - x0), (H - 2 * pad) / Math.max(1, y1 - y0));
      return zoomIdentity.translate(-((x0 + x1) / 2) * k, -((y0 + y1) / 2) * k).scale(k);
    };
    let fitted = false;
    sim.on('end', () => {
      if (fitted) return;
      fitted = true;
      svg.transition().duration(400).call(z.transform, fit());
    });
    svg.call(z).on('dblclick.zoom', null);
    svg.on('dblclick', () => svg.transition().duration(250).call(z.transform, fit()));
    svg.on('click', () => handlers.current.onSelectNode?.(null));

    // The drag of the example: the node follows the pointer and lets go when it is dropped.
    node.call(
      drag<SVGGElement, GNode>()
        .on('start', (event) => {
          if (!event.active) sim.alphaTarget(0.3).restart();
          event.subject.fx = event.subject.x;
          event.subject.fy = event.subject.y;
        })
        .on('drag', (event) => {
          event.subject.fx = event.x;
          event.subject.fy = event.y;
        })
        .on('end', (event) => {
          if (!event.active) sim.alphaTarget(0);
          event.subject.fx = null;
          event.subject.fy = null;
        }),
    );
    node.on('click', (event: MouseEvent, d) => {
      event.stopPropagation();
      if (d.kind === 'turn' && d.row) handlers.current.onPickTurn?.(d.row);
      else handlers.current.onSelectNode?.(d.id);
    });

    return () => {
      sim.stop();
    };
  }, [graph, W, H]);

  // Light the path of the turn on show, and ring its node.
  useEffect(() => {
    const links = linksSel.current;
    const nodes = nodesSel.current;
    const badges = badgeLayer.current;
    if (!links || !nodes || !badges) return;
    badges.selectAll('*').remove();
    nodes.select('circle')
      .attr('stroke', (d) => (d.id === turnNode ? RING : d.id === selectedId ? '#ffffff' : NODE_STROKE))
      .attr('stroke-width', (d) => (d.id === turnNode || d.id === selectedId ? 2.5 : 1));
    if (!path || path.length === 0) {
      links.classed('ego-path', false).attr('stroke', LINK).attr('stroke-opacity', 0.5);
      nodes.attr('opacity', 1);
      return;
    }
    const onLinks = new Set(path.flatMap((s) => s.linkIds));
    const onNodes = new Set(['agent', ...(turnNode ? [turnNode] : []), ...path.map((s) => s.nodeId), ...path.flatMap((s) => s.linkIds.map((id) => id.split('>')[1]))]);
    const failedLinks = new Set(path.filter((s) => s.failed).flatMap((s) => s.linkIds));
    links
      .classed('ego-path', (d) => onLinks.has(d.id))
      .attr('stroke', (d) => (failedLinks.has(d.id) ? FAILED : onLinks.has(d.id) ? '#f5f3ff' : LINK))
      .attr('stroke-opacity', (d) => (onLinks.has(d.id) ? 1 : 0.2));
    // The other turns fade most; the models, servers and tools off the path only a little.
    nodes.attr('opacity', (d) => (onNodes.has(d.id) ? 1 : d.kind === 'turn' ? 0.25 : 0.4));

    // One badge per node, numbering the steps that reached it.
    const byNode = new Map<string, PathStep[]>();
    for (const s of path) byNode.set(s.nodeId, [...(byNode.get(s.nodeId) ?? []), s]);
    for (const [id, steps] of byNode) {
      const g = badges.append('g').attr('class', 'ego-badge').attr('data-node', id);
      const text = steps.map((s) => s.n).join(',');
      const w = Math.max(16, 7 * text.length + 8);
      const bad = steps.some((s) => s.failed);
      g.append('rect').attr('x', -w / 2).attr('y', -8).attr('width', w).attr('height', 16).attr('rx', 8).attr('fill', bad ? FAILED : '#f5f3ff');
      g.append('text').text(text).attr('text-anchor', 'middle').attr('dy', 3.5).attr('font-size', 10).attr('font-weight', 700).attr('fill', bad ? '#fff' : '#14141f');
      const n = dataRef.current.find((x) => x.id === id);
      const r = n ? radius(n) : 0;
      g.attr('transform', `translate(${(n?.x ?? 0) + r * 0.8},${(n?.y ?? 0) - r * 0.8})`);
    }
  }, [path, graph, W, H, turnNode, selectedId]);

  return (
    <div ref={boxRef} style={{ width: '100%', height: '100%' }}>
      <svg
        ref={svgRef}
        width={W}
        height={H}
        viewBox={`${-W / 2} ${-H / 2} ${W} ${H}`}
        style={{ display: 'block' }}
        role="img"
        aria-label="The agent, its models, MCP servers and tools, and the turns that used them"
      />
    </div>
  );
}
