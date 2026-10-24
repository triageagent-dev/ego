package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/triageagent-dev/ego/internal/adk"
	"github.com/triageagent-dev/ego/internal/otlp"
	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

// Turns of an always-on agent. A turn is one trace of a session: the work an agent did for one prompt or one wake
// (Claude Code makes a trace per prompt, a chat per message). The view of an agent over time needs a row per turn
// across all its sessions, with how the time went (model, tools, waiting for approval), what failed and what it cost,
// and not the spans themselves: the spans of the one turn on show are read when it is picked.

const (
	defaultTurnMinutes = 24 * 60
	maxTurnMinutes     = 30 * 24 * 60
	// maxTurnSegments bounds the segments read for one answer, newest first; maxTurnRows the turns returned.
	maxTurnSegments = 80
	maxTurnRows     = 3000

	opToolWait = "claude_code.tool.blocked_on_user"
)

// ToolCount is how often a tool (or a model) was called in a turn, how often the call failed and how long the calls
// took. Server names the MCP server a tool belongs to; it is empty for a built-in tool and for a model.
type ToolCount struct {
	Calls  int    `json:"calls"`
	Failed int    `json:"failed,omitempty"`
	Ms     int64  `json:"ms,omitempty"`
	Server string `json:"server,omitempty"`
}

// TurnRow is one turn of GET /api/activity/turns.
type TurnRow struct {
	Session string `json:"session"`
	Part    int    `json:"part,omitempty"`
	TraceID string `json:"traceId"`
	// RootSpanID names the turn's root span, which is how the spans of a session are matched to the row.
	RootSpanID string `json:"rootSpanId"`
	Operation  string `json:"operation"`
	StartMs    int64  `json:"startMs"`
	DurationMs int64  `json:"durationMs"`
	// Failed is true when any span of the turn has an error status; FailedTools counts the tool calls that failed
	// or were rejected.
	Failed      bool `json:"failed,omitempty"`
	FailedTools int  `json:"failedTools,omitempty"`
	Requests    int  `json:"requests"`
	Tools       int  `json:"tools"`
	// ModelMs, ToolMs and ApprovalMs add up the time of the turn's model requests, of its tool calls without the
	// wait for approval, and of that wait. The rest of the turn is the agent itself.
	ModelMs     int64 `json:"modelMs"`
	ToolMs      int64 `json:"toolMs"`
	ApprovalMs  int64 `json:"approvalMs"`
	InputTokens int64 `json:"inputTokens"`
	// CacheReadTokens is the part of InputTokens read from the model's cache.
	CacheReadTokens int64 `json:"cacheReadTokens,omitempty"`
	OutputTokens    int64 `json:"outputTokens"`
	// PromptChars is the length of the prompt, when the sender says.
	PromptChars int                  `json:"promptChars,omitempty"`
	ToolCalls   map[string]ToolCount `json:"toolCalls,omitempty"`
	// ModelCalls counts the model requests by model.
	ModelCalls map[string]ToolCount `json:"modelCalls,omitempty"`
}

// TurnSegment places one session of the subject on the same time axis as the turns.
type TurnSegment struct {
	Session string `json:"session"`
	Part    int    `json:"part,omitempty"`
	StartMs int64  `json:"startMs"`
	EndMs   int64  `json:"endMs"`
}

// TurnsResult is the answer of GET /api/activity/turns.
type TurnsResult struct {
	Agent     string        `json:"agent"`
	Tenant    string        `json:"tenant,omitempty"`
	FromMs    int64         `json:"fromMs"`
	ToMs      int64         `json:"toMs"`
	Segments  []TurnSegment `json:"segments"`
	Turns     []TurnRow     `json:"turns"`
	Truncated bool          `json:"truncated,omitempty"`
}

func tagInt(sp *tracepkg.Span, key string) int64 {
	switch n := sp.Tags[key].(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	case string:
		v, _ := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
		return v
	}
	return 0
}

func isToolCallSpan(sp *tracepkg.Span) bool {
	return sp.TagString("gen_ai.tool.name") != "" || sp.TagString("gen_ai.operation.name") == "execute_tool"
}

func spanErrored(sp *tracepkg.Span) bool { return sp.TagString("otel.status_code") == "ERROR" }

// subtreeErrored is true when sp or any span below it has an error status, or a tool call was refused.
func subtreeErrored(sp *tracepkg.Span) bool {
	if spanErrored(sp) || sp.TagString("decision") == "reject" {
		return true
	}
	for _, c := range sp.Children {
		if subtreeErrored(c) {
			return true
		}
	}
	return false
}

// toolName is the tool's own name and the MCP server it belongs to: a tool of an MCP server is named
// mcp__<server>__<tool>, and the sender may also say the server in an attribute.
func toolName(sp *tracepkg.Span) (name, server string) {
	for _, k := range []string{"gen_ai.tool.name", "tool_name"} {
		if n := sp.TagString(k); n != "" {
			name = n
			break
		}
	}
	if name == "" {
		name = sp.OperationName
	}
	if rest, ok := strings.CutPrefix(name, "mcp__"); ok {
		if i := strings.Index(rest, "__"); i > 0 {
			return rest[i+2:], rest[:i]
		}
	}
	return name, sp.TagString("anthropic.mcp.server.name")
}

func modelName(sp *tracepkg.Span) string {
	for _, k := range []string{"gen_ai.request.model", "model", "gen_ai.response.model"} {
		if m := sp.TagString(k); m != "" && m != "unknown" {
			return m
		}
	}
	return "model"
}

// turnOf summarizes the turn that root starts, or reports false for a root that is no turn: a stray span with no
// model request or tool call under it.
func turnOf(session string, part int, root *tracepkg.Span) (TurnRow, bool) {
	row := TurnRow{
		Session: session, Part: part, TraceID: root.TraceID, RootSpanID: root.SpanID, Operation: root.OperationName,
		StartMs: root.StartTime / 1000, DurationMs: root.Duration / 1000,
		PromptChars: int(tagInt(root, "user_prompt_length")),
	}
	var toolUs, approvalUs int64
	var walk func(sp *tracepkg.Span)
	walk = func(sp *tracepkg.Span) {
		switch {
		case adk.IsLLMSpan(sp):
			row.Requests++
			row.ModelMs += sp.Duration / 1000
			in, out, _ := adk.ExtractTokenUsageFromAttrs(sp.Tags)
			row.InputTokens += in
			row.OutputTokens += out
			row.CacheReadTokens += tagInt(sp, "gen_ai.usage.cache_read.input_tokens")
			if row.ModelCalls == nil {
				row.ModelCalls = map[string]ToolCount{}
			}
			m := modelName(sp)
			mc := row.ModelCalls[m]
			mc.Calls++
			mc.Ms += sp.Duration / 1000
			if spanErrored(sp) {
				mc.Failed++
			}
			row.ModelCalls[m] = mc
		case isToolCallSpan(sp):
			row.Tools++
			toolUs += sp.Duration
			name, server := toolName(sp)
			bad := subtreeErrored(sp)
			if row.ToolCalls == nil {
				row.ToolCalls = map[string]ToolCount{}
			}
			c := row.ToolCalls[name]
			c.Calls++
			c.Ms += sp.Duration / 1000
			c.Server = server
			if bad {
				c.Failed++
				row.FailedTools++
			}
			row.ToolCalls[name] = c
		case sp.OperationName == opToolWait:
			approvalUs += sp.Duration
		}
		if spanErrored(sp) {
			row.Failed = true
		}
		for _, c := range sp.Children {
			walk(c)
		}
	}
	walk(root)
	if row.Requests == 0 && row.Tools == 0 && !strings.HasPrefix(root.OperationName, claudeCodeInteraction) {
		return TurnRow{}, false
	}
	row.ApprovalMs = approvalUs / 1000
	if t := toolUs - approvalUs; t > 0 {
		row.ToolMs = t / 1000
	}
	return row, true
}

const claudeCodeInteraction = "claude_code.interaction"

// turnRows lists the turns of one session. The walk runs under s.mu, like SpanRows: BuildTrace rewires the shared
// span pointers' children.
func (s *SessionStore) turnRows(sessionID string, part int) ([]TurnRow, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.lookupLocked(sessionID)
	if !ok {
		return nil, false
	}
	tr := otlp.BuildTrace(sessionID, session.Spans)
	var out []TurnRow
	for _, root := range tr.RootSpans {
		if row, ok := turnOf(sessionID, part, root); ok {
			out = append(out, row)
		}
	}
	return out, true
}

// Turns lists the turns of one subject (an agent in a tenant) that began in the last window, oldest first, with the
// sessions they belong to.
func (s *SessionStore) Turns(sel subjectKey, window time.Duration, info RoleInfo) TurnsResult {
	now := time.Now()
	res := TurnsResult{Agent: sel.agent, Tenant: sel.tenant, FromMs: now.Add(-window).UnixMilli(), ToMs: now.UnixMilli(),
		Segments: []TurnSegment{}, Turns: []TurnRow{}}
	var segs []activityRow
	for _, r := range s.activityRows(time.UnixMilli(res.FromMs), info) {
		for _, k := range r.keys() {
			if k == sel || (sel.tenant == "" && k.agent == sel.agent) {
				segs = append(segs, r)
				break
			}
		}
	}
	sort.SliceStable(segs, func(i, j int) bool { return segs[i].endMs > segs[j].endMs })
	if len(segs) > maxTurnSegments {
		segs, res.Truncated = segs[:maxTurnSegments], true
	}
	for _, seg := range segs {
		rows, ok := s.turnRows(seg.id, seg.part)
		if !ok {
			continue
		}
		res.Segments = append(res.Segments, TurnSegment{Session: seg.id, Part: seg.part, StartMs: seg.startMs, EndMs: seg.endMs})
		for _, r := range rows {
			if r.StartMs+r.DurationMs >= res.FromMs {
				res.Turns = append(res.Turns, r)
			}
		}
	}
	sort.SliceStable(res.Turns, func(i, j int) bool { return res.Turns[i].StartMs < res.Turns[j].StartMs })
	sort.SliceStable(res.Segments, func(i, j int) bool { return res.Segments[i].StartMs < res.Segments[j].StartMs })
	if len(res.Turns) > maxTurnRows {
		res.Turns, res.Truncated = res.Turns[len(res.Turns)-maxTurnRows:], true
	}
	return res
}

// turnsHandler implements GET /api/activity/turns?agent=&tenant=&minutes=.
func turnsHandler(store *SessionStore) http.HandlerFunc {
	cache := newReadCache()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		q := r.URL.Query()
		agent := strings.TrimSpace(q.Get("agent"))
		if agent == "" {
			writeEvaluateError(w, http.StatusBadRequest, "agent is required")
			return
		}
		minutes := defaultTurnMinutes
		if v := q.Get("minutes"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > maxTurnMinutes {
				writeEvaluateError(w, http.StatusBadRequest, "minutes must be between 1 and "+strconv.Itoa(maxTurnMinutes))
				return
			}
			minutes = n
		}
		info := roleInfoOrOpen(r.Context())
		tenant := strings.TrimSpace(q.Get("tenant"))
		key := roleKey(info) + "|" + agent + "|" + tenant + "|" + strconv.Itoa(minutes) + "|" + strconv.FormatUint(store.changes(), 10)
		w.Header().Set("Content-Type", "application/json")
		if body, ok := cache.get(key); ok {
			_, _ = w.Write(body)
			return
		}
		if !cache.acquire() {
			w.Header().Set("Retry-After", "2")
			writeEvaluateError(w, http.StatusTooManyRequests, "busy: try again in a moment")
			return
		}
		defer cache.release()
		body, err := json.Marshal(map[string]any{"data": store.Turns(subjectKey{agent, tenant}, time.Duration(minutes)*time.Minute, info), "error": nil})
		if err != nil {
			writeEvaluateError(w, http.StatusInternalServerError, "failed to encode turns")
			return
		}
		body = append(body, '\n')
		cache.put(key, body)
		_, _ = w.Write(body)
	}
}
