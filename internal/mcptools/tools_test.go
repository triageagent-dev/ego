package mcptools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// toolBackend answers the tools these tests call and records what the handlers asked of it.
type toolBackend struct {
	Backend
	usage    UsageQuery
	activity ActivityQuery
	limit    int
	statuses []string
}

func (b *toolBackend) Metrics(context.Context) ([]MetricInfo, error) {
	return []MetricInfo{{Name: "hallucinations_v1", Category: "grounding", RequiresLLM: true, Working: true}}, nil
}

func (b *toolBackend) Runs(_ context.Context, limit int, statuses []string) ([]RunListItem, error) {
	b.limit, b.statuses = limit, statuses
	return []RunListItem{{RunID: "run-1", Status: "succeeded", CreatedAt: "2026-10-27T10:00:00Z"}}, nil
}

func (b *toolBackend) Usage(_ context.Context, q UsageQuery) (*Usage, error) {
	b.usage = q
	return &Usage{}, nil
}

func (b *toolBackend) EgoScore(_ context.Context, q ActivityQuery) ([]TurnScore, error) {
	b.activity = q
	score := 0.75
	return []TurnScore{{Agent: "alpha", Hours: q.Hours, Turns: 4, Score: &score}}, nil
}

func (b *toolBackend) EgoErrors(_ context.Context, q ActivityQuery) ([]EgoErrors, error) {
	b.activity = q
	return []EgoErrors{{Agent: "alpha", Hours: q.Hours, Errors: []ErrorGroup{{Tool: "search", Message: "timeout", Count: 3, Session: "s1"}}}}, nil
}

func (b *toolBackend) RunFindings(context.Context) (*SweepReport, error) {
	return &SweepReport{Candidates: 2, Written: 1, SkippedBudget: 1}, nil
}

func allTools(b Backend) map[string]server.ServerTool {
	out := map[string]server.ServerTool{}
	// evaluate_traces is added by ego mcp, which reads local files; any handler stands in for it here.
	stub := func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("ok"), nil
	}
	for _, t := range Tools(b, Options{Admin: true, EvaluateTraces: stub}) {
		out[t.Tool.Name] = t
	}
	return out
}

func call(t *testing.T, tool server.ServerTool, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	var req mcp.CallToolRequest
	req.Params.Name = tool.Tool.Name
	req.Params.Arguments = args
	res, err := tool.Handler(context.Background(), req)
	if err != nil {
		t.Fatalf("%s: %v", tool.Tool.Name, err)
	}
	return res
}

func text(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// A host treats a missing hint as the unsafe default and a directory refuses the tool, so every tool declares all
// four, and they say what the handler does: what only reads ego's data is read-only, what writes is not, what
// removes or replaces a role binding is destructive, and what reaches a model or a local file is open-world.
func TestToolsDeclareTheirBehaviour(t *testing.T) {
	type want struct{ readOnly, destructive, idempotent, openWorld bool }
	reads := want{true, false, true, false}
	expected := map[string]want{
		"list_metrics":        reads,
		"evaluate_traces":     {true, false, true, true},
		"list_sessions":       reads,
		"list_threads":        reads,
		"summarize_session":   reads,
		"get_session_spans":   reads,
		"list_runs":           reads,
		"get_run_results":     reads,
		"get_usage":           reads,
		"get_quality":         reads,
		"get_ego_score":       reads,
		"get_ego_tools":       reads,
		"get_ego_errors":      reads,
		"explain_regression":  reads,
		"explain_session":     reads,
		"get_findings":        reads,
		"list_corrections":    reads,
		"list_role_bindings":  reads,
		"register_correction": {false, false, false, false},
		"label_session":       {false, false, false, false},
		"set_role_binding":    {false, true, true, false},
		"run_findings":        {false, false, false, true},
		"delete_role_binding": {false, true, true, false},
	}
	tools := allTools(&toolBackend{})
	if len(tools) != len(expected) {
		t.Errorf("%d tools registered, the table names %d: a tool was added or removed without its hints", len(tools), len(expected))
	}
	for name, w := range expected {
		tool, ok := tools[name]
		if !ok {
			t.Errorf("%s is not registered", name)
			continue
		}
		a := tool.Tool.Annotations
		if a.ReadOnlyHint == nil || a.DestructiveHint == nil || a.IdempotentHint == nil || a.OpenWorldHint == nil {
			t.Errorf("%s: a hint is missing: %+v", name, a)
			continue
		}
		if got := (want{*a.ReadOnlyHint, *a.DestructiveHint, *a.IdempotentHint, *a.OpenWorldHint}); got != w {
			t.Errorf("%s: hints %+v, want %+v", name, got, w)
		}
	}
	for name := range tools {
		if _, ok := expected[name]; !ok {
			t.Errorf("%s has no expected hints in this test", name)
		}
	}
}

func TestListMetricsTool(t *testing.T) {
	res := call(t, allTools(&toolBackend{})["list_metrics"], nil)
	if res.IsError || !strings.Contains(string(mustJSON(t, res.StructuredContent)), "hallucinations_v1") {
		t.Errorf("list_metrics = %+v", res)
	}
}

func TestListRunsTool(t *testing.T) {
	b := &toolBackend{}
	res := call(t, allTools(b)["list_runs"], map[string]any{"limit": 5, "status": []any{"failed"}})
	if res.IsError || b.limit != 5 || len(b.statuses) != 1 || b.statuses[0] != "failed" {
		t.Errorf("list_runs passed limit %d status %v, result %+v", b.limit, b.statuses, res)
	}
	call(t, allTools(b)["list_runs"], nil)
	if b.limit != 20 || b.statuses != nil {
		t.Errorf("list_runs defaults: limit %d status %v, want 20 and none", b.limit, b.statuses)
	}
}

func TestGetUsageTool(t *testing.T) {
	b := &toolBackend{}
	tool := allTools(b)["get_usage"]
	if res := call(t, tool, nil); res.IsError || b.usage.Hours != defaultUsageHours || b.usage.Group != UsageByKind || b.usage.Top != defaultUsageTop {
		t.Errorf("defaults: query %+v, result error %v", b.usage, res.IsError)
	}
	call(t, tool, map[string]any{"hours": 48, "tenant": "s/t", "group": UsageByModel, "top": 3})
	if b.usage.Hours != 48 || b.usage.Tenant != "s/t" || b.usage.Group != UsageByModel || b.usage.Top != 3 {
		t.Errorf("arguments not passed: %+v", b.usage)
	}
	for name, args := range map[string]map[string]any{
		"window too long": {"hours": maxUsageHours + 1},
		"no window":       {"hours": 0},
		"too many":        {"top": maxUsageTop + 1},
		"unknown group":   {"group": "colour"},
	} {
		if res := call(t, tool, args); !res.IsError {
			t.Errorf("%s: want an error result", name)
		}
	}
}

func TestEgoScoreAndErrorsTools(t *testing.T) {
	b := &toolBackend{}
	tools := allTools(b)

	res := call(t, tools["get_ego_score"], map[string]any{"agent": "alpha", "hours": 12})
	if res.IsError || b.activity.Agent != "alpha" || b.activity.Hours != 12 || b.activity.By != ScoreByAgent {
		t.Errorf("get_ego_score: query %+v, result %+v", b.activity, res)
	}
	if !strings.Contains(text(res), "alpha") {
		t.Errorf("get_ego_score answer lacks the agent: %q", text(res))
	}

	res = call(t, tools["get_ego_errors"], nil)
	if res.IsError || b.activity.Hours != defaultActivityHours {
		t.Errorf("get_ego_errors: query %+v, result %+v", b.activity, res)
	}
	if out := text(res); !strings.Contains(out, "search") || !strings.Contains(out, "timeout") {
		t.Errorf("get_ego_errors answer lacks the failing tool and its message: %q", out)
	}

	for _, name := range []string{"get_ego_score", "get_ego_errors"} {
		for label, args := range map[string]map[string]any{
			"window too long": {"hours": maxActivityHours + 1},
			"unknown cut":     {"by": "weather"},
			"too many rows":   {"limit": MaxScoreRows + 1},
		} {
			if res := call(t, tools[name], args); !res.IsError {
				t.Errorf("%s, %s: want an error result", name, label)
			}
		}
	}
}

func TestRunFindingsTool(t *testing.T) {
	res := call(t, allTools(&toolBackend{})["run_findings"], nil)
	out := string(mustJSON(t, res.StructuredContent))
	if res.IsError || !strings.Contains(out, `"written":1`) || !strings.Contains(out, `"candidates":2`) {
		t.Errorf("run_findings = %s", out)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// What a host and a directory read is the tools/list answer, so the hints are checked on the wire: all four keys
// on every tool, with false written out and not dropped.
func TestToolsListCarriesAllFourHints(t *testing.T) {
	h := NewHTTPHandler(NewREST("", "", nil), Options{})
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var answer struct {
		Result struct {
			Tools []struct {
				Name        string         `json:"name"`
				Annotations map[string]any `json:"annotations"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
		t.Fatalf("tools/list: %v: %s", err, rec.Body.String())
	}
	if len(answer.Result.Tools) == 0 {
		t.Fatal("no tools listed")
	}
	for _, tool := range answer.Result.Tools {
		for _, key := range []string{"readOnlyHint", "destructiveHint", "idempotentHint", "openWorldHint"} {
			if _, ok := tool.Annotations[key].(bool); !ok {
				t.Errorf("%s: %s is not a boolean on the wire: %v", tool.Name, key, tool.Annotations[key])
			}
		}
	}
}
