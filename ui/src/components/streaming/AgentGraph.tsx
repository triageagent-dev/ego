import { useEffect, useRef, useState } from 'react';
import { drag } from 'd3-drag';
import { forceCenter, forceCollide, forceLink, forceManyBody, forceSimulation, forceX, forceY, type Simulation } from 'd3-force';
import { select, type Selection } from 'd3-selection';
import { zoom, zoomIdentity } from 'd3-zoom';
import 'd3-transition';
import type { GLink, GNode, Graph, NodeKind, PathStep } from '../../lib/agentGraph';
import { fmtDuration } from '../../lib/turns';

const COLOR: Record<NodeKind, string> = { agent: '#a79ff0', model: '#10b981', server: '#5eead4', tool: '#b5abfc' };
const FAILED = '#ef4444';
const EDGE = 'rgba(167, 159, 240, 0.38)';

// A long name keeps its start and its end: ids differ at the end as often as at the start. The tooltip has it whole.
const MAX_LABEL = 26;
const shorten = (label: string) => (label.length <= MAX_LABEL ? label : `${label.slice(0, 15)}…${label.slice(-10)}`);

const radius = (n: GNode) => (n.kind === 'agent' ? 30 : Math.min(36, 9 + 4 * Math.sqrt(Math.max(1, n.calls))));
const strokeW = (l: GLink) => 1 + 1.8 * Math.log10(1 + l.calls);
const failShare = (e: { calls: number; failed: number }) => (e.calls > 0 ? e.failed / e.calls : 0);


// The agent and what it works with, drawn as a force graph you can drag and zoom: the agent in the middle, the models
// it calls and the servers whose tools it uses around it, a server's tools beyond it. A node is as big as it was called
// often and has a red ring as thick as its share of failures; a link is as thick as the calls it carried and turns red
// with their failures. Pick a turn and its path lights up, step by step, in the order it ran.
export function AgentGraph({ graph, path, selectedId, onSelectNode }: { graph: Graph; path: PathStep[] | null; selectedId?: string | null; onSelectNode?: (id: string | null) => void }) {
  const boxRef = useRef<HTMLDivElement>(null);
  const svgRef = useRef<SVGSVGElement>(null);
  // The graph fills the box it is in, so the box is measured and the drawing laid out in its own pixels.
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
  // The click handler is read through a ref, so a new one does not redraw the graph.
  const onSelectRef = useRef(onSelectNode);
  useEffect(() => {
    onSelectRef.current = onSelectNode;
  }, [onSelectNode]);
  const simRef = useRef<Simulation<GNode, GLink> | null>(null);
  const nodesSel = useRef<Selection<SVGGElement, GNode, SVGGElement, unknown> | null>(null);
  const linksSel = useRef<Selection<SVGLineElement, GLink, SVGGElement, unknown> | null>(null);
  const badgeLayer = useRef<Selection<SVGGElement, unknown, null, undefined> | null>(null);
  const [tip, setTip] = useState<{ x: number; y: number; node: GNode } | null>(null);

  // Draw the graph, and run its layout.
  const { w: W, h: H } = size;
  useEffect(() => {
    if (W === 0 || H === 0) return;
    const svg = select(svgRef.current!);
    svg.selectAll('*').remove();
    // Copy: the layout writes positions into the nodes and replaces link ends with them.
    const nodes: GNode[] = graph.nodes.map((n) => ({ ...n }));
    const links: GLink[] = graph.links.map((l) => ({ ...l }));
    const agent = nodes.find((n) => n.kind === 'agent');
    if (agent) {
      agent.fx = W / 2;
      agent.fy = H / 2;
    }

    svg.append('defs').append('style').text(`
      @keyframes ego-flow { to { stroke-dashoffset: -16; } }
      .ego-path { stroke-dasharray: 5 3; animation: ego-flow 0.7s linear infinite; }
    `);
    const layer = svg.append('g');
    const linkLayer = layer.append('g');
    const nodeLayer = layer.append('g');
    badgeLayer.current = layer.append('g').attr('pointer-events', 'none');

    const link = linkLayer.selectAll<SVGLineElement, GLink>('line').data(links, (d) => d.id).join('line')
      .attr('stroke', (d) => (d.failed > 0 ? `rgba(239, 68, 68, ${0.35 + 0.65 * failShare(d)})` : EDGE))
      .attr('stroke-width', strokeW)
      .attr('stroke-linecap', 'round');
    linksSel.current = link;

    const node = nodeLayer.selectAll<SVGGElement, GNode>('g').data(nodes, (d) => d.id).join('g').attr('cursor', 'grab');
    node.append('circle')
      .attr('r', radius)
      .attr('fill', (d) => COLOR[d.kind])
      .attr('fill-opacity', (d) => (d.kind === 'agent' ? 0.95 : 0.8))
      .attr('stroke', (d) => (d.failed > 0 ? FAILED : 'rgba(255,255,255,0.18)'))
      .attr('stroke-width', (d) => (d.failed > 0 ? 1.5 + 4 * failShare(d) : 1));
    node.append('text')
      .text((d) => shorten(d.label))
      .attr('dy', (d) => radius(d) + 15)
      .attr('text-anchor', 'middle')
      .attr('font-family', 'var(--font-body)')
      .attr('font-size', (d) => (d.kind === 'agent' ? 13 : d.kind === 'tool' ? 11.5 : 12.5))
      .attr('font-weight', (d) => (d.kind === 'server' || d.kind === 'agent' ? 600 : 500))
      .attr('fill', (d) => (d.kind === 'agent' ? 'var(--text-primary)' : d.kind === 'tool' ? 'var(--text-tertiary)' : 'var(--text-secondary)'))
      // A halo in the colour of the box keeps a label readable where a link runs under it.
      .attr('paint-order', 'stroke')
      .attr('stroke', 'var(--card-surface)')
      .attr('stroke-width', 3)
      .attr('stroke-linejoin', 'round')
      .attr('pointer-events', 'none');
    nodesSel.current = node;

    // The links and nodes at the places the layout gave them, and the badges numbering a path.
    const place = () => {
      link
        .attr('x1', (d) => (d.source as GNode).x ?? 0).attr('y1', (d) => (d.source as GNode).y ?? 0)
        .attr('x2', (d) => (d.target as GNode).x ?? 0).attr('y2', (d) => (d.target as GNode).y ?? 0);
      node.attr('transform', (d) => `translate(${d.x ?? 0},${d.y ?? 0})`);
      badgeLayer.current?.selectAll<SVGGElement, unknown>('g.ego-badge').attr('transform', function () {
        const id = (this as SVGGElement).dataset.node;
        const n = nodes.find((x) => x.id === id);
        return `translate(${(n?.x ?? 0) + (n ? radius(n) : 0) * 0.75},${(n?.y ?? 0) - (n ? radius(n) : 0) * 0.75})`;
      });
    };

    const sim = forceSimulation<GNode>(nodes)
      .force('link', forceLink<GNode, GLink>(links).id((d) => d.id).distance((l) => {
        const t = l.target as GNode;
        return t.kind === 'tool' ? 95 : t.kind === 'server' ? 200 : 185;
      }).strength(0.7))
      .force('charge', forceManyBody<GNode>().strength((d) => (d.kind === 'tool' ? -160 : -520)))
      .force('collide', forceCollide<GNode>((d) => radius(d) + (d.kind === 'tool' ? 22 : 28)))
      .force('center', forceCenter(W / 2, H / 2))
      .force('x', forceX(W / 2).strength(0.04))
      .force('y', forceY(H / 2).strength(0.07))
      .on('tick', place);
    simRef.current = sim;

    const z = zoom<SVGSVGElement, unknown>()
      .scaleExtent([0.3, 4])
      .on('zoom', (event) => layer.attr('transform', event.transform.toString()));
    // The view that holds the whole graph: a small graph is blown up, a large one shrunk.
    const fit = () => {
      let x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
      for (const n of nodes) {
        const r = radius(n);
        // A label sits under its node and is about as wide as its text.
        const half = Math.max(r, shorten(n.label).length * 3.6);
        x0 = Math.min(x0, (n.x ?? 0) - half); x1 = Math.max(x1, (n.x ?? 0) + half);
        y0 = Math.min(y0, (n.y ?? 0) - r); y1 = Math.max(y1, (n.y ?? 0) + r + 22);
      }
      const pad = 36;
      const k = Math.min(1.8, (W - 2 * pad) / Math.max(1, x1 - x0), (H - 2 * pad) / Math.max(1, y1 - y0));
      return zoomIdentity.translate(W / 2 - ((x0 + x1) / 2) * k, H / 2 - ((y0 + y1) / 2) * k).scale(k);
    };
    // Lay it out before it is shown, so it does not drift into place.
    sim.stop();
    sim.tick(300);
    place();
    svg.call(z).on('dblclick.zoom', null);
    svg.call(z.transform, fit());
    svg.on('dblclick', () => svg.transition().duration(250).call(z.transform, fit()));
    svg.on('click', () => onSelectRef.current?.(null));

    node.call(
      drag<SVGGElement, GNode>()
        .on('start', (event, d) => {
          if (!event.active) sim.alphaTarget(0.25).restart();
          d.fx = d.x;
          d.fy = d.y;
        })
        .on('drag', (event, d) => {
          d.fx = event.x;
          d.fy = event.y;
        })
        .on('end', (event, d) => {
          if (!event.active) sim.alphaTarget(0);
          // The agent stays where the layout is centred; the rest let go.
          if (d.kind !== 'agent') {
            d.fx = null;
            d.fy = null;
          }
        }),
    );

    node.on('click', (event: MouseEvent, d) => {
      event.stopPropagation();
      onSelectRef.current?.(d.id);
    });
    node.on('mousemove', (event: MouseEvent, d) => {
      const box = boxRef.current!;
      const rect = box.getBoundingClientRect();
      // Client pixels relative to the box, divided by the box's own scale: the page may be zoomed.
      const scale = rect.width / Math.max(1, box.clientWidth);
      setTip({ x: (event.clientX - rect.left) / scale, y: (event.clientY - rect.top) / scale, node: d });
    }).on('mouseleave', () => setTip(null));

    return () => {
      sim.stop();
      simRef.current = null;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [graph, W, H]);

  // Light the path of the picked turn.
  useEffect(() => {
    const links = linksSel.current;
    const nodes = nodesSel.current;
    const badges = badgeLayer.current;
    if (!links || !nodes || !badges) return;
    badges.selectAll('*').remove();
    if (!path || path.length === 0) {
      links.classed('ego-path', false).attr('opacity', 1).attr('stroke', (d) => (d.failed > 0 ? `rgba(239, 68, 68, ${0.35 + 0.65 * failShare(d)})` : EDGE));
      nodes.attr('opacity', 1);
      return;
    }
    const onLinks = new Set(path.flatMap((s) => s.linkIds));
    const onNodes = new Set(['agent', ...path.map((s) => s.nodeId), ...path.flatMap((s) => s.linkIds.map((id) => id.split('>')[1]))]);
    const failedLinks = new Set(path.filter((s) => s.failed).flatMap((s) => s.linkIds));
    links
      .classed('ego-path', (d) => onLinks.has(d.id))
      .attr('opacity', (d) => (onLinks.has(d.id) ? 1 : 0.1))
      .attr('stroke', (d) => (failedLinks.has(d.id) ? FAILED : onLinks.has(d.id) ? '#f5f3ff' : d.failed > 0 ? `rgba(239, 68, 68, ${0.35 + 0.65 * failShare(d)})` : EDGE));
    nodes.attr('opacity', (d) => (onNodes.has(d.id) ? 1 : 0.18));

    // One badge per node, numbering the steps that reached it.
    const byNode = new Map<string, PathStep[]>();
    for (const s of path) byNode.set(s.nodeId, [...(byNode.get(s.nodeId) ?? []), s]);
    for (const [id, steps] of byNode) {
      const g = badges.append('g').attr('class', 'ego-badge').attr('data-node', id);
      const text = steps.map((s) => s.n).join(',');
      const w = Math.max(16, 7 * text.length + 8);
      g.append('rect').attr('x', -w / 2).attr('y', -8).attr('width', w).attr('height', 16).attr('rx', 8)
        .attr('fill', steps.some((s) => s.failed) ? FAILED : '#f5f3ff');
      g.append('text').text(text).attr('text-anchor', 'middle').attr('dy', 3.5).attr('font-size', 10).attr('font-weight', 700)
        .attr('fill', steps.some((s) => s.failed) ? '#fff' : '#14141f');
    }
    simRef.current?.alpha(0.001).restart();
  }, [path, graph, W, H]);

  // The node picked for the panel beside the graph has a white ring.
  useEffect(() => {
    nodesSel.current?.select('circle')
      .attr('stroke', (d) => (d.id === selectedId ? '#ffffff' : d.failed > 0 ? FAILED : 'rgba(255,255,255,0.18)'))
      .attr('stroke-width', (d) => (d.id === selectedId ? 3.5 : d.failed > 0 ? 1.5 + 4 * failShare(d) : 1));
  }, [selectedId, graph, W, H]);

  const n = tip?.node;
  return (
    <div ref={boxRef} style={{ position: 'relative', width: '100%', height: '100%' }}>
      <svg ref={svgRef} width={W} height={H} style={{ display: 'block', cursor: 'default' }} role="img" aria-label="The agent and the models, servers and tools it uses" />
      {n && tip && (
        <div style={{
          position: 'absolute', left: tip.x + 14, top: tip.y + 14, pointerEvents: 'none', zIndex: 3, background: 'rgba(0,0,0,0.92)', color: '#fff',
          padding: '7px 10px', borderRadius: 6, fontSize: 12, lineHeight: 1.5, whiteSpace: 'nowrap',
        }}>
          <div style={{ fontWeight: 600 }}>{n.label}<span style={{ fontWeight: 400, opacity: 0.65 }}> · {n.kind === 'tool' && n.server ? `tool of ${n.server}` : n.kind}</span></div>
          {n.kind === 'agent' ? (
            <div>{n.calls.toLocaleString()} turns{n.failed ? ` · ${n.failed} with a failure` : ''}</div>
          ) : (
            <>
              <div>{n.calls.toLocaleString()} call{n.calls === 1 ? '' : 's'}{n.failed ? <span style={{ color: '#fca5a5' }}> · {n.failed} failed ({Math.round(failShare(n) * 100)}%)</span> : ''}</div>
              {n.ms > 0 && <div>{fmtDuration(n.ms / Math.max(1, n.calls))} on average · {fmtDuration(n.ms)} in all</div>}
            </>
          )}
        </div>
      )}
    </div>
  );
}
