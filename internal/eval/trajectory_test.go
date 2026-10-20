package eval

import (
	"testing"

	"github.com/triageagent-dev/ego/internal/adk"
)

func inv(calls ...adk.FunctionCall) adk.Invocation {
	return adk.Invocation{IntermediateData: &adk.IntermediateData{ToolUses: calls}}
}

func call(name string, args map[string]any) adk.FunctionCall {
	return adk.FunctionCall{Name: name, Args: args}
}

func TestToolTrajectoryAvgScore_Exact(t *testing.T) {
	a := call("a", map[string]any{"x": 1.0})
	b := call("b", nil)

	tests := []struct {
		name     string
		actual   []adk.FunctionCall
		expected []adk.FunctionCall
		want     float64
	}{
		{"identical", []adk.FunctionCall{a, b}, []adk.FunctionCall{a, b}, 1.0},
		{"extra actual call fails exact", []adk.FunctionCall{a, b}, []adk.FunctionCall{a}, 0.0},
		{"wrong order fails exact", []adk.FunctionCall{b, a}, []adk.FunctionCall{a, b}, 0.0},
		{"both empty", nil, nil, 1.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ToolTrajectoryAvgScore([]adk.Invocation{inv(tt.actual...)}, []adk.Invocation{inv(tt.expected...)}, MatchExact, 0.5)
			if result.Error != "" {
				t.Fatalf("unexpected error: %s", result.Error)
			}
			if result.Score != tt.want {
				t.Errorf("score = %v, want %v", result.Score, tt.want)
			}
		})
	}
}

func TestToolTrajectoryAvgScore_InOrder(t *testing.T) {
	a := call("a", nil)
	b := call("b", nil)
	c := call("c", nil)

	// extra call "c" interleaved is fine for IN_ORDER, not for EXACT.
	actual := []adk.FunctionCall{a, c, b}
	expected := []adk.FunctionCall{a, b}

	got := ToolTrajectoryAvgScore([]adk.Invocation{inv(actual...)}, []adk.Invocation{inv(expected...)}, MatchInOrder, 0.5)
	if got.Score != 1.0 {
		t.Errorf("IN_ORDER score = %v, want 1.0", got.Score)
	}

	gotExact := ToolTrajectoryAvgScore([]adk.Invocation{inv(actual...)}, []adk.Invocation{inv(expected...)}, MatchExact, 0.5)
	if gotExact.Score != 0.0 {
		t.Errorf("EXACT score = %v, want 0.0", gotExact.Score)
	}
}

func TestToolTrajectoryAvgScore_AnyOrder(t *testing.T) {
	a := call("a", nil)
	b := call("b", nil)

	actual := []adk.FunctionCall{b, a}
	expected := []adk.FunctionCall{a, b}

	got := ToolTrajectoryAvgScore([]adk.Invocation{inv(actual...)}, []adk.Invocation{inv(expected...)}, MatchAnyOrder, 0.5)
	if got.Score != 1.0 {
		t.Errorf("ANY_ORDER score = %v, want 1.0", got.Score)
	}

	gotInOrder := ToolTrajectoryAvgScore([]adk.Invocation{inv(actual...)}, []adk.Invocation{inv(expected...)}, MatchInOrder, 0.5)
	if gotInOrder.Score != 0.0 {
		t.Errorf("IN_ORDER score = %v, want 0.0", gotInOrder.Score)
	}
}

func TestToolTrajectoryAvgScore_NoExpected(t *testing.T) {
	got := ToolTrajectoryAvgScore([]adk.Invocation{inv()}, nil, MatchExact, 0.5)
	if got.Error == "" {
		t.Fatal("expected an error when expected invocations are nil")
	}
}

// TestToolTrajectoryAvgScore_ComparisonsNeverNilSlices guards against a
// regression that crashed the UI: adk.Invocation.ToolCalls() returns nil
// (not []) when an invocation has no tool calls, and a Go nil slice
// marshals to JSON null, not []. ui/src/components/inspector's
// TrajectoryComparisonDetails.tsx/MetricsComparisonSection.tsx read
// comparison.expected/.actual.length directly - if either landed in
// Details as a JSON null, that threw "Cannot read properties of null
// (reading 'length')". Details["comparisons"][i]["expected"/"actual"]
// must always be a concrete, non-nil []adk.FunctionCall, even when the
// invocation itself had zero tool calls.
func TestToolTrajectoryAvgScore_ComparisonsNeverNilSlices(t *testing.T) {
	// Neither invocation has any tool calls, so ToolCalls() returns nil
	// for both.
	got := ToolTrajectoryAvgScore([]adk.Invocation{inv()}, []adk.Invocation{inv()}, MatchExact, 0.5)
	if got.Error != "" {
		t.Fatalf("unexpected error: %s", got.Error)
	}
	comparisons, ok := got.Details["comparisons"].([]map[string]any)
	if !ok || len(comparisons) != 1 {
		t.Fatalf("Details[\"comparisons\"] = %#v, want a 1-element []map[string]any", got.Details["comparisons"])
	}
	expected, ok := comparisons[0]["expected"].([]adk.FunctionCall)
	if !ok {
		t.Fatalf("comparisons[0][\"expected\"] = %#v (%T), want a non-nil []adk.FunctionCall", comparisons[0]["expected"], comparisons[0]["expected"])
	}
	if expected == nil {
		t.Error("comparisons[0][\"expected\"] is nil; it must be a concrete empty slice so it marshals as JSON [] not null")
	}
	actual, ok := comparisons[0]["actual"].([]adk.FunctionCall)
	if !ok {
		t.Fatalf("comparisons[0][\"actual\"] = %#v (%T), want a non-nil []adk.FunctionCall", comparisons[0]["actual"], comparisons[0]["actual"])
	}
	if actual == nil {
		t.Error("comparisons[0][\"actual\"] is nil; it must be a concrete empty slice so it marshals as JSON [] not null")
	}
}

func TestParseMatchType(t *testing.T) {
	for _, tt := range []struct {
		in      string
		want    MatchType
		wantErr bool
	}{
		{"", MatchExact, false},
		{"EXACT", MatchExact, false},
		{"IN_ORDER", MatchInOrder, false},
		{"ANY_ORDER", MatchAnyOrder, false},
		{"bogus", "", true},
	} {
		got, err := ParseMatchType(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseMatchType(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
		}
		if got != tt.want {
			t.Errorf("ParseMatchType(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
