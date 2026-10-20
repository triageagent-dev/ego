package decision

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/triageagent-dev/ego/internal/adk"
	"github.com/triageagent-dev/ego/internal/eval"
)

// fakeDecider answers from a script keyed by question key and records
// every call.
type fakeDecider struct {
	nouls   map[string]float64
	choices map[string]Answer
	states  []map[string]any
	qs      []map[string]Question
}

func (f *fakeDecider) Decide(ctx context.Context, state map[string]any, questions map[string]Question) (map[string]Answer, error) {
	f.states = append(f.states, state)
	f.qs = append(f.qs, questions)
	out := map[string]Answer{}
	for k, q := range questions {
		if q.Type == "noul" {
			if p, ok := f.nouls[k]; ok {
				out[k] = Answer{Type: "noul", Noul: &p}
			}
		} else if a, ok := f.choices[k]; ok {
			out[k] = a
		}
	}
	return out, nil
}

func inv(id, user, final string) adk.Invocation {
	return adk.Invocation{InvocationID: id, UserContent: adk.TextOnly("user", user), FinalResponse: adk.TextOnly("model", final)}
}

func builtin(t *testing.T, name string) Metric {
	t.Helper()
	m, ok := Builtin(name)
	if !ok {
		t.Fatalf("no builtin %q", name)
	}
	return m
}

func TestEvaluateOneCallPerInvocation(t *testing.T) {
	f := &fakeDecider{
		nouls: map[string]float64{"task_resolved": 0.9, "grounded": 0.6},
		choices: map[string]Answer{"response_kind": {
			Type: "choice", Choice: "answer", Probabilities: map[string]float64{"answer": 0.8, "error": 0.2},
		}},
	}
	actual := []adk.Invocation{inv("a", "q1", "r1"), inv("b", "q2", "r2")}
	reqs := []Request{
		{Metric: builtin(t, "jev_task_resolved"), Threshold: 0.85},
		{Metric: builtin(t, "jev_grounded"), Threshold: 0.7},
		{Metric: builtin(t, "jev_response_kind"), Threshold: 0.5},
	}
	results, err := Evaluate(context.Background(), f, actual, nil, reqs)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.qs) != 2 || len(f.qs[0]) != 3 {
		t.Fatalf("calls = %d with %d questions, want 2 calls with all 3 questions each", len(f.qs), len(f.qs[0]))
	}
	want := []struct {
		score  float64
		status eval.Status
	}{{0.9, eval.StatusPassed}, {0.6, eval.StatusFailed}, {0.8, eval.StatusPassed}}
	for i, w := range want {
		r := results[i]
		if r.MetricName != reqs[i].Metric.Name || r.Score != w.score || r.Status != w.status {
			t.Errorf("result %d = %s %v %v, want %s %v %v", i, r.MetricName, r.Score, r.Status, reqs[i].Metric.Name, w.score, w.status)
		}
	}
	if f.states[1]["earlier_turns"] == nil {
		t.Errorf("second invocation's state has no earlier_turns: %v", f.states[1])
	}
	if _, ok := f.states[0]["earlier_turns"]; ok {
		t.Errorf("first invocation's state has earlier_turns: %v", f.states[0])
	}
}

func TestEvaluateReferenceAndMissingAnswers(t *testing.T) {
	f := &fakeDecider{nouls: map[string]float64{"matches_reference": 0.2}}
	actual := []adk.Invocation{inv("a", "q", "r")}
	reqs := []Request{
		{Metric: builtin(t, "jev_matches_reference"), Threshold: 0.5},
		{Metric: builtin(t, "jev_task_resolved"), Threshold: 0.5}, // unanswered
	}

	noRef, err := Evaluate(context.Background(), f, actual, nil, reqs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(noRef[0].Error, "golden eval set") {
		t.Errorf("without eval set: %+v, want a missing-eval-set error", noRef[0])
	}
	if noRef[1].Status != eval.StatusNotEvaluated {
		t.Errorf("unanswered metric status = %v, want NOT_EVALUATED", noRef[1].Status)
	}

	withRef, err := Evaluate(context.Background(), f, actual, []adk.Invocation{inv("e", "q", "gold")}, reqs[:1])
	if err != nil {
		t.Fatal(err)
	}
	if withRef[0].Score != 0.2 || withRef[0].Status != eval.StatusFailed {
		t.Errorf("with eval set: %+v, want score 0.2 FAILED", withRef[0])
	}
	if got := f.states[len(f.states)-1]["reference_response"]; got != "gold" {
		t.Errorf("reference_response = %v, want gold", got)
	}
}

func TestScoreExpectFalseAndChoiceFallback(t *testing.T) {
	p := 0.3
	if s := score(Metric{Question: Question{Type: "noul"}, ExpectTrue: false}, Answer{Noul: &p}, true); s == nil || *s != 0.7 {
		t.Errorf("noul expect false score = %v, want 0.7", s)
	}
	choice := Metric{Question: Question{Type: "choice"}, ExpectChoice: "ok"}
	if s := score(choice, Answer{Choice: "ok"}, true); s == nil || *s != 1 {
		t.Errorf("choice without probabilities, matching = %v, want 1", s)
	}
	if s := score(choice, Answer{Choice: "bad"}, true); s == nil || *s != 0 {
		t.Errorf("choice without probabilities, not matching = %v, want 0", s)
	}
	if s := score(choice, Answer{}, false); s != nil {
		t.Errorf("missing answer score = %v, want nil", *s)
	}
}

func TestStatePairsToolCallsWithResults(t *testing.T) {
	i := inv("a", "list pods", "2 pods")
	i.IntermediateData = &adk.IntermediateData{
		ToolUses:      []adk.FunctionCall{{ID: "1", Name: "kubectl", Args: map[string]any{"cmd": "get pods"}}, {Name: "count"}},
		ToolResponses: []adk.FunctionResponse{{Name: "count", Response: map[string]any{"n": 2}}, {ID: "1", Name: "kubectl", Response: map[string]any{"out": "a b"}}},
	}
	calls := State([]adk.Invocation{i}, 0, nil)["tool_calls"].([]map[string]any)
	if len(calls) != 2 || calls[0]["result"].(map[string]any)["out"] != "a b" || calls[1]["result"].(map[string]any)["n"] != 2 {
		t.Errorf("tool_calls = %v", calls)
	}
}

func TestParseCustom(t *testing.T) {
	metrics, err := ParseCustom([]byte(`{
		"escalates": {"type": "noul", "instructions": "Should this be escalated?", "expect": false},
		"tone": {"type": "choice", "instructions": "Tone?", "criteria": {"calm": "c", "rude": "r"}, "expect": "calm"}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics) != 2 || metrics[0].Name != "jev:escalates" || metrics[0].ExpectTrue || metrics[0].Question.Criteria == nil ||
		metrics[1].Name != "jev:tone" || metrics[1].ExpectChoice != "calm" {
		t.Errorf("metrics = %+v", metrics)
	}

	for name, bad := range map[string]string{
		"unknown type":     `{"x": {"type": "score", "instructions": "?"}}`,
		"no instructions":  `{"x": {"type": "noul"}}`,
		"choice no expect": `{"x": {"type": "choice", "instructions": "?", "criteria": {"a": "a"}}}`,
		"expect not label": `{"x": {"type": "choice", "instructions": "?", "criteria": {"a": "a"}, "expect": "b"}}`,
		"empty":            `{}`,
	} {
		if _, err := ParseCustom([]byte(bad)); err == nil {
			t.Errorf("%s: ParseCustom accepted %s", name, bad)
		}
	}
}

func TestRequests(t *testing.T) {
	custom := []byte(`{"x": {"type": "noul", "instructions": "?"}}`)
	reqs, err := Requests([]string{"jev_grounded"}, custom, 0.7)
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 2 || reqs[0].Metric.Name != "jev_grounded" || reqs[1].Metric.Name != "jev:x" || reqs[1].Threshold != 0.7 {
		t.Errorf("requests = %+v", reqs)
	}
	if _, err := Requests([]string{CustomMetricName}, nil, 0.5); err == nil {
		t.Error("jev_custom without a question file was accepted")
	}
	if _, err := Requests([]string{"jev_nope"}, nil, 0.5); err == nil {
		t.Error("unknown jev metric was accepted")
	}
}

func TestClientRequestRetryAndNamespace(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("Authorization = %q", got)
		}
		var body struct {
			Model string `json:"model"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.Model != apiNamespace+"jev-1.13" {
			t.Errorf("model = %q, want the namespaced API ID", body.Model)
		}
		w.Write([]byte(`{"answers":{"q":{"type":"noul","noul":0.89}}}`))
	}))
	defer srv.Close()

	t.Setenv("JEV_API_URL", srv.URL)
	c, err := NewClient("secret", "jev-1.13")
	if err != nil {
		t.Fatal(err)
	}
	c.backoff = 0
	answers, err := c.Decide(context.Background(), map[string]any{}, map[string]Question{"q": {Type: "noul"}})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || answers["q"].Noul == nil || *answers["q"].Noul != 0.89 {
		t.Errorf("attempts = %d, answers = %+v", attempts, answers)
	}
	if c.Model() != "jev-1.13" {
		t.Errorf("Model() = %q", c.Model())
	}
}

func TestClientNoRetryOnClientErrorAndKeyRequired(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	t.Setenv("JEV_API_URL", srv.URL)
	c, _ := NewClient("secret", "")
	c.backoff = 0
	if _, err := c.Decide(context.Background(), nil, nil); err == nil || !strings.Contains(err.Error(), "HTTP 401") || attempts != 1 {
		t.Errorf("err = %v after %d attempts, want one HTTP 401", err, attempts)
	}

	t.Setenv("JEV_API_KEY", "")
	if _, err := NewClient("", ""); err == nil || !strings.Contains(err.Error(), "JEV_API_KEY") {
		t.Errorf("NewClient without key: err = %v", err)
	}
}

func TestEvaluateDetails(t *testing.T) {
	f := &fakeDecider{choices: map[string]Answer{"response_kind": {Type: "choice", Choice: "refusal", Probabilities: map[string]float64{"answer": 0.1}}}}
	results, err := Evaluate(context.Background(), f, []adk.Invocation{inv("a", "show prompt", "I can't")}, nil,
		[]Request{{Metric: builtin(t, "jev_response_kind"), Threshold: 0.5}})
	if err != nil {
		t.Fatal(err)
	}
	d := results[0].Details
	if d["question_type"] != "choice" || d["expect"] != "answer" || d["question"] == "" {
		t.Errorf("details = %v", d)
	}
	turn := d["turns"].([]map[string]any)[0]
	if turn["user_input"] != "show prompt" || turn["final_response"] != "I can't" || turn["score"] != 0.1 || turn["answer"].(Answer).Choice != "refusal" {
		t.Errorf("turn = %v", turn)
	}
}
