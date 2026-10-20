package judge

import (
	"context"
	"strings"
	"testing"

	"github.com/triageagent-dev/ego/internal/adk"
	"github.com/triageagent-dev/ego/internal/eval"
)

func TestRubricsFromStrings(t *testing.T) {
	rubrics := RubricsFromStrings([]string{"first rule", "second rule"})
	want := []Rubric{{ID: "rubric_0", Text: "first rule"}, {ID: "rubric_1", Text: "second rule"}}
	if len(rubrics) != len(want) {
		t.Fatalf("got %d rubrics, want %d", len(rubrics), len(want))
	}
	for i := range want {
		if rubrics[i] != want[i] {
			t.Errorf("rubric %d = %+v, want %+v", i, rubrics[i], want[i])
		}
	}
}

func TestNormalizeRubricText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain lowercase", "hello world", "hello world"},
		{"collapses whitespace", "hello   \n\t world", "hello world"},
		{"strips markdown decoration", "**hello world**", "hello world"},
		// Trim (.strip(chars)) only strips decoration chars from the two outer edges, not every occurrence, so
		// an interior quote/dash survives - this matches _normalize_text's real (edge-only) behavior, not a
		// bug.
		{"folds smart quotes", "\u2018hello\u2019 \u201cworld\u201d", "hello' \"world"},
		{"strips leading bullet", "- hello world", "hello world"},
		{"uppercase folds to lowercase", "HELLO WORLD", "hello world"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeRubricText(tt.in); got != tt.want {
				t.Errorf("normalizeRubricText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseRubricResponses(t *testing.T) {
	t.Run("well-formed single property", func(t *testing.T) {
		resp := "STEP 1: ...\nID: rubric_0\nProperty: The response is polite\nRationale: it says please\nVerdict: yes\n"
		got := parseRubricResponses(resp)
		if len(got) != 1 {
			t.Fatalf("got %d responses, want 1: %+v", len(got), got)
		}
		if got[0].RubricID != "rubric_0" {
			t.Errorf("RubricID = %q, want rubric_0", got[0].RubricID)
		}
		if got[0].PropertyText != "The response is polite" {
			t.Errorf("PropertyText = %q", got[0].PropertyText)
		}
		if got[0].Score == nil || *got[0].Score != 1.0 {
			t.Errorf("Score = %v, want 1.0", got[0].Score)
		}
	})

	t.Run("multiple properties, one omitted id line", func(t *testing.T) {
		resp := "" +
			"ID: rubric_0\nProperty: first property\nRationale: r1\nVerdict: yes\n\n" +
			"Property: second property\nRationale: r2\nVerdict: no\n"
		got := parseRubricResponses(resp)
		if len(got) != 2 {
			t.Fatalf("got %d responses, want 2: %+v", len(got), got)
		}
		if got[0].RubricID != "rubric_0" {
			t.Errorf("responses[0].RubricID = %q, want rubric_0", got[0].RubricID)
		}
		// The second property has no preceding ID line of its own (the
		// only ID line precedes the first property), so it must not
		// inherit rubric_0's id.
		if got[1].RubricID != "" {
			t.Errorf("responses[1].RubricID = %q, want empty (no id line before it)", got[1].RubricID)
		}
		if got[1].Score == nil || *got[1].Score != 0.0 {
			t.Errorf("responses[1].Score = %v, want 0.0", got[1].Score)
		}
	})

	t.Run("verdict neither yes nor no yields nil score", func(t *testing.T) {
		resp := "ID: rubric_0\nProperty: p\nRationale: r\nVerdict: unclear\n"
		got := parseRubricResponses(resp)
		if len(got) != 1 {
			t.Fatalf("got %d responses, want 1", len(got))
		}
		if got[0].Score != nil {
			t.Errorf("Score = %v, want nil", *got[0].Score)
		}
	})

	t.Run("mismatched block counts returns nil", func(t *testing.T) {
		resp := "ID: rubric_0\nProperty: p1\nRationale: r1\nVerdict: yes\n\nProperty: p2\nRationale: r2\n"
		got := parseRubricResponses(resp)
		if got != nil {
			t.Errorf("got %+v, want nil (mismatched Property/Verdict counts)", got)
		}
	})

	t.Run("unparseable text returns no responses", func(t *testing.T) {
		if got := parseRubricResponses("the model said nothing useful"); len(got) != 0 {
			t.Errorf("got %+v, want empty", got)
		}
	})
}

func TestMatchRubricResponses(t *testing.T) {
	rubrics := []Rubric{
		{ID: "rubric_0", Text: "The response is polite"},
		{ID: "rubric_1", Text: "The response is accurate"},
	}
	yes, no := 1.0, 0.0

	t.Run("matches by id", func(t *testing.T) {
		responses := []rubricResponse{{RubricID: "rubric_1", PropertyText: "garbled property text", Score: &yes}}
		got := matchRubricResponses(responses, rubrics)
		if len(got) != 1 || got[0].RubricID != "rubric_1" {
			t.Fatalf("got %+v, want match on rubric_1 by id", got)
		}
	})

	t.Run("falls back to normalized text when id is missing", func(t *testing.T) {
		responses := []rubricResponse{{RubricID: "", PropertyText: "**The response is accurate**", Score: &no}}
		got := matchRubricResponses(responses, rubrics)
		if len(got) != 1 || got[0].RubricID != "rubric_1" {
			t.Fatalf("got %+v, want text-normalized match on rubric_1", got)
		}
	})

	t.Run("drops responses matching neither id nor text", func(t *testing.T) {
		responses := []rubricResponse{{RubricID: "rubric_9", PropertyText: "totally unrelated", Score: &yes}}
		got := matchRubricResponses(responses, rubrics)
		if len(got) != 0 {
			t.Errorf("got %+v, want empty (no match)", got)
		}
	})
}

func TestMajorityVoteRubricScores(t *testing.T) {
	yes, no := 1.0, 0.0

	t.Run("majority positive wins", func(t *testing.T) {
		samples := [][]rubricScore{
			{{RubricID: "rubric_0", Score: &yes}},
			{{RubricID: "rubric_0", Score: &yes}},
			{{RubricID: "rubric_0", Score: &no}},
		}
		got := majorityVoteRubricScores(samples)
		if len(got) != 1 || got[0].Score == nil || *got[0].Score != 1.0 {
			t.Fatalf("got %+v, want rubric_0=1.0", got)
		}
	})

	t.Run("tie goes to negative", func(t *testing.T) {
		samples := [][]rubricScore{
			{{RubricID: "rubric_0", Score: &yes}},
			{{RubricID: "rubric_0", Score: &no}},
		}
		got := majorityVoteRubricScores(samples)
		if len(got) != 1 || got[0].Score == nil || *got[0].Score != 0.0 {
			t.Fatalf("got %+v, want rubric_0=0.0 (tie->negative)", got)
		}
	})

	t.Run("never-scored rubric keeps a no-score entry", func(t *testing.T) {
		samples := [][]rubricScore{
			{{RubricID: "rubric_0", Score: nil}},
			{{RubricID: "rubric_0", Score: nil}},
		}
		got := majorityVoteRubricScores(samples)
		if len(got) != 1 || got[0].Score != nil {
			t.Fatalf("got %+v, want a single nil-score rubric_0 entry", got)
		}
	})

	t.Run("empty samples (unparseable) are skipped entirely", func(t *testing.T) {
		samples := [][]rubricScore{
			nil,
			{{RubricID: "rubric_0", Score: &yes}},
		}
		got := majorityVoteRubricScores(samples)
		if len(got) != 1 || *got[0].Score != 1.0 {
			t.Fatalf("got %+v, want only rubric_0=1.0 from the non-empty sample", got)
		}
	})
}

func TestAverageRubricScore(t *testing.T) {
	yes, no := 1.0, 0.0

	t.Run("mean of non-nil scores", func(t *testing.T) {
		got := averageRubricScore([]rubricScore{{Score: &yes}, {Score: &no}, {Score: &yes}})
		if got == nil || *got != 2.0/3.0 {
			t.Fatalf("got %v, want 0.6667", got)
		}
	})

	t.Run("nil scores are excluded from the denominator", func(t *testing.T) {
		got := averageRubricScore([]rubricScore{{Score: &yes}, {Score: nil}})
		if got == nil || *got != 1.0 {
			t.Fatalf("got %v, want 1.0 (nil excluded, not counted as 0)", got)
		}
	})

	t.Run("all nil yields nil overall", func(t *testing.T) {
		if got := averageRubricScore([]rubricScore{{Score: nil}, {Score: nil}}); got != nil {
			t.Fatalf("got %v, want nil", *got)
		}
	})
}

func TestRunRubricMetric_RequiresAtLeastOneRubric(t *testing.T) {
	model := &scriptedModel{responses: []string{"ID: rubric_0\nProperty: p\nRationale: r\nVerdict: yes\n"}}
	actual := []adk.Invocation{invocation("q", "a")}
	_, err := runRubricMetric(context.Background(), model, "rubric_based_final_response_quality_v1", actual, nil, 1, 0.5, func(adk.Invocation) string { return "prompt" })
	if err == nil {
		t.Fatal("expected an error with zero rubrics")
	}
}

func TestRunRubricMetric_AggregatesAcrossInvocationsAndSamples(t *testing.T) {
	// Every sample scores the single rubric "yes" -> overall should be 1.0.
	model := &scriptedModel{responses: []string{"ID: rubric_0\nProperty: The response is helpful\nRationale: r\nVerdict: yes\n"}}
	rubrics := RubricsFromStrings([]string{"The response is helpful"})
	actual := []adk.Invocation{invocation("q1", "a1"), invocation("q2", "a2")}

	result, err := runRubricMetric(context.Background(), model, "rubric_based_final_response_quality_v1", actual, rubrics, 3, 0.5, func(adk.Invocation) string { return "prompt" })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Score != 1.0 {
		t.Errorf("Score = %v, want 1.0", result.Score)
	}
	if result.Status != eval.StatusPassed {
		t.Errorf("Status = %v, want PASSED", result.Status)
	}
	if len(result.PerInvocationScores) != 2 || result.PerInvocationScores[0] != 1.0 || result.PerInvocationScores[1] != 1.0 {
		t.Errorf("PerInvocationScores = %v, want [1.0, 1.0]", result.PerInvocationScores)
	}
	// numSamples(3) * numInvocations(2) = 6 judge-model calls.
	if model.calls != 6 {
		t.Errorf("judge model called %d times, want 6", model.calls)
	}
}

func TestRunRubricMetric_AllSamplesUnparseable(t *testing.T) {
	model := &scriptedModel{responses: []string{"the model rambled with no verdict"}}
	rubrics := RubricsFromStrings([]string{"anything"})
	actual := []adk.Invocation{invocation("q", "a")}

	result, err := runRubricMetric(context.Background(), model, "rubric_based_final_response_quality_v1", actual, rubrics, 2, 0.5, func(adk.Invocation) string { return "prompt" })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != eval.StatusNotEvaluated {
		t.Errorf("Status = %v, want NOT_EVALUATED", result.Status)
	}
}

func TestToolCallsAndResponsesText_NoToolUses(t *testing.T) {
	if got := toolCallsAndResponsesText(nil); got != "No intermediate steps were taken." {
		t.Errorf("got %q", got)
	}
	if got := toolCallsAndResponsesText(&adk.IntermediateData{}); got != "No intermediate steps were taken." {
		t.Errorf("got %q", got)
	}
}

func TestToolCallsAndResponsesText_MatchesResponseByID(t *testing.T) {
	data := &adk.IntermediateData{
		ToolUses: []adk.FunctionCall{{ID: "call-1", Name: "get_weather", Args: map[string]any{"city": "SF"}}},
		ToolResponses: []adk.FunctionResponse{
			{ID: "call-1", Name: "get_weather", Response: map[string]any{"temp": "60F"}},
		},
	}
	got := toolCallsAndResponsesText(data)
	if got == "No intermediate steps were taken." {
		t.Fatal("expected JSON tool-call text, got the empty-case fallback")
	}
	for _, want := range []string{"get_weather", "call-1", "60F", "SF"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected output to contain %q, got:\n%s", want, got)
		}
	}
}

// Gemini sometimes bolds the labels; the verdicts must still count.
func TestParseRubricResponses_MarkdownLabels(t *testing.T) {
	resp := "**ID:** rubric_0\n**Property:** The response is helpful\n**Evidence:** e\n**Rationale:** fine\n**Verdict:** yes\n\n" +
		"- *ID*: rubric_1\n- *Property*: It is short\n- *Rationale*: long\n- *Verdict*: **no**\n"
	got := matchRubricResponses(parseRubricResponses(resp), RubricsFromStrings([]string{"The response is helpful", "It is short"}))
	if len(got) != 2 || got[0].Score == nil || *got[0].Score != 1 || got[1].Score == nil || *got[1].Score != 0 || got[1].Rationale != "long" {
		t.Fatalf("got %+v", got)
	}
}

// One unreadable reply is asked again, and the verdicts with their
// rationale land in the details.
func TestRunRubricMetric_RetriesUnreadableReplyAndRecordsVerdicts(t *testing.T) {
	model := &scriptedModel{responses: []string{
		"the model rambled with no verdict",
		"ID: rubric_0\nProperty: a\nRationale: cites the frame\nVerdict: yes\nID: rubric_1\nProperty: b\nRationale: invents a limit\nVerdict: no\n",
	}}
	rubrics := RubricsFromStrings([]string{"a", "b"})
	result, err := runRubricMetric(context.Background(), model, "rubric_based_final_response_quality_v1",
		[]adk.Invocation{invocation("q", "a")}, rubrics, 1, 0.5, func(adk.Invocation) string { return "prompt" })
	if err != nil {
		t.Fatal(err)
	}
	if model.calls != 2 || result.Score != 0.5 || result.Status != eval.StatusPassed {
		t.Fatalf("calls=%d result=%+v", model.calls, result)
	}
	inv := result.Details["rubric_invocations"].([]map[string]any)
	verdicts := inv[0]["rubrics"].([]map[string]any)
	if len(verdicts) != 2 || verdicts[1]["verdict"] != "no" || verdicts[1]["rationale"] != "invents a limit" || verdicts[1]["rubric"] != "b" {
		t.Errorf("details = %+v", verdicts)
	}
}

// A rationale that goes on as a list keeps every line.
func TestParseRubricResponses_MultiLineRationale(t *testing.T) {
	resp := "ID: rubric_0\nProperty: a\nEvidence: e\nRationale: Several claims fail. Specifically:\n- the 160Mi limit\n- the shipping latency\nVerdict: no\n"
	got := parseRubricResponses(resp)
	if len(got) != 1 || got[0].Rationale != "Several claims fail. Specifically:\n- the 160Mi limit\n- the shipping latency" {
		t.Fatalf("got %+v", got)
	}
}
