package judge

import (
	"strings"
	"testing"

	"github.com/triageagent-dev/ego/internal/adk"
)

// Both text judges are shown the context documents as evidence; a turn without them has no such section.
func TestJudgePromptsShowContextDocuments(t *testing.T) {
	inv := adk.Invocation{UserContent: adk.TextOnly("user", "what is down?"), FinalResponse: adk.TextOnly("model", "a")}
	rubrics := []Rubric{{ID: "r1", Text: "cites a fact"}}
	if got := buildHallucinationContextWith(inv, nil, ToolOutputLimits{}); strings.Contains(got, "Context the agent was given") {
		t.Errorf("a turn without documents has a context section:\n%s", got)
	}
	inv.Context = []adk.ContextDoc{{ID: "cell-7", Text: "the image tag is missing in the registry"}}
	for name, prompt := range map[string]string{
		"hallucinations": buildHallucinationContextWith(inv, nil, ToolOutputLimits{}),
		"rubric":         formatRubricBasedFinalResponseQualityV1Prompt(inv, rubrics),
	} {
		if !strings.Contains(prompt, "the image tag is missing in the registry") || !strings.Contains(prompt, "[cell-7]") {
			t.Errorf("%s: the document is not in the prompt", name)
		}
	}
}
