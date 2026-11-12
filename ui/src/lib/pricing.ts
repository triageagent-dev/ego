/**
 * Best-effort USD list price per 1,000,000 tokens, by exact model id.
 *
 * These are public list prices at time of writing, not committed spend -
 * verify before relying on them for real billing. The rates match the ones the
 * core's own usage view uses, so the two screens price a model the same way.
 * They are the plain text input and output rates. The ledger keeps how much of a call's input was read from and
 * written to the prompt cache, and a model whose rate lists cache factors prices those parts with them; for any other
 * model that input is priced at the plain rate, so a call with cached input is priced high. Deliberately NOT
 * fuzzy-matched against unlisted models (e.g. a "-native-audio" or "-live"
 * variant of a listed text model): audio/live pricing is usually
 * structured differently from text pricing, so guessing via substring
 * match risks a confidently wrong number. An unmapped model renders no
 * cost rather than a fabricated one - add an entry here instead.
 */
import { useEffect, useSyncExternalStore } from 'react';
import { config } from '../config';

interface ModelRate {
  inputPerMillion: number;
  outputPerMillion: number;
  // What a cache read and a cache write cost, as a share of the input rate.
  cacheReadFactor?: number;
  cacheWriteFactor?: number;
}

// Anthropic: a cache read costs a tenth of the input rate, a write (5 minute cache) a quarter more.
const CLAUDE = { cacheReadFactor: 0.1, cacheWriteFactor: 1.25 };

const RATES: Record<string, ModelRate> = {
  'gemini-2.5-flash': { inputPerMillion: 0.3, outputPerMillion: 2.5 },
  'gemini-2.5-pro': { inputPerMillion: 1.25, outputPerMillion: 10.0 },
  'gemini-2.0-flash': { inputPerMillion: 0.1, outputPerMillion: 0.4 },
  'gemini-3-flash-preview': { inputPerMillion: 0.5, outputPerMillion: 3.0 },
  'gemini-3.5-flash': { inputPerMillion: 1.5, outputPerMillion: 9.0 },
  'gemini-3.8-flash': { inputPerMillion: 0.75, outputPerMillion: 3.75 },
  'gemini-3-pro-preview': { inputPerMillion: 2.0, outputPerMillion: 12.0 },
  'gemini-3.1-pro-preview': { inputPerMillion: 2.0, outputPerMillion: 12.0 },
  // The decision model, priced from its billed per-call costs; it reports no output tokens.
  'typesafe/jev-1.13': { inputPerMillion: 0.042, outputPerMillion: 0 },
  // Gemini Decision 1: $0.10 per 1M input tokens, no charge for output (no thinking-token or per-question billing).
  'gemini-decision-1': { inputPerMillion: 0.1, outputPerMillion: 0 },
  // Anthropic list prices. The input rate is the plain one: an agent that reads most of its prompt from the cache
  // (Claude Code does) is priced high, as the note above says. claude-haiku-5-5 is left out on purpose: its rate
  // changes above 100K prompt tokens and the ledger holds no per-call prompt size.
  'claude-fable-5-1': { inputPerMillion: 10.0, outputPerMillion: 50.0, ...CLAUDE },
  'claude-fable-5': { inputPerMillion: 10.0, outputPerMillion: 50.0, ...CLAUDE },
  'claude-opus-5-5': { inputPerMillion: 4.0, outputPerMillion: 20.0, ...CLAUDE },
  'claude-opus-5': { inputPerMillion: 5.0, outputPerMillion: 25.0, ...CLAUDE },
  'claude-opus-4-8': { inputPerMillion: 5.0, outputPerMillion: 25.0, ...CLAUDE },
  'claude-opus-4-7': { inputPerMillion: 5.0, outputPerMillion: 25.0, ...CLAUDE },
  'claude-opus-4-6': { inputPerMillion: 5.0, outputPerMillion: 25.0, ...CLAUDE },
  'claude-sonnet-5-5': { inputPerMillion: 2.0, outputPerMillion: 10.0, ...CLAUDE },
  'claude-sonnet-5': { inputPerMillion: 2.0, outputPerMillion: 10.0, ...CLAUDE },
  'claude-sonnet-4-6': { inputPerMillion: 3.0, outputPerMillion: 15.0, ...CLAUDE },
  'claude-haiku-4-5': { inputPerMillion: 1.0, outputPerMillion: 5.0, ...CLAUDE },
  'gpt-4o': { inputPerMillion: 2.5, outputPerMillion: 10.0 },
  'gpt-4o-mini': { inputPerMillion: 0.15, outputPerMillion: 0.6 },
  // Gemini Live API native-audio model (Vertex AI id `gemini-live-2.5-flash-native-audio`;
  // Gemini Developer API id `gemini-2.5-flash-native-audio-preview-*`). Audio-to-audio
  // sessions bill almost entirely in audio tokens, so this uses the audio rate from
  // https://ai.google.dev/gemini-api/docs/pricing#gemini-2.5-flash-native-audio
  // ($3.00/1M input, $12.00/1M output), not the (lower) text rate on the same page.
  'gemini-live-2.5-flash-native-audio': { inputPerMillion: 3.0, outputPerMillion: 12.0 },
  'gemini-2.5-flash-native-audio-preview-12-2025': { inputPerMillion: 3.0, outputPerMillion: 12.0 },
  'gemini-2.5-flash-native-audio-preview-09-2025': { inputPerMillion: 3.0, outputPerMillion: 12.0 },
  // Same audio-dominated reasoning for the 3.8 Live model.
  'gemini-3.8-live': { inputPerMillion: 3.0, outputPerMillion: 12.0 },
};

// Prices the operator set on the server (`pricing` in the chart, `--pricing-file`): they win over the list above, so
// a price changes without a release. Loaded by the views that show a cost; until then the built-in list prices.
let overrides: Record<string, ModelRate> = {};
let version = 0;
const listeners = new Set<() => void>();

const num = (v: unknown): v is number => typeof v === 'number' && Number.isFinite(v) && v >= 0;

async function loadOverrides(): Promise<void> {
  try {
    const response = await fetch(`${config.api.endpoints.usage}/pricing`);
    if (!response.ok) return;
    const rates = ((await response.json()) as { data?: { rates?: Record<string, Partial<ModelRate>> } }).data?.rates ?? {};
    const next: Record<string, ModelRate> = {};
    for (const [id, r] of Object.entries(rates)) {
      if (!num(r.inputPerMillion) || !num(r.outputPerMillion)) continue;
      next[normalize(id)] = {
        inputPerMillion: r.inputPerMillion,
        outputPerMillion: r.outputPerMillion,
        ...(num(r.cacheReadFactor) ? { cacheReadFactor: r.cacheReadFactor } : {}),
        ...(num(r.cacheWriteFactor) ? { cacheWriteFactor: r.cacheWriteFactor } : {}),
      };
    }
    if (JSON.stringify(next) === JSON.stringify(overrides)) return;
    overrides = next;
    version++;
    listeners.forEach((l) => l());
  } catch {
    // The built-in list stands when the server cannot say.
  }
}

/** Loads the server's prices on each visit of a view that shows a cost, and re-renders it when they differ. Returns a number that changes with them, for a memo that prices things. */
export function usePricing(): number {
  useEffect(() => {
    void loadOverrides();
  }, []);
  return useSyncExternalStore(
    (onChange) => {
      listeners.add(onChange);
      return () => listeners.delete(onChange);
    },
    () => version,
    () => 0,
  );
}

function normalize(model: string): string {
  return model.trim().toLowerCase();
}

export function estimateCostUsd(
  model: string | undefined,
  inputTokens: number,
  outputTokens: number,
  cacheReadTokens = 0,
  cacheWriteTokens = 0,
): number | null {
  if (!model) return null;
  const key = normalize(model);
  const rate = overrides[key] ?? RATES[key];
  if (!rate) return null;
  // The cache parts are inside inputTokens; they are priced apart only where the model's cache factors are known.
  const read = rate.cacheReadFactor != null ? Math.min(cacheReadTokens, inputTokens) : 0;
  const write = rate.cacheWriteFactor != null ? Math.min(cacheWriteTokens, inputTokens - read) : 0;
  const plain = inputTokens - read - write;
  const input = plain + read * (rate.cacheReadFactor ?? 1) + write * (rate.cacheWriteFactor ?? 1);
  return (input / 1_000_000) * rate.inputPerMillion + (outputTokens / 1_000_000) * rate.outputPerMillion;
}

export function formatCostUsd(cost: number): string {
  if (cost < 0.01) return `$${cost.toFixed(4)}`;
  return `$${cost.toFixed(2)}`;
}
