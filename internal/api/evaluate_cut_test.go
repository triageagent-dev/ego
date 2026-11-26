package api

import (
	"context"
	"errors"
	"testing"

	"github.com/triageagent-dev/ego/internal/adk"
	"github.com/triageagent-dev/ego/internal/decision"
	"github.com/triageagent-dev/ego/internal/eval"
	"github.com/triageagent-dev/ego/internal/judge"
	"github.com/triageagent-dev/ego/internal/vertexeval"
)

// Metrics that check an answer against its input are skipped for a turn whose input is cut, and no judge is asked.
func TestEvaluateInvocations_CutInputIsNotRated(t *testing.T) {
	invocations := []adk.Invocation{{InvocationID: "i", UserContent: adk.TextOnly("user", "q"), FinalResponse: adk.TextOnly("model", "a"), InputCut: true}}
	evaluators := []evaluatorConfigDTO{{Name: "hallucinations_v1"}, {Name: "rubric_based_final_response_quality_v1", Rubrics: []string{"cites a fact"}}, {Name: "jev_grounded"}}
	got := evaluateInvocations(context.Background(), traceResultDTO{}, invocations, evaluators, nil,
		func(string) (judge.Model, error) {
			t.Error("a judge was asked about a cut turn")
			return nil, errors.New("no")
		},
		func() (*vertexeval.Client, error) { return nil, errors.New("no") },
		func() (*decision.Client, error) {
			t.Error("the decision model was asked about a cut turn")
			return nil, errors.New("no")
		}, nil)
	if len(got.MetricResults) != len(evaluators) {
		t.Fatalf("results = %d, want %d: %+v", len(got.MetricResults), len(evaluators), got.MetricResults)
	}
	for _, r := range got.MetricResults {
		if r.EvalStatus != string(eval.StatusSkipped) || r.Score != nil || r.Error == nil || *r.Error != cutInputSkip {
			t.Errorf("%s: status %q score %v error %v, want skipped with the cut reason", r.MetricName, r.EvalStatus, r.Score, r.Error)
		}
	}
}
