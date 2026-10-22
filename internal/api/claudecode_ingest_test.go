package api

import (
	"path/filepath"
	"testing"
	"time"
)

// claudeCodeExport is what a Claude Code process sends for one prompt: an interaction root, two model requests
// (one with cache reads and writes), and a tool with its execution child, with one subagent request nested under
// the tool. The tenant is only on the resource, where OTEL_RESOURCE_ATTRIBUTES puts it.
func claudeCodeExport(traceID, sessionID, prompt string, startMs int64) map[string]any {
	attr := func(k string, v map[string]any) map[string]any { return map[string]any{"key": k, "value": v} }
	str := func(s string) map[string]any { return map[string]any{"stringValue": s} }
	num := func(n int) map[string]any { return map[string]any{"intValue": strconvI(int64(n))} }
	ns := func(ms int64) string { return strconvI(ms * 1_000_000) }
	span := func(id, parent, name string, from, to int64, attrs ...map[string]any) map[string]any {
		s := map[string]any{
			"traceId": traceID, "spanId": id, "name": name,
			"startTimeUnixNano": ns(startMs + from), "endTimeUnixNano": ns(startMs + to),
			"attributes": append([]any{attr("session.id", str(sessionID))}, anySlice(attrs)...),
		}
		if parent != "" {
			s["parentSpanId"] = parent
		}
		return s
	}
	return map[string]any{"resourceSpans": []any{map[string]any{
		"resource": map[string]any{"attributes": []any{
			attr("service.name", str("claude-code")), attr("ego.tenant", str("tenant-a")),
		}},
		"scopeSpans": []any{map[string]any{"scope": map[string]any{"name": "com.anthropic.claude_code"}, "spans": []any{
			span(traceID+"-i", "", "claude_code.interaction", 0, 9000, attr("user_prompt", str(prompt))),
			span(traceID+"-l1", traceID+"-i", "claude_code.llm_request", 100, 2000,
				attr("model", str("claude-model-a")), attr("input_tokens", num(100)), attr("output_tokens", num(50)),
				attr("cache_read_tokens", num(1000)), attr("cache_creation_tokens", num(200)), attr("stop_reason", str("tool_use"))),
			span(traceID+"-t", traceID+"-i", "claude_code.tool", 2100, 6000,
				attr("tool_name", str("Bash")), attr("tool_use_id", str("toolu_"+traceID))),
			span(traceID+"-te", traceID+"-t", "claude_code.tool.execution", 2200, 2900,
				attr("tool_use_id", str("toolu_"+traceID))),
			span(traceID+"-sub", traceID+"-t", "claude_code.llm_request", 3000, 5000,
				attr("model", str("claude-model-a")), attr("input_tokens", num(10)), attr("output_tokens", num(5)),
				attr("agent_id", str("agent-1")), attr("parent_agent_id", str("main"))),
			span(traceID+"-l2", traceID+"-i", "claude_code.llm_request", 6100, 8000,
				attr("model", str("claude-model-a")), attr("input_tokens", num(20)), attr("output_tokens", num(30)),
				attr("cache_read_tokens", num(1300)), attr("stop_reason", str("end_turn"))),
		}}},
	}}}
}

func anySlice(in []map[string]any) []any {
	out := make([]any, len(in))
	for i, v := range in {
		out[i] = v
	}
	return out
}

func TestClaudeCodeTracesBecomeASessionWithUsage(t *testing.T) {
	archive, err := NewSQLiteStore(filepath.Join(t.TempDir(), "cc.db"))
	if err != nil {
		t.Fatal(err)
	}
	store := NewSessionStoreWithArchive(nil, archive, SessionLimits{})
	defer store.Close()

	start := time.Now().Add(-time.Hour).UnixMilli()
	// Two prompts of one Claude Code session are two traces; they must meet in one session.
	store.Ingest(claudeCodeExport("cc00000000000001", "cc-session-1", "fix the build", start))
	store.Ingest(claudeCodeExport("cc00000000000002", "cc-session-1", "<REDACTED>", start+60_000))
	// Another Claude Code session is another ego session.
	store.Ingest(claudeCodeExport("cc00000000000003", "cc-session-2", "<REDACTED>", start+120_000))
	if err := store.flushPersist(); err != nil {
		t.Fatal(err)
	}

	list := store.List()
	if len(list) != 2 {
		t.Fatalf("got %d sessions, want 2 (one per session.id)", len(list))
	}
	var s SessionSummary
	for _, c := range list {
		if c.SessionID == "cc-session-1" {
			s = c
		}
	}
	if s.SessionID == "" {
		t.Fatalf("no session named after session.id: %+v", list)
	}
	if s.SpanCount != 12 {
		t.Errorf("spans = %d, want 12 (two prompts of six spans)", s.SpanCount)
	}
	if s.LLMCalls != 6 {
		t.Errorf("LLM calls = %d, want 6", s.LLMCalls)
	}

	// Per prompt: (100+1000+200) + 50 + (10 + 5) + (20+1300) + 30 = 2715; the cache counts are part of the input.
	const perPrompt = 1300 + 50 + 10 + 5 + 1320 + 30
	if s.TotalTokens != 2*perPrompt {
		t.Errorf("total tokens = %d, want %d", s.TotalTokens, 2*perPrompt)
	}
	if len(s.Agents) != 1 || s.Agents[0] != "claude-code" {
		t.Errorf("agents = %v, want [claude-code]", s.Agents)
	}
	if len(s.Tenants) != 1 || s.Tenants[0] != "tenant-a" {
		t.Errorf("tenants = %v, want [tenant-a]", s.Tenants)
	}
	if len(s.Models) != 1 || s.Models[0] != "claude-model-a" {
		t.Errorf("models = %v, want [claude-model-a]", s.Models)
	}
	if s.Service != "claude-code" {
		t.Errorf("service = %q, want claude-code", s.Service)
	}

	// The ledger has one row per model request (six, two of them in the other session), with the same tokens.
	cells, err := archive.UsageSeries(time.Now().Add(-24*time.Hour), time.Now(), int64(24*time.Hour/time.Millisecond), nil)
	if err != nil {
		t.Fatal(err)
	}
	var calls, in, out int64
	for _, c := range cells {
		if c.Kind != "claude-code" || c.Tenant != "tenant-a" || c.Model != "claude-model-a" {
			t.Errorf("unexpected ledger cell %+v", c)
		}
		calls += c.Calls
		in += c.InputTokens
		out += c.OutputTokens
	}
	if calls != 9 || in != 3*(1300+10+1320) || out != 3*(50+5+30) {
		t.Errorf("ledger calls=%d in=%d out=%d, want 9 calls, in=%d out=%d", calls, in, out, 3*(1300+10+1320), 3*(50+5+30))
	}

	// On completion the session converts: the plain prompt is the user message of its interaction and the tool
	// is a tool call of the session; the redacted prompt gives no user message.
	store.completeSession("cc-session-1")
	got, ok := store.Summary("cc-session-1")
	if !ok {
		t.Fatal("session not found")
	}
	var withPrompt, toolCalls int
	for _, inv := range got.Invocations {
		if inv.UserText == "fix the build" {
			withPrompt++
		}
		if inv.UserText == "<REDACTED>" {
			t.Errorf("a redacted prompt became a user message")
		}
		for _, tc := range inv.ToolCalls {
			if tc.Name == "Bash" {
				toolCalls++
			}
		}
	}
	if withPrompt != 1 || toolCalls != 1 {
		t.Errorf("invocations %+v: prompt turns=%d Bash calls=%d, want 1 and 1", got.Invocations, withPrompt, toolCalls)
	}
}

// A tenant attribute named by --usage-tenant-attr and sent only on the resource still labels the session and its
// usage rows: resource attributes are merged into every span at decode time.
func TestResourceOnlyTenantAttribute(t *testing.T) {
	archive, err := NewSQLiteStore(filepath.Join(t.TempDir(), "rt.db"))
	if err != nil {
		t.Fatal(err)
	}
	store := NewSessionStoreWithArchive(nil, archive, SessionLimits{UsageTenantAttr: "tenant.id"})
	defer store.Close()

	body := claudeCodeExport("cc00000000000009", "cc-session-9", "<REDACTED>", time.Now().Add(-time.Hour).UnixMilli())
	res := body["resourceSpans"].([]any)[0].(map[string]any)["resource"].(map[string]any)
	res["attributes"] = []any{
		map[string]any{"key": "service.name", "value": map[string]any{"stringValue": "claude-code"}},
		map[string]any{"key": "tenant.id", "value": map[string]any{"stringValue": "tenant-b"}},
	}
	store.Ingest(body)
	if err := store.flushPersist(); err != nil {
		t.Fatal(err)
	}

	s, ok := store.Summary("cc-session-9")
	if !ok || len(s.Tenants) != 1 || s.Tenants[0] != "tenant-b" {
		t.Fatalf("session tenants = %v (found %v), want [tenant-b]", s.Tenants, ok)
	}
	cells, err := archive.UsageSeries(time.Now().Add(-24*time.Hour), time.Now(), int64(24*time.Hour/time.Millisecond), nil)
	if err != nil || len(cells) == 0 {
		t.Fatalf("usage cells = %v, err %v", cells, err)
	}
	for _, c := range cells {
		if c.Tenant != "tenant-b" {
			t.Errorf("usage cell tenant = %q, want tenant-b", c.Tenant)
		}
	}
}
