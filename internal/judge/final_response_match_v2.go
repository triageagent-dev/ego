package judge

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/triageagent-dev/ego/internal/adk"
	"github.com/triageagent-dev/ego/internal/eval"
)

// Changing a word here changes what the judge model is asked, so this is copied verbatim rather than
// paraphrased.
const finalResponseMatchV2Prompt = `You are an expert rater for an AI agent. The AI agent is going to call an API to answer the user query and generate API tool use code based for the choice of the API and API arguments. The ideal model response should be a function call that fulfills user query, or a natural language response hedges or asks users for further clarification if a function call does not apply.
The primary focus of this rating task is to check correctness of the model responses.

The data consists of:
- A user query.
- A model generated response for the prompt. The responses can consist of:
  - Natural language, when the model is asking for clarification, or tells the user it does not possess the requested functionality / option.
  - Code, in the form of one or multiple function calls, and additional code as needed, for when the model is fulfilling the user request.
You can use the help from a reference response annotated by a human rater. This reference response is of high quality. You can compare the agent's response with the reference response and decide if the agent's response is valid.
Note sometimes the reference response only contains the key entities of the correct answer and you need to be flexible to allow the agent response to contain more information than the reference response, or to present the key entities in a different format or structure or in shorter or longer format.
When the agent response is provided in the form of tables/dataframes or should be best provided in the form of tables/dataframes: focus on the key entities and main components requested in the user query and check whether you can retrieve those from the agent response. Likewise, if you have the reference response, then find out the key entities and main components in them and check whether you can retrieve those from the agent response. If the prompt does not specify any format instructions and the main items/components are included in the response then tolerate the differences in the formatting of those tables/dataframes.

You should follow the constitutions below very carefully to rate the model response:
- Allow flexibility of format even when reference code only uses one of the possible format, unless API spec or user prompt has explicit format requirement
  - e.g. For state name, allow both abbreviation and full name unless API spec has explicit requirement. e.g. both 'tx' and 'Texas' should be allowed in the agent response even when reference code only uses one of them.
  - e.g. If a reference response list outputs in a list format, the agent response is allowed to use sentence format and vice versa unless user prompt explicitly asks for a specific format.
  - e.g. For numbers, allow flexibility of formatting, e.g. 1000000 vs 1,000,000.
- The model shouldn't assume that it doesn't have access to according data or incapable of answering the question if reference response is able to find a legit answer.
- If the model response contains the correct final answer, rate it as valid even when the model response contains more information than the reference response.
- If the user prompt has csv or other table format data, don't read it yourself. Trust the reference response final answer instead.
- When the validation needs maths, date calculations, do not use your own calculator. Trust the reference response final answer instead.
- Be mindful about unit of numbers. For example, if the reference response says 100 miles, but the model response says 100 km, it is invalid.
- When the agent response or the reference response is provided in the form of tables/dataframes: focus on the key entities and main components requested in the user query and check whether you can retrieve those from the agent response and whether those match the reference response. If the user query does not specify any format instructions and the main items/components are included in the response then tolerate the differences in the formatting of those tables/dataframes.
- When the answer is in numeric format, check whether there are any format requirements in the numeric format, rounding, precision, number of decimals, etc. specified in the user query and the prompt. If there are no such instructions, then tolerate different numerical formats.
- When the answer is in numeric format and there are rounding or precision differences between the agent response and the reference response, if no further instructions are provided evaluate if the rounding strategy or precision in the agent response follows the standards for that entity. For instance, model accuracy scores must be reported with at least two decimal places (e.g., 0.798 → 0.80 is acceptable,  but 0.7 is not).

Below are the inputs:
{
  "User prompt": %s,
  "Agent response": %s,
  "Reference response": %s,
}

The answer should be a json alone which follows the json structure below:
{
  "reasoning": [reasoning],
  "is_the_agent_response_valid": [valid or invalid],
}
Answer with assertiveness:
`

// formatFinalResponseMatchV2Prompt fills the prompt template.
func formatFinalResponseMatchV2Prompt(actual, expected adk.Invocation) string {
	userPrompt := expected.UserContent.Text()
	response := actual.FinalResponse.Text()
	reference := expected.FinalResponse.Text()
	return fmt.Sprintf(finalResponseMatchV2Prompt, userPrompt, response, reference)
}

// label.
type label int

const (
	labelNotFound label = iota
	labelValid
	labelInvalid
)

var (
	validFieldRe   = regexp.MustCompile(`"is_the_agent_response_valid":\s*\[*[\n\s]*"*([^"^\]\s]*)"*[\n\s]*\]*\s*[,\n}]`)
	invalidFieldRe = regexp.MustCompile(`"is_the_agent_response_invalid":\s*\[*[\n\s]*"*([^"^\]\s]*)"*[\n\s]*\]*\s*[,\n}]`)
)

// parseCritique extracts the valid/invalid label from a judge model's raw text response.
func parseCritique(response string) label {
	if m := validFieldRe.FindStringSubmatch(response); m != nil {
		l := strings.TrimRight(strings.TrimSpace(m[1]), ",}")
		switch l {
		case "invalid", "almost", "false", "partially_valid", "partially valid", "partially":
			return labelInvalid
		case "valid", "true":
			return labelValid
		default:
			return labelNotFound
		}
	}
	if m := invalidFieldRe.FindStringSubmatch(response); m != nil {
		l := strings.TrimRight(strings.TrimSpace(m[1]), ",}")
		if l == "true" || l == "invalid" {
			return labelInvalid
		}
		return labelValid
	}
	return labelNotFound
}

// sampleScore is nil for NOT_EVALUATED (judge response didn't parse to a label), else 1.0 (valid) or 0.0
// (invalid).
func sampleScore(judgeResponse string) *float64 {
	switch parseCritique(judgeResponse) {
	case labelValid:
		v := 1.0
		return &v
	case labelInvalid:
		v := 0.0
		return &v
	default:
		return nil
	}
}

// majorityVote aggregates numSamples 0.0/1.0/NOT_EVALUATED scores for one invocation into a single score, ties
// going to invalid.
func majorityVote(samples []*float64) *float64 {
	var positive, negative int
	for _, s := range samples {
		if s == nil {
			continue
		}
		if *s == 1.0 {
			positive++
		} else if *s == 0.0 {
			negative++
		}
	}
	if positive == 0 && negative == 0 {
		return nil // NOT_EVALUATED
	}
	v := 0.0
	if positive > negative {
		v = 1.0
	}
	return &v
}

// FinalResponseMatchV2 scores each actual invocation's final response against the expected one by sampling the
// judge model numSamples times per invocation, taking a majority vote per invocation, then averaging the
// fraction of valid invocations across the trace.
func FinalResponseMatchV2(ctx context.Context, model Model, actual, expected []adk.Invocation, numSamples int, threshold float64) (eval.Result, error) {
	const name = "final_response_match_v2"
	if expected == nil {
		return eval.Result{MetricName: name, Error: fmt.Sprintf(
			"Metric '%s' requires expected invocations (golden eval set), but none were provided or matched.", name)}, nil
	}
	if len(actual) != len(expected) {
		return eval.Result{MetricName: name, Error: fmt.Sprintf(
			"actual and expected invocation lists differ in length (%d vs %d)", len(actual), len(expected))}, nil
	}
	if numSamples <= 0 {
		numSamples = DefaultNumSamples
	}

	perInvocation := make([]*float64, len(actual))
	for i := range actual {
		prompt := formatFinalResponseMatchV2Prompt(actual[i], expected[i])

		samples := make([]*float64, 0, numSamples)
		for s := 0; s < numSamples; s++ {
			resp, err := model.Generate(ctx, prompt)
			if err != nil {
				return eval.Result{}, fmt.Errorf("invocation %d sample %d: %w", i, s, err)
			}
			samples = append(samples, sampleScore(resp))
		}
		perInvocation[i] = majorityVote(samples)
	}

	var numValid, numEvaluated float64
	scores := make([]float64, len(perInvocation))
	for i, s := range perInvocation {
		if s == nil {
			continue
		}
		scores[i] = *s
		numEvaluated++
		numValid += *s
	}

	if numEvaluated == 0 {
		return eval.Result{MetricName: name, Status: eval.StatusNotEvaluated}, nil
	}

	overall := numValid / numEvaluated
	status := eval.StatusFailed
	if overall >= threshold {
		status = eval.StatusPassed
	}
	return eval.Result{
		MetricName:          name,
		Score:               overall,
		Status:              status,
		PerInvocationScores: scores,
	}, nil
}
