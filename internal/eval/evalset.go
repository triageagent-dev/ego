package eval

import (
	"encoding/json"
	"os"
	"strings"

	"github.com/triageagent-dev/ego/internal/adk"
)

func LoadEvalSet(path string) (*adk.EvalSet, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var set adk.EvalSet
	if err := json.Unmarshal(raw, &set); err != nil {
		return nil, err
	}
	return &set, nil
}

// FindExpectedInvocations matches a trace's actual invocations to an eval case's conversation. With a single
// eval case it's used unconditionally; with several, the case whose first invocation's user text matches the
// actual first invocation's is picked, falling back to the first case.
func FindExpectedInvocations(actual []adk.Invocation, set *adk.EvalSet) []adk.Invocation {
	if set == nil || len(set.EvalCases) == 0 {
		return nil
	}

	if len(set.EvalCases) == 1 {
		return set.EvalCases[0].Conversation
	}

	var actualUserText string
	if len(actual) > 0 {
		actualUserText = actual[0].UserText()
	}
	if actualUserText == "" {
		return set.EvalCases[0].Conversation
	}

	for _, c := range set.EvalCases {
		if len(c.Conversation) == 0 {
			continue
		}
		expectedText := c.Conversation[0].UserText()
		if expectedText != "" && textMatches(actualUserText, expectedText) {
			return c.Conversation
		}
	}

	return set.EvalCases[0].Conversation
}

func textMatches(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}
