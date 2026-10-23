import { useEffect, useRef, useState } from 'react';
import { drag } from 'd3-drag';
import { forceCenter, forceCollide, forceLink, forceManyBody, forceSimulation, forceX, forceY, type Simulation } from 'd3-force';
import { select, type Selection } from 'd3-selection';
import { zoom, zoomIdentity } from 'd3-zoom';
import 'd3-transition';
import type { GLink, GNode, Graph, NodeKind, PathStep } from '../../lib/agentGraph';
import { fmtDuration } from '../../lib/turns';

const W = 1000;
const H = 460;

const COLOR: Record<NodeKind, string> = { agent: '#a79ff0', model: '#10b981', server: '#5eead4', tool: '#b5abfc' };
const FAILED = '#ef4444';
const EDGE = 'rgba(167, 159, 240, 0.38)';

const radius = (n: GNode) => (n.kind === 'agent' ? 24 : Math.min(26, 5 + 3 * Math.sqrt(Math.max(1, n.calls))));
const strokeW = (l: GLink) => 1 + 1.8 * Math.log10(1 + l.calls);
const failShare = (e: { calls: number; failed: number }) => (e.calls > 0 ? e.failed / e.calls : 0);


// The agent and what it works with, drawn as a force graph you can drag and zoom: the agent in the middle, the models
// it calls and the servers whose tools it uses around it, a server's tools beyond it. A node is as big as it was called
// often and has a red ring as thick as its share of failures; a link is as thick as the calls it carried and turns red
// with their failures. Pick a turn and its path lights up, step by step, in the order it ran.
export function AgentGraph({ graph, path }: { graph: Graph; path: PathStep[] | null }) {
  const svgRef = useRef<SVGSVGElement>(null);
  const simRef = useRef<Simulation<GNode, GLink> | null>(null);
  const nodesSel = useRef<Selection<SVGGElement, GNode, SVGGElement, unknown> | null>(null);
  const linksSel = useRef<Selection<SVGLineElement, GLink, SVGGElement, unknown> | null>(null);
  const badgeLayer = useRef<Selection<SVGGElement, unknown, null, undefined> | null>(null);
  const [tip, setTip] = useState<{ x: number; y: number; node: GNode } | null>(null);

  // Draw the graph, and run its layout.
  useEffect(() => {
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
      .text((d) => d.label)
      .attr('dy', (d) => (d.kind === 'agent' ? 0 : radius(d) + 11))
      .attr('text-anchor', 'middle')
      .attr('alignment-baseline', (d) => (d.kind === 'agent' ? 'middle' : 'auto'))
      .attr('font-size', (d) => (d.kind === 'agent' ? 11 : d.kind === 'tool' ? 9.5 : 11))
      .attr('font-weight', (d) => (d.kind === 'server' || d.kind === 'agent' ? 600 : 400))
      .attr('fill', (d) => (d.kind === 'agent' ? '#14141f' : 'var(--text-secondary)'))
      .attr('paint-order', 'stroke')
      .attr('stroke', (d) => (d.kind === 'agent' ? 'none' : 'var(--bg-primary)'))
      .attr('stroke-width', 3)
      .attr('pointer-events', 'none');
    nodesSel.current = node;

    const sim = forceSimulation<GNode>(nodes)
      .force('link', forceLink<GNode, GLink>(links).id((d) => d.id).distance((l) => {
        const t = l.target as GNode;
        return t.kind === 'tool' ? 62 : t.kind === 'server' ? 150 : 140;
      }).strength(0.7))
      .force('charge', forceManyBody<GNode>().strength((d) => (d.kind === 'tool' ? -90 : -330)))
      .force('collide', forceCollide<GNode>((d) => radius(d) + (d.kind === 'tool' ? 14 : 20)))
      .force('center', forceCenter(W / 2, H / 2))
      .force('x', forceX(W / 2).strength(0.04))
      .force('y', forceY(H / 2).strength(0.07))
      .on('tick', () => {
        link
          .attr('x1', (d) => (d.source as GNode).x ?? 0).attr('y1', (d) => (d.source as GNode).y ?? 0)
          .attr('x2', (d) => (d.target as GNode).x ?? 0).attr('y2', (d) => (d.target as GNode).y ?? 0);
        node.attr('transform', (d) => `translate(${d.x ?? 0},${d.y ?? 0})`);
        // Badges follow the nodes they number.
        badgeLayer.current?.selectAll<SVGGElement, string>('g.ego-badge').attr('transform', function () {
          const id = (this as SVGGElement).dataset.node;
          const n = nodes.find((x) => x.id === id);
          return `translate(${(n?.x ?? 0) + (n ? radius(n) : 0) * 0.75},${(n?.y ?? 0) - (n ? radius(n) : 0) * 0.75})`;
        });
      });
    simRef.current = sim;

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

    node.on('mousemove', (event: MouseEvent, d) => {
      const box = svgRef.current!.parentElement!.getBoundingClientRect();
      // Client pixels relative to the box, divided by the box's own scale: the page may be zoomed.
      const scale = box.width / Math.max(1, (svgRef.current!.parentElement as HTMLElement).clientWidth);
      setTip({ x: (event.clientX - box.left) / scale, y: (event.clientY - box.top) / scale, node: d });
    }).on('mouseleave', () => setTip(null));

    const z = zoom<SVGSVGElement, unknown>()
      .scaleExtent([0.4, 3.5])
      .on('zoom', (event) => layer.attr('transform', event.transform.toString()));
    svg.call(z).on('dblclick.zoom', null).on('dblclick', () => svg.transition().duration(250).call(z.transform, zoomIdentity));

    return () => {
      sim.stop();
      simRef.current = null;
    };
  }, [graph]);

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
  }, [path, graph]);

  const n = tip?.node;
  return (
    <div style={{ position: 'relative', width: '100%' }}>
      <svg ref={svgRef} viewBox={`0 0 ${W} ${H}`} style={{ display: 'block', width: '100%', maxHeight: '64vh', cursor: 'default' }} role="img" aria-label="The agent and the models, servers and tools it uses" />
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
