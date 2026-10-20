package judge

import (
	"context"
	"testing"

	"github.com/triageagent-dev/ego/internal/adk"
	"github.com/triageagent-dev/ego/internal/eval"
)

func TestParseCritique(t *testing.T) {
	tests := []struct {
		name     string
		response string
		want     label
	}{
		{"valid", `{"reasoning": "looks right", "is_the_agent_response_valid": "valid"}`, labelValid},
		{"true spelling", `{"is_the_agent_response_valid": "true"}`, labelValid},
		{"invalid", `{"is_the_agent_response_valid": "invalid"}`, labelInvalid},
		{"almost counts as invalid", `{"is_the_agent_response_valid": "almost"}`, labelInvalid},
		{"partially_valid counts as invalid", `{"is_the_agent_response_valid": "partially_valid"}`, labelInvalid},
		{"unrecognized value", `{"is_the_agent_response_valid": "maybe"}`, labelNotFound},
		{"no label field at all", `not json at all`, labelNotFound},
		{"bracketed value", "{\"is_the_agent_response_valid\": [\"valid\"]}", labelValid},
		{"alternate invalid-named field, true means invalid", `{"is_the_agent_response_invalid": "true"}`, labelInvalid},
		{"alternate invalid-named field, false means valid", `{"is_the_agent_response_invalid": "false"}`, labelValid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseCritique(tt.response); got != tt.want {
				t.Errorf("parseCritique(%q) = %v, want %v", tt.response, got, tt.want)
			}
		})
	}
}

func TestMajorityVote(t *testing.T) {
	v1, v0 := 1.0, 0.0
	tests := []struct {
		name    string
		samples []*float64
		want    *float64
	}{
		{"majority valid", []*float64{&v1, &v1, &v0}, &v1},
		{"majority invalid", []*float64{&v0, &v0, &v1}, &v0},
		{"tie goes to invalid", []*float64{&v1, &v0}, &v0},
		{"all not-evaluated", []*float64{nil, nil}, nil},
		{"not-evaluated samples ignored, majority still decides", []*float64{&v1, &v1, nil}, &v1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := majorityVote(tt.samples)
			if (got == nil) != (tt.want == nil) {
				t.Fatalf("majorityVote() = %v, want %v", got, tt.want)
			}
			if got != nil && *got != *tt.want {
				t.Errorf("majorityVote() = %v, want %v", *got, *tt.want)
			}
		})
	}
}

// scriptedModel returns responses in order, one per Generate call,
// regardless of prompt content - enough to drive FinalResponseMatchV2's
// per-invocation/per-sample loop deterministically.
type scriptedModel struct {
	responses []string
	calls     int
}

func (m *scriptedModel) Generate(ctx context.Context, prompt string) (string, error) {
	r := m.responses[m.calls%len(m.responses)]
	m.calls++
	return r, nil
}

func invocation(userText, finalText string) adk.Invocation {
	return adk.Invocation{
		UserContent:   adk.TextOnly("user", userText),
		FinalResponse: adk.TextOnly("model", finalText),
	}
}

func TestFinalResponseMatchV2_AllValid(t *testing.T) {
	model := &scriptedModel{responses: []string{`{"is_the_agent_response_valid": "valid"}`}}
	actual := []adk.Invocation{invocation("what's the weather", "it's sunny")}
	expected := []adk.Invocation{invocation("what's the weather", "sunny today")}

	result, err := FinalResponseMatchV2(context.Background(), model, actual, expected, 3, 0.5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Score != 1.0 {
		t.Errorf("score = %v, want 1.0", result.Score)
	}
	if result.Status != eval.StatusPassed {
		t.Errorf("status = %v, want PASSED", result.Status)
	}
	if model.calls != 3 {
		t.Errorf("judge model called %d times, want 3 (numSamples)", model.calls)
	}
}

func TestFinalResponseMatchV2_AllInvalid(t *testing.T) {
	model := &scriptedModel{responses: []string{`{"is_the_agent_response_valid": "invalid"}`}}
	actual := []adk.Invocation{invocation("q", "wrong answer")}
	expected := []adk.Invocation{invocation("q", "right answer")}

	result, err := FinalResponseMatchV2(context.Background(), model, actual, expected, 3, 0.5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Score != 0.0 {
		t.Errorf("score = %v, want 0.0", result.Score)
	}
	if result.Status != eval.StatusFailed {
		t.Errorf("status = %v, want FAILED", result.Status)
	}
}

func TestFinalResponseMatchV2_NoExpectedInvocations(t *testing.T) {
	model := &scriptedModel{responses: []string{`{"is_the_agent_response_valid": "valid"}`}}
	actual := []adk.Invocation{invocation("q", "a")}

	result, err := FinalResponseMatchV2(context.Background(), model, actual, nil, 1, 0.5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error == "" {
		t.Error("expected an error result when expected invocations are nil")
	}
}

func TestFinalResponseMatchV2_AllSamplesUnparseable(t *testing.T) {
	model := &scriptedModel{responses: []string{"the judge model rambled without a verdict"}}
	actual := []adk.Invocation{invocation("q", "a")}
	expected := []adk.Invocation{invocation("q", "a")}

	result, err := FinalResponseMatchV2(context.Background(), model, actual, expected, 2, 0.5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != eval.StatusNotEvaluated {
		t.Errorf("status = %v, want NOT_EVALUATED", result.Status)
	}
}
