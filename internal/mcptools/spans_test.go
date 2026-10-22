package mcptools

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

func claudeCodeTrace() *tracepkg.Trace {
	run := &tracepkg.Span{SpanID: "run", OperationName: "claude_code.tool.execution", StartTime: 3_000_000, Duration: 244_000, Tags: map[string]any{
		"otel.status_code": "ERROR", "error": "ShellError", "error.type": "tool_error", "host.arch": "amd64", "success": false,
	}}
	tool := &tracepkg.Span{SpanID: "tool", OperationName: "claude_code.tool", StartTime: 2_000_000, Duration: 300_000, Tags: map[string]any{
		"tool_name": "Bash", "bash_command_class": "container_infra", "bash_argv0": "kubectl", "service.name": "spot",
	}, Children: []*tracepkg.Span{run}}
	llm := &tracepkg.Span{SpanID: "llm", OperationName: "claude_code.llm_request", StartTime: 1_000_000, Duration: 3_000_000, Tags: map[string]any{
		"model": "claude-sonnet-5-5", "user_prompt": strings.Repeat("x", 1000),
	}}
	root := &tracepkg.Span{SpanID: "root", OperationName: "claude_code.interaction", StartTime: 500_000, Duration: 9_000_000, Tags: map[string]any{}, Children: []*tracepkg.Span{tool, llm}}
	return &tracepkg.Trace{TraceID: "t", RootSpans: []*tracepkg.Span{root}, AllSpans: []*tracepkg.Span{root, llm, tool, run}}
}

func TestBuildSessionSpansFindsTheFailedCallAndItsTool(t *testing.T) {
	got := BuildSessionSpans("s", []*tracepkg.Trace{claudeCodeTrace()}, SpanQuery{ErrorsOnly: true})
	if got.NumSpans != 4 || got.NumErrors != 1 || got.Returned != 1 || len(got.Spans) != 1 {
		t.Fatalf("counts = %+v", got)
	}
	sp := got.Spans[0]
	if sp.Operation != "claude_code.tool.execution" || !sp.Error || sp.Message != "ShellError" {
		t.Fatalf("span = %+v", sp)
	}
	if strings.Join(sp.Path, ">") != "claude_code.interaction>claude_code.tool" {
		t.Errorf("path = %v", sp.Path)
	}
	if sp.Parent["tool_name"] != "Bash" || sp.Parent["bash_argv0"] != "kubectl" {
		t.Errorf("parent = %v", sp.Parent)
	}
	if _, noisy := sp.Attributes["host.arch"]; noisy {
		t.Error("noise attributes were not left out")
	}
	if sp.DurationMs != 244 || sp.Depth != 2 || sp.AtMs != 2500 || sp.SpanID != "run" {
		t.Errorf("duration %v ms, depth %d, at %d ms, id %q", sp.DurationMs, sp.Depth, sp.AtMs, sp.SpanID)
	}
	if got.Started != "1970-01-01T00:00:00.500Z" {
		t.Errorf("started = %s", got.Started)
	}
}

func TestBuildSessionSpansTimelineHasNoAttributesUnlessAsked(t *testing.T) {
	traces := []*tracepkg.Trace{claudeCodeTrace()}
	timeline := BuildSessionSpans("s", traces, SpanQuery{})
	for _, sp := range timeline.Spans {
		if sp.Error {
			if sp.Attributes == nil || sp.Message == "" {
				t.Errorf("failed span lost its cause: %+v", sp)
			}
		} else if sp.Attributes != nil || sp.Path != nil || sp.SpanID != "" {
			t.Errorf("a span that did not fail carries detail: %+v", sp)
		}
	}
	tools := BuildSessionSpans("s", traces, SpanQuery{Operation: "tool", Attributes: true})
	if tools.NumSpans != 4 || len(tools.Spans) != 2 {
		t.Fatalf("operation filter: %d of %d spans", len(tools.Spans), tools.NumSpans)
	}
	if tools.Spans[0].Attributes["tool_name"] != "Bash" {
		t.Errorf("attributes = %v", tools.Spans[0].Attributes)
	}
}

func TestBuildSessionSpansOrdersByStartClipsAndLimits(t *testing.T) {
	got := BuildSessionSpans("s", []*tracepkg.Trace{claudeCodeTrace()}, SpanQuery{Limit: 3, Attributes: true})
	var ops []string
	for _, sp := range got.Spans {
		ops = append(ops, sp.Operation)
	}
	// the interaction, then its children by start time: the model request began before the tool call's own span
	if want := "claude_code.interaction claude_code.llm_request claude_code.tool"; strings.Join(ops, " ") != want {
		t.Errorf("order = %v, want %s", ops, want)
	}
	if got.Returned != 3 || !got.Truncated || got.NumSpans != 4 {
		t.Errorf("counts = %+v", got)
	}
	prompt, _ := got.Spans[1].Attributes["user_prompt"].(string)
	if len(got.Spans[1].Attributes) == 0 {
		t.Fatal("attributes were asked for and not returned")
	}
	if !strings.Contains(prompt, "1000 chars") || len(prompt) > maxAttrChars+30 {
		t.Errorf("long value not clipped: %d chars", len(prompt))
	}
}

type spansBackend struct {
	Backend
	traces []*tracepkg.Trace
}

func (b spansBackend) SessionTraces(context.Context, string) ([]*tracepkg.Trace, int, error) {
	return b.traces, 4, nil
}

func TestGetSessionSpansToolIsRegistered(t *testing.T) {
	var req mcp.CallToolRequest
	req.Params.Arguments = map[string]any{"session_id": "s", "errors_only": true}
	for _, tool := range Tools(spansBackend{traces: []*tracepkg.Trace{claudeCodeTrace()}}, Options{}) {
		if tool.Tool.Name != "get_session_spans" {
			continue
		}
		res, err := tool.Handler(context.Background(), req)
		if err != nil || res.IsError {
			t.Fatalf("handler: %v %+v", err, res)
		}
		out, _ := res.StructuredContent.(SessionSpans)
		if out.NumErrors != 1 || len(out.Spans) != 1 {
			t.Fatalf("result = %+v", out)
		}
		return
	}
	t.Fatal("get_session_spans is not in the registry")
}

func TestSummarizeSessionWithoutInvocationsPointsToTheSpans(t *testing.T) {
	var req mcp.CallToolRequest
	req.Params.Arguments = map[string]any{"session_id": "s"}
	for _, tool := range Tools(spansBackend{traces: []*tracepkg.Trace{claudeCodeTrace()}}, Options{}) {
		if tool.Tool.Name != "summarize_session" {
			continue
		}
		res, err := tool.Handler(context.Background(), req)
		if err != nil || res.IsError {
			t.Fatalf("handler: %v %+v", err, res)
		}
		out, _ := res.StructuredContent.(SessionSummary)
		if out.NumInvocations != 0 || !strings.Contains(out.Note, "get_session_spans") {
			t.Fatalf("summary = %+v", out)
		}
		return
	}
	t.Fatal("summarize_session is not in the registry")
}
