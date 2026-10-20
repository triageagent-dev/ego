package adk

import (
	"encoding/json"
	"strings"

	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

// parseJSON decodes raw into a generic JSON value. raw may already be a decoded map/slice (as produced by Go's
// own json.Unmarshal of the surrounding tag), or a string containing embedded JSON (.
func parseJSON(raw any) any {
	switch v := raw.(type) {
	case nil:
		return nil
	case string:
		var out any
		if err := json.Unmarshal([]byte(v), &out); err != nil {
			return nil
		}
		return out
	default:
		return v
	}
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func getContents(m map[string]any) []any {
	if v, ok := m["contents"]; ok {
		return asSlice(v)
	}
	return asSlice(m["Contents"])
}

func getContentField(m map[string]any) map[string]any {
	if v, ok := m["content"]; ok {
		return asMap(v)
	}
	return asMap(m["Content"])
}

// IsADKScope reports whether span was emitted by Google ADK instrumentation.
func IsADKScope(span *tracepkg.Span) bool {
	if span.TagString(OtelScope) == AdkScopeValue {
		return true
	}
	if span.TagString(GenAISystem) == AdkScopeValue {
		return true
	}
	for _, marker := range adkMarkerTags {
		if span.HasTag(marker) {
			return true
		}
	}
	return false
}

// HasADKDescendant reports whether any descendant of span is ADK-instrumented.
func HasADKDescendant(span *tracepkg.Span) bool {
	for _, child := range span.Children {
		if IsADKScope(child) || HasADKDescendant(child) {
			return true
		}
	}
	return false
}

func isADKGenerateContentLLMSpan(span *tracepkg.Span) bool {
	if !strings.HasPrefix(span.OperationName, "generate_content") && span.TagString(GenAIOp) != "generate_content" {
		return false
	}
	return span.HasTag(AdkLLMRequest) || span.HasTag(AdkLLMResponse)
}

// FindADKLLMSpansIn returns root's descendant call_llm spans (preferred) or, failing that, ADK generate_content
// spans, sorted by start time.
func FindADKLLMSpansIn(root *tracepkg.Span) []*tracepkg.Span {
	var callLLM, generateContent []*tracepkg.Span
	var walk func(*tracepkg.Span)
	walk = func(span *tracepkg.Span) {
		for _, child := range span.Children {
			if strings.HasPrefix(child.OperationName, "call_llm") {
				callLLM = append(callLLM, child)
			} else if isADKGenerateContentLLMSpan(child) {
				generateContent = append(generateContent, child)
			}
			walk(child)
		}
	}
	walk(root)
	sortByStart(callLLM)
	sortByStart(generateContent)
	if len(callLLM) > 0 {
		return callLLM
	}
	return generateContent
}

// FindChildrenByOp returns every descendant of root (not including root itself) whose operation name has the
// given prefix, sorted by start time.
func FindChildrenByOp(root *tracepkg.Span, opPrefix string) []*tracepkg.Span {
	var acc []*tracepkg.Span
	var walk func(*tracepkg.Span)
	walk = func(span *tracepkg.Span) {
		for _, child := range span.Children {
			if strings.HasPrefix(child.OperationName, opPrefix) {
				acc = append(acc, child)
			}
			walk(child)
		}
	}
	walk(root)
	sortByStart(acc)
	return acc
}

func sortByStart(spans []*tracepkg.Span) {
	for i := 1; i < len(spans); i++ {
		for j := i; j > 0 && spans[j-1].StartTime > spans[j].StartTime; j-- {
			spans[j-1], spans[j] = spans[j], spans[j-1]
		}
	}
}

// extractTextFromMessage reads a GenAI semconv message's text content, content- or parts-based.
func extractTextFromMessage(msg map[string]any) string {
	switch content := msg["content"].(type) {
	case string:
		if content != "" {
			return content
		}
	case []any:
		var parts []string
		for _, item := range content {
			if m, ok := item.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, " ")
		}
	}

	if partsRaw, ok := msg["parts"].([]any); ok {
		var texts []string
		for _, p := range partsRaw {
			part, ok := p.(map[string]any)
			if !ok || part["type"] != "text" {
				continue
			}
			text, _ := part["content"].(string)
			if text == "" {
				text, _ = part["text"].(string)
			}
			if text != "" {
				texts = append(texts, text)
			}
		}
		if len(texts) > 0 {
			return strings.Join(texts, " ")
		}
	}

	return ""
}

// ExtractUserTextFromAttrs extracts user input text from span attributes, ADK-first.
func ExtractUserTextFromAttrs(tags map[string]any) string {
	if llmRequest := asMap(parseJSON(tags[AdkLLMRequest])); llmRequest != nil {
		contents := getContents(llmRequest)
		for i := len(contents) - 1; i >= 0; i-- {
			cd := asMap(contents[i])
			if cd == nil || cd["role"] != "user" {
				continue
			}
			parts := asSlice(cd["parts"])
			var texts []string
			for _, p := range parts {
				if pm, ok := p.(map[string]any); ok {
					if t, ok := pm["text"].(string); ok {
						texts = append(texts, t)
					}
				}
			}
			if len(texts) > 0 {
				return strings.Join(texts, " ")
			}
		}
		for _, c := range contents {
			cd := asMap(c)
			if cd != nil && cd["role"] == "user" {
				parts := asSlice(cd["parts"])
				var texts []string
				for _, p := range parts {
					if pm, ok := p.(map[string]any); ok {
						t, _ := pm["text"].(string)
						texts = append(texts, t)
					}
				}
				if len(parts) > 0 {
					return strings.Join(texts, " ")
				}
			}
		}
	}

	if messages := asSlice(parseJSON(tags[GenAIInputMessages])); messages != nil {
		for i := len(messages) - 1; i >= 0; i-- {
			msg := asMap(messages[i])
			if msg != nil && userRoles[stringField(msg, "role")] {
				if text := extractTextFromMessage(msg); text != "" {
					return text
				}
			}
		}
	}

	if v, ok := tags[OpenInferenceInputValue].(string); ok && v != "" {
		return v
	}

	return ""
}

// ExtractAgentResponseFromAttrs extracts agent response text from span attributes, ADK-first.
func ExtractAgentResponseFromAttrs(tags map[string]any) string {
	if llmResponse := asMap(parseJSON(tags[AdkLLMResponse])); llmResponse != nil {
		if contentDict := getContentField(llmResponse); contentDict != nil {
			parts := asSlice(contentDict["parts"])
			var texts []string
			for _, p := range parts {
				if pm, ok := p.(map[string]any); ok {
					if t, ok := pm["text"].(string); ok {
						texts = append(texts, t)
					}
				}
			}
			if len(texts) > 0 {
				return strings.Join(texts, " ")
			}
		}
	}

	if messages := asSlice(parseJSON(tags[GenAIOutputMessages])); messages != nil {
		for i := len(messages) - 1; i >= 0; i-- {
			msg := asMap(messages[i])
			if msg != nil && assistantRoles[stringField(msg, "role")] {
				if text := extractTextFromMessage(msg); text != "" {
					return text
				}
			}
		}
	}

	if v, ok := tags[OpenInferenceOutputValue].(string); ok && v != "" {
		return v
	}

	return ""
}

func stringField(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

// ExtractTokenUsageFromAttrs extracts (inputTokens, outputTokens, model) from span attributes, ADK-first.
func ExtractTokenUsageFromAttrs(tags map[string]any) (inputTokens, outputTokens int64, model string) {
	model, _ = tags[GenAIRequestModel].(string)
	if model == "" {
		model, _ = tags[OpenInferenceModelName].(string)
	}
	if model == "" {
		model = "unknown"
	}

	if llmResponse := asMap(parseJSON(tags[AdkLLMResponse])); llmResponse != nil {
		usage := asMap(llmResponse["usage_metadata"])
		in := toInt64(usage["prompt_token_count"])
		out := toInt64(usage["candidates_token_count"])
		if in != 0 || out != 0 {
			if llmRequest := asMap(parseJSON(tags[AdkLLMRequest])); llmRequest != nil {
				if m, ok := llmRequest["model"].(string); ok && m != "" {
					model = m
				}
			}
			return in, out, model
		}
	}

	in := toInt64(tags[GenAIUsageInputTokens])
	out := toInt64(tags[GenAIUsageOutputTokens])
	if in != 0 || out != 0 {
		return in, out, model
	}

	in = toInt64(tags[OpenInferenceTokenPrompt])
	out = toInt64(tags[OpenInferenceTokenCompletion])
	if in != 0 || out != 0 {
		return in, out, model
	}

	return 0, 0, model
}

func toInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	default:
		return 0
	}
}

// ToolCallInfo is the normalized result of extracting a tool call from a
// span's attributes.
type ToolCallInfo struct {
	ID   string
	Name string
	Args map[string]any
}

// ExtractToolCallFromAttrs extracts tool call info from span attributes.
func ExtractToolCallFromAttrs(tags map[string]any, operationName, spanID string) *ToolCallInfo {
	toolName := tags[GenAIToolName]
	name, _ := toolName.(string)
	if name == "" {
		name, _ = tags[OpenInferenceToolName].(string)
	}
	if name == "" {
		const prefix = "execute_tool "
		if strings.HasPrefix(operationName, prefix) {
			name = strings.TrimPrefix(operationName, prefix)
		} else {
			return nil
		}
	}

	id := spanID
	if v, ok := tags[GenAIToolCallID].(string); ok && v != "" {
		id = v
	}
	if id == "" {
		id = "unknown"
	}

	args := asMap(parseJSON(tags[GenAIToolCallArguments]))
	if args == nil {
		args = asMap(parseJSON(tags[AdkToolCallArgs]))
	}
	if args == nil {
		if v, ok := tags[OpenInferenceInputValue].(string); ok && v != "" {
			args = asMap(parseJSON(v))
		}
	}
	if args == nil {
		args = map[string]any{}
	}

	return &ToolCallInfo{ID: id, Name: name, Args: args}
}

// ToolResultInfo is the normalized result of extracting a tool response
// from a span's attributes.
type ToolResultInfo struct {
	Response map[string]any
	IsError  bool
}

// parseToolResponseContent parses raw tool response content into a response map.
func parseToolResponseContent(content any) map[string]any {
	switch v := content.(type) {
	case string:
		var parsed any
		if err := json.Unmarshal([]byte(v), &parsed); err == nil {
			if m, ok := parsed.(map[string]any); ok {
				return m
			}
			return map[string]any{"result": parsed}
		}
		return map[string]any{"result": v}
	case map[string]any:
		return v
	default:
		return map[string]any{"result": v}
	}
}

// ExtractToolResultFromAttrs extracts a tool result from span attributes, ADK-first.
func ExtractToolResultFromAttrs(tags map[string]any) *ToolResultInfo {
	raw, ok := tags[AdkToolResponse]
	if !ok {
		raw, ok = tags[GenAIToolCallResult]
	}
	if ok && raw != nil {
		parsed := parseToolResponseContent(raw)
		isError, _ := parsed["isError"].(bool)
		return &ToolResultInfo{Response: parsed, IsError: isError}
	}

	if v, ok := tags[OpenInferenceOutputValue].(string); ok && v != "" {
		parsed := parseToolResponseContent(v)
		isError, _ := parsed["isError"].(bool)
		return &ToolResultInfo{Response: parsed, IsError: isError}
	}

	return nil
}
