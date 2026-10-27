package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// ego run reads an OTLP file and converts it by the convention it finds, as the server does: a LangGraph trace
// exported by LangSmith is a GenAI trace, not an ADK one.
func TestRunScoresAnOTLPGenAITrace(t *testing.T) {
	evalSet := filepath.Join(t.TempDir(), "set.json")
	const set = `{"eval_set_id":"lg","eval_cases":[{"eval_id":"c1","conversation":[{"invocation_id":"i1",
		"user_content":{"role":"user","parts":[{"text":"What is the weather in Paris?"}]},
		"final_response":{"role":"model","parts":[{"text":"It is sunny, 21C in Paris."}]},
		"intermediate_data":{"tool_uses":[{"name":"get_weather","args":{"city":"Paris"}}]}}]}]}`
	if err := os.WriteFile(evalSet, []byte(set), 0o600); err != nil {
		t.Fatal(err)
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	runErr := runCmd([]string{"../../internal/otlp/testdata/langgraph_langsmith.json", "--eval-set", evalSet,
		"-m", "tool_trajectory_avg_score", "--output", "json"})
	os.Stdout = stdout
	w.Close()
	if runErr != nil {
		t.Fatal(runErr)
	}

	out, _ := io.ReadAll(r)
	var reports []traceReport
	if err := json.Unmarshal(out, &reports); err != nil {
		t.Fatalf("output %q: %v", out, err)
	}
	if len(reports) != 1 || len(reports[0].Results) != 1 || reports[0].Results[0].Score != 1 {
		t.Errorf("reports = %+v, want one trace whose trajectory matches exactly", reports)
	}
}
