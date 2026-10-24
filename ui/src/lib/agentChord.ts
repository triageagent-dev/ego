import { BUILTIN, type Graph } from './agentGraph';
import type { TurnRow } from './types';

// The call flow of an agent, for the chord view: the models and tools of the graph on a circle, and between two of
// them a link as strong as the number of times one followed the other in a turn. The graph shows what an agent uses;
// this shows what it does next, so a loop, a tool that every request ends in, and the step after a failure show.

export interface FlowNode {
  id: string;
  label: string;
  // The MCP server of a tool, BUILTIN for a built-in one, '' for a model.
  group: string;
  calls: number;
  failed: number;
  // How often it followed itself.
  repeats: number;
}

export interface FlowEdge {
  from: string;
  to: string;
  n: number;
}

export interface FlowData {
  nodes: FlowNode[];
  edges: FlowEdge[];
}

export const MAX_FLOW_NODES = 40;

// A tool the graph folded into its server's "more" node is that node here too.
function inGraph(id: string, known: Set<string>): string | null {
  if (known.has(id)) return id;
  if (id.startsWith('t:')) {
    const slash = id.indexOf('/');
    const more = slash > 0 ? `${id.slice(0, slash)}/…` : '';
    if (more && known.has(more)) return more;
  }
  return null;
}

export function flowOf(rows: TurnRow[], graph: Graph): FlowData {
  const known = new Set(graph.nodes.map((n) => n.id));
  const byId = new Map(graph.nodes.map((n) => [n.id, n]));
  const edges = new Map<string, FlowEdge>();
  const repeats = new Map<string, number>();
  for (const r of rows) {
    for (const e of r.flow ?? []) {
      const from = inGraph(e.from, known);
      const to = inGraph(e.to, known);
      if (!from || !to) continue;
      if (from === to) {
        repeats.set(from, (repeats.get(from) ?? 0) + e.n);
        continue;
      }
      const key = `${from}>${to}`;
      const cur = edges.get(key) ?? { from, to, n: 0 };
      cur.n += e.n;
      edges.set(key, cur);
    }
  }
  // The busiest steps that take part in the flow.
  const inFlow = new Set<string>();
  for (const e of edges.values()) {
    inFlow.add(e.from);
    inFlow.add(e.to);
  }
  for (const id of repeats.keys()) inFlow.add(id);
  const nodes: FlowNode[] = [...inFlow]
    .map((id) => byId.get(id)!)
    .filter((n) => n && (n.kind === 'model' || n.kind === 'tool'))
    .sort((a, b) => b.calls - a.calls)
    .slice(0, MAX_FLOW_NODES)
    .map((n) => ({
      id: n.id,
      label: n.label,
      group: n.kind === 'model' ? '' : n.server || BUILTIN,
      calls: n.calls,
      failed: n.failed,
      repeats: repeats.get(n.id) ?? 0,
    }));
  const kept = new Set(nodes.map((n) => n.id));
  return { nodes, edges: [...edges.values()].filter((e) => kept.has(e.from) && kept.has(e.to)) };
}
