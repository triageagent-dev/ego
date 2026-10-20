package eval

import (
	"fmt"
	"reflect"

	"github.com/triageagent-dev/ego/internal/adk"
)

// MatchType selects how actual tool calls are compared against expected
// ones. Mirrors google.adk.evaluation.eval_metrics.ToolTrajectoryCriterion.MatchType.
type MatchType string

const (
	MatchExact    MatchType = "EXACT"
	MatchInOrder  MatchType = "IN_ORDER"
	MatchAnyOrder MatchType = "ANY_ORDER"
)

// ToolTrajectoryAvgScore compares each actual invocation's tool calls against the corresponding expected
// invocation's, using matchType, and averages the per-invocation 1.0/0.0 scores.
func ToolTrajectoryAvgScore(actual, expected []adk.Invocation, matchType MatchType, threshold float64) Result {
	const name = "tool_trajectory_avg_score"
	if expected == nil {
		return errorResult(name, "Metric '%s' requires expected invocations (golden eval set), but none were provided or matched.", name)
	}
	if len(actual) != len(expected) {
		return errorResult(name, "actual and expected invocation lists differ in length (%d vs %d)", len(actual), len(expected))
	}
	if matchType == "" {
		matchType = MatchExact
	}

	var total float64
	scores := make([]float64, len(actual))
	comparisons := make([]map[string]any, len(actual))

	for i := range actual {
		actualCalls := actual[i].ToolCalls()
		expectedCalls := expected[i].ToolCalls()

		var matched bool
		switch matchType {
		case MatchExact:
			matched = exactMatch(actualCalls, expectedCalls)
		case MatchInOrder:
			matched = inOrderMatch(actualCalls, expectedCalls)
		case MatchAnyOrder:
			matched = anyOrderMatch(actualCalls, expectedCalls)
		default:
			return errorResult(name, "unsupported match type %q", matchType)
		}

		score := 0.0
		if matched {
			score = 1.0
		}
		scores[i] = score
		total += score
		// append onto non-nil empty slices (rather than the possibly-nil
		// actualCalls/expectedCalls directly) so a nil ToolCalls() result
		// still marshals as JSON [] not null - the UI reads
		// comparison.expected/.actual.length without a null guard.
		comparisons[i] = map[string]any{
			"invocation_id": actual[i].InvocationID,
			"expected":      append([]adk.FunctionCall{}, expectedCalls...),
			"actual":        append([]adk.FunctionCall{}, actualCalls...),
			"matched":       matched,
		}
	}

	overall := total / float64(len(actual))
	return Result{
		MetricName:          name,
		Score:               overall,
		Status:              statusFor(overall, threshold),
		PerInvocationScores: scores,
		Details:             map[string]any{"comparisons": comparisons},
	}
}

func argsEqual(a, b map[string]any) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

func callsEqual(a, b adk.FunctionCall) bool {
	return a.Name == b.Name && argsEqual(a.Args, b.Args)
}

// exactMatch requires the same tool calls in the same order, no extras or omissions.
func exactMatch(actual, expected []adk.FunctionCall) bool {
	if len(actual) != len(expected) {
		return false
	}
	for i := range actual {
		if !callsEqual(actual[i], expected[i]) {
			return false
		}
	}
	return true
}

// inOrderMatch requires every expected call to appear in actual, in order, with other calls allowed in between.
func inOrderMatch(actual, expected []adk.FunctionCall) bool {
	if len(expected) == 0 {
		return true
	}
	if len(actual) == 0 {
		return false
	}
	idx := 0
	for _, a := range actual {
		if callsEqual(a, expected[idx]) {
			idx++
			if idx == len(expected) {
				return true
			}
		}
	}
	return false
}

// anyOrderMatch requires every expected call to appear somewhere in actual, each actual call consumed at most
// once.
func anyOrderMatch(actual, expected []adk.FunctionCall) bool {
	if len(expected) == 0 {
		return true
	}
	if len(actual) == 0 {
		return false
	}
	remaining := append([]adk.FunctionCall(nil), actual...)
	for _, exp := range expected {
		found := -1
		for i, a := range remaining {
			if callsEqual(a, exp) {
				found = i
				break
			}
		}
		if found == -1 {
			return false
		}
		remaining = append(remaining[:found], remaining[found+1:]...)
	}
	return true
}

// ParseMatchType maps a --trajectory-match-type flag value to a MatchType,
// defaulting to EXACT on an empty string.
func ParseMatchType(s string) (MatchType, error) {
	switch MatchType(s) {
	case "", MatchExact:
		return MatchExact, nil
	case MatchInOrder:
		return MatchInOrder, nil
	case MatchAnyOrder:
		return MatchAnyOrder, nil
	default:
		return "", fmt.Errorf("unsupported trajectory match type %q (want EXACT, IN_ORDER, or ANY_ORDER)", s)
	}
}
