import { useMemo, useState } from 'react';
import { css } from '@emotion/react';
import { BUILTIN, type GNode, type Graph, type PathStep } from '../../lib/agentGraph';
import { fmtDuration } from '../../lib/turns';

// The agent as a tree: the agent at the top, under it the models and the MCP servers it used, under each server its
// tools. A row is as long as the calls it had, and the red part of it is the share that failed. When a turn is on show,
// the rows it used say how many of its steps went through them and the rest fade.

interface Row {
  node: GNode;
  depth: 0 | 1 | 2;
  // The rows under it, for a server.
  tools?: Row[];
}

const calls = (n: GNode) => `${n.calls.toLocaleString()} call${n.calls === 1 ? '' : 's'}`;
const byCalls = (a: Row, b: Row) => b.node.calls - a.node.calls || a.node.label.localeCompare(b.node.label);

function treeOf(graph: Graph): { agent: GNode | undefined; rows: Row[] } {
  const agent = graph.nodes.find((n) => n.kind === 'agent');
  const toolsOf = new Map<string, Row[]>();
  for (const n of graph.nodes) {
    if (n.kind !== 'tool') continue;
    const k = n.server ?? BUILTIN;
    toolsOf.set(k, [...(toolsOf.get(k) ?? []), { node: n, depth: 2 }]);
  }
  const rows: Row[] = [];
  for (const n of graph.nodes) {
    if (n.kind === 'model') rows.push({ node: n, depth: 1 });
    if (n.kind === 'server') rows.push({ node: n, depth: 1, tools: (toolsOf.get(n.label) ?? []).sort(byCalls) });
  }
  return { agent, rows: rows.sort(byCalls) };
}

export function AgentTree({ graph, path, selectedId, onSelectNode }: {
  graph: Graph;
  path: PathStep[] | null;
  selectedId: string | null;
  onSelectNode: (id: string | null) => void;
}) {
  const { agent, rows } = useMemo(() => treeOf(graph), [graph]);
  const [closed, setClosed] = useState<Set<string>>(new Set());
  const toggle = (id: string) => setClosed((c) => {
    const next = new Set(c);
    if (!next.delete(id)) next.add(id);
    return next;
  });

  // How many steps of the turn on show went through each node.
  const steps = useMemo(() => {
    const m = new Map<string, { n: number; failed: boolean }>();
    for (const s of path ?? []) {
      const e = m.get(s.nodeId) ?? { n: 0, failed: false };
      e.n++;
      e.failed ||= s.failed;
      m.set(s.nodeId, e);
    }
    return m;
  }, [path]);
  const lit = (r: Row): { n: number; failed: boolean } | undefined => {
    const own = steps.get(r.node.id);
    if (!r.tools) return own;
    // A server is on the path through its tools.
    let n = own?.n ?? 0;
    let failed = own?.failed ?? false;
    for (const t of r.tools) {
      const s = steps.get(t.node.id);
      if (s) { n += s.n; failed ||= s.failed; }
    }
    return n > 0 ? { n, failed } : undefined;
  };
  const showing = steps.size > 0;

  const maxOf = (depth: 1 | 2) => {
    let m = 1;
    for (const r of rows) {
      if (depth === 1) m = Math.max(m, r.node.calls);
      else for (const t of r.tools ?? []) m = Math.max(m, t.node.calls);
    }
    return m;
  };
  const max1 = maxOf(1);
  const max2 = maxOf(2);

  const line = (r: Row, max: number) => {
    const n = r.node;
    const on = lit(r);
    const share = n.calls > 0 ? n.failed / n.calls : 0;
    const len = Math.max(2, Math.round((n.calls / max) * 100));
    const open = !closed.has(n.id);
    const tip = `${n.label} · ${n.kind === 'tool' && n.server ? `tool of ${n.server}` : n.kind}\n${calls(n)}${n.failed > 0 ? `, ${n.failed} failed (${Math.round(share * 100)}%)` : ''}${n.ms > 0 ? `\n${fmtDuration(n.ms / Math.max(1, n.calls))} on average, ${fmtDuration(n.ms)} in all` : ''}`;
    return (
      <div
        key={n.id}
        role="treeitem"
        aria-selected={selectedId === n.id}
        aria-expanded={r.tools ? open : undefined}
        title={tip}
        css={[rowStyle, r.depth === 2 && toolRowStyle, selectedId === n.id && selectedStyle, on && litStyle, showing && !on && fadedStyle]}
        onClick={() => onSelectNode(selectedId === n.id ? null : n.id)}
      >
        <span css={nameStyle}>
          {r.tools ? (
            <button
              css={chevronStyle}
              aria-label={open ? 'Hide the tools' : 'Show the tools'}
              onClick={(e) => { e.stopPropagation(); toggle(n.id); }}
            >
              {open ? '▾' : '▸'}
            </button>
          ) : r.depth === 1 ? <span css={markStyle}>◆</span> : null}
          <span css={labelStyle}>{n.label}</span>
          {on && <span css={onStyle(on.failed)}>×{on.n}</span>}
        </span>
        <span css={trackStyle}>
          <span css={fillStyle} style={{ width: `${len}%`, background: share > 0 ? `linear-gradient(90deg, var(--status-failure) ${Math.max(share * 100, 4)}%, var(--bar) ${Math.max(share * 100, 4)}%)` : 'var(--bar)' }} />
        </span>
        <span css={numStyle}>{n.calls.toLocaleString()}</span>
        <span css={[numStyle, n.failed > 0 && failStyle]}>{n.failed > 0 ? `${n.failed} failed` : ''}</span>
        <span css={[numStyle, dimStyle]}>{n.ms > 0 ? fmtDuration(n.ms / Math.max(1, n.calls)) : ''}</span>
      </div>
    );
  };

  if (!agent) return null;
  const agentShare = agent.calls > 0 ? agent.failed / agent.calls : 0;
  return (
    <div css={boxStyle} role="tree" aria-label="The agent, its models, MCP servers and tools">
      <div css={agentStyle}>
        <span css={agentNameStyle}>{agent.label}</span>
        <span css={dimStyle}>
          {agent.calls.toLocaleString()} turn{agent.calls === 1 ? '' : 's'}
          {agent.failed > 0 && <span css={failStyle}> · {agent.failed} with a failure ({Math.round(agentShare * 100)}%)</span>}
        </span>
      </div>
      <div css={branchStyle}>
        {rows.map((r) => (
          <div key={r.node.id}>
            {line(r, max1)}
            {r.tools && !closed.has(r.node.id) && <div css={branchStyle}>{r.tools.map((t) => line(t, max2))}</div>}
          </div>
        ))}
      </div>
    </div>
  );
}

const boxStyle = css`
  --bar: #4b5563;
  height: 100%;
  overflow-y: auto;
  padding: 12px 14px 14px;
  font-size: 12px;
`;

const agentStyle = css`
  display: flex;
  align-items: baseline;
  gap: 12px;
  flex-wrap: wrap;
  padding: 2px 6px 10px;
`;

const agentNameStyle = css`
  color: var(--text-primary, #e5e7eb);
  font-size: 15px;
  font-weight: 600;
`;

const branchStyle = css`
  margin-left: 8px;
  padding-left: 12px;
  border-left: 1px solid var(--border-default);
`;

const rowStyle = css`
  display: grid;
  grid-template-columns: minmax(120px, 1.3fr) minmax(60px, 1fr) 52px 70px 56px;
  align-items: center;
  gap: 10px;
  padding: 3px 6px;
  border-radius: 4px;
  cursor: pointer;
  &:hover { background: var(--bg-selected); }
`;

const toolRowStyle = css`
  color: var(--text-muted);
`;

const selectedStyle = css`
  outline: 1px solid var(--accent-magenta);
`;

const litStyle = css`
  background: var(--bg-selected);
  color: var(--text-primary, #e5e7eb);
`;

const fadedStyle = css`
  opacity: 0.4;
`;

const nameStyle = css`
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
`;

const labelStyle = css`
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  min-width: 0;
`;

const markStyle = css`
  width: 14px;
  color: var(--text-muted);
  font-size: 9px;
  text-align: center;
  flex: none;
`;

const chevronStyle = css`
  width: 14px;
  padding: 0;
  border: none;
  background: transparent;
  color: var(--text-muted);
  font-size: 11px;
  line-height: 1;
  cursor: pointer;
  flex: none;
`;

const onStyle = (failed: boolean) => css`
  flex: none;
  padding: 0 6px;
  border-radius: 8px;
  background: ${failed ? 'var(--status-failure)' : 'var(--accent-magenta)'};
  color: #14141f;
  font-weight: 700;
  font-size: 11px;
`;

const trackStyle = css`
  height: 8px;
  border-radius: 4px;
  background: rgba(255, 255, 255, 0.04);
  overflow: hidden;
`;

const fillStyle = css`
  display: block;
  height: 100%;
  border-radius: 4px;
`;

const numStyle = css`
  text-align: right;
  white-space: nowrap;
  font-variant-numeric: tabular-nums;
`;

const failStyle = css`
  color: var(--status-failure);
`;

const dimStyle = css`
  color: var(--text-muted);
`;
