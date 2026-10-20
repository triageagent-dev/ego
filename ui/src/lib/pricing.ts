/**
 * Best-effort USD list price per 1,000,000 tokens, by exact model id.
 *
 * These are public list prices at time of writing, not committed spend -
 * verify before relying on them for real billing. Deliberately NOT
 * fuzzy-matched against unlisted models (e.g. a "-native-audio" or "-live"
 * variant of a listed text model): audio/live pricing is usually
 * structured differently from text pricing, so guessing via substring
 * match risks a confidently wrong number. An unmapped model renders no
 * cost rather than a fabricated one - add an entry here instead.
 */
interface ModelRate {
  inputPerMillion: number;
  outputPerMillion: number;
}

const RATES: Record<string, ModelRate> = {
  'gemini-2.5-flash': { inputPerMillion: 0.3, outputPerMillion: 2.5 },
  'gemini-2.5-pro': { inputPerMillion: 1.25, outputPerMillion: 10.0 },
  'gemini-2.0-flash': { inputPerMillion: 0.1, outputPerMillion: 0.4 },
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
};

function normalize(model: string): string {
  return model.trim().toLowerCase();
}

export function estimateCostUsd(
  model: string | undefined,
  inputTokens: number,
  outputTokens: number,
): number | null {
  if (!model) return null;
  const rate = RATES[normalize(model)];
  if (!rate) return null;
  return (inputTokens / 1_000_000) * rate.inputPerMillion + (outputTokens / 1_000_000) * rate.outputPerMillion;
}

export function formatCostUsd(cost: number): string {
  if (cost < 0.01) return `$${cost.toFixed(4)}`;
  return `$${cost.toFixed(2)}`;
}
