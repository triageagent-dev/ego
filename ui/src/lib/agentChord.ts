import { BUILTIN, modelId, toolId, type Graph } from './agentGraph';
import type { TurnRow } from './types';

// What is used together, for the chord view: the models and tools of the graph as arcs, and between two of them a
// ribbon as wide as the number of turns that used both. The graph shows what an agent uses; this shows what it uses
// at the same time.

export interface ChordItem {
  // The id of the node of the same thing in the graph, so a click selects it there.
  id: string;
  label: string;
  // The MCP server of a tool, BUILTIN for a built-in one, '' for a model.
  group: string;
  calls: number;
  failed: number;
}

export interface ChordData {
  items: ChordItem[];
  // matrix[i][j]: turns that used both items i and j.
  matrix: number[][];
}

export const MAX_CHORD_ITEMS = 18;

export function chordOf(rows: TurnRow[], graph: Graph): ChordData {
  const known = new Set(graph.nodes.map((n) => n.id));
  const items = new Map<string, ChordItem>();
  const usedBy: string[][] = [];
  for (const r of rows) {
    const used = new Set<string>();
    for (const [name, c] of Object.entries(r.modelCalls ?? {})) {
      const id = modelId(name);
      if (!known.has(id)) continue;
      const it = items.get(id) ?? { id, label: name, group: '', calls: 0, failed: 0 };
      it.calls += c.calls;
      it.failed += c.failed ?? 0;
      items.set(id, it);
      used.add(id);
    }
    for (const [name, c] of Object.entries(r.toolCalls ?? {})) {
      const id = toolId(c.server ?? '', name);
      if (!known.has(id)) continue;
      const it = items.get(id) ?? { id, label: name, group: c.server || BUILTIN, calls: 0, failed: 0 };
      it.calls += c.calls;
      it.failed += c.failed ?? 0;
      items.set(id, it);
      used.add(id);
    }
    usedBy.push([...used]);
  }

  // The busiest, kept together by what they belong to: the models first, then each server's tools.
  const top = [...items.values()].sort((a, b) => b.calls - a.calls).slice(0, MAX_CHORD_ITEMS);
  const groupCalls = new Map<string, number>();
  for (const it of top) groupCalls.set(it.group, (groupCalls.get(it.group) ?? 0) + it.calls);
  top.sort((a, b) => (a.group === b.group ? b.calls - a.calls : a.group === '' ? -1 : b.group === '' ? 1 : (groupCalls.get(b.group) ?? 0) - (groupCalls.get(a.group) ?? 0)));

  const index = new Map(top.map((it, i) => [it.id, i]));
  const matrix = top.map(() => top.map(() => 0));
  for (const used of usedBy) {
    const here = used.filter((id) => index.has(id)).map((id) => index.get(id)!);
    for (const i of here) for (const j of here) if (i !== j) matrix[i][j]++;
  }
  return { items: top, matrix };
}
