package otlp

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/triageagent-dev/ego/internal/adk"
	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

// testdata/langgraph_langsmith.json is a real export: a LangGraph agent (model node, tool node, one tool call)
// run with langsmith[otel], LANGSMITH_OTEL_ENABLED and a configurable thread id, as the OTLP receiver decodes it.
func TestLangSmithLangGraphTraceConverts(t *testing.T) {
	raw, err := os.ReadFile("testdata/langgraph_langsmith.json")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	traces := ParseExportRequest(body)
	if len(traces) != 1 {
		t.Fatalf("got %d traces, want 1", len(traces))
	}
	tr := traces[0]

	for _, s := range tr.AllSpans {
		if got := s.Tags[attrConversationID]; got != "thread-42" {
			t.Errorf("span %q conversation id = %v, want the graph's thread id", s.OperationName, got)
		}
	}

	res := adk.ConvertTrace(tr)
	if len(res.Invocations) != 1 {
		t.Fatalf("got %d invocations (warnings %v), want 1", len(res.Invocations), res.Warnings)
	}
	inv := res.Invocations[0]
	if got := inv.UserContent.Text(); got != "What is the weather in Paris?" {
		t.Errorf("user message = %q", got)
	}
	if got := inv.FinalResponse.Text(); got != "It is sunny, 21C in Paris." {
		t.Errorf("final response = %q", got)
	}
	if inv.IntermediateData == nil || len(inv.IntermediateData.ToolUses) != 1 {
		t.Fatalf("tool uses = %+v, want exactly one (the tool run and the model's call are the same call)", inv.IntermediateData)
	}
	use := inv.IntermediateData.ToolUses[0]
	if use.Name != "get_weather" || use.Args["city"] != "Paris" {
		t.Errorf("tool use = %+v", use)
	}
	if len(inv.IntermediateData.ToolResponses) != 1 {
		t.Fatalf("tool responses = %+v, want one", inv.IntermediateData.ToolResponses)
	}

	meta := adk.ExtractTraceMetadata(tr, res.Invocations)
	if meta.AgentName != "LangGraph" || meta.Model != "fake-gpt" {
		t.Errorf("agent %q model %q, want the graph's name and the model's", meta.AgentName, meta.Model)
	}
}

func lsSpan(parent string, tags tagMap) *tracepkg.Span {
	return &tracepkg.Span{TraceID: "t", SpanID: "s", ParentSpanID: parent, OperationName: "LangGraph", Tags: tags}
}

func TestLangSmithOnlyTouchesLangSmithSpans(t *testing.T) {
	s := lsSpan("", tagMap{"gen_ai.prompt": `{"messages":[["user","hi"]]}`, "langsmith.metadata.thread_id": "x"})
	normalizeLangSmith(s)
	if _, ok := s.Tags[attrConversationID]; ok || len(s.Tags) != 2 {
		t.Errorf("a span without langsmith.span.kind was changed: %v", s.Tags)
	}
}

func TestLangSmithFillsGapsOnly(t *testing.T) {
	s := lsSpan("p", tagMap{
		langSmithKind:                  "llm",
		"gen_ai.request.model":         "gpt-x",
		"gen_ai.input.messages":        `[{"role":"user","content":"kept"}]`,
		"gen_ai.prompt":                `{"messages":[["user","replaced?"]]}`,
		"gen_ai.conversation.id":       "mine",
		"langsmith.metadata.thread_id": "other",
	})
	normalizeLangSmith(s)
	if s.Tags["gen_ai.input.messages"] != `[{"role":"user","content":"kept"}]` || s.Tags[attrConversationID] != "mine" || s.Tags[attrRequestModel] != "gpt-x" {
		t.Errorf("a value the sender set was replaced: %v", s.Tags)
	}
}

func TestLangSmithLLMWithoutModelStillCounts(t *testing.T) {
	s := lsSpan("p", tagMap{langSmithKind: "llm", "gen_ai.prompt": `{"messages":[[{"role":"user","content":"hi"}]]}`})
	normalizeLangSmith(s)
	if s.Tags[attrRequestModel] != "unknown" {
		t.Errorf("model = %v, want the unknown sentinel so the span is an LLM call", s.Tags[attrRequestModel])
	}
	s = lsSpan("p", tagMap{langSmithKind: "llm", "langsmith.metadata.ls_model_name": "from-metadata"})
	normalizeLangSmith(s)
	if s.Tags[attrRequestModel] != "from-metadata" {
		t.Errorf("model = %v, want the metadata's", s.Tags[attrRequestModel])
	}
}

func TestLangSmithRootTakesTheLastTurn(t *testing.T) {
	state := `{"messages":[
		{"type":"human","content":"first question"},
		{"type":"ai","content":"first answer"},
		{"type":"human","content":"second question"}]}`
	cases := []struct {
		name, completion, wantOut string
	}{
		{"answered", `{"messages":[{"type":"human","content":"second question"},{"type":"ai","content":"second answer"}]}`, "second answer"},
		{"ended on a tool call", `{"messages":[{"type":"human","content":"second question"},{"type":"ai","content":"","tool_calls":[{"name":"t","args":{},"id":"1"}]}]}`, ""},
	}
	for _, c := range cases {
		s := lsSpan("", tagMap{langSmithKind: "chain", "gen_ai.operation.name": "chain", "gen_ai.prompt": state, "gen_ai.completion": c.completion})
		normalizeLangSmith(s)
		if s.Tags[attrInputValue] != "second question" {
			t.Errorf("%s: input = %v, want the latest user message, not the history", c.name, s.Tags[attrInputValue])
		}
		got, _ := s.Tags[attrOutputValue].(string)
		if got != c.wantOut {
			t.Errorf("%s: output = %q, want %q", c.name, got, c.wantOut)
		}
		if s.Tags[attrOperationName] != "invoke_agent" {
			t.Errorf("%s: operation = %v, want the root run to be the invocation", c.name, s.Tags[attrOperationName])
		}
	}

	child := lsSpan("parent", tagMap{langSmithKind: "chain", "gen_ai.operation.name": "chain", "gen_ai.prompt": state})
	normalizeLangSmith(child)
	if _, ok := child.Tags[attrInputValue]; ok || child.Tags[attrOperationName] != "chain" {
		t.Errorf("a node run was made an invocation: %v", child.Tags)
	}
}

func TestLangSmithMessageShapes(t *testing.T) {
	call := []any{map[string]any{"id": "c1", "type": "function", "function": map[string]any{"name": "lookup", "arguments": map[string]any{"q": "x"}}}}
	tests := []struct {
		name    string
		payload string
		in      bool
		want    []map[string]any
	}{
		{"tuple in a list", `{"messages":[["user","hi"]]}`, true, []map[string]any{{"role": "user", "content": "hi"}}},
		{"bare string state", `{"messages":"hi"}`, true, []map[string]any{{"role": "user", "content": "hi"}}},
		{"string input", `{"input":"hi"}`, true, []map[string]any{{"role": "user", "content": "hi"}}},
		{"constructor batch", `{"messages":[[{"lc":1,"type":"constructor","id":["langchain","schema","messages","HumanMessage"],"kwargs":{"content":"hi","type":"human"}}]]}`, true,
			[]map[string]any{{"role": "user", "content": "hi"}}},
		{"openai roles and block content", `{"messages":[{"role":"system","content":"be brief"},{"role":"user","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}]}`, true,
			[]map[string]any{{"role": "system", "content": "be brief"}, {"role": "user", "content": "a b"}}},
		{"generation message with tool call", `{"generations":[[{"text":"","type":"ChatGeneration","message":{"lc":1,"type":"constructor","id":["langchain","schema","messages","AIMessage"],"kwargs":{"content":"","type":"ai","tool_calls":[{"name":"lookup","args":{"q":"x"},"id":"c1","type":"tool_call"}]}}}]]}`, false,
			[]map[string]any{{"role": "assistant", "content": "", "tool_calls": call}}},
		{"generation text only", `{"generations":[[{"text":"hello","type":"Generation"}]]}`, false, []map[string]any{{"role": "assistant", "content": "hello"}}},
		{"openai response with string arguments", `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]}}]}`, false,
			[]map[string]any{{"role": "assistant", "content": "", "tool_calls": call}}},
		{"openai tool calls under additional_kwargs", `{"generations":[[{"message":{"type":"ai","content":"","additional_kwargs":{"tool_calls":[{"id":"c1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]}}}]]}`, false,
			[]map[string]any{{"role": "assistant", "content": "", "tool_calls": call}}},
		{"anthropic tool_use block", `{"generations":[[{"message":{"type":"ai","content":[{"type":"text","text":"ok"},{"type":"tool_use","id":"c1","name":"lookup","input":{"q":"x"}}]}}]]}`, false,
			[]map[string]any{{"role": "assistant", "content": "ok", "tool_calls": call}}},
		{"tool message", `{"messages":[{"type":"tool","content":"42","tool_call_id":"c1","name":"lookup"}]}`, false,
			[]map[string]any{{"role": "tool", "content": "42", "tool_call_id": "c1"}}},
		{"not a message", `{"output":{"x":1}}`, false, nil},
		{"not json", `plain text`, true, nil},
	}
	for _, tc := range tests {
		var got []map[string]any
		if tc.in {
			got = lsInputMessages(tc.payload)
		} else {
			got = lsOutputMessages(tc.payload)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s:\n got  %v\n want %v", tc.name, got, tc.want)
		}
	}
}

func TestLangSmithToolRun(t *testing.T) {
	s := lsSpan("p", tagMap{
		langSmithKind:       "tool",
		"gen_ai.prompt":     `{"city":"Paris"}`,
		"gen_ai.completion": `{"output":{"content":"sunny","type":"tool","name":"w","tool_call_id":"c1","status":"success"}}`,
	})
	normalizeLangSmith(s)
	if s.Tags[attrToolArguments] != `{"city":"Paris"}` || s.Tags[attrToolResult] != "sunny" {
		t.Errorf("tool tags = %v", s.Tags)
	}

	s = lsSpan("p", tagMap{langSmithKind: "tool", "gen_ai.prompt": `{"input":"{\"q\": 1}"}`,
		"gen_ai.completion": `{"output":{"content":"boom","type":"tool","status":"error"}}`})
	normalizeLangSmith(s)
	if s.Tags[attrToolArguments] != `{"q":1}` {
		t.Errorf("a single string argument holding JSON was not unwrapped: %v", s.Tags[attrToolArguments])
	}
	if res, _ := s.Tags[attrToolResult].(string); res != `{"isError":true,"result":"boom"}` {
		t.Errorf("a failed tool call = %q, want it flagged as an error", res)
	}

	s = lsSpan("p", tagMap{langSmithKind: "tool", "gen_ai.prompt": `{"input":"plain text"}`, "gen_ai.completion": `{"output":"done"}`})
	normalizeLangSmith(s)
	if s.Tags[attrToolArguments] != `{"input":"plain text"}` || s.Tags[attrToolResult] != "done" {
		t.Errorf("tool tags = %v", s.Tags)
	}
}

func TestLangSmithConversationIDFromMetadata(t *testing.T) {
	for _, tc := range []struct {
		name string
		tags tagMap
		want any
	}{
		{"thread id", tagMap{langSmithKind: "chain", "langsmith.metadata.thread_id": "t1"}, "t1"},
		{"numeric thread id", tagMap{langSmithKind: "chain", "langsmith.metadata.thread_id": int64(7)}, "7"},
		{"conversation id wins over session id", tagMap{langSmithKind: "chain", "langsmith.metadata.conversation_id": "c", "langsmith.metadata.session_id": "s"}, "c"},
		{"the project is not a conversation", tagMap{langSmithKind: "chain", "langsmith.trace.session_id": "project", "langsmith.trace.session_name": "default"}, nil},
	} {
		s := lsSpan("p", tc.tags)
		normalizeLangSmith(s)
		if got := s.Tags[attrConversationID]; got != tc.want {
			t.Errorf("%s: conversation id = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A LangGraph agent called inside a distributed trace has a remote parent, so its root run is not a root of the
// LangSmith spans. The trace converts all the same: the turn, its tool call and the agent come from the model and
// tool runs and the run's name. Marking such a run as the invocation on ingest would also mark a node run whose
// parent arrives in a later batch, so it is not done.
func TestLangSmithRootWithARemoteParentStillConverts(t *testing.T) {
	raw, err := os.ReadFile("testdata/langgraph_langsmith.json")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	for _, rs := range body["resourceSpans"].([]any) {
		for _, ss := range rs.(map[string]any)["scopeSpans"].([]any) {
			for _, sp := range ss.(map[string]any)["spans"].([]any) {
				if m := sp.(map[string]any); m["name"] == "LangGraph" {
					m["parentSpanId"] = "aaaaaaaaaaaaaaaa"
				}
			}
		}
	}
	tr := ParseExportRequest(body)[0]
	res := adk.ConvertTrace(tr)
	if len(res.Invocations) != 1 {
		t.Fatalf("got %d invocations (warnings %v), want 1", len(res.Invocations), res.Warnings)
	}
	inv := res.Invocations[0]
	if inv.UserContent.Text() != "What is the weather in Paris?" || inv.FinalResponse.Text() != "It is sunny, 21C in Paris." {
		t.Errorf("turn = %q / %q", inv.UserContent.Text(), inv.FinalResponse.Text())
	}
	if inv.IntermediateData == nil || len(inv.IntermediateData.ToolUses) != 1 || inv.IntermediateData.ToolUses[0].Name != "get_weather" {
		t.Errorf("tool uses = %+v", inv.IntermediateData)
	}
	if meta := adk.ExtractTraceMetadata(tr, res.Invocations); meta.AgentName != "LangGraph" {
		t.Errorf("agent = %q, want the run's name", meta.AgentName)
	}
}
