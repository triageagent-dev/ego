package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/triageagent-dev/ego/internal/adk"
	"github.com/triageagent-dev/ego/internal/decision"
)

func TestRunDecisionEvaluators(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body struct {
			Questions map[string]decision.Question `json:"questions"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		answers := map[string]any{}
		for k := range body.Questions {
			answers[k] = map[string]any{"type": "noul", "noul": 0.75}
		}
		json.NewEncoder(w).Encode(map[string]any{"answers": answers})
	}))
	defer srv.Close()
	t.Setenv("JEV_API_URL", srv.URL)
	t.Setenv("JEV_API_KEY", "k")
	t.Setenv("JEV_MODEL", "")

	high := 0.9
	evaluators := []evaluatorConfigDTO{
		{Type: "builtin", Name: "tool_trajectory_avg_score"},
		{Type: "builtin", Name: "jev_task_resolved"},
		{Type: "builtin", Name: "jev_custom", Threshold: &high, Questions: json.RawMessage(
			`{"polite": {"type": "noul", "instructions": "Is it polite?"}, "short": {"type": "noul", "instructions": "Is it short?"}}`)},
		{Type: "builtin", Name: "jev_custom"}, // no questions: its own error
		{Type: "builtin", Name: "jev_matches_reference"},
	}
	actual := []adk.Invocation{
		{InvocationID: "a", UserContent: adk.TextOnly("user", "hi"), FinalResponse: adk.TextOnly("model", "hello")},
		{InvocationID: "b", UserContent: adk.TextOnly("user", "bye"), FinalResponse: adk.TextOnly("model", "bye")},
	}

	byEvaluator, model := runDecisionEvaluators(context.Background(), evaluators, actual, nil, newDecisionClientGetter())
	if model != decision.DefaultModel {
		t.Errorf("model = %q, want %q", model, decision.DefaultModel)
	}
	if calls != 2 {
		t.Errorf("Decisions API calls = %d, want 2 (one per invocation for all questions)", calls)
	}
	if _, ok := byEvaluator[0]; ok {
		t.Error("non-jev evaluator got a jev result")
	}
	if r := byEvaluator[1]; len(r) != 1 || r[0].MetricName != "jev_task_resolved" || r[0].Score != 0.75 || r[0].Status != "PASSED" {
		t.Errorf("jev_task_resolved = %+v", r)
	}
	if r := byEvaluator[2]; len(r) != 2 || r[0].MetricName != "jev:polite" || r[1].MetricName != "jev:short" || r[0].Status != "FAILED" {
		t.Errorf("jev_custom = %+v, want jev:polite and jev:short FAILED at threshold 0.9", r)
	}
	if r := byEvaluator[3]; len(r) != 1 || !strings.Contains(r[0].Error, "questions") {
		t.Errorf("jev_custom without questions = %+v", r)
	}
	if r := byEvaluator[4]; len(r) != 1 || !strings.Contains(r[0].Error, "golden eval set") {
		t.Errorf("jev_matches_reference without eval set = %+v", r)
	}
}

func TestRunDecisionEvaluatorsNoKey(t *testing.T) {
	t.Setenv("JEV_API_KEY", "")
	evaluators := []evaluatorConfigDTO{{Type: "builtin", Name: "jev_grounded"}}
	byEvaluator, _ := runDecisionEvaluators(context.Background(), evaluators, []adk.Invocation{{}}, nil, newDecisionClientGetter())
	if r := byEvaluator[0]; len(r) != 1 || !strings.Contains(r[0].Error, "JEV_API_KEY") {
		t.Errorf("result = %+v, want a missing-key error", r)
	}
}
