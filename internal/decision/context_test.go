package decision

import (
	"testing"

	"github.com/triageagent-dev/ego/internal/adk"
)

// The decision model's state holds the context documents, and a client with a size limit says when it would cut them.
func TestStateHoldsContextDocuments(t *testing.T) {
	inv := adk.Invocation{UserContent: adk.TextOnly("user", "q"), FinalResponse: adk.TextOnly("model", "a")}
	if _, ok := State([]adk.Invocation{inv}, 0, nil)["context_documents"]; ok {
		t.Error("a turn without documents has context_documents")
	}
	inv.Context = []adk.ContextDoc{{ID: "cell-7", Text: "the image tag is missing"}}
	state := State([]adk.Invocation{inv}, 0, nil)
	if _, ok := state["context_documents"]; !ok {
		t.Fatalf("state = %v, want context_documents", state)
	}
	if (&Client{}).Cuts(state) || !(&Client{maxState: 16}).Cuts(state) {
		t.Error("Cuts is wrong: no limit cuts nothing, a limit of 16 bytes cuts this state")
	}
}
