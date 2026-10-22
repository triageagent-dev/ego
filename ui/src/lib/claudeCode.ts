import type { SessionSpan } from '../api/client';

// Claude Code's own traces: one claude_code.interaction span per prompt, with the model requests and the tool calls
// of that prompt under it. The spans hold the shape of the work (which model, how long, what it cost, which tool, was
// it approved, did it run) but not the messages: the prompt is there only when the sender sends it, and no span
// carries the model's reply or a tool's output. resolveClaudeCode turns the flat span list into turns a person can
// read, and is empty for a session that is not Claude Code's.

export const INTERACTION = 'claude_code.interaction';
const LLM_REQUEST = 'claude_code.llm_request';
const TOOL = 'claude_code.tool';
const TOOL_WAIT = 'claude_code.tool.blocked_on_user';
const TOOL_RUN = 'claude_code.tool.execution';

// What Claude Code sends as the prompt unless the operator opted in to prompt content.
const REDACTED = '<REDACTED>';

export interface CCPrompt {
  // The prompt, when the sender sent it.
  text?: string;
  // Its length in characters, known even when the text is not.
  length?: number;
}

export interface CCModelRequest {
  kind: 'llm';
  startUs: number;
  model: string;
  ms: number;
  ttftMs?: number;
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens: number;
  stopReason?: string;
  error?: string;
}

export interface CCToolCall {
  kind: 'tool';
  startUs: number;
  name: string;
  // The MCP server the tool belongs to, when it is an MCP tool.
  server?: string;
  // What the span says about the call: a shell command's class and program.
  detail?: string;
  ms: number;
  // The wait for a person (or the permission config) to approve the call.
  approval?: { ms: number; decision: string; source?: string };
  // The run itself, after the approval.
  run?: { ms: number; ok: boolean };
  error?: string;
}

export interface CCOther {
  kind: 'other';
  startUs: number;
  op: string;
  ms: number;
}

export type CCStep = CCModelRequest | CCToolCall | CCOther;

export interface CCTurn {
  id: string;
  sequence: number;
  startUs: number;
  ms: number;
  prompt: CCPrompt;
  steps: CCStep[];
  requests: number;
  tools: number;
  failedTools: number;
  inputTokens: number;
  outputTokens: number;
}

interface Node {
  span: SessionSpan;
  children: Node[];
}

const str = (v: unknown): string | undefined => (typeof v === 'string' && v !== '' ? v : undefined);
const num = (v: unknown): number | undefined => {
  if (typeof v === 'number' && Number.isFinite(v)) return v;
  if (typeof v === 'string' && v.trim() !== '' && Number.isFinite(Number(v))) return Number(v);
  return undefined;
};

// The flat list is depth-first with a depth per span: the tree is the spans between a span and the next one that is
// no deeper.
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

function find(node: Node, op: string): Node | undefined {
  for (const c of node.children) {
    if (c.span.operation_name === op) return c;
    const deeper = find(c, op);
    if (deeper) return deeper;
  }
  return undefined;
}

const msOf = (span: SessionSpan) => num(span.tags.duration_ms) ?? span.duration / 1000;

function modelRequest(span: SessionSpan): CCModelRequest {
  const t = span.tags;
  const input = num(t['gen_ai.usage.input_tokens']) ?? (num(t.input_tokens) ?? 0) + (num(t.cache_read_tokens) ?? 0) + (num(t.cache_creation_tokens) ?? 0);
  const failed = t.success === false || t['otel.status_code'] === 'ERROR';
  return {
    kind: 'llm',
    startUs: span.start_time,
    model: str(t.model) ?? str(t['gen_ai.request.model']) ?? 'model',
    ms: msOf(span),
    ttftMs: num(t.ttft_ms),
    inputTokens: input,
    outputTokens: num(t['gen_ai.usage.output_tokens']) ?? num(t.output_tokens) ?? 0,
    cacheReadTokens: num(t['gen_ai.usage.cache_read.input_tokens']) ?? num(t.cache_read_tokens) ?? 0,
    stopReason: str(t.stop_reason),
    error: failed ? str(t.error) ?? str(t['otel.status_description']) ?? 'failed' : undefined,
  };
}

function toolCall(node: Node): CCToolCall {
  const t = node.span.tags;
  const wait = find(node, TOOL_WAIT)?.span;
  const run = find(node, TOOL_RUN)?.span;
  const detail = [str(t.bash_command_class), str(t.bash_argv0)].filter(Boolean).join(' · ');
  const runOk = run ? run.tags.success !== false && run.tags['otel.status_code'] !== 'ERROR' : undefined;
  // An MCP tool is named mcp__<server>__<tool>; the server shows as a chip, so the name is the tool's own.
  const full = str(t.tool_name) ?? str(t['gen_ai.tool.name']) ?? str(t.tool_name_safe) ?? 'tool';
  const mcp = /^mcp__(.+?)__(.+)$/.exec(full);
  return {
    kind: 'tool',
    startUs: node.span.start_time,
    name: mcp ? mcp[2] : full,
    server: str(t['anthropic.mcp.server.name']) ?? (mcp ? mcp[1] : undefined),
    detail: detail || undefined,
    ms: msOf(node.span),
    approval: wait ? { ms: msOf(wait), decision: str(wait.tags.decision) ?? 'accept', source: str(wait.tags.source) } : undefined,
    run: run ? { ms: msOf(run), ok: !!runOk } : undefined,
    error: runOk === false || t['otel.status_code'] === 'ERROR' ? str(run?.tags.error) ?? str(t['otel.status_description']) ?? 'failed' : undefined,
  };
}

function turnOf(node: Node, index: number): CCTurn {
  const t = node.span.tags;
  const raw = str(t.user_prompt);
  const steps: CCStep[] = [];
  for (const child of [...node.children].sort((a, b) => a.span.start_time - b.span.start_time)) {
    const op = child.span.operation_name;
    if (op === LLM_REQUEST) steps.push(modelRequest(child.span));
    else if (op === TOOL) steps.push(toolCall(child));
    else steps.push({ kind: 'other', startUs: child.span.start_time, op, ms: msOf(child.span) });
  }
  const requests = steps.filter((s): s is CCModelRequest => s.kind === 'llm');
  const tools = steps.filter((s): s is CCToolCall => s.kind === 'tool');
  return {
    id: node.span.span_id,
    sequence: index + 1,
    startUs: node.span.start_time,
    ms: msOf(node.span),
    prompt: {
      text: raw && raw.trim() !== REDACTED ? raw : str(t['input.value']),
      length: num(t.user_prompt_length),
    },
    steps,
    requests: requests.length,
    tools: tools.length,
    failedTools: tools.filter((s) => s.error || s.approval?.decision === 'reject').length,
    inputTokens: requests.reduce((n, r) => n + r.inputTokens, 0),
    outputTokens: requests.reduce((n, r) => n + r.outputTokens, 0),
  };
}

// The turns of a Claude Code session, oldest first; empty when the spans are not Claude Code's.
export function resolveClaudeCode(spans: SessionSpan[]): CCTurn[] {
  const turns: CCTurn[] = [];
  const visit = (nodes: Node[]) => {
    for (const n of nodes) {
      if (n.span.operation_name === INTERACTION) turns.push(turnOf(n, turns.length));
      else visit(n.children);
    }
  };
  visit(toTree(spans));
  // Claude Code counts its prompts per trace (every one says 1), so the turns are numbered here, in order.
  return turns.sort((a, b) => a.startUs - b.startUs).map((t, i) => ({ ...t, sequence: i + 1 }));
}
