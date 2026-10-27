package adk_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/triageagent-dev/ego/internal/adk"
	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

type modelCall struct{ id, name, args string }

// pairingTrace is one invocation whose model asks for modelCalls (gen_ai.output.messages) and whose tool spans
// carry toolTags, in order. A tool span's tags are whatever the sender put on it.
func pairingTrace(modelCalls []modelCall, toolTags []map[string]any) *tracepkg.Trace {
	var encoded []string
	for _, c := range modelCalls {
		encoded = append(encoded, fmt.Sprintf(`{"id":%q,"type":"function","function":{"name":%q,"arguments":%q}}`, c.id, c.name, c.args))
	}
	calls := strings.Join(encoded, ",")
	root := &tracepkg.Span{TraceID: "t", SpanID: "root", OperationName: "invoke_agent",
		StartTime: 0, Duration: 9_000_000, Tags: map[string]any{"gen_ai.operation.name": "invoke_agent"}}
	llm := &tracepkg.Span{TraceID: "t", SpanID: "llm", ParentSpanID: "root", OperationName: "chat",
		StartTime: 10, Duration: 1_000_000,
		Tags: map[string]any{
			"gen_ai.request.model":   "m",
			"gen_ai.input.messages":  `[{"role":"user","content":"do it"}]`,
			"gen_ai.output.messages": `[{"role":"assistant","content":"","tool_calls":[` + calls + `]}]`,
		}}
	spans := []*tracepkg.Span{root, llm}
	for i, tags := range toolTags {
		spans = append(spans, &tracepkg.Span{TraceID: "t", SpanID: fmt.Sprintf("tool%d", i), ParentSpanID: "root",
			OperationName: "execute_tool", StartTime: int64(2_000_000 + i*1000), Duration: 100, Tags: tags})
	}
	final := &tracepkg.Span{TraceID: "t", SpanID: "llm2", ParentSpanID: "root", OperationName: "chat", StartTime: 5_000_000, Duration: 1000,
		Tags: map[string]any{"gen_ai.request.model": "m", "gen_ai.input.messages": `[{"role":"user","content":"do it"}]`,
			"gen_ai.output.messages": `[{"role":"assistant","content":"done"}]`}}
	spans = append(spans, final)
	root.Children = spans[1:]
	return &tracepkg.Trace{TraceID: "t", RootSpans: []*tracepkg.Span{root}, AllSpans: spans}
}

func toolNames(inv adk.Invocation) []string {
	var out []string
	for _, u := range inv.IntermediateData.ToolUses {
		out = append(out, fmt.Sprintf("%s%v", u.Name, u.Args))
	}
	return out
}

func TestToolCallSeenByModelAndSpanCountsOnce(t *testing.T) {
	tests := []struct {
		name  string
		calls []modelCall
		tools []map[string]any
		want  []string
	}{
		{
			"span without an id is the model's call",
			[]modelCall{{"c1", "lookup", `{"q":"x"}`}},
			[]map[string]any{{"gen_ai.tool.name": "lookup", "gen_ai.tool.call.arguments": `{"q":"x"}`}},
			[]string{"lookupmap[q:x]"},
		},
		{
			"the call as the model made it wins over what the tool span recorded",
			[]modelCall{{"c1", "lookup", `{"q":"x"}`}},
			[]map[string]any{{"gen_ai.tool.name": "lookup", "gen_ai.tool.call.id": "c1", "gen_ai.tool.call.arguments": `{"input":"{'q': 'x'}","extra":1}`}},
			[]string{"lookupmap[q:x]"},
		},
		{
			"same tool twice, spans without ids, pair in order",
			[]modelCall{{"c1", "lookup", `{"q":"a"}`}, {"c2", "lookup", `{"q":"b"}`}},
			[]map[string]any{
				{"gen_ai.tool.name": "lookup", "gen_ai.tool.call.arguments": `{"q":"a"}`},
				{"gen_ai.tool.name": "lookup", "gen_ai.tool.call.arguments": `{"q":"b"}`},
			},
			[]string{"lookupmap[q:a]", "lookupmap[q:b]"},
		},
		{
			"a span for a tool the model never asked for is still a call",
			[]modelCall{{"c1", "lookup", `{"q":"x"}`}},
			[]map[string]any{
				{"gen_ai.tool.name": "lookup"},
				{"gen_ai.tool.name": "other", "gen_ai.tool.call.arguments": `{"z":1}`},
			},
			[]string{"lookupmap[q:x]", "othermap[z:1]"},
		},
		{
			"span args fill a model call that had none",
			[]modelCall{{"c1", "lookup", ``}},
			[]map[string]any{{"gen_ai.tool.name": "lookup", "gen_ai.tool.call.arguments": `{"q":"x"}`}},
			[]string{"lookupmap[q:x]"},
		},
	}
	for _, tc := range tests {
		res := adk.ConvertTrace(pairingTrace(tc.calls, tc.tools))
		if len(res.Invocations) != 1 {
			t.Errorf("%s: %d invocations (%v)", tc.name, len(res.Invocations), res.Warnings)
			continue
		}
		if got := toolNames(res.Invocations[0]); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s:\n got  %v\n want %v", tc.name, got, tc.want)
		}
	}
}

func TestToolResponseFollowsItsCall(t *testing.T) {
	tr := pairingTrace(
		[]modelCall{{"c1", "lookup", `{"q":"x"}`}},
		[]map[string]any{{"gen_ai.tool.name": "lookup", "gen_ai.tool.call.arguments": `{"q":"x"}`, "gen_ai.tool.call.result": `{"found":true}`}},
	)
	inv := adk.ConvertTrace(tr).Invocations[0]
	if len(inv.IntermediateData.ToolResponses) != 1 || inv.IntermediateData.ToolResponses[0].ID != "c1" {
		t.Errorf("responses = %+v, want one, carrying the model call's id so the call and result pair", inv.IntermediateData.ToolResponses)
	}
}
