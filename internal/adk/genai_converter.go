package adk

import (
	"fmt"
	"strings"

	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

// isLLMSpan reports whether span is an LLM call span: GenAI semconv (gen_ai.request.model.span.kind=LLM or
// llm.model_name, as Phoenix reads them).
func isLLMSpan(span *tracepkg.Span) bool {
	return span.HasTag(GenAIRequestModel) ||
		span.TagString(OpenInferenceSpanKind) == "LLM" ||
		span.TagString(OpenInferenceModelName) != ""
}

// isOpenInferenceTurnSpan reports whether span carries one side of an
// OpenInference-style utterance (input.value XOR output.value - a
// voice/realtime session emits one span per utterance, never both, and a
// session-level span carries neither). A span with both is a complete
// request/response call, handled by extractSingleTurn; treating it as a
// turn side would pair its input with nothing and drop its output.
func isOpenInferenceTurnSpan(span *tracepkg.Span) bool {
	hasIn := span.TagString(OpenInferenceInputValue) != ""
	hasOut := span.TagString(OpenInferenceOutputValue) != ""
	return hasIn != hasOut
}

// isToolSpan reports whether span is a GenAI-semconv or OpenInference tool span.
func isToolSpan(span *tracepkg.Span) bool {
	return span.HasTag(GenAIToolName) || span.HasTag(OpenInferenceToolName)
}

// IsLLMSpan and IsToolSpan are exported aliases of isLLMSpan/isToolSpan for callers outside this package (e.g.
// internal/api's per-invocation metrics bucketing, which.
var (
	IsLLMSpan  = isLLMSpan
	IsToolSpan = isToolSpan
)

// isInvocationSpan reports whether span represents an agent invocation.
func isInvocationSpan(span *tracepkg.Span) bool {
	if span.TagString(GenAIOp) == "invoke_agent" {
		return true
	}
	lower := strings.ToLower(span.OperationName)
	for _, kw := range []string{"agent", "chain", "executor", "workflow"} {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// genAIWalk applies predicate to span and every descendant (span itself
// included, unlike FindChildrenByOp), matching GenAIExtractor._walk.
func genAIWalk(span *tracepkg.Span, predicate func(*tracepkg.Span) bool, acc *[]*tracepkg.Span) {
	if predicate(span) {
		*acc = append(*acc, span)
	}
	for _, child := range span.Children {
		genAIWalk(child, predicate, acc)
	}
}

// findGenAILLMSpansIn returns root and every descendant that is an LLM span, sorted by start time.
func findGenAILLMSpansIn(root *tracepkg.Span) []*tracepkg.Span {
	var acc []*tracepkg.Span
	genAIWalk(root, isLLMSpan, &acc)
	sortByStart(acc)
	return acc
}

// findGenAIToolSpansIn returns root and every descendant that is a tool span, sorted by start time.
func findGenAIToolSpansIn(root *tracepkg.Span) []*tracepkg.Span {
	var acc []*tracepkg.Span
	genAIWalk(root, isToolSpan, &acc)
	sortByStart(acc)
	return acc
}

// hasGenAILLMChildren reports whether any descendant (not span itself) is an LLM span.
func hasGenAILLMChildren(span *tracepkg.Span) bool {
	for _, child := range span.Children {
		if isLLMSpan(child) || hasGenAILLMChildren(child) {
			return true
		}
	}
	return false
}

// DetectGenAIFormat reports whether tr looks like a GenAI-semconv or OpenInference (non-ADK) trace: any LLM
// span (see isLLMSpan) or any span carrying gen_ai.input.messages.
func DetectGenAIFormat(tr *tracepkg.Trace) bool {
	for _, span := range tr.AllSpans {
		if isLLMSpan(span) || span.HasTag(GenAIInputMessages) {
			return true
		}
	}
	return false
}

// ConvertTrace auto-detects a trace's format and converts it to Invocations: ADK (gcp.vertex.agent.* attrs) if
// any span is ADK-scoped, else GenAI-semconv/OpenInference if any span carries GenAI-semconv markers, else ADK
// by default.
func ConvertTrace(tr *tracepkg.Trace) ConversionResult {
	for _, span := range tr.AllSpans {
		if IsADKScope(span) {
			return ConvertADKTrace(tr)
		}
	}
	if DetectGenAIFormat(tr) {
		return ConvertGenAITrace(tr)
	}
	return ConvertADKTrace(tr)
}

// conversationTurn is one extracted user/assistant exchange plus its tool calls.
type conversationTurn struct {
	InvocationID   string
	UserText       string
	AssistantText  string
	ToolCalls      []turnToolCall
	ToolResponses  []turnToolResponse
	StartTimeMicro int64
}

type turnToolCall struct {
	Name string
	Args map[string]any
	ID   string // "" means no usable ID
}

type turnToolResponse struct {
	Name     string
	Response map[string]any
	ID       string
}

// ConvertGenAITrace converts a GenAI-semconv or OpenInference trace (not routed through ADK) into Invocations.
func ConvertGenAITrace(tr *tracepkg.Trace) ConversionResult {
	result := ConversionResult{TraceID: tr.TraceID}

	var llmRootSpans []*tracepkg.Span
	for _, s := range tr.RootSpans {
		if isLLMSpan(s) {
			llmRootSpans = append(llmRootSpans, s)
		}
	}

	if len(llmRootSpans) > 1 {
		anyInvocation := false
		for _, s := range llmRootSpans {
			if isInvocationSpan(s) {
				anyInvocation = true
				break
			}
		}
		if !anyInvocation {
			hasEnriched := false
			for _, s := range llmRootSpans {
				if s.HasTag(GenAIInputMessages) && s.HasTag(GenAIOutputMessages) {
					hasEnriched = true
					break
				}
			}
			if hasEnriched && isBroadcastEnriched(llmRootSpans[0]) {
				turns := extractMultiturnTurns(llmRootSpans)
				for _, t := range turns {
					result.Invocations = append(result.Invocations, turnToInvocation(t))
				}
				return result
			}
		}
	}

	invocationSpans := findGenAIInvocationSpans(tr)
	if len(invocationSpans) == 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("trace %s: no GenAI invocation spans found", tr.TraceID))
		return result
	}

	for _, invSpan := range invocationSpans {
		turns, err := extractTurns(invSpan)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("failed to convert span %s: %v", invSpan.SpanID, err))
			continue
		}
		for _, t := range turns {
			result.Invocations = append(result.Invocations, turnToInvocation(t))
		}
	}

	result.Invocations = deduplicateGenAIInvocations(result.Invocations)
	return result
}

// findGenAIInvocationSpans finds the spans to treat as invocation boundaries.
func findGenAIInvocationSpans(tr *tracepkg.Trace) []*tracepkg.Span {
	var candidates []*tracepkg.Span
	for _, s := range tr.RootSpans {
		if isInvocationSpan(s) {
			candidates = append(candidates, s)
		}
	}

	if len(candidates) == 0 {
		for _, s := range tr.RootSpans {
			if hasGenAILLMChildren(s) {
				candidates = append(candidates, s)
			}
		}
	}

	if len(candidates) == 0 && len(tr.RootSpans) > 0 {
		var llmSpans []*tracepkg.Span
		for _, s := range tr.RootSpans {
			if isLLMSpan(s) {
				llmSpans = append(llmSpans, s)
			}
		}
		if len(llmSpans) > 1 {
			hasEnrichedMessages := false
			for _, s := range llmSpans {
				if s.HasTag(GenAIInputMessages) || s.HasTag(GenAIOutputMessages) {
					hasEnrichedMessages = true
					break
				}
			}
			if hasEnrichedMessages && isBroadcastEnriched(llmSpans[0]) {
				return []*tracepkg.Span{llmSpans[0]}
			}
		}
		if len(llmSpans) > 0 {
			candidates = llmSpans
		} else {
			candidates = append(candidates, tr.RootSpans...)
		}
	}

	if len(candidates) == 0 && len(tr.RootSpans) > 0 {
		candidates = append(candidates, tr.RootSpans...)
	}

	sortByStart(candidates)
	return candidates
}

// extractTurns converts one invocation span into its conversation turn(s).
func extractTurns(invSpan *tracepkg.Span) ([]conversationTurn, error) {
	llmSpans := findGenAILLMSpansIn(invSpan)
	if len(llmSpans) == 0 {
		if isLLMSpan(invSpan) {
			llmSpans = []*tracepkg.Span{invSpan}
		} else {
			return nil, fmt.Errorf("invocation span %s has no LLM call spans", invSpan.SpanID)
		}
	}

	anyOpenInference := false
	for _, s := range llmSpans {
		if isOpenInferenceTurnSpan(s) {
			anyOpenInference = true
			break
		}
	}
	if anyOpenInference {
		turns := extractOpenInferenceTurns(invSpan, llmSpans)
		if len(turns) > 0 {
			return turns, nil
		}
	}

	turn, err := extractSingleTurn(invSpan, llmSpans)
	if err != nil {
		return nil, err
	}
	return []conversationTurn{turn}, nil
}

// extractSingleTurn builds one turn from an invocation's whole LLM/tool span set.
func extractSingleTurn(invSpan *tracepkg.Span, llmSpans []*tracepkg.Span) (conversationTurn, error) {
	toolSpans := findGenAIToolSpansIn(invSpan)

	// An OpenInference AGENT/CHAIN invocation span often carries the clean
	// prompt and answer itself while its LLM children record only the call
	// (or the raw request body), so it is the fallback for either side.
	userText := ExtractUserTextFromAttrs(llmSpans[0].Tags)
	if userText == "" && invSpan != llmSpans[0] {
		userText = ExtractUserTextFromAttrs(invSpan.Tags)
	}
	if userText == "" {
		return conversationTurn{}, fmt.Errorf(
			"LLM span %s: no user message found (checked gen_ai.input.messages, ADK llm_request and input.value)",
			llmSpans[0].SpanID)
	}
	assistantText := ExtractAgentResponseFromAttrs(llmSpans[len(llmSpans)-1].Tags)
	if assistantText == "" && invSpan != llmSpans[len(llmSpans)-1] {
		assistantText = ExtractAgentResponseFromAttrs(invSpan.Tags)
	}
	toolCalls, toolResponses := extractGenAIToolCalls(toolSpans, llmSpans)

	return conversationTurn{
		InvocationID:   "genai-" + invSpan.SpanID,
		UserText:       userText,
		AssistantText:  assistantText,
		ToolCalls:      toolCalls,
		ToolResponses:  toolResponses,
		StartTimeMicro: invSpan.StartTime,
	}, nil
}

// extractOpenInferenceTurns pairs alternating single-sided OpenInference LLM spans into turns, attaching tool
// spans by time window.
func extractOpenInferenceTurns(invSpan *tracepkg.Span, llmSpans []*tracepkg.Span) []conversationTurn {
	var turnSpans []*tracepkg.Span
	for _, s := range llmSpans {
		if isOpenInferenceTurnSpan(s) {
			turnSpans = append(turnSpans, s)
		}
	}
	if len(turnSpans) == 0 {
		return nil
	}
	sortByStart(turnSpans)

	toolSpans := findGenAIToolSpansIn(invSpan)
	sortByStart(toolSpans)

	var turns []conversationTurn
	var pendingUser *string
	pendingStart := turnSpans[0].StartTime
	windowStart := invSpan.StartTime

	flush := func(assistantText string, endTime int64) {
		idx := len(turns) + 1
		var windowTools []*tracepkg.Span
		for _, t := range toolSpans {
			if t.StartTime >= windowStart && t.StartTime <= endTime {
				windowTools = append(windowTools, t)
			}
		}
		toolCalls, toolResponses := extractGenAIToolCalls(windowTools, nil)
		userText := ""
		if pendingUser != nil {
			userText = *pendingUser
		}
		shortID := invSpan.SpanID
		if len(shortID) > 8 {
			shortID = shortID[:8]
		}
		turns = append(turns, conversationTurn{
			InvocationID:   fmt.Sprintf("genai-turn-%d-%s", idx, shortID),
			UserText:       userText,
			AssistantText:  assistantText,
			ToolCalls:      toolCalls,
			ToolResponses:  toolResponses,
			StartTimeMicro: pendingStart,
		})
		pendingUser = nil
		windowStart = endTime
	}

	for _, s := range turnSpans {
		inVal := s.TagString(OpenInferenceInputValue)
		outVal := s.TagString(OpenInferenceOutputValue)
		if inVal != "" {
			if pendingUser != nil {
				flush("", s.StartTime)
			}
			v := inVal
			pendingUser = &v
			pendingStart = s.StartTime
		} else if outVal != "" {
			if pendingUser == nil {
				pendingStart = s.StartTime
			}
			flush(outVal, s.EndTime())
		}
	}

	if pendingUser != nil {
		flush("", turnSpans[len(turnSpans)-1].EndTime())
	}

	return turns
}

// extractMultiturnTurns extracts turns from a broadcast-enriched span's full
// gen_ai.input.messages/gen_ai.output.messages arrays.
func extractMultiturnTurns(llmSpans []*tracepkg.Span) []conversationTurn {
	inputMessages, inputOK := parseJSON(llmSpans[0].Tags[GenAIInputMessages]).([]any)
	outputMessages, outputOK := parseJSON(llmSpans[0].Tags[GenAIOutputMessages]).([]any)

	if !inputOK || !outputOK {
		userText := ExtractUserTextFromAttrs(llmSpans[0].Tags)
		assistantText := ExtractAgentResponseFromAttrs(llmSpans[len(llmSpans)-1].Tags)
		return []conversationTurn{{
			InvocationID:   "genai-" + llmSpans[0].SpanID,
			UserText:       userText,
			AssistantText:  assistantText,
			StartTimeMicro: llmSpans[0].StartTime,
		}}
	}

	var userMessages, assistantMessages []map[string]any
	for _, m := range inputMessages {
		if mm := asMap(m); mm != nil && userRoles[stringField(mm, "role")] {
			userMessages = append(userMessages, mm)
		}
	}
	for _, m := range outputMessages {
		if mm := asMap(m); mm != nil && assistantRoles[stringField(mm, "role")] {
			assistantMessages = append(assistantMessages, mm)
		}
	}

	shortID := llmSpans[0].SpanID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}

	var turns []conversationTurn
	assistantIdx := 0

	for userIdx, userMsg := range userMessages {
		userText := extractTextFromMessage(userMsg)
		if userText == "" {
			continue
		}

		var toolCalls []turnToolCall
		assistantText := ""

		for assistantIdx < len(assistantMessages) {
			assistantMsg := assistantMessages[assistantIdx]
			for _, tc := range extractToolCallsFromMessage(assistantMsg) {
				toolCalls = append(toolCalls, turnToolCall{Name: tc.Name, Args: tc.Arguments, ID: tc.ID})
			}
			content := extractTextFromMessage(assistantMsg)
			assistantIdx++
			if content != "" {
				assistantText = content
				break
			}
		}

		turns = append(turns, conversationTurn{
			InvocationID:   fmt.Sprintf("genai-turn-%d-%s", userIdx+1, shortID),
			UserText:       userText,
			AssistantText:  assistantText,
			ToolCalls:      toolCalls,
			StartTimeMicro: llmSpans[0].StartTime,
		})
	}

	return turns
}

// isBroadcastEnriched detects whether span was enriched via broadcast (the full conversation history injected
// into every span, so it carries more than one user message) rather than per-span (OTLP path, at most one).
func isBroadcastEnriched(span *tracepkg.Span) bool {
	messages, ok := parseJSON(span.Tags[GenAIInputMessages]).([]any)
	if !ok {
		return false
	}
	count := 0
	for _, m := range messages {
		if mm := asMap(m); mm != nil && userRoles[stringField(mm, "role")] {
			count++
		}
	}
	return count > 1
}

// trimCumulativeOutput returns only the current turn's output messages for a cumulative-history trace (OpenAI
// instrumentor v2 stores the full conversation history in each span's output).
func trimCumulativeOutput(llmSpan *tracepkg.Span, outputMessages []any) []any {
	inputRaw, ok := llmSpan.Tags[GenAIInputMessages]
	if !ok || inputRaw == nil {
		return outputMessages
	}
	inputMessages, ok := parseJSON(inputRaw).([]any)
	if !ok {
		return outputMessages
	}

	userCount := 0
	for _, m := range inputMessages {
		if mm := asMap(m); mm != nil && userRoles[stringField(mm, "role")] {
			userCount++
		}
	}
	if userCount <= 1 {
		return outputMessages
	}

	previousTurns := userCount - 1
	textResponsesSeen := 0

	for i, m := range outputMessages {
		mm := asMap(m)
		if mm == nil || !assistantRoles[stringField(mm, "role")] {
			continue
		}
		if extractTextFromMessage(mm) != "" {
			textResponsesSeen++
			if textResponsesSeen >= previousTurns {
				if i+1 < len(outputMessages) {
					return outputMessages[i+1:]
				}
				return nil
			}
		}
	}

	return outputMessages
}

// extractGenAIToolCalls extracts tool calls/responses from a set of tool spans, optionally augmented from LLM
// spans' gen_ai.output.messages tool_calls arrays.
func extractGenAIToolCalls(toolSpans, llmSpans []*tracepkg.Span) ([]turnToolCall, []turnToolResponse) {
	toolCallsByID := map[string]turnToolCall{}
	var idOrder []string
	var toolCallsNoID []turnToolCall
	var toolResponses []turnToolResponse

	for _, toolSpan := range toolSpans {
		toolCall := ExtractToolCallFromAttrs(toolSpan.Tags, toolSpan.OperationName, toolSpan.SpanID)
		if toolCall == nil {
			continue
		}

		isPlaceholder := toolCall.ID == "unknown" || toolCall.ID == toolSpan.SpanID
		dedupID := toolCall.ID
		if isPlaceholder {
			dedupID = ""
		}

		tc := turnToolCall{Name: toolCall.Name, Args: toolCall.Args, ID: dedupID}
		if dedupID != "" {
			if _, exists := toolCallsByID[dedupID]; !exists {
				idOrder = append(idOrder, dedupID)
			}
			toolCallsByID[dedupID] = tc
		} else {
			toolCallsNoID = append(toolCallsNoID, tc)
		}

		if toolResult := ExtractToolResultFromAttrs(toolSpan.Tags); toolResult != nil {
			toolResponses = append(toolResponses, turnToolResponse{
				Name:     toolCall.Name,
				Response: toolResult.Response,
				ID:       dedupID,
			})
		}
	}

	for _, llmSpan := range llmSpans {
		messages, ok := parseJSON(llmSpan.Tags[GenAIOutputMessages]).([]any)
		if !ok {
			continue
		}
		trimmed := trimCumulativeOutput(llmSpan, messages)

		for _, m := range trimmed {
			mm := asMap(m)
			if mm == nil || !assistantRoles[stringField(mm, "role")] {
				continue
			}
			for _, tc := range extractToolCallsFromMessage(mm) {
				newTC := turnToolCall{Name: tc.Name, Args: tc.Arguments, ID: tc.ID}
				if tc.ID != "" {
					if existing, exists := toolCallsByID[tc.ID]; exists {
						if len(tc.Arguments) > 0 && len(existing.Args) == 0 {
							toolCallsByID[tc.ID] = newTC
						}
					} else {
						idOrder = append(idOrder, tc.ID)
						toolCallsByID[tc.ID] = newTC
					}
				} else {
					toolCallsNoID = append(toolCallsNoID, newTC)
				}
			}
		}
	}

	toolCalls := make([]turnToolCall, 0, len(idOrder)+len(toolCallsNoID))
	for _, id := range idOrder {
		toolCalls = append(toolCalls, toolCallsByID[id])
	}
	toolCalls = append(toolCalls, toolCallsNoID...)

	return toolCalls, toolResponses
}

// genaiToolCallMsg is the normalized shape extract_tool_calls_from_message
// returns: {name, id, arguments}.
type genaiToolCallMsg struct {
	Name      string
	ID        string
	Arguments map[string]any
}

// extractToolCallsFromMessage extracts tool calls from a GenAI message in either content-based ({"tool_calls":
// [{"type": "function", "function": {...}}]}) or parts-based ({"parts": [{"type": "tool_call", ...}]}) form.
func extractToolCallsFromMessage(msg map[string]any) []genaiToolCallMsg {
	var result []genaiToolCallMsg

	if toolCalls := asSlice(msg["tool_calls"]); toolCalls != nil {
		for _, raw := range toolCalls {
			tc := asMap(raw)
			if tc == nil {
				continue
			}
			if stringField(tc, "type") != "function" {
				continue
			}
			fn := asMap(tc["function"])
			if fn == nil {
				continue
			}
			id, _ := tc["id"].(string)
			result = append(result, genaiToolCallMsg{
				Name:      stringField(fn, "name"),
				ID:        id,
				Arguments: parseToolCallArgs(fn["arguments"]),
			})
		}
	}

	if len(result) == 0 {
		if parts := asSlice(msg["parts"]); parts != nil {
			for _, raw := range parts {
				part := asMap(raw)
				if part == nil || stringField(part, "type") != "tool_call" {
					continue
				}
				id, _ := part["id"].(string)
				result = append(result, genaiToolCallMsg{
					Name:      stringField(part, "name"),
					ID:        id,
					Arguments: parseToolCallArgs(part["arguments"]),
				})
			}
		}
	}

	return result
}

// parseToolCallArgs normalizes a tool call's raw "arguments" value (already a map, or a JSON-encoded string)
// into a map.
func parseToolCallArgs(raw any) map[string]any {
	if m, ok := raw.(map[string]any); ok {
		return m
	}
	if s, ok := raw.(string); ok {
		if m := asMap(parseJSON(s)); m != nil {
			return m
		}
	}
	return map[string]any{}
}

// deduplicateGenAIInvocations drops earlier invocations that share the same (non-empty) user text with a later
// one, keeping the last (which has the final response, not an intermediate tool-call-only one).
func deduplicateGenAIInvocations(invocations []Invocation) []Invocation {
	if len(invocations) <= 1 {
		return invocations
	}

	userText := func(inv Invocation) string {
		if inv.UserContent != nil && len(inv.UserContent.Parts) > 0 {
			return inv.UserContent.Parts[0].Text
		}
		return ""
	}

	seen := map[string]int{}
	alwaysKeep := map[int]bool{}
	for i, inv := range invocations {
		text := userText(inv)
		if strings.TrimSpace(text) == "" {
			alwaysKeep[i] = true
		} else {
			seen[text] = i
		}
	}

	if len(seen)+len(alwaysKeep) == len(invocations) {
		return invocations
	}

	keep := map[int]bool{}
	for i := range alwaysKeep {
		keep[i] = true
	}
	for _, i := range seen {
		keep[i] = true
	}

	out := make([]Invocation, 0, len(keep))
	for i, inv := range invocations {
		if keep[i] {
			out = append(out, inv)
		}
	}
	return out
}

// turnToInvocation converts one conversationTurn into an Invocation.
func turnToInvocation(turn conversationTurn) Invocation {
	toolUses := make([]FunctionCall, 0, len(turn.ToolCalls))
	for _, tc := range turn.ToolCalls {
		toolUses = append(toolUses, FunctionCall{ID: tc.ID, Name: tc.Name, Args: tc.Args})
	}
	responses := make([]FunctionResponse, 0, len(turn.ToolResponses))
	for _, tr := range turn.ToolResponses {
		responses = append(responses, FunctionResponse{ID: tr.ID, Name: tr.Name, Response: tr.Response})
	}

	return Invocation{
		InvocationID:  turn.InvocationID,
		UserContent:   TextOnly("user", turn.UserText),
		FinalResponse: TextOnly("model", turn.AssistantText),
		IntermediateData: &IntermediateData{
			ToolUses:      toolUses,
			ToolResponses: responses,
		},
		CreationTimestamp: float64(turn.StartTimeMicro) / 1_000_000.0,
	}
}
