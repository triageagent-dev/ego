// The models the judge metrics can run on, with the provider whose API key each needs.
export const JUDGE_MODELS = [
  { value: 'gemini-3.8-flash', provider: 'google' },
  { value: 'gemini-3.5-flash', provider: 'google' },
  { value: 'gemini-3.1-pro-preview', provider: 'google' },
  { value: 'gemini-3-pro-preview', provider: 'google' },
  { value: 'gemini-3-flash-preview', provider: 'google' },
  { value: 'gemini-2.5-flash', provider: 'google' },
  { value: 'gemini-2.0-flash', provider: 'google' },
  { value: 'anthropic/claude-3.5-sonnet', provider: 'anthropic' },
  { value: 'openai/gpt-4o', provider: 'openai' },
] as const;

export type JudgeModelProvider = typeof JUDGE_MODELS[number]['provider'];
