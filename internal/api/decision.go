package api

import (
	"context"
	"fmt"

	"github.com/triageagent-dev/ego/internal/adk"
	"github.com/triageagent-dev/ego/internal/decision"
	"github.com/triageagent-dev/ego/internal/eval"
)

// newDecisionClientGetter returns a memoized decision.Client factory, so a
// request's jev_* evaluators share one client (and its key check).
func newDecisionClientGetter() func() (*decision.Client, error) {
	var client *decision.Client
	var err error
	var built bool
	return func() (*decision.Client, error) {
		if !built {
			client, err = decision.NewClient("", "")
			built = true
		}
		return client, err
	}
}

// runDecisionEvaluators runs every jev_* evaluator of one trace in a single decision.Evaluate pass (one
// Decisions API call per invocation for all of them) and returns their results keyed by evaluator index; the
// jev_custom evaluator yields one result per question in its "questions" field.
func runDecisionEvaluators(ctx context.Context, evaluators []evaluatorConfigDTO, actual, expected []adk.Invocation, getClient func() (*decision.Client, error)) (byEvaluator map[int][]eval.Result, model string) {
	byEvaluator = map[int][]eval.Result{}
	var reqs []decision.Request
	var owner []int // owner[k] is the evaluator index of reqs[k]
	for i, ev := range evaluators {
		if (ev.Type != "" && ev.Type != "builtin") || !decision.IsMetric(ev.Name) {
			continue
		}
		threshold := defaultThreshold
		if ev.Threshold != nil {
			threshold = *ev.Threshold
		}
		var metrics []decision.Metric
		if ev.Name == decision.CustomMetricName {
			if len(ev.Questions) == 0 {
				byEvaluator[i] = []eval.Result{{MetricName: ev.Name, Error: fmt.Sprintf("metric %q requires a \"questions\" object", ev.Name)}}
				continue
			}
			parsed, err := decision.ParseCustom(ev.Questions)
			if err != nil {
				byEvaluator[i] = []eval.Result{{MetricName: ev.Name, Error: err.Error()}}
				continue
			}
			metrics = parsed
		} else if m, ok := decision.Builtin(ev.Name); ok {
			metrics = []decision.Metric{m}
		} else {
			byEvaluator[i] = []eval.Result{{MetricName: ev.Name, Error: fmt.Sprintf("unknown jev metric %q", ev.Name)}}
			continue
		}
		for _, m := range metrics {
			reqs = append(reqs, decision.Request{Metric: m, Threshold: threshold})
			owner = append(owner, i)
		}
	}
	if len(reqs) == 0 {
		return byEvaluator, ""
	}

	fail := func(msg string) {
		for k, r := range reqs {
			byEvaluator[owner[k]] = append(byEvaluator[owner[k]], eval.Result{MetricName: r.Metric.Name, Error: msg})
		}
	}
	client, err := getClient()
	if err != nil {
		fail(err.Error())
		return byEvaluator, ""
	}
	results, err := decision.Evaluate(ctx, client, actual, expected, reqs)
	if err != nil {
		fail(err.Error())
		return byEvaluator, client.Model()
	}
	for k, r := range results {
		byEvaluator[owner[k]] = append(byEvaluator[owner[k]], r)
	}
	return byEvaluator, client.Model()
}
