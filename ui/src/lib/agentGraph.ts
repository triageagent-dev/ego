import type { TurnDetail } from './turnSteps';
import type { TurnRow } from './types';

// The agent as a graph: the agent in the middle, the models it calls and the MCP servers whose tools it uses around
// it, each server's tools beyond it. Built from the turns of a window, so the weight of a node is how often it was
// called and the colour of its ring and its link how often that failed. The path one turn took through it is found
// from the turn's own steps.

export type NodeKind = 'agent' | 'model' | 'server' | 'tool' | 'turn';

export interface GNode {
  id: string;
  kind: NodeKind;
  label: string;
  calls: number;
  failed: number;
  ms: number;
  // A tool's server, for its tooltip.
  server?: string;
  // A turn node: the turn it stands for.
  row?: TurnRow;
  x?: number;
  y?: number;
  fx?: number | null;
  fy?: number | null;
}

export interface GLink {
  id: string;
  source: string | GNode;
  target: string | GNode;
  calls: number;
  failed: number;
  ms: number;
}

export interface Graph {
  nodes: GNode[];
  links: GLink[];
}

// A server with more tools than this shows its busiest ones and one node for the rest.
const MAX_TOOLS = 10;
// The newest turns that get a node of their own; the numbers of all of them are in the other nodes.
export const MAX_TURN_NODES = 400;
export const BUILTIN = 'built-in';

const modelId = (m: string) => `m:${m}`;
const serverId = (s: string) => `s:${s || BUILTIN}`;
const toolId = (s: string, t: string) => `t:${s || BUILTIN}/${t}`;
const moreId = (s: string) => `t:${s || BUILTIN}/…`;
export const turnKey = (r: TurnRow) => `${r.session}:${r.rootSpanId}`;
export const turnNodeId = (r: TurnRow) => `turn:${turnKey(r)}`;

export function buildGraph(rows: TurnRow[], agent: string): Graph {
  const nodes = new Map<string, GNode>();
  const links = new Map<string, GLink>();
  const node = (id: string, kind: NodeKind, label: string, server?: string): GNode => {
    let n = nodes.get(id);
    if (!n) {
      n = { id, kind, label, calls: 0, failed: 0, ms: 0, server };
      nodes.set(id, n);
    }
    return n;
  };
  const link = (from: string, to: string): GLink => {
    const id = `${from}>${to}`;
    let l = links.get(id);
    if (!l) {
      l = { id, source: from, target: to, calls: 0, failed: 0, ms: 0 };
      links.set(id, l);
    }
    return l;
  };
  const add = (e: { calls: number; failed: number; ms: number }, calls: number, failed: number, ms: number) => {
    e.calls += calls;
    e.failed += failed;
    e.ms += ms;
  };

  const root = node('agent', 'agent', agent);
  root.calls = rows.length;
  for (const r of rows) {
    if (r.failed || r.failedTools) root.failed++;
    root.ms += r.durationMs;
    for (const [name, c] of Object.entries(r.modelCalls ?? {})) {
      const n = node(modelId(name), 'model', name);
      add(n, c.calls, c.failed ?? 0, c.ms ?? 0);
      add(link('agent', n.id), c.calls, c.failed ?? 0, c.ms ?? 0);
    }
    for (const [name, c] of Object.entries(r.toolCalls ?? {})) {
      const server = c.server ?? '';
      const s = node(serverId(server), 'server', server || BUILTIN);
      add(s, c.calls, c.failed ?? 0, c.ms ?? 0);
      add(link('agent', s.id), c.calls, c.failed ?? 0, c.ms ?? 0);
      const t = node(toolId(server, name), 'tool', name, server || BUILTIN);
      add(t, c.calls, c.failed ?? 0, c.ms ?? 0);
      add(link(s.id, t.id), c.calls, c.failed ?? 0, c.ms ?? 0);
    }
  }

  // A node for each of the newest turns, joined to the models and tools it used: the turns gather round what they
  // used, and a turn that failed stands out among them.
  for (const r of rows.slice(-MAX_TURN_NODES)) {
    const id = turnNodeId(r);
    const t = node(id, 'turn', new Date(r.startMs).toLocaleString(undefined, { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit', hour12: false }));
    t.row = r;
    t.calls = 1;
    t.failed = r.failed || r.failedTools ? 1 : 0;
    t.ms = r.durationMs;
    for (const [name, c] of Object.entries(r.modelCalls ?? {})) add(link(id, modelId(name)), c.calls, c.failed ?? 0, c.ms ?? 0);
    for (const [name, c] of Object.entries(r.toolCalls ?? {})) add(link(id, toolId(c.server ?? '', name)), c.calls, c.failed ?? 0, c.ms ?? 0);
  }

  // Fold the tail of a server's tools into one node.
  const bySever = new Map<string, GNode[]>();
  for (const n of nodes.values()) if (n.kind === 'tool') bySever.set(n.server ?? BUILTIN, [...(bySever.get(n.server ?? BUILTIN) ?? []), n]);
  for (const [server, tools] of bySever) {
    if (tools.length <= MAX_TOOLS) continue;
    tools.sort((a, b) => b.calls - a.calls);
    const sid = serverId(server === BUILTIN ? '' : server);
    const rest = node(moreId(server === BUILTIN ? '' : server), 'tool', `+${tools.length - MAX_TOOLS} more`, server);
    for (const t of tools.slice(MAX_TOOLS)) {
      add(rest, t.calls, t.failed, t.ms);
      add(link(sid, rest.id), t.calls, t.failed, t.ms);
      nodes.delete(t.id);
      links.delete(`${sid}>${t.id}`);
      // The turns that used it now reach the node that holds it.
      for (const l of [...links.values()]) {
        if (l.target === t.id && String(l.source).startsWith('turn:')) {
          links.delete(l.id);
          add(link(String(l.source), rest.id), l.calls, l.failed, l.ms);
        }
      }
    }
  }
  return { nodes: [...nodes.values()], links: [...links.values()] };
}

export interface PathStep {
  // 1-based position in the turn.
  n: number;
  nodeId: string;
  // The links from the agent to the node, in order.
  linkIds: string[];
  failed: boolean;
  ms: number;
  label: string;
}

// The route each step of a turn takes through the graph: a model request is one link from the agent, a tool call goes
// through its server. A step whose node the graph does not hold (it was folded into "more") ends at that node.
export function pathOf(detail: TurnDetail, graph: Graph, turnNode?: string | null): PathStep[] {
  const has = new Set(graph.nodes.map((n) => n.id));
  // With the turn's own node, a step is the link from that node to what it used; without it (an older turn that has
  // no node), the route from the agent.
  const own = !!turnNode && has.has(turnNode);
  const out: PathStep[] = [];
  detail.steps.forEach((s, i) => {
    const ms = s.durationUs / 1000;
    if (s.kind === 'model') {
      const id = modelId(s.label);
      if (has.has(id)) out.push({ n: i + 1, nodeId: id, linkIds: [own ? `${turnNode}>${id}` : `agent>${id}`], failed: s.failed, ms, label: s.label });
      return;
    }
    const server = s.server ?? '';
    let id = toolId(server, s.label);
    if (!has.has(id)) id = moreId(server);
    if (!has.has(id)) return;
    out.push({ n: i + 1, nodeId: id, linkIds: own ? [`${turnNode}>${id}`] : [`agent>${serverId(server)}`, `${serverId(server)}>${id}`], failed: s.failed, ms, label: s.label });
  });
  return out;
}
