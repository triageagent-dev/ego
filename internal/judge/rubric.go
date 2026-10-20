package judge

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/triageagent-dev/ego/internal/adk"
	"github.com/triageagent-dev/ego/internal/eval"
)

// Rubric is one named judging criterion supplied by the caller.
type Rubric struct {
	ID   string
	Text string
}

func RubricsFromStrings(texts []string) []Rubric {
	rubrics := make([]Rubric, len(texts))
	for i, t := range texts {
		rubrics[i] = Rubric{ID: fmt.Sprintf("rubric_%d", i), Text: t}
	}
	return rubrics
}

func formatRubricsText(rubrics []Rubric) string {
	lines := make([]string, len(rubrics))
	for i, r := range rubrics {
		lines[i] = fmt.Sprintf("*  [id: %s] %s", r.ID, r.Text)
	}
	return strings.Join(lines, "\n")
}

// rubricScore is one rubric's verdict, either for a single sample or for
// the majority-voted aggregate across samples. Score is nil when the
// judge's response for this rubric could not be parsed to yes/no
// (NOT_EVALUATED for this rubric).
type rubricScore struct {
	RubricID  string
	Score     *float64
	Rationale string
}

var (
	rubricIDPattern        = regexp.MustCompile(`(?m)^\s*ID: (.*)$`)
	rubricPropertyPattern  = regexp.MustCompile(`(?m)^\s*Property: (.*)$`)
	rubricRationalePattern = regexp.MustCompile(`Rationale: (.*)`)
	rubricVerdictPattern   = regexp.MustCompile(`Verdict: (.*)`)
	// rubricRationaleBlockPattern is the whole rationale up to its verdict: judges often write a line and then
	// a list.
	rubricRationaleBlockPattern = regexp.MustCompile(`(?s)Rationale: (.*?)\n[ \t]*Verdict: `)
)

// rubricLabelPattern finds a label line the judge decorated with markdown ("**Verdict:** yes", "- *Property*:
// ...", "### ID: rubric_0"), so the plain label patterns above still match it.
var rubricLabelPattern = regexp.MustCompile(`(?m)^[ \t>*_#-]*(ID|Property|Evidence|Rationale|Verdict)[*_]*[ \t]*:[*_]*[ \t]*`)

var rubricSmartChars = strings.NewReplacer(
	"\u2018", "'", "\u2019", "'",
	"\u201c", `"`, "\u201d", `"`,
	"\u2013", "-", "\u2014", "-",
)

const rubricDecorationChars = " *_`#>-\u2022\"'"

var rubricWhitespacePattern = regexp.MustCompile(`\s+`)

// Smart-quote/dash folding, whitespace collapsing, decoration stripping, and lowercasing are all still applied
// - this covers the markdown/quote-wrapping judge models routinely add when echoing rubric text back; only the
// rarer full-width/ligature-folding NFKC cases are not normalized.
func normalizeRubricText(text string) string {
	text = rubricSmartChars.Replace(text)
	text = rubricWhitespacePattern.ReplaceAllString(text, " ")
	text = strings.TrimSpace(text)
	text = strings.Trim(text, rubricDecorationChars)
	return strings.ToLower(text)
}

// rubricResponse is one parsed property block from the auto-rater's raw
// text response, before it has been matched back to a known Rubric.
type rubricResponse struct {
	RubricID     string // may be empty: the judge omitted (or mangled) the ID line.
	PropertyText string
	Rationale    string
	Score        *float64
}

// A partial parse (mismatched counts of Property/Rationale/Verdict blocks) returns nil rather than risk silently omitting a failed rubric and inflating the score.
func parseRubricResponses(response string) []rubricResponse {
	response = rubricLabelPattern.ReplaceAllString(response, "$1: ")
	propertyMatches := rubricPropertyPattern.FindAllStringSubmatchIndex(response, -1)
	idMatches := rubricIDPattern.FindAllStringSubmatchIndex(response, -1)
	rationaleMatches := rubricRationalePattern.FindAllStringSubmatch(response, -1)
	verdictMatches := rubricVerdictPattern.FindAllStringSubmatch(response, -1)

	scores := make([]*float64, 0, len(verdictMatches))
	for _, m := range verdictMatches {
		v := strings.ToLower(m[1])
		switch {
		case strings.Contains(v, "yes"):
			s := 1.0
			scores = append(scores, &s)
		case strings.Contains(v, "no"):
			s := 0.0
			scores = append(scores, &s)
		default:
			scores = append(scores, nil)
		}
	}

	if len(propertyMatches) != len(rationaleMatches) || len(propertyMatches) != len(scores) {
		return nil
	}

	rationales := make([]string, len(rationaleMatches))
	for i, m := range rationaleMatches {
		rationales[i] = strings.TrimSpace(m[1])
	}
	if blocks := rubricRationaleBlockPattern.FindAllStringSubmatch(response, -1); len(blocks) == len(rationales) {
		for i, m := range blocks {
			rationales[i] = strings.TrimSpace(m[1])
		}
	}

	responses := make([]rubricResponse, 0, len(propertyMatches))
	for i, pm := range propertyMatches {
		previousStart := -1
		if i > 0 {
			previousStart = propertyMatches[i-1][0]
		}
		propertyStart := pm[0]

		// Match each id to the property it immediately precedes (not by index), so an omitted id line can't
		// shift a later id onto an earlier property.
		rubricID := ""
		for _, im := range idMatches {
			idStart := im[0]
			if previousStart < idStart && idStart < propertyStart {
				rubricID = strings.TrimSpace(response[im[2]:im[3]])
			}
		}

		responses = append(responses, rubricResponse{
			RubricID:     rubricID,
			PropertyText: strings.TrimSpace(response[pm[2]:pm[3]]),
			Rationale:    rationales[i],
			Score:        scores[i],
		})
	}
	return responses
}

func matchRubricResponses(responses []rubricResponse, rubrics []Rubric) []rubricScore {
	byID := make(map[string]Rubric, len(rubrics))
	byNormalizedText := make(map[string]Rubric, len(rubrics))
	for _, r := range rubrics {
		byID[r.ID] = r
		byNormalizedText[normalizeRubricText(r.Text)] = r
	}

	scores := make([]rubricScore, 0, len(responses))
	for _, resp := range responses {
		rubric, ok := Rubric{}, false
		if resp.RubricID != "" {
			rubric, ok = byID[resp.RubricID]
		}
		if !ok {
			rubric, ok = byNormalizedText[normalizeRubricText(resp.PropertyText)]
		}
		if !ok {
			continue
		}
		scores = append(scores, rubricScore{RubricID: rubric.ID, Score: resp.Score, Rationale: resp.Rationale})
	}
	return scores
}

// majorityVoteRubricScores aggregates numSamples samples' rubric_scores into one score per rubric id.
func majorityVoteRubricScores(samples [][]rubricScore) []rubricScore {
	type buckets struct {
		noScore  []rubricScore
		positive []rubricScore
		negative []rubricScore
	}
	byID := make(map[string]*buckets)
	var order []string
	for _, sample := range samples {
		if len(sample) == 0 {
			continue
		}
		for _, rs := range sample {
			b, ok := byID[rs.RubricID]
			if !ok {
				b = &buckets{}
				byID[rs.RubricID] = b
				order = append(order, rs.RubricID)
			}
			switch {
			case rs.Score == nil:
				b.noScore = append(b.noScore, rs)
			case *rs.Score == 1.0:
				b.positive = append(b.positive, rs)
			default:
				b.negative = append(b.negative, rs)
			}
		}
	}

	aggregated := make([]rubricScore, 0, len(order))
	for _, id := range order {
		b := byID[id]
		switch {
		case len(b.positive) == 0 && len(b.negative) == 0:
			aggregated = append(aggregated, b.noScore[0])
		case len(b.positive) > len(b.negative):
			aggregated = append(aggregated, b.positive[0])
		default:
			aggregated = append(aggregated, b.negative[0])
		}
	}
	return aggregated
}

func averageRubricScore(scores []rubricScore) *float64 {
	var sum float64
	var n int
	for _, s := range scores {
		if s.Score != nil {
			sum += *s.Score
			n++
		}
	}
	if n == 0 {
		return nil
	}
	v := sum / float64(n)
	return &v
}

// runRubricMetric implements the shared body of both rubric_based_*_v1
// metrics: LlmAsJudge.evaluate_invocations (numSamples samples per
// invocation with the same prompt, majority-voted per rubric via
// majorityVoteRubricScores) followed by
// MeanInvocationResultsSummarizer.summarize (the overall score is the
// mean of every aggregated (invocation, rubric) score across the whole
// trace - NOT a mean of per-invocation means, since invocations may
// carry different numbers of applicable rubrics).
func runRubricMetric(ctx context.Context, model Model, name string, actual []adk.Invocation, rubrics []Rubric, numSamples int, threshold float64, formatPrompt func(adk.Invocation) string) (eval.Result, error) {
	if len(rubrics) == 0 {
		return eval.Result{}, fmt.Errorf("%s requires at least one rubric", name)
	}
	if numSamples <= 0 {
		numSamples = DefaultNumSamples
	}

	textByID := make(map[string]string, len(rubrics))
	for _, r := range rubrics {
		textByID[r.ID] = r.Text
	}
	perInvocation := make([]float64, len(actual))
	perInvocationDetail := make([]map[string]any, 0, len(actual))
	var allScores []rubricScore
	for i, inv := range actual {
		prompt := formatPrompt(inv)

		samples := make([][]rubricScore, 0, numSamples)
		for s := 0; s < numSamples; s++ {
			matched, err := generateRubricSample(ctx, model, prompt, rubrics)
			if err != nil {
				return eval.Result{}, fmt.Errorf("invocation %d sample %d: %w", i, s, err)
			}
			samples = append(samples, matched)
		}

		aggregated := majorityVoteRubricScores(samples)
		allScores = append(allScores, aggregated...)
		score := averageRubricScore(aggregated)
		if score != nil {
			perInvocation[i] = *score
		}
		verdicts := make([]map[string]any, 0, len(aggregated))
		for _, rs := range aggregated {
			verdicts = append(verdicts, map[string]any{
				"id": rs.RubricID, "rubric": textByID[rs.RubricID],
				"verdict": verdictText(rs.Score), "rationale": rs.Rationale,
			})
		}
		perInvocationDetail = append(perInvocationDetail, map[string]any{
			"invocation_id": inv.InvocationID, "score": score, "rubrics": verdicts,
		})
	}
	details := map[string]any{"rubric_invocations": perInvocationDetail}

	overall := averageRubricScore(allScores)
	if overall == nil {
		return eval.Result{MetricName: name, Status: eval.StatusNotEvaluated, Details: details}, nil
	}
	status := eval.StatusFailed
	if *overall >= threshold {
		status = eval.StatusPassed
	}
	return eval.Result{MetricName: name, Score: *overall, Status: status, PerInvocationScores: perInvocation, Details: details}, nil
}

// generateRubricSample asks the judge once and matches its verdicts to the
// rubrics. A reply with no readable verdict is asked once more: one bad
// reply would otherwise leave this sample unscored.
func generateRubricSample(ctx context.Context, model Model, prompt string, rubrics []Rubric) ([]rubricScore, error) {
	var matched []rubricScore
	for attempt := 0; attempt < 2; attempt++ {
		resp, err := model.Generate(ctx, prompt)
		if err != nil {
			return nil, err
		}
		matched = matchRubricResponses(parseRubricResponses(resp), rubrics)
		for _, m := range matched {
			if m.Score != nil {
				return matched, nil
			}
		}
	}
	return matched, nil
}

func verdictText(score *float64) any {
	switch {
	case score == nil:
		return nil
	case *score == 1.0:
		return "yes"
	default:
		return "no"
	}
}

type toolCallJSON struct {
	ID   string         `json:"id,omitempty"`
	Name string         `json:"name,omitempty"`
	Args map[string]any `json:"args,omitempty"`
}

type toolResponseJSON struct {
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name,omitempty"`
	Response map[string]any `json:"response,omitempty"`
}

type toolCallAndResponseJSON struct {
	Step         int          `json:"step"`
	ToolCall     toolCallJSON `json:"tool_call"`
	ToolResponse any          `json:"tool_response"`
}

type toolCallsAndResponsesJSON struct {
	ToolCallsAndResponse []toolCallAndResponseJSON `json:"tool_calls_and_response"`
}

func toolCallsAndResponsesText(data *adk.IntermediateData) string {
	if data == nil || len(data.ToolUses) == 0 {
		return "No intermediate steps were taken."
	}

	responsesByID := make(map[string]adk.FunctionResponse, len(data.ToolResponses))
	for _, r := range data.ToolResponses {
		responsesByID[r.ID] = r
	}

	entries := make([]toolCallAndResponseJSON, 0, len(data.ToolUses))
	for i, call := range data.ToolUses {
		entry := toolCallAndResponseJSON{
			Step:     i,
			ToolCall: toolCallJSON{ID: call.ID, Name: call.Name, Args: call.Args},
		}
		if resp, ok := responsesByID[call.ID]; ok {
			entry.ToolResponse = toolResponseJSON{ID: resp.ID, Name: resp.Name, Response: resp.Response}
		} else {
			entry.ToolResponse = "None"
		}
		entries = append(entries, entry)
	}

	b, err := json.MarshalIndent(toolCallsAndResponsesJSON{ToolCallsAndResponse: entries}, "", "  ")
	if err != nil {
		return "No intermediate steps were taken."
	}
	return string(b)
}
