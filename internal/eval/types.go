// Only the metrics computed locally today are implemented: tool_trajectory_avg_score and response_match_score.
// Everything else (LLM-as-judge metrics, the Vertex-backed GCP metrics) is listed in NotYetImplemented - see
// README.md.
package eval

import "fmt"

// Status mirrors google.adk.evaluation.evaluator.EvalStatus.
type Status string

const (
	StatusPassed       Status = "PASSED"
	StatusFailed       Status = "FAILED"
	StatusNotEvaluated Status = "NOT_EVALUATED"
)

func statusFor(score, threshold float64) Status {
	if score >= threshold {
		return StatusPassed
	}
	return StatusFailed
}

// Result is one metric's outcome for a trace.
type Result struct {
	MetricName          string
	Score               float64
	Status              Status
	PerInvocationScores []float64
	Error               string
	Details             map[string]any
}

func errorResult(metric, format string, args ...any) Result {
	return Result{MetricName: metric, Error: fmt.Sprintf(format, args...)}
}
