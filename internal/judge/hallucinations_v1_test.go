package judge

import (
	"context"
	"strings"
	"testing"

	"github.com/triageagent-dev/ego/internal/adk"
	"github.com/triageagent-dev/ego/internal/eval"
)

func TestBuildHallucinationContext(t *testing.T) {
	inv := invocation("what's the weather in SF?", "it's sunny")
	got := buildHallucinationContext(inv)
	want := "Developer instructions:\n\n\nUser prompt:\nwhat's the weather in SF?\n\nTool definitions:\nAgent has no tools.\n"
	if got != want {
		t.Errorf("buildHallucinationContext() =\n%q\nwant\n%q", got, want)
	}
}

func TestParseHallucinationSentences(t *testing.T) {
	resp := "<sentence>Apples are red.</sentence>\n<sentence>Bananas are green.</sentence>\n<sentence>Pears are purple.</sentence>"
	got := parseHallucinationSentences(resp)
	want := []string{"Apples are red.", "Bananas are green.", "Pears are purple."}
	if len(got) != len(want) {
		t.Fatalf("got %d sentences, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sentence %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParseHallucinationSentences_NoMatches(t *testing.T) {
	if got := parseHallucinationSentences("no sentence tags here"); len(got) != 0 {
		t.Errorf("got %+v, want empty", got)
	}
}

func TestParseHallucinationValidationResults(t *testing.T) {
	resp := `sentence: Apples are red.
label: supported
rationale: The context explicitly states that apples are red.
supporting_excerpt: Apples are red fruits.
contradicting_excerpt: null

sentence: Bananas are green.
label: contradictory
rationale: The context states that bananas are yellow, not green.
supporting_excerpt: null
contradicting_excerpt: Bananas are yellow fruits.

sentence: Enjoy your fruit!
label: not_applicable
rationale: This is a general expression and does not require factual attribution.
supporting_excerpt: null
contradicting_excerpt: null`

	got := parseHallucinationValidationResults(resp)
	wantLabels := []string{"supported", "contradictory", "not_applicable"}
	if len(got) != len(wantLabels) {
		t.Fatalf("got %d results, want %d: %+v", len(got), len(wantLabels), got)
	}
	for i, want := range wantLabels {
		if got[i].Label != want {
			t.Errorf("result %d label = %q, want %q", i, got[i].Label, want)
		}
	}
}

func TestParseHallucinationValidationResults_CaseInsensitiveLabel(t *testing.T) {
	resp := "sentence: X.\nlabel: UNSUPPORTED\nrationale: r\nsupporting_excerpt: null\ncontradicting_excerpt: null"
	got := parseHallucinationValidationResults(resp)
	if len(got) != 1 || got[0].Label != "unsupported" {
		t.Fatalf("got %+v, want a single lowercased 'unsupported' label", got)
	}
}

// hallucinationScriptedModel returns one response per Generate call from a
// fixed list, keyed by call order - enough to drive the
// segmenter-then-validator two-stage pipeline deterministically (call 0 is
// always the segmenter prompt, call 1 the validator prompt, per invocation).
type hallucinationScriptedModel struct {
	responses []string
	calls     int
}

func (m *hallucinationScriptedModel) Generate(ctx context.Context, prompt string) (string, error) {
	r := m.responses[m.calls]
	m.calls++
	return r, nil
}

func TestHallucinationsV1_AllSupported(t *testing.T) {
	model := &hallucinationScriptedModel{responses: []string{
		"<sentence>It's sunny.</sentence>",
		"sentence: It's sunny.\nlabel: supported\nrationale: r\nsupporting_excerpt: e\ncontradicting_excerpt: null",
	}}
	actual := []adk.Invocation{invocation("what's the weather", "it's sunny")}

	result, err := HallucinationsV1(context.Background(), model, actual, 0.5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Score != 1.0 {
		t.Errorf("Score = %v, want 1.0", result.Score)
	}
	if result.Status != eval.StatusPassed {
		t.Errorf("Status = %v, want PASSED", result.Status)
	}

	// Details["per_invocation"] must match ui/src/lib/types.ts's
	// PersistedHallucinationInvocation/HallucinationSentence shape
	// exactly (snake_case sentence fields) so the same rendering code
	// works for both the live /api/evaluate response and the persisted
	// Runs details blob.
	perInv, ok := result.Details["per_invocation"].([]map[string]any)
	if !ok || len(perInv) != 1 {
		t.Fatalf("Details[\"per_invocation\"] = %#v, want a 1-element []map[string]any", result.Details["per_invocation"])
	}
	sentences, ok := perInv[0]["sentences"].([]map[string]any)
	if !ok || len(sentences) != 1 {
		t.Fatalf("per_invocation[0][\"sentences\"] = %#v, want a 1-element []map[string]any", perInv[0]["sentences"])
	}
	s := sentences[0]
	if s["sentence"] != "It's sunny." || s["label"] != "supported" || s["rationale"] != "r" || s["supporting_excerpt"] != "e" || s["contradicting_excerpt"] != "" {
		t.Errorf("sentence detail = %#v, unexpected field values", s)
	}
	if perInv[0]["score"] != 1.0 {
		t.Errorf("per_invocation[0][\"score\"] = %v, want 1.0", perInv[0]["score"])
	}
}

func TestHallucinationsV1_Contradictory(t *testing.T) {
	model := &hallucinationScriptedModel{responses: []string{
		"<sentence>It's raining.</sentence>",
		"sentence: It's raining.\nlabel: contradictory\nrationale: r\nsupporting_excerpt: null\ncontradicting_excerpt: e",
	}}
	actual := []adk.Invocation{invocation("what's the weather", "it's raining")}

	result, err := HallucinationsV1(context.Background(), model, actual, 0.5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Score != 0.0 {
		t.Errorf("Score = %v, want 0.0", result.Score)
	}
	if result.Status != eval.StatusFailed {
		t.Errorf("Status = %v, want FAILED", result.Status)
	}
}

func TestHallucinationsV1_EmptyFinalResponseSkipped(t *testing.T) {
	model := &hallucinationScriptedModel{responses: nil}
	actual := []adk.Invocation{invocation("q", "")}

	result, err := HallucinationsV1(context.Background(), model, actual, 0.5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != eval.StatusNotEvaluated {
		t.Errorf("Status = %v, want NOT_EVALUATED (no scoreable invocations)", result.Status)
	}
	if model.calls != 0 {
		t.Errorf("judge model called %d times, want 0 (empty response should short-circuit)", model.calls)
	}
}

func TestHallucinationsV1_SegmenterFindsNoSentences(t *testing.T) {
	model := &hallucinationScriptedModel{responses: []string{"no sentence tags in this reply"}}
	actual := []adk.Invocation{invocation("q", "a")}

	result, err := HallucinationsV1(context.Background(), model, actual, 0.5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != eval.StatusNotEvaluated {
		t.Errorf("Status = %v, want NOT_EVALUATED", result.Status)
	}
	// Only the segmenter should have been called; no sentences means no
	// validator call follows.
	if model.calls != 1 {
		t.Errorf("judge model called %d times, want 1 (segmenter only)", model.calls)
	}
}

func TestHallucinationPromptTemplates_HaveExactlyOneSubstitutionSite(t *testing.T) {
	// A single %s is expected in the segmenter prompt (the response
	// text); two in the validator prompt (context, then sentences).
	if n := strings.Count(hallucinationsSegmenterPrompt, "%s"); n != 1 {
		t.Errorf("segmenter prompt has %d %%s placeholders, want 1", n)
	}
	if n := strings.Count(hallucinationsValidatorPrompt, "%s"); n != 2 {
		t.Errorf("validator prompt has %d %%s placeholders, want 2", n)
	}
}
