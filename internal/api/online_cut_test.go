package api

import (
	"context"
	"testing"
	"time"

	"github.com/triageagent-dev/ego/internal/online"
)

// A session whose every metric would be skipped for a cut input takes no sample: with one sample an hour, the whole
// session that follows it is still scored.
func TestOnlineEngine_ACutSessionTakesNoSample(t *testing.T) {
	e, sessions, _, fake := newTestEngine(t, testOnlineConfig)
	for i := range e.conf().Profiles {
		e.conf().Profiles[i].PerHour = 1
		e.conf().Profiles[i].PerDay = 1
		e.conf().Profiles[i].Evaluators = []online.Evaluator{{Name: "hallucinations_v1", NumSamples: 1}}
	}
	now := time.Date(2026, 10, 4, 12, 30, 0, 0, time.UTC)
	e.now = func() time.Time { return now }
	question := "why is the checkout down right now? give me the cause"
	answer := "The payment service is out of memory: the heap grew past its limit after the 14:02 deploy, and the pod was killed three times in ten minutes."

	cut := onlineSessionBody("eeeeeeeeeeee0005", now.UnixMilli(), "rca", question, answer)
	spans := cut["resourceSpans"].([]any)[0].(map[string]any)["scopeSpans"].([]any)[0].(map[string]any)["spans"].([]any)
	llm := spans[1].(map[string]any)
	llm["attributes"] = append(llm["attributes"].([]any), map[string]any{"key": "ego.input.truncated", "value": map[string]any{"boolValue": true}})
	sessions.Ingest(cut)
	e.process(context.Background(), "otlp-eeeeeeeeeeee")
	st := e.status()
	if st.Counts[online.SkipCutInput] != 1 || st.Counts["sampled_base"] != 0 || fake.calls != 0 {
		t.Fatalf("counts = %v, judge calls = %d; want one cut-input skip and nothing sampled", st.Counts, fake.calls)
	}

	sessions.Ingest(onlineSessionBody("ffffffffffff0006", now.UnixMilli(), "rca", question+" and since when?", answer))
	e.process(context.Background(), "otlp-ffffffffffff")
	if got := e.status().Counts; got["sampled_base"] != 1 {
		t.Errorf("the whole session was not sampled: counts = %v", got)
	}
}
