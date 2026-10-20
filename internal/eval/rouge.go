package eval

import (
	"strings"
	"unicode"

	"github.com/triageagent-dev/ego/internal/adk"
)

// ResponseMatchScore scores each actual invocation's final response against the expected one using ROUGE-1
// F-measure, and averages the per-invocation scores.
func ResponseMatchScore(actual, expected []adk.Invocation, threshold float64) Result {
	const name = "response_match_score"
	if expected == nil {
		return errorResult(name, "Metric '%s' requires expected invocations (golden eval set), but none were provided or matched.", name)
	}
	if len(actual) != len(expected) {
		return errorResult(name, "actual and expected invocation lists differ in length (%d vs %d)", len(actual), len(expected))
	}

	var total float64
	scores := make([]float64, len(actual))
	for i := range actual {
		reference := expected[i].FinalResponse.Text()
		response := actual[i].FinalResponse.Text()
		score := rouge1FMeasure(response, reference)
		scores[i] = score
		total += score
	}

	overall := total / float64(len(actual))
	return Result{
		MetricName:          name,
		Score:               overall,
		Status:              statusFor(overall, threshold),
		PerInvocationScores: scores,
	}
}

// tokenize lowercases and splits on runs of non-letter/non-digit characters.
func tokenize(s string) []string {
	s = strings.ToLower(s)
	var tokens []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return tokens
}

func tokenCounts(tokens []string) map[string]int {
	counts := make(map[string]int, len(tokens))
	for _, t := range tokens {
		counts[t]++
	}
	return counts
}

// rouge1FMeasure computes the ROUGE-1 F-measure (harmonic mean of unigram
// precision and recall) between candidate and reference text.
func rouge1FMeasure(candidate, reference string) float64 {
	candTokens := tokenize(candidate)
	refTokens := tokenize(reference)
	if len(candTokens) == 0 || len(refTokens) == 0 {
		return 0
	}

	candCounts := tokenCounts(candTokens)
	refCounts := tokenCounts(refTokens)

	overlap := 0
	for tok, refCount := range refCounts {
		if candCount, ok := candCounts[tok]; ok {
			if candCount < refCount {
				overlap += candCount
			} else {
				overlap += refCount
			}
		}
	}

	precision := float64(overlap) / float64(len(candTokens))
	recall := float64(overlap) / float64(len(refTokens))
	if precision+recall == 0 {
		return 0
	}
	return 2 * precision * recall / (precision + recall)
}
