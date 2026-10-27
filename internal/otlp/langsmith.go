package otlp

import (
	"encoding/json"
	"fmt"
	"strings"

	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

// LangSmith's OpenTelemetry export is what LangChain documents for LangChain and LangGraph (langsmith[otel] with
// LANGSMITH_OTEL_ENABLED). It does not use the GenAI message attributes: each run's whole input and output
// arrive as the run's own JSON in gen_ai.prompt and gen_ai.completion (LangChain messages, a graph's state, a
// tool's arguments), and the graph's thread id is a metadata attribute. Without a translation the user message
// of a LangGraph trace would be that JSON. normalizeLangSmith adds the attributes ego reads beside the
// originals and only fills gaps, like normalizeClaudeCode. The langsmith.span.kind attribute marks the span.
const langSmithKind = "langsmith.span.kind"

const langSmithMetadata = "langsmith.metadata."

const (
	attrPrompt         = "gen_ai.prompt"
	attrCompletion     = "gen_ai.completion"
	attrInputMessages  = "gen_ai.input.messages"
	attrOutputMessages = "gen_ai.output.messages"
	attrToolArguments  = "gen_ai.tool.call.arguments"
	attrToolResult     = "gen_ai.tool.call.result"
	attrOutputValue    = "output.value"
	attrResponseModel  = "gen_ai.response.model"
)

func normalizeLangSmith(span *tracepkg.Span, b *decodeBudget) {
	kind, _ := span.Tags[langSmithKind].(string)
	if kind == "" {
		return
	}
	tags := span.Tags

	// LangGraph puts configurable.thread_id into every run's metadata, so the thread, not the trace, is the
	// conversation. langsmith.trace.session_id is the LangSmith project and must not group sessions.
	if absent(tags, attrSessionName) && absent(tags, attrConversationID) {
		for _, key := range []string{"thread_id", "conversation_id", "session_id"} {
			if id := scalarString(tags[langSmithMetadata+key]); id != "" {
				tags[attrConversationID] = id
				break
			}
		}
	}

	switch kind {
	case "llm":
		normalizeLangSmithLLM(tags, b)
	case "tool":
		normalizeLangSmithTool(tags, b)
	case "chain":
		if span.ParentSpanID == "" {
			normalizeLangSmithRoot(span, b)
		}
	}
}

// normalizeLangSmithRoot makes a trace's root run the agent invocation, with the turn's user message and answer
// read from the graph's state.
func normalizeLangSmithRoot(span *tracepkg.Span, b *decodeBudget) {
	tags := span.Tags
	if op, _ := tags[attrOperationName].(string); op == "" || op == "chain" {
		tags[attrOperationName] = "invoke_agent"
	}
	setIfAbsent(tags, attrAgentName, span.OperationName)

	if msgs := lsInputMessages(lsDecode(tags[attrPrompt], b)); len(msgs) > 0 {
		for i := len(msgs) - 1; i >= 0; i-- {
			if text := lsText(msgs[i]); msgs[i]["role"] == "user" && text != "" {
				setIfAbsent(tags, attrInputValue, text)
				break
			}
		}
	}
	// The last message of the final state is the answer; an earlier assistant message belongs to an earlier turn.
	if msgs := lsOutputMessages(lsDecode(tags[attrCompletion], b)); len(msgs) > 0 {
		last := msgs[len(msgs)-1]
		if text := lsText(last); last["role"] == "assistant" && text != "" {
			setIfAbsent(tags, attrOutputValue, text)
		}
	}
}

func normalizeLangSmithLLM(tags map[string]any, b *decodeBudget) {
	// A span is an LLM call to ego when it names a model; LangSmith leaves the name out when the model class
	// does not report one, and the call must still count.
	model, _ := tags[attrRequestModel].(string)
	if model == "" {
		model = scalarString(tags[langSmithMetadata+"ls_model_name"])
	}
	if model == "" {
		model, _ = tags[attrResponseModel].(string)
	}
	if model == "" {
		model = "unknown"
	}
	setIfAbsent(tags, attrRequestModel, model)

	if absent(tags, attrInputMessages) {
		if msgs := lsInputMessages(lsDecode(tags[attrPrompt], b)); len(msgs) > 0 {
			tags[attrInputMessages] = jsonString(msgs)
		}
	}
	if absent(tags, attrOutputMessages) {
		if msgs := lsOutputMessages(lsDecode(tags[attrCompletion], b)); len(msgs) > 0 {
			tags[attrOutputMessages] = jsonString(msgs)
		}
	}
}

func normalizeLangSmithTool(tags map[string]any, b *decodeBudget) {
	setIfAbsent(tags, attrOperationName, "execute_tool")

	if absent(tags, attrToolArguments) {
		if args := lsToolArguments(lsDecode(tags[attrPrompt], b)); args != nil {
			tags[attrToolArguments] = jsonString(args)
		}
	}
	if absent(tags, attrToolResult) {
		if res, ok := lsToolResult(lsDecode(tags[attrCompletion], b)); ok {
			tags[attrToolResult] = res
		}
	}
}

// lsToolArguments reads a tool run's input. LangChain passes the arguments as an object; a tool called with a
// single string argument arrives as {"input": "..."} and that string may itself be a JSON object.
func lsToolArguments(v any) map[string]any {
	m, ok := v.(map[string]any)
	if !ok {
		if v == nil {
			return nil
		}
		return map[string]any{"input": v}
	}
	if s, isStr := m["input"].(string); isStr && len(m) == 1 {
		var inner map[string]any
		if tracepkg.JSONFits(s, maxPayloadBytes, maxPayloadElements) && json.Unmarshal([]byte(s), &inner) == nil {
			return inner
		}
	}
	return m
}

// lsToolResult returns the tool run's output as the text ego stores for a tool result. A ToolMessage with
// status "error" is a failed call and says so the way an MCP tool result does.
func lsToolResult(v any) (string, bool) {
	if v == nil {
		return "", false
	}
	out := v
	if m, ok := v.(map[string]any); ok {
		if o, has := m["output"]; has {
			out = o
		}
	}
	text, failed := "", false
	if msg, ok := lsMessage(out); ok {
		text = lsText(msg)
		if body := lsBody(out); body != nil {
			failed = body["status"] == "error"
		}
	} else if s, ok := out.(string); ok {
		text = s
	} else {
		text = jsonString(out)
	}
	if failed {
		return jsonString(map[string]any{"isError": true, "result": text}), true
	}
	return text, true
}

// What a payload may cost to decode. A run's input or output is a conversation, rarely over a few hundred kilobytes;
// a decoded value takes many times the room of its text, so a payload over the size or element limit is left as it
// came, and one request may not decode more than maxRequestDecode bytes of them whatever their number.
const (
	maxPayloadBytes    = 1 << 20
	maxPayloadElements = 100_000
	maxRequestDecode   = 4 << 20
)

// decodeBudget is what one request may still decode. A nil budget has no limit of its own (a trace file read by
// the CLI is the operator's); the limits on a single payload apply all the same.
type decodeBudget struct{ left int }

func newDecodeBudget() *decodeBudget { return &decodeBudget{left: maxRequestDecode} }

func (b *decodeBudget) take(n int) bool {
	if b == nil {
		return true
	}
	if n > b.left {
		return false
	}
	b.left -= n
	return true
}

// lsDecode decodes the JSON a run's input or output was sent as; nil when it is not JSON, is too big to decode or
// would use more than the request's share.
func lsDecode(raw any, b *decodeBudget) any {
	s, ok := raw.(string)
	if !ok || strings.TrimSpace(s) == "" || !tracepkg.JSONFits(s, maxPayloadBytes, maxPayloadElements) || !b.take(len(s)) {
		return nil
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil
	}
	return v
}

// lsInputMessages reads the messages a run was given: a graph's state, a chat model's batch of messages, or a
// string prompt.
func lsInputMessages(payload any) []map[string]any {
	switch v := payload.(type) {
	case map[string]any:
		if m, ok := v["messages"]; ok {
			return lsMessages(m)
		}
		if s, ok := v["input"].(string); ok && s != "" {
			return []map[string]any{{"role": "user", "content": s}}
		}
		if prompts, ok := v["prompts"].([]any); ok {
			return lsMessages(prompts)
		}
	case []any, string:
		return lsMessages(v)
	}
	return nil
}

// lsOutputMessages reads what a run returned: a chat model's generations, a graph's state, an OpenAI-style
// response, or a single message or string.
func lsOutputMessages(payload any) []map[string]any {
	switch v := payload.(type) {
	case map[string]any:
		if gens, ok := v["generations"].([]any); ok {
			for _, batch := range gens {
				for _, g := range asAnySlice(batch) {
					gm, _ := g.(map[string]any)
					if msg, ok := lsMessage(gm["message"]); ok {
						return []map[string]any{msg}
					}
					if text, _ := gm["text"].(string); text != "" {
						return []map[string]any{{"role": "assistant", "content": text}}
					}
				}
			}
			return nil
		}
		if choices, ok := v["choices"].([]any); ok {
			var out []map[string]any
			for _, c := range choices {
				cm, _ := c.(map[string]any)
				if msg, ok := lsMessage(cm["message"]); ok {
					out = append(out, msg)
				}
			}
			return out
		}
		if m, ok := v["messages"]; ok {
			return lsMessages(m)
		}
		if o, ok := v["output"]; ok {
			if s, isStr := o.(string); isStr {
				if s == "" {
					return nil
				}
				return []map[string]any{{"role": "assistant", "content": s}}
			}
			return lsMessages(o)
		}
		if msg, ok := lsMessage(v); ok {
			return []map[string]any{msg}
		}
	case []any, string:
		return lsMessages(v)
	}
	return nil
}

// lsMessages flattens whatever holds messages: a list, a batch of lists, ("role", "text") tuples, a bare string
// (LangGraph treats it as one user message) or one message.
func lsMessages(v any) []map[string]any {
	switch x := v.(type) {
	case string:
		if x == "" {
			return nil
		}
		return []map[string]any{{"role": "user", "content": x}}
	case []any:
		if len(x) == 2 {
			role, _ := x[0].(string)
			text, isText := x[1].(string)
			if r := lsRole(role); r != "" && isText {
				return []map[string]any{{"role": r, "content": text}}
			}
		}
		var out []map[string]any
		for _, e := range x {
			out = append(out, lsMessages(e)...)
		}
		return out
	case map[string]any:
		if msg, ok := lsMessage(x); ok {
			return []map[string]any{msg}
		}
	}
	return nil
}

// lsBody returns the fields of a LangChain message, whether it was sent in the constructor form
// ({"lc": 1, "id": [..., "HumanMessage"], "kwargs": {...}}) or as its plain dump.
func lsBody(v any) map[string]any {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	if m["lc"] != nil {
		kw, _ := m["kwargs"].(map[string]any)
		return kw
	}
	return m
}

// lsMessage normalizes one message to the {role, content, tool_calls, tool_call_id} shape the GenAI converter
// reads. A map that is not a message (a generation, a state) is not one.
func lsMessage(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, false
	}
	body := lsBody(m)
	if body == nil {
		return nil, false
	}
	role := lsRole(scalarString(body["role"]))
	if role == "" {
		role = lsRole(scalarString(body["type"]))
	}
	if role == "" && m["lc"] != nil {
		if id, _ := m["id"].([]any); len(id) > 0 {
			role = lsRole(scalarString(id[len(id)-1]))
		}
	}
	if role == "" {
		return nil, false
	}

	msg := map[string]any{"role": role, "content": lsContentText(body["content"])}
	if calls := lsToolCalls(body); len(calls) > 0 {
		msg["tool_calls"] = calls
	}
	if id := scalarString(body["tool_call_id"]); id != "" {
		msg["tool_call_id"] = id
	}
	return msg, true
}

// lsRole maps the names LangChain and OpenAI use for a message's author to the roles the converter knows.
func lsRole(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimSuffix(s, "chunk")
	s = strings.TrimSuffix(s, "message")
	switch s {
	case "human", "user":
		return "user"
	case "ai", "assistant":
		return "assistant"
	case "system", "developer":
		return "system"
	case "tool", "function":
		return "tool"
	}
	return ""
}

func lsText(msg map[string]any) string {
	s, _ := msg["content"].(string)
	return s
}

// lsContentText joins the text of a message's content, a string or a list of blocks.
func lsContentText(c any) string {
	switch x := c.(type) {
	case string:
		return x
	case []any:
		var parts []string
		for _, b := range x {
			switch blk := b.(type) {
			case string:
				parts = append(parts, blk)
			case map[string]any:
				if t, _ := blk["text"].(string); t != "" {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, " ")
	}
	return ""
}

// lsToolCalls reads a message's tool calls in the three places they occur: LangChain's tool_calls
// ({name, args, id}), OpenAI's ({function: {name, arguments}, id}) there or under additional_kwargs, and
// Anthropic tool_use content blocks.
func lsToolCalls(body map[string]any) []any {
	var out []any
	add := func(id, name string, args map[string]any) {
		if name == "" {
			return
		}
		if args == nil {
			args = map[string]any{}
		}
		out = append(out, map[string]any{
			"id": id, "type": "function",
			"function": map[string]any{"name": name, "arguments": args},
		})
	}
	read := func(list any) {
		for _, raw := range asAnySlice(list) {
			tc, _ := raw.(map[string]any)
			if fn, ok := tc["function"].(map[string]any); ok {
				add(scalarString(tc["id"]), scalarString(fn["name"]), lsArgs(fn["arguments"]))
				continue
			}
			add(scalarString(tc["id"]), scalarString(tc["name"]), lsArgs(tc["args"]))
		}
	}
	read(body["tool_calls"])
	if len(out) == 0 {
		if extra, ok := body["additional_kwargs"].(map[string]any); ok {
			read(extra["tool_calls"])
		}
	}
	if len(out) == 0 {
		for _, b := range asAnySlice(body["content"]) {
			if blk, ok := b.(map[string]any); ok && blk["type"] == "tool_use" {
				add(scalarString(blk["id"]), scalarString(blk["name"]), lsArgs(blk["input"]))
			}
		}
	}
	return out
}

// lsArgs reads tool arguments sent as an object or as a JSON string.
func lsArgs(v any) map[string]any {
	switch x := v.(type) {
	case map[string]any:
		return x
	case string:
		var m map[string]any
		if tracepkg.JSONFits(x, maxPayloadBytes, maxPayloadElements) && json.Unmarshal([]byte(x), &m) == nil {
			return m
		}
	}
	return nil
}

func asAnySlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func jsonString(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// scalarString renders an attribute a sender may encode as a string or a number.
func scalarString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case int64, int, float64, bool:
		return fmt.Sprint(x)
	}
	return ""
}
