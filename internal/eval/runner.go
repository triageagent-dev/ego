package eval

import (
	"github.com/triageagent-dev/ego/internal/adk"
)

// NotYetImplemented lists built-in metric names internal/eval.RunMetric
// itself cannot compute (they need a live judge-model or Vertex AI client,
// which this package deliberately has no dependency on - see
// internal/judge and internal/vertexeval). RunMetric returns an error
// Result for any of these instead of silently scoring 0, if it's ever
// called directly for one of them without going through the CLI's/API's
// judgeMetrics/vertexEvalMetrics dispatch first (cmd/ego/main.go,
// internal/api/evaluate.go) - the only two call sites that route these
// names to their real implementation.
var NotYetImplemented = map[string]bool{
	"final_response_match_v2":                true, // internal/judge (LLM-as-judge)
	"hallucinations_v1":                      true, // internal/judge (LLM-as-judge)
	"rubric_based_final_response_quality_v1": true, // internal/judge (LLM-as-judge)
	"rubric_based_tool_use_quality_v1":       true, // internal/judge (LLM-as-judge)
	"safety_v1":                              true, // internal/vertexeval (Vertex :evaluateInstances)
	"multi_turn_task_success_v1":             true, // internal/vertexeval (Vertex :evaluateInstances)
	"multi_turn_trajectory_quality_v1":       true, // internal/vertexeval (Vertex :evaluateInstances)
	"multi_turn_tool_use_quality_v1":         true, // internal/vertexeval (Vertex :evaluateInstances)
}

var unsupportedMetrics = map[string]bool{
	"per_turn_user_simulator_quality_v1": true, // needs a ConversationScenario the data model does not have
	"response_evaluation_score":          true, // Vertex maps this to a GCS-hosted prompt + client-side result-parsing function, not a predefined metric spec
}

// RunMetric evaluates a single built-in metric by name, dispatching to the Go-native implementation if one
// exists..
func RunMetric(metric string, actual, expected []adk.Invocation, matchType MatchType, threshold float64) Result {
	switch metric {
	case "tool_trajectory_avg_score":
		return ToolTrajectoryAvgScore(actual, expected, matchType, threshold)
	case "response_match_score":
		return ResponseMatchScore(actual, expected, threshold)
	default:
		if unsupportedMetrics[metric] {
			return errorResult(metric, "metric %q is not supported yet (see docs/STATUS.md)", metric)
		}
		if NotYetImplemented[metric] {
			return errorResult(metric, "metric %q is not computed by this package directly (see README.md) - it needs the CLI's/API's judge-model or Vertex-eval dispatch, or wasn't reached through it", metric)
		}
		return errorResult(metric, "unknown metric %q", metric)
	}
}

// RunMetrics evaluates every named metric against one trace's actual
// invocations, matching expected invocations from set once per trace.
func RunMetrics(actual []adk.Invocation, set *adk.EvalSet, metrics []string, matchType MatchType, thresholds map[string]float64) []Result {
	expected := FindExpectedInvocations(actual, set)

	results := make([]Result, 0, len(metrics))
	for _, m := range metrics {
		threshold := 0.5
		if t, ok := thresholds[m]; ok {
			threshold = t
		}
		results = append(results, RunMetric(m, actual, expected, matchType, threshold))
	}
	return results
}
