package otlp

import (
	"math"
	"strconv"
	"strings"

	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

// Claude Code's own OpenTelemetry traces use their own attribute names (input_tokens, session.id, tool_name,
// ...) and set no gen_ai.usage.* attributes, so without a translation their spans would reach ego as plain
// spans: no token usage, no session, no tool calls. normalizeClaudeCode adds the GenAI attributes ego reads,
// beside the originals, at ingest. It only fills gaps: an attribute the sender already set is never replaced,
// and a span whose name does not start with claudeCodeSpanPrefix is not touched at all. The prefix, not
// service.name, marks the span, because OTEL_SERVICE_NAME may rename the service.
const claudeCodeSpanPrefix = "claude_code."

const (
	ccLLMRequest  = "claude_code.llm_request"
	ccTool        = "claude_code.tool"
	ccInteraction = "claude_code.interaction"

	// ccRedacted is what Claude Code sends as user_prompt unless the operator opted in to prompt content.
	ccRedacted = "<REDACTED>"
)

const (
	attrConversationID   = "gen_ai.conversation.id"
	attrSessionName      = "ego.session_name"
	attrAgentName        = "gen_ai.agent.name"
	attrOperationName    = "gen_ai.operation.name"
	attrRequestModel     = "gen_ai.request.model"
	attrFinishReasons    = "gen_ai.response.finish_reasons"
	attrUsageInput       = "gen_ai.usage.input_tokens"
	attrUsageOutput      = "gen_ai.usage.output_tokens"
	attrUsageCacheRead   = "gen_ai.usage.cache_read.input_tokens"
	attrUsageCacheCreate = "gen_ai.usage.cache_creation.input_tokens"
	attrUsageCacheWrite  = "gen_ai.usage.cache_write.input_tokens"
	attrToolName         = "gen_ai.tool.name"
	attrToolCallID       = "gen_ai.tool.call.id"
	attrInputValue       = "input.value"
	attrServiceName      = "service.name"
)

func normalizeClaudeCode(span *tracepkg.Span) {
	if !strings.HasPrefix(span.OperationName, claudeCodeSpanPrefix) {
		return
	}
	if span.Tags == nil {
		span.Tags = map[string]any{}
	}
	tags := span.Tags

	// One Claude Code session is many traces (one per prompt) and may span processes (`--resume`), so the
	// session id, not the trace id or the process, is what groups the spans.
	if absent(tags, attrSessionName) && absent(tags, attrConversationID) {
		if id, _ := tags["session.id"].(string); id != "" {
			tags[attrConversationID] = id
		}
	}

	// The resource's own gen_ai.agent.name is already merged into the span's tags when present, so what is
	// left to fall back on is the service name.
	if absent(tags, attrAgentName) {
		if name, _ := tags[attrServiceName].(string); name != "" {
			tags[attrAgentName] = name
		}
	}

	switch span.OperationName {
	case ccLLMRequest:
		normalizeClaudeCodeLLM(tags)
	case ccTool:
		normalizeClaudeCodeTool(tags)
	case ccInteraction:
		// A prompt the sender redacted is no prompt: it must not become the session's user message.
		if p, _ := tags["user_prompt"].(string); absent(tags, attrInputValue) && strings.TrimSpace(p) != "" && strings.TrimSpace(p) != ccRedacted {
			tags[attrInputValue] = p
		}
	}
}

func normalizeClaudeCodeLLM(tags map[string]any) {
	setIfAbsent(tags, attrOperationName, "chat")

	if model, _ := tags["model"].(string); model != "" {
		setIfAbsent(tags, attrRequestModel, model)
	}
	if stop, _ := tags["stop_reason"].(string); stop != "" && absent(tags, attrFinishReasons) {
		tags[attrFinishReasons] = []any{stop}
	}

	in, hasIn := intAttr(tags, "input_tokens")
	read, hasRead := intAttr(tags, "cache_read_tokens")
	create, hasCreate := intAttr(tags, "cache_creation_tokens")
	out, hasOut := intAttr(tags, "output_tokens")

	// Claude Code's input_tokens leaves out cached tokens; the GenAI convention counts them in the input.
	if hasIn || hasRead || hasCreate {
		setIfAbsent(tags, attrUsageInput, in+read+create)
	}
	if hasOut {
		setIfAbsent(tags, attrUsageOutput, out)
	}
	if hasRead {
		setIfAbsent(tags, attrUsageCacheRead, read)
	}
	if hasCreate {
		// The convention's name for this changed between versions; a reader may know either.
		setIfAbsent(tags, attrUsageCacheCreate, create)
		setIfAbsent(tags, attrUsageCacheWrite, create)
	}

	// A failed request is an error span: the usage view counts failed LLM calls from otel.status_code.
	if ok, has := tags["success"].(bool); has && !ok {
		setIfAbsent(tags, "otel.status_code", "ERROR")
		if msg, _ := tags["error"].(string); msg != "" {
			setIfAbsent(tags, "otel.status_description", msg)
		}
	}
}

func normalizeClaudeCodeTool(tags map[string]any) {
	setIfAbsent(tags, attrOperationName, "execute_tool")
	name, _ := tags["tool_name"].(string)
	if name == "" {
		name, _ = tags["tool_name_safe"].(string)
	}
	if name != "" {
		setIfAbsent(tags, attrToolName, name)
	}
	if id, _ := tags["tool_use_id"].(string); id != "" {
		setIfAbsent(tags, attrToolCallID, id)
	}
}

// absent reports whether key is unset or an empty string.
func absent(tags map[string]any, key string) bool {
	v, ok := tags[key]
	if !ok || v == nil {
		return true
	}
	s, isString := v.(string)
	return isString && s == ""
}

func setIfAbsent(tags map[string]any, key string, value any) {
	if absent(tags, key) {
		tags[key] = value
	}
}

// intAttr reads a token count, which a sender may encode as an int, a double or a numeric string.
func intAttr(tags map[string]any, key string) (int64, bool) {
	switch n := tags[key].(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return 0, false
		}
		return int64(n), true
	case string:
		v, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
		return v, err == nil
	}
	return 0, false
}
