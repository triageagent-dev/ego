import type { Trace, Span } from './types';

export const ADK_SCOPE = 'gcp.vertex.agent';

export const USER_ROLES = ['user', 'human'];
export const ASSISTANT_ROLES = ['assistant', 'model', 'ai'];

function isGenAISpan(span: Span): boolean {
  return !!(
    span.tags['gen_ai.request.model'] ||
    span.tags['gen_ai.system'] ||
    span.tags['gen_ai.input.messages'] ||
    span.tags['gen_ai.prompt'] ||
    span.tags['gen_ai.request.messages']
  );
}

export function detectTraceFormat(trace: Trace): 'adk' | 'genai' {
  const check = (spans: Span[]): 'adk' | 'genai' | null => {
    let hasGenai = false;
    for (const span of spans) {
      if (span.tags['otel.scope.name'] === ADK_SCOPE) {
        return 'adk';
      }
      if (!hasGenai && isGenAISpan(span)) {
        hasGenai = true;
      }
    }
    return hasGenai ? 'genai' : null;
  };

  const initial = check(trace.allSpans.slice(0, 10));
  if (initial) return initial;

  if (trace.allSpans.length > 10) {
    const full = check(trace.allSpans);
    if (full) return full;
  }

  return 'adk';
}

export function findChildrenByOperation(root: Span, opPrefix: string): Span[] {
  const results: Span[] = [];
  walkSpanTree(root, opPrefix, results);
  results.sort((a, b) => a.startTime - b.startTime);
  return results;
}

function walkSpanTree(span: Span, opPrefix: string, acc: Span[]): void {
  for (const child of span.children) {
    if (child.operationName.startsWith(opPrefix)) {
      acc.push(child);
    }
    walkSpanTree(child, opPrefix, acc);
  }
}

export function findDescendantLLMSpans(root: Span): Span[] {
  const results: Span[] = [];
  const queue = [root];

  while (queue.length > 0) {
    const span = queue.shift()!;
    if (isGenAISpan(span)) {
      results.push(span);
    }
    queue.push(...span.children);
  }

  results.sort((a, b) => a.startTime - b.startTime);
  return results;
}

/** A turn's start as "20 Oct, 06:58:14" in the viewer's time zone. */
export function formatTurnTime(ms: number): string {
  return new Date(ms).toLocaleString(undefined, {
    day: 'numeric',
    month: 'short',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  });
}

/** A completed session's span window: ms under a second, then 0.1s, then m ss. */
export function formatSpanDuration(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
  const totalSeconds = Math.round(ms / 1000);
  return `${Math.floor(totalSeconds / 60)}m ${String(totalSeconds % 60).padStart(2, '0')}s`;
}

// A model id without the provider path, the date stamp and the preview/latest marker.
function shortModel(id: string): string {
  const short = id
    .slice(id.lastIndexOf('/') + 1)
    .replace(/-(\d{8}|\d{4}-\d{2}-\d{2})$/, '')
    .replace(/-(preview|latest)(-\d{2}-\d{2,4})?$/, '');
  return short || id;
}

// What the session row shows for the models it used: the id as is for one, and for several the short names with
// the leading part they share written once: `gemini-3-flash / pro`.
export function modelsLabel(ids: string[]): string {
  if (ids.length <= 1) return ids[0] ?? '';
  const names = [...new Set(ids.map(shortModel))];
  if (names.length === 1) return names[0];
  const parts = names.map((n) => n.split('-'));
  // gpt-4o next to gpt-4o-mini: collapsing would leave `4o-mini`, which reads as a different model.
  if (names.some((a) => names.some((b) => a !== b && b.startsWith(`${a}-`)))) return names.join(' / ');
  let shared = 0;
  while (parts.every((p) => p.length > shared + 1 && p[shared] === parts[0][shared])) shared++;
  return [names[0], ...parts.slice(1).map((p) => p.slice(shared).join('-'))].join(' / ');
}

// When a session started, as short as it can be read: `21 Oct, 18:55`, with the year only when it is not this one.
export function formatStarted(iso: string): string {
  const d = new Date(iso);
  const thisYear = d.getFullYear() === new Date().getFullYear();
  return d.toLocaleString(undefined, {
    day: 'numeric',
    month: 'short',
    ...(thisYear ? {} : { year: 'numeric' }),
    hour: '2-digit',
    minute: '2-digit',
  });
}
