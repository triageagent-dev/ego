// What the size of a tile of the treemap stands for.
export const WEIGHTS = ['calls', 'errors', 'time'] as const;
export type Weight = (typeof WEIGHTS)[number];

export const weightOf = (t: { calls: number; failed: number; ms: number }, w: Weight) => (w === 'calls' ? t.calls : w === 'errors' ? t.failed : t.ms);
