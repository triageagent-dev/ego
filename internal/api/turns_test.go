package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

func ccSpan(id, op string, startMs, durMs int64, tags map[string]any, children ...*tracepkg.Span) *tracepkg.Span {
	if tags == nil {
		tags = map[string]any{}
	}
	return &tracepkg.Span{TraceID: "t1", SpanID: id, OperationName: op, StartTime: startMs * 1000, Duration: durMs * 1000, Tags: tags, Children: children}
}

func TestTurnOfSumsHowTheTimeWentAndWhatFailed(t *testing.T) {
	llm := func(id string, in, out, cache int64) *tracepkg.Span {
		return ccSpan(id, "claude_code.llm_request", 0, 3000, map[string]any{
			"gen_ai.request.model": "m", "gen_ai.usage.input_tokens": in, "gen_ai.usage.output_tokens": out,
			"gen_ai.usage.cache_read.input_tokens": cache,
		})
	}
	okTool := ccSpan("tool1", "claude_code.tool", 0, 800, map[string]any{"gen_ai.tool.name": "Bash"},
		ccSpan("w1", opToolWait, 0, 700, map[string]any{"decision": "accept"}),
		ccSpan("r1", "claude_code.tool.execution", 0, 100, nil))
	badTool := ccSpan("tool2", "claude_code.tool", 0, 300, map[string]any{"gen_ai.tool.name": "mcp__core__say_in_workspace"},
		ccSpan("w2", opToolWait, 0, 100, map[string]any{"decision": "accept"}),
		ccSpan("r2", "claude_code.tool.execution", 0, 200, map[string]any{"otel.status_code": "ERROR"}))
	root := ccSpan("root", "claude_code.interaction", 5000, 9000, map[string]any{"user_prompt_length": int64(245)},
		llm("l1", 1000, 50, 800), okTool, llm("l2", 2000, 60, 1900), badTool)

	got, ok := turnOf("s", 2, root)
	if !ok {
		t.Fatal("a Claude Code interaction is a turn")
	}
	if got.Requests != 2 || got.Tools != 2 || got.FailedTools != 1 || !got.Failed {
		t.Errorf("counts = %+v", got)
	}
	if got.ModelMs != 6000 || got.ApprovalMs != 800 || got.ToolMs != 300 {
		t.Errorf("time = model %d tool %d approval %d, want 6000 300 800", got.ModelMs, got.ToolMs, got.ApprovalMs)
	}
	if got.InputTokens != 3000 || got.OutputTokens != 110 || got.CacheReadTokens != 2700 || got.PromptChars != 245 {
		t.Errorf("tokens = %+v", got)
	}
	if c := got.ToolCalls["say_in_workspace"]; c.Calls != 1 || c.Failed != 1 || c.Server != "core" || c.Ms != 300 {
		t.Errorf("tool counts = %+v", got.ToolCalls)
	}
	if c := got.ModelCalls["m"]; c.Calls != 2 || c.Ms != 6000 || c.Failed != 0 {
		t.Errorf("model counts = %+v", got.ModelCalls)
	}
	if c := got.ToolCalls["Bash"]; c.Calls != 1 || c.Failed != 0 {
		t.Errorf("tool counts = %+v", got.ToolCalls)
	}
	if got.StartMs != 5000 || got.DurationMs != 9000 || got.Part != 2 {
		t.Errorf("place = %+v", got)
	}
}

func TestTurnOfSkipsAStraySpan(t *testing.T) {
	if _, ok := turnOf("s", 1, ccSpan("x", "http.request", 0, 10, nil)); ok {
		t.Fatal("a root with no model request or tool call is not a turn")
	}
}

func TestTurnOfCountsARejectedToolAsFailed(t *testing.T) {
	tool := ccSpan("t", "claude_code.tool", 0, 50, map[string]any{"gen_ai.tool.name": "Bash"},
		ccSpan("w", opToolWait, 0, 40, map[string]any{"decision": "reject"}))
	got, _ := turnOf("s", 1, ccSpan("root", "claude_code.interaction", 0, 100, nil, tool))
	if got.FailedTools != 1 {
		t.Fatalf("failed tools = %d, want 1", got.FailedTools)
	}
}

func TestTurnsHandlerWindowIsInMinutes(t *testing.T) {
	h := turnsHandler(NewSessionStore(nil))
	get := func(query string) int {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, "/api/activity/turns?"+query, nil))
		return rec.Code
	}
	for query, want := range map[string]int{
		"agent=a&minutes=5":     http.StatusOK,
		"agent=a":               http.StatusOK,
		"agent=a&minutes=43200": http.StatusOK,
		"agent=a&minutes=0":     http.StatusBadRequest,
		"agent=a&minutes=43201": http.StatusBadRequest,
		"agent=a&minutes=x":     http.StatusBadRequest,
		"minutes=5":             http.StatusBadRequest,
	} {
		if got := get(query); got != want {
			t.Errorf("%s = %d, want %d", query, got, want)
		}
	}
}

// An answer is reused while nothing changed, never after a change, and the gate refuses what exceeds it.
func TestReadCacheReusesOnlyWhileNothingChanged(t *testing.T) {
	store := NewSessionStore(nil)
	before := store.changes()
	if store.changes() != before {
		t.Fatal("the change counter moved with no change")
	}
	store.Ingest(labelledTracesBody("s", "aaaa", "p1", "x"))
	if store.changes() == before {
		t.Error("the change counter did not move after an ingest, so a cached answer would be served stale")
	}

	cache := newReadCache()
	cache.put("k", []byte("answer"))
	if got, ok := cache.get("k"); !ok || string(got) != "answer" {
		t.Errorf("a fresh answer was not reused: %q %v", got, ok)
	}
	if _, ok := cache.get("k2"); ok {
		t.Error("another key hit")
	}
	cache.now = func() time.Time { return time.Now().Add(readCacheTTL + time.Second) }
	if _, ok := cache.get("k"); ok {
		t.Error("an answer older than the TTL was reused")
	}

	c := newReadCache()
	for i := 0; i < readConcurrency; i++ {
		if !c.acquire() {
			t.Fatalf("place %d refused", i)
		}
	}
	if c.acquire() {
		t.Error("a place past the concurrency limit was given")
	}
	c.release()
	if !c.acquire() {
		t.Error("a released place was not given again")
	}
}

func TestRoleKeySeparatesWhatRolesSee(t *testing.T) {
	a := roleKey(RoleInfo{Role: RoleMember, Tenants: []string{"x/1"}})
	b := roleKey(RoleInfo{Role: RoleMember, Tenants: []string{"x/2"}})
	c := roleKey(RoleInfo{Role: RoleMember})
	if a == b || a == c || b == c {
		t.Errorf("roles that see different things share a cache key: %q %q %q", a, b, c)
	}
}

func TestTurnOfRecordsWhatFollowedWhat(t *testing.T) {
	llm := func(id string, at int64) *tracepkg.Span {
		return ccSpan(id, "claude_code.llm_request", at, 10, map[string]any{"gen_ai.request.model": "m"})
	}
	tool := func(id string, at int64, name string) *tracepkg.Span {
		return ccSpan(id, "claude_code.tool", at, 10, map[string]any{"gen_ai.tool.name": name})
	}
	// Out of time order on purpose: the order is the one the steps began in.
	root := ccSpan("root", "claude_code.interaction", 0, 100, nil,
		tool("t2", 40, "mcp__core__say"), llm("l1", 0), tool("t1", 20, "Bash"), llm("l2", 30), llm("l3", 60))
	got, _ := turnOf("s", 0, root)
	want := map[[2]string]int{
		{"m:m", "t:built-in/Bash"}: 1,
		{"t:built-in/Bash", "m:m"}: 1,
		{"m:m", "t:core/say"}:      1,
		{"t:core/say", "m:m"}:      1,
	}
	if len(got.Flow) != len(want) {
		t.Fatalf("flow = %+v, want %d edges", got.Flow, len(want))
	}
	for _, e := range got.Flow {
		if want[[2]string{e.From, e.To}] != e.N {
			t.Errorf("unexpected edge %+v in %+v", e, got.Flow)
		}
	}
}
