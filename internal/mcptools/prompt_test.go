package mcptools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/triageagent-dev/ego/skills"
)

func rpc(t *testing.T, b Backend, method string, params any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	resp := NewServer(b, Options{}).HandleMessage(context.Background(), body)
	raw, _ := json.Marshal(resp)
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// Both transports build the server with NewServer, so this is the prompt a stdio client and /mcp both get.
func TestRSILoopPromptIsTheEmbeddedSkill(t *testing.T) {
	list := rpc(t, NewREST("", "", nil), "prompts/list", map[string]any{})
	prompts, _ := list["result"].(map[string]any)["prompts"].([]any)
	if len(prompts) != 1 || prompts[0].(map[string]any)["name"] != RSILoopPrompt {
		t.Fatalf("prompts/list = %v", list)
	}
	got := rpc(t, NewREST("", "", nil), "prompts/get", map[string]any{"name": RSILoopPrompt})
	msgs, _ := got["result"].(map[string]any)["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("prompts/get = %v", got)
	}
	m := msgs[0].(map[string]any)
	if m["role"] != "user" || m["content"].(map[string]any)["text"] != skills.EgoRSI {
		t.Errorf("rsi_loop does not return the embedded skill text: %v", m)
	}
}

// The skill tells an agent which tools to call; each must exist in the registry, and the six steps must be there.
func TestSkillNamesOnlyRegisteredTools(t *testing.T) {
	s := NewServer(NewREST("", "", nil), Options{})
	for _, tool := range []string{"get_quality", "get_findings", "explain_session", "explain_regression", "summarize_session",
		"register_correction", "list_corrections", "label_session"} {
		if s.GetTool(tool) == nil {
			t.Errorf("the skill names %s, which the registry lacks", tool)
		}
		if !strings.Contains(skills.EgoRSI, "`"+tool+"`") {
			t.Errorf("the skill does not mention %s", tool)
		}
	}
	if !strings.HasPrefix(skills.EgoRSI, "---\nname: ego-rsi\ndescription: ") {
		t.Error("the skill lacks its frontmatter")
	}
	for _, step := range []string{"### 1.", "### 2.", "### 3.", "### 4.", "### 5.", "### 6.", "service.version"} {
		if !strings.Contains(skills.EgoRSI, step) {
			t.Errorf("the skill lacks %q", step)
		}
	}
}
