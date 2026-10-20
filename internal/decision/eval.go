package decision

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/triageagent-dev/ego/internal/adk"
	"github.com/triageagent-dev/ego/internal/eval"
)

// Metric is one jev_* metric: a typed question asked about every
// invocation, and the answer that counts as a pass.
type Metric struct {
	// Name is the metric name: "jev_<key>" for built-ins, "jev:<key>" for
	// custom questions.
	Name string
	// Key names the question in the Decisions API request; the model
	// reads it, so it should describe the question (e.g. "task_resolved").
	Key         string
	Description string
	Question    Question
	// ExpectTrue is the passing answer of a "noul" question; ExpectChoice
	// the passing label of a "choice" question.
	ExpectTrue   bool
	ExpectChoice string
	// NeedsReference marks questions about the eval set's reference
	// response; they need a golden eval set.
	NeedsReference bool
}

// CustomMetricName is the evaluator name that runs a caller's own question
// file; each question in it becomes a "jev:<key>" result.
const CustomMetricName = "jev_custom"

// Builtins are the ready-made jev_* metrics.
var Builtins = []Metric{
	{
		Name:        "jev_task_resolved",
		Key:         "task_resolved",
		Description: "Jev: did the final response resolve the user's request?",
		Question: Question{
			Type: "noul",
			Instructions: "Did the agent's final response resolve the user's request in this turn, " +
				"fully and correctly as far as the state shows? Use the earlier turns for context.",
			Criteria: map[string]string{
				"true":  "The request is answered or the asked action is done",
				"false": "Not resolved: wrong, partial, deflected, refused without a reason, or failed",
			},
		},
		ExpectTrue: true,
	},
	{
		Name:        "jev_grounded",
		Key:         "grounded",
		Description: "Jev: are the response's factual claims backed by tool results, the user input or earlier turns?",
		Question: Question{
			Type: "noul",
			Instructions: "Are the factual claims in the agent's final response backed by the tool results, the user input " +
				"or the earlier turns in the state? Greetings, questions, offers of help and statements about the " +
				"assistant's own limits need no backing.",
			Criteria: map[string]string{
				"true":  "Every factual claim is backed by the state, or the response makes no factual claim",
				"false": "At least one factual claim has no backing in the state or contradicts it",
			},
		},
		ExpectTrue: true,
	},
	{
		Name:        "jev_tool_use_appropriate",
		Key:         "tool_use_appropriate",
		Description: "Jev: did the agent call the right tools with sensible arguments?",
		Question: Question{
			Type: "noul",
			Instructions: "Did the agent use tools appropriately for the user's request: the right tools, sensible " +
				"arguments, no needless or repeated calls? If no tool was needed and none was called, that is appropriate.",
			Criteria: map[string]string{
				"true":  "Tool use fits the request, or no tool was needed and none was called",
				"false": "A needed tool was not called, a wrong tool or wrong arguments were used, or calls were needless",
			},
		},
		ExpectTrue: true,
	},
	{
		Name:        "jev_matches_reference",
		Key:         "matches_reference",
		Description: "Jev: does the final response give the same key answer as the eval set's reference?",
		Question: Question{
			Type: "noul",
			Instructions: "Does the agent's final response give the same key answer as the reference response? " +
				"Allow different wording, format, order, extra detail and number formatting; units and key entities must match.",
			Criteria: map[string]string{
				"true":  "Same key answer and entities as the reference",
				"false": "Different, missing or contradicting key answer, or wrong units",
			},
		},
		ExpectTrue:     true,
		NeedsReference: true,
	},
	{
		Name:        "jev_response_kind",
		Key:         "response_kind",
		Description: "Jev: is the final response an answer (rather than a clarification, refusal or error)?",
		Question: Question{
			Type:         "choice",
			Instructions: "What kind of reply is the agent's final response to the user's request?",
			Criteria: map[string]string{
				"answer":        "Answers the request or reports the action taken",
				"clarification": "Asks the user for missing information instead of answering",
				"refusal":       "Declines to help",
				"error":         "Reports a failure or that it could not complete the request",
			},
		},
		ExpectChoice: "answer",
	},
}

// IsMetric reports whether a metric name belongs to the jev_* family
// (built-ins, custom results, and the custom evaluator itself).
func IsMetric(name string) bool {
	return strings.HasPrefix(name, "jev_") || strings.HasPrefix(name, "jev:")
}

// Builtin returns the built-in metric with this name.
func Builtin(name string) (Metric, bool) {
	for _, m := range Builtins {
		if m.Name == name {
			return m, true
		}
	}
	return Metric{}, false
}

// customQuestion is one entry of a custom question file: a Question plus
// the passing answer (true/false for noul, a criteria label for choice).
type customQuestion struct {
	Question
	Expect         json.RawMessage `json:"expect"`
	NeedsReference bool            `json:"needs_reference,omitempty"`
}

// ParseCustom parses a custom question file: a JSON object mapping a
// question key to {type, instructions, criteria, expect, needs_reference}.
// Each question becomes a "jev:<key>" metric, in key order.
func ParseCustom(data []byte) ([]Metric, error) {
	var raw map[string]customQuestion
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing jev questions: %w", err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("jev questions: no questions defined")
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	metrics := make([]Metric, 0, len(keys))
	for _, k := range keys {
		q := raw[k]
		m := Metric{Name: "jev:" + k, Key: k, Question: q.Question, NeedsReference: q.NeedsReference}
		if q.Instructions == "" {
			return nil, fmt.Errorf("jev question %q: instructions are required", k)
		}
		switch q.Type {
		case "noul":
			if len(q.Expect) == 0 {
				m.ExpectTrue = true
			} else if err := json.Unmarshal(q.Expect, &m.ExpectTrue); err != nil {
				return nil, fmt.Errorf("jev question %q: noul expect must be true or false", k)
			}
			if q.Criteria == nil {
				m.Question.Criteria = map[string]string{"true": "Yes", "false": "No"}
			}
		case "choice":
			if err := json.Unmarshal(q.Expect, &m.ExpectChoice); err != nil || m.ExpectChoice == "" {
				return nil, fmt.Errorf("jev question %q: choice expect must name one of the criteria", k)
			}
			if _, ok := q.Criteria[m.ExpectChoice]; !ok {
				return nil, fmt.Errorf("jev question %q: expect %q is not one of the criteria", k, m.ExpectChoice)
			}
		default:
			return nil, fmt.Errorf("jev question %q: type must be noul or choice, got %q", k, q.Type)
		}
		metrics = append(metrics, m)
	}
	return metrics, nil
}

// Request is one metric to evaluate with its pass threshold.
type Request struct {
	Metric    Metric
	Threshold float64
}

// historyTurns caps how many earlier turns go into each invocation's state.
const historyTurns = 5

// State builds the structured state Jev reads for invocation i: the user
// input, final response, tool calls with their results, up to
// historyTurns earlier turns, and the reference response when reference
// is set.
func State(actual []adk.Invocation, i int, reference *adk.Invocation) map[string]any {
	inv := actual[i]
	state := map[string]any{
		"user_input":     inv.UserText(),
		"final_response": inv.FinalResponse.Text(),
		"tool_calls":     toolCalls(inv.IntermediateData),
	}
	if i > 0 {
		var turns []map[string]string
		for j := max(0, i-historyTurns); j < i; j++ {
			turns = append(turns, map[string]string{
				"user":  actual[j].UserText(),
				"agent": actual[j].FinalResponse.Text(),
			})
		}
		state["earlier_turns"] = turns
	}
	if reference != nil {
		state["reference_response"] = reference.FinalResponse.Text()
	}
	return state
}

// toolCalls pairs each tool call with its response (by ID, else by name in
// order) so Jev sees what every call returned.
func toolCalls(data *adk.IntermediateData) []map[string]any {
	if data == nil {
		return []map[string]any{}
	}
	used := make([]bool, len(data.ToolResponses))
	out := make([]map[string]any, 0, len(data.ToolUses))
	for _, call := range data.ToolUses {
		entry := map[string]any{"name": call.Name, "args": call.Args}
		for k, resp := range data.ToolResponses {
			if used[k] {
				continue
			}
			if (call.ID != "" && resp.ID == call.ID) || (call.ID == "" && resp.Name == call.Name) {
				entry["result"] = resp.Response
				used[k] = true
				break
			}
		}
		out = append(out, entry)
	}
	return out
}

// score converts an answer to the probability of the passing answer, or
// nil when the answer is missing or of the wrong type.
func score(m Metric, a Answer, ok bool) *float64 {
	if !ok {
		return nil
	}
	var v float64
	switch m.Question.Type {
	case "noul":
		if a.Noul == nil {
			return nil
		}
		v = *a.Noul
		if !m.ExpectTrue {
			v = 1 - v
		}
	case "choice":
		if p, has := a.Probabilities[m.ExpectChoice]; has {
			v = p
		} else if a.Choice == "" {
			return nil
		} else if a.Choice == m.ExpectChoice {
			v = 1
		}
	default:
		return nil
	}
	return &v
}

// Evaluate asks every requested question about every invocation, one
// Decisions API call per invocation, and returns one result per request in
// order. A metric's score is the mean, over the invocations Jev answered,
// of the probability it gave the passing answer; it passes at or above its
// threshold. Questions that need a reference are skipped (with an error
// result) when expected is nil.
func Evaluate(ctx context.Context, d Decider, actual, expected []adk.Invocation, reqs []Request) ([]eval.Result, error) {
	results := make([]eval.Result, len(reqs))
	var active []int
	for i, r := range reqs {
		switch {
		case r.Metric.NeedsReference && expected == nil:
			results[i] = eval.Result{MetricName: r.Metric.Name, Error: fmt.Sprintf(
				"Metric '%s' requires expected invocations (golden eval set), but none were provided or matched.", r.Metric.Name)}
		case r.Metric.NeedsReference && len(expected) != len(actual):
			results[i] = eval.Result{MetricName: r.Metric.Name, Error: fmt.Sprintf(
				"actual and expected invocation lists differ in length (%d vs %d)", len(actual), len(expected))}
		default:
			active = append(active, i)
		}
	}
	if len(active) == 0 {
		return results, nil
	}

	useRef := false
	for _, i := range active {
		useRef = useRef || reqs[i].Metric.NeedsReference
	}

	perInvocation := make([][]*float64, len(reqs))
	details := make([][]map[string]any, len(reqs))
	for inv := range actual {
		var ref *adk.Invocation
		if useRef {
			ref = &expected[inv]
		}
		questions := make(map[string]Question, len(active))
		for _, i := range active {
			questions[reqs[i].Metric.Key] = reqs[i].Metric.Question
		}
		answers, err := d.Decide(ctx, State(actual, inv, ref), questions)
		if err != nil {
			return nil, fmt.Errorf("invocation %d: %w", inv, err)
		}
		for _, i := range active {
			a, ok := answers[reqs[i].Metric.Key]
			s := score(reqs[i].Metric, a, ok)
			perInvocation[i] = append(perInvocation[i], s)
			detail := map[string]any{
				"invocation_id":  actual[inv].InvocationID,
				"user_input":     actual[inv].UserText(),
				"final_response": actual[inv].FinalResponse.Text(),
				"score":          nil,
			}
			if s != nil {
				detail["score"] = *s
			}
			if ok {
				detail["answer"] = a
			}
			details[i] = append(details[i], detail)
		}
	}

	for _, i := range active {
		m := reqs[i].Metric
		scores := make([]float64, len(actual))
		var sum float64
		var n int
		for inv, s := range perInvocation[i] {
			if s != nil {
				scores[inv] = *s
				sum += *s
				n++
			}
		}
		if n == 0 {
			results[i] = eval.Result{MetricName: m.Name, Status: eval.StatusNotEvaluated}
			continue
		}
		overall := sum / float64(n)
		status := eval.StatusFailed
		if overall >= reqs[i].Threshold {
			status = eval.StatusPassed
		}
		results[i] = eval.Result{
			MetricName:          m.Name,
			Score:               overall,
			Status:              status,
			PerInvocationScores: scores,
			Details:             resultDetails(m, details[i]),
		}
	}
	return results, nil
}

// Requests turns jev_* metric names into requests, for callers that take
// metric names plus an optional custom question file (the CLI and MCP).
// CustomMetricName expands to every question in custom; a non-empty
// custom adds them even when CustomMetricName is not named.
func Requests(names []string, custom []byte, threshold float64) ([]Request, error) {
	var reqs []Request
	wantCustom := len(custom) > 0
	for _, name := range names {
		if name == CustomMetricName {
			if len(custom) == 0 {
				return nil, fmt.Errorf("metric %q needs a question file (--jev-questions)", name)
			}
			continue
		}
		m, ok := Builtin(name)
		if !ok {
			return nil, fmt.Errorf("unknown jev metric %q", name)
		}
		reqs = append(reqs, Request{Metric: m, Threshold: threshold})
	}
	if wantCustom {
		metrics, err := ParseCustom(custom)
		if err != nil {
			return nil, err
		}
		for _, m := range metrics {
			reqs = append(reqs, Request{Metric: m, Threshold: threshold})
		}
	}
	return reqs, nil
}

// resultDetails is a jev_* result's Details: the question, its type and passing
// answer, and per turn the user input, final response, score and Jev's
// raw answer (ui MetricsComparisonSection renders these as the "why").
func resultDetails(m Metric, turns []map[string]any) map[string]any {
	var expect any = m.ExpectTrue
	if m.Question.Type == "choice" {
		expect = m.ExpectChoice
	}
	return map[string]any{
		"question":      m.Question.Instructions,
		"question_type": m.Question.Type,
		"expect":        expect,
		"turns":         turns,
	}
}
