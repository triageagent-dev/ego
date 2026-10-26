import type { EvalConfig, RubricKind } from './types';

/** The rubric metrics and the kind of rubric set each one takes. */
export const RUBRIC_KIND_OF: Record<string, RubricKind> = {
  rubric_based_final_response_quality_v1: 'final_response',
  rubric_based_tool_use_quality_v1: 'tool_use',
};

interface BuildEvalConfigParams {
  selectedEvaluatorNames: string[];
  judgeModel: string;
  threshold: number;
  trajectoryMatchType: string;
  // The rubric texts chosen for each kind of rubric metric.
  rubrics?: Partial<Record<RubricKind, string[]>>;
}

export function buildEvalConfig({
  selectedEvaluatorNames,
  judgeModel,
  threshold,
  trajectoryMatchType,
  rubrics,
}: BuildEvalConfigParams): EvalConfig {
  return {
    evaluators: selectedEvaluatorNames.map((evaluatorName) => {
      const kind = RUBRIC_KIND_OF[evaluatorName];
      const texts = kind ? rubrics?.[kind] : undefined;
      return {
        type: 'builtin',
        name: evaluatorName,
        judgeModel,
        threshold,
        trajectoryMatchType,
        ...(texts && texts.length > 0 ? { rubrics: texts } : {}),
      };
    }),
  };
}
