package api

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/triageagent-dev/ego/internal/adk"
	"github.com/triageagent-dev/ego/internal/decision"
	"github.com/triageagent-dev/ego/internal/mcptools"
)

// Limits that keep the evidence of one metric small enough to read in a tool result: an agent that wants the
// whole judge output still has get_run_results.
const (
	maxEvidenceItems  = 60
	maxEvidenceText   = 400
	maxEvidenceQuote  = 300
	hallucinationName = "hallucinations_v1"
	trajectoryName    = "tool_trajectory_avg_score"
)

// failingSentenceLabels are the hallucination labels that count against a reply.
var failingSentenceLabels = map[string]bool{"unsupported": true, "contradictory": true, "disputed": true}

// evidenceDetails is the union of what the metrics store in a result row's details (see internal/judge,
// internal/eval and internal/decision); a metric fills only its own part.
type evidenceDetails struct {
	PerInvocation []struct {
		InvocationID string `json:"invocation_id"`
		Sentences    []struct {
			Sentence             string `json:"sentence"`
			Label                string `json:"label"`
			Rationale            string `json:"rationale"`
			SupportingExcerpt    string `json:"supporting_excerpt"`
			ContradictingExcerpt string `json:"contradicting_excerpt"`
		} `json:"sentences"`
	} `json:"per_invocation"`
	RubricInvocations []struct {
		InvocationID string `json:"invocation_id"`
		Rubrics      []struct {
			ID        string  `json:"id"`
			Rubric    string  `json:"rubric"`
			Verdict   *string `json:"verdict"`
			Rationale string  `json:"rationale"`
		} `json:"rubrics"`
	} `json:"rubric_invocations"`
	Comparisons []struct {
		InvocationID string             `json:"invocation_id"`
		Matched      bool               `json:"matched"`
		Expected     []adk.FunctionCall `json:"expected"`
		Actual       []adk.FunctionCall `json:"actual"`
	} `json:"comparisons"`
	Question string `json:"question"`
	Expect   any    `json:"expect"`
	Turns    []struct {
		InvocationID  string           `json:"invocation_id"`
		UserInput     string           `json:"user_input"`
		FinalResponse string           `json:"final_response"`
		Score         *float64         `json:"score"`
		Answer        *decision.Answer `json:"answer"`
	} `json:"turns"`
}

// ExtractEvidence turns one stored result row of metric into compact judge evidence: the score and status, the
// rationale of the first failing item, and the verdicts that explain the score, failing ones first. It is the one
// place that reads a judge's stored details, so explain_session, explain_regression and the findings pass see the
// same evidence.
func ExtractEvidence(row runResultRowDTO, metric string) mcptools.Evidence {
	ev := mcptools.Evidence{Metric: metric, Score: row.Score, Status: row.Status}
	if row.ErrorText != nil {
		ev.Error = *row.ErrorText
	}
	var d evidenceDetails
	if raw, err := json.Marshal(row.Details); err == nil {
		// A details blob that does not fit its metric's shape gives no evidence, not an error.
		_ = json.Unmarshal(raw, &d)
	}

	switch {
	case metric == hallucinationName:
		var all []mcptools.SentenceVerdict
		for _, inv := range d.PerInvocation {
			for _, s := range inv.Sentences {
				all = append(all, mcptools.SentenceVerdict{
					InvocationID:         inv.InvocationID,
					Sentence:             clip(s.Sentence, maxEvidenceText),
					Label:                s.Label,
					Rationale:            clip(s.Rationale, maxEvidenceText),
					SupportingExcerpt:    clip(s.SupportingExcerpt, maxEvidenceQuote),
					ContradictingExcerpt: clip(s.ContradictingExcerpt, maxEvidenceQuote),
				})
			}
		}
		failing := func(s mcptools.SentenceVerdict) bool { return failingSentenceLabels[s.Label] }
		ev.Sentences, ev.Failing, ev.Total, ev.Truncated = failingFirst(all, failing)
		for _, s := range ev.Sentences {
			if failing(s) {
				ev.Rationale = s.Rationale
				break
			}
		}

	case strings.HasPrefix(metric, "rubric_based_"):
		var all []mcptools.RubricVerdict
		for _, inv := range d.RubricInvocations {
			for _, r := range inv.Rubrics {
				v := ""
				if r.Verdict != nil {
					v = *r.Verdict
				}
				all = append(all, mcptools.RubricVerdict{
					InvocationID: inv.InvocationID, ID: r.ID, Rubric: clip(r.Rubric, maxEvidenceText),
					Verdict: v, Rationale: clip(r.Rationale, maxEvidenceText),
				})
			}
		}
		failing := func(r mcptools.RubricVerdict) bool { return r.Verdict == "no" }
		ev.Rubrics, ev.Failing, ev.Total, ev.Truncated = failingFirst(all, failing)
		for _, r := range ev.Rubrics {
			if failing(r) {
				ev.Rationale = r.Rationale
				break
			}
		}

	case metric == trajectoryName:
		var all []mcptools.TrajectoryComparison
		for _, c := range d.Comparisons {
			all = append(all, mcptools.TrajectoryComparison{
				InvocationID: c.InvocationID, Matched: c.Matched, Expected: toolCalls(c.Expected), Actual: toolCalls(c.Actual),
			})
		}
		ev.Comparisons, ev.Failing, ev.Total, ev.Truncated = failingFirst(all, func(c mcptools.TrajectoryComparison) bool { return !c.Matched })
		for _, c := range ev.Comparisons {
			if !c.Matched {
				ev.Rationale = fmt.Sprintf("expected %s, got %s", callNames(c.Expected), callNames(c.Actual))
				break
			}
		}

	case decision.IsMetric(metric) || strings.HasPrefix(metric, "jev:"):
		var all []mcptools.DecisionTurn
		for _, t := range d.Turns {
			all = append(all, mcptools.DecisionTurn{
				InvocationID: t.InvocationID, Question: clip(d.Question, maxEvidenceText), Expect: d.Expect,
				UserInput: clip(t.UserInput, maxEvidenceQuote), FinalResponse: clip(t.FinalResponse, maxEvidenceQuote),
				Score: t.Score, Answer: answerText(t.Answer),
			})
		}
		// The worst decision first, so the lowest score leads and its answer is the rationale.
		sort.SliceStable(all, func(i, j int) bool { return lowerScore(all[i].Score, all[j].Score) })
		ev.Decisions, ev.Failing, ev.Total, ev.Truncated = failingFirst(all, func(t mcptools.DecisionTurn) bool { return t.Score != nil && *t.Score < 1 })
		if len(ev.Decisions) > 0 {
			ev.Rationale = ev.Decisions[0].Answer
		}
	}
	return ev
}

// failingFirst puts the failing items first (each group keeps its order), keeps at most maxEvidenceItems and
// reports how many fail, how many there were and how many were left out.
func failingFirst[T any](items []T, failing func(T) bool) (out []T, nFailing, total, truncated int) {
	total = len(items)
	var rest []T
	for _, it := range items {
		if failing(it) {
			out = append(out, it)
			nFailing++
		} else {
			rest = append(rest, it)
		}
	}
	out = append(out, rest...)
	if len(out) > maxEvidenceItems {
		truncated = len(out) - maxEvidenceItems
		out = out[:maxEvidenceItems]
	}
	return out, nFailing, total, truncated
}

// lowerScore orders scores ascending with unscored last.
func lowerScore(a, b *float64) bool {
	switch {
	case a == nil:
		return false
	case b == nil:
		return true
	}
	return *a < *b
}

func toolCalls(calls []adk.FunctionCall) []mcptools.ToolCall {
	out := make([]mcptools.ToolCall, 0, len(calls))
	for _, c := range calls {
		args := c.Args
		if args == nil {
			args = map[string]any{}
		}
		out = append(out, mcptools.ToolCall{Tool: c.Name, Args: args})
	}
	return out
}

func callNames(calls []mcptools.ToolCall) string {
	names := make([]string, len(calls))
	for i, c := range calls {
		names[i] = c.Tool
	}
	return "[" + strings.Join(names, ", ") + "]"
}

// answerText states a Jev answer in one line.
func answerText(a *decision.Answer) string {
	switch {
	case a == nil:
		return ""
	case a.Noul != nil:
		return fmt.Sprintf("noul %.2f", *a.Noul)
	case a.Choice != "":
		return fmt.Sprintf("choice %q (confidence %.2f)", a.Choice, a.Confidence)
	}
	return a.Type
}

// clip cuts s to at most n runes.
func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
