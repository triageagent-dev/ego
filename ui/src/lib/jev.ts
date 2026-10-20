import type { JevAnswer, JevTurn } from './types';

// isJevMetric matches the jev_* eval family (internal/decision): built-ins
// are "jev_<name>", custom questions "jev:<key>".
export const isJevMetric = (name: string) => name.startsWith('jev_') || name.startsWith('jev:');

// jevVerdict renders one turn's Jev answer: p(yes) for a yes/no question,
// or the picked label with every label's probability for a choice.
export const jevVerdict = (a: JevAnswer | undefined): string => {
  if (!a) return 'Jev gave no answer for this turn';
  if (a.type === 'noul' && a.noul !== undefined) return `Jev: p(yes) = ${a.noul.toFixed(2)}`;
  if (a.choice) {
    const probs = a.probabilities
      ? ' · ' + Object.entries(a.probabilities)
        .sort((x, y) => y[1] - x[1])
        .map(([k, v]) => `${k} ${v.toFixed(2)}`)
        .join(', ')
      : '';
    return `Jev picked "${a.choice}"${probs}`;
  }
  return 'Jev answer could not be read';
};

// jevPassingAnswer renders the answer that counts as a pass.
export const jevPassingAnswer = (questionType: unknown, expect: unknown): string =>
  questionType === 'choice' ? `"${String(expect)}"` : expect === false ? 'no' : 'yes';

// jevTurns reads a jev_* result's per-turn detail. Results stored by the
// first jev release kept it under per_invocation (no turn text); those are
// read too, so old Runs entries still expand.
export const jevTurns = (details: Record<string, unknown> | null | undefined): JevTurn[] => {
  if (!details) return [];
  if (Array.isArray(details.turns)) return details.turns as JevTurn[];
  if (Array.isArray(details.per_invocation)) {
    return (details.per_invocation as Array<Record<string, unknown>>).map((p) => ({
      invocation_id: String(p.invocation_id ?? ''),
      user_input: '',
      final_response: '',
      score: typeof p.score === 'number' ? p.score : null,
      answer: p.answer as JevAnswer | undefined,
    }));
  }
  return [];
};
