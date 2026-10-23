import type { SessionSpan } from '../api/client';

// The inside of one turn: its model requests and tool calls on a time axis, found from the session's spans. It reads
// the same signs the server reads to summarize a turn (a model request has a model, a tool call a tool name), so the
// picture and the row it belongs to agree, and it works for any agent that sends GenAI spans, not only Claude Code.

export interface TurnStep {
  kind: 'model' | 'tool';
  label: string;
  // What the sender says beyond the name: the MCP server, a shell command's class and program.
  detail?: string;
  // Microseconds after the turn began, and how long it took.
  offsetUs: number;
  durationUs: number;
  // A tool call waits to be approved, then runs: the wait is the first part of the span.
  approvalUs?: number;
  failed: boolean;
  message?: string;
  tokensIn?: number;
  tokensOut?: number;
  cached?: number;
}

export interface TurnDetail {
  startUs: number;
  durationUs: number;
  operation: string;
  steps: TurnStep[];
}

interface Node {
  span: SessionSpan;
  children: Node[];
}

function toTree(spans: SessionSpan[]): Node[] {
  const roots: Node[] = [];
  const stack: Node[] = [];
  for (const span of spans) {
    const node: Node = { span, children: [] };
    while (stack.length > 0 && stack[stack.length - 1].span.depth >= span.depth) stack.pop();
    (stack.length > 0 ? stack[stack.length - 1].children : roots).push(node);
    stack.push(node);
  }
  return roots;
}

const str = (v: unknown): string | undefined => (typeof v === 'string' && v !== '' ? v : undefined);
const num = (v: unknown): number => {
  if (typeof v === 'number' && Number.isFinite(v)) return v;
  if (typeof v === 'string' && v.trim() !== '' && Number.isFinite(Number(v))) return Number(v);
  return 0;
};

const isModel = (s: SessionSpan) =>
  !!(str(s.tags['gen_ai.request.model']) || s.operation_name.endsWith('llm_request')) && !str(s.tags['gen_ai.tool.name']);
const isTool = (s: SessionSpan) => !!str(s.tags['gen_ai.tool.name']) || s.tags['gen_ai.operation.name'] === 'execute_tool';
const isError = (s: SessionSpan) => s.tags['otel.status_code'] === 'ERROR';
const WAIT = 'claude_code.tool.blocked_on_user';

function failedBelow(n: Node): boolean {
  return isError(n.span) || n.span.tags.decision === 'reject' || n.children.some(failedBelow);
}

function findOp(n: Node, op: string): SessionSpan | undefined {
  for (const c of n.children) {
    if (c.span.operation_name === op) return c.span;
    const deeper = findOp(c, op);
    if (deeper) return deeper;
  }
  return undefined;
}

function failureText(n: Node): string | undefined {
  const own = (s: SessionSpan) => str(s.tags['otel.status_description']) ?? str(s.tags.error) ?? str(s.tags['exception.message']);
  if (isError(n.span)) return own(n.span) ?? 'failed';
  if (n.span.tags.decision === 'reject') return 'rejected';
  for (const c of n.children) {
    const t = failureText(c);
    if (t) return t;
  }
  return undefined;
}

function collect(n: Node, out: Node[]) {
  for (const c of [...n.children].sort((a, b) => a.span.start_time - b.span.start_time)) {
    if (isModel(c.span) || isTool(c.span)) out.push(c);
    // A tool call's own children are its approval and run, shown inside it; a model request has none that matter.
    if (!isTool(c.span) && !isModel(c.span)) collect(c, out);
  }
}

// The turn whose root span is rootSpanId, or undefined when the session's spans do not hold it.
export function resolveTurn(spans: SessionSpan[], rootSpanId: string): TurnDetail | undefined {
  const find = (nodes: Node[]): Node | undefined => {
    for (const n of nodes) {
      if (n.span.span_id === rootSpanId) return n;
      const deeper = find(n.children);
      if (deeper) return deeper;
    }
    return undefined;
  };
  const root = find(toTree(spans));
  if (!root) return undefined;
  const found: Node[] = [];
  collect(root, found);
  const steps: TurnStep[] = found.map((n) => {
    const t = n.span.tags;
    const base = { offsetUs: n.span.start_time - root.span.start_time, durationUs: n.span.duration, failed: failedBelow(n), message: failureText(n) };
    if (isModel(n.span)) {
      return {
        ...base,
        kind: 'model' as const,
        label: str(t.model) ?? str(t['gen_ai.request.model']) ?? 'model',
        tokensIn: num(t['gen_ai.usage.input_tokens']),
        tokensOut: num(t['gen_ai.usage.output_tokens']),
        cached: num(t['gen_ai.usage.cache_read.input_tokens']),
      };
    }
    const full = str(t.tool_name) ?? str(t['gen_ai.tool.name']) ?? n.span.operation_name;
    const mcp = /^mcp__(.+?)__(.+)$/.exec(full);
    const server = str(t['anthropic.mcp.server.name']) ?? (mcp ? mcp[1] : undefined);
    const command = [str(t.bash_command_class), str(t.bash_argv0)].filter(Boolean).join(' · ');
    const wait = findOp(n, WAIT);
    return {
      ...base,
      kind: 'tool' as const,
      label: mcp ? mcp[2] : full,
      detail: [server, command].filter(Boolean).join(' · ') || undefined,
      approvalUs: wait ? wait.duration : undefined,
    };
  });
  return { startUs: root.span.start_time, durationUs: root.span.duration, operation: root.span.operation_name, steps };
}
