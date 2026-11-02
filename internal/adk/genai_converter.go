package adk

import (
	"fmt"
	"sort"
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
// voice/realtime session emits one span per utterance when the words and the
// answer fall in different model rounds, and a session-level span carries
// neither). One such span marks the session as a multi-turn conversation. A
// span with both sides is a complete request/response call: on its own it is
// handled by extractSingleTurn; inside a conversation it is one whole turn
// (see extractOpenInferenceTurns).
func isOpenInferenceTurnSpan(span *tracepkg.Span) bool {
	hasIn := span.TagString(OpenInferenceInputValue) != ""
	hasOut := span.TagString(OpenInferenceOutputValue) != ""
	return hasIn != hasOut
}

// hasOpenInferenceText reports whether span carries either side of an utterance.
func hasOpenInferenceText(span *tracepkg.Span) bool {
	return span.TagString(OpenInferenceInputValue) != "" || span.TagString(OpenInferenceOutputValue) != ""
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

// lastAnswerSpan picks the LLM span whose output is the invocation's answer: the last call made by the agent
// that opened the invocation. An agent may call a second model on its own answer before returning (a verifier
// judging whether the answer is supported, a guard), in the same conversation; that call's output is a verdict
// about the answer, not the answer, and judging it would score the verifier. Spans without an agent name are
// taken as the lead agent's, so a trace that never names agents keeps its last call.
func lastAnswerSpan(llmSpans []*tracepkg.Span) *tracepkg.Span {
	lead := llmSpans[0].TagString(GenAIAgentName)
	last := llmSpans[0]
	for _, s := range llmSpans[1:] {
		if name := s.TagString(GenAIAgentName); name == "" || name == lead {
			last = s
		}
	}
	return last
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
	Pending        bool
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
	answer := lastAnswerSpan(llmSpans)
	assistantText := ExtractAgentResponseFromAttrs(answer.Tags)
	if assistantText == "" && invSpan != answer {
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
		Pending:        boolTag(invSpan, EgoTurnPending),
	}, nil
}

// boolTag reads a boolean attribute written as a bool or as the string "true".
func boolTag(span *tracepkg.Span, key string) bool {
	switch v := span.Tags[key].(type) {
	case bool:
		return v
	case string:
		return v == "true"
	}
	return false
}

// extractOpenInferenceTurns pairs alternating single-sided OpenInference LLM spans into turns. A turn runs from
// its first span to the start of the next turn, so the model rounds after an answer and before the next user
// input (a voice model that keeps calling tools on its own) stay with the turn that answered, the same window
// the per-turn token metrics use. A tool call belongs to the model round that consumed its result: the first
// LLM span at or after it. That holds for a span emitted when the model asked (the round after it carries the
// answer) and for one emitted when the round ended (a voice module writes the round's span after
// its tools returned).
func extractOpenInferenceTurns(invSpan *tracepkg.Span, llmSpans []*tracepkg.Span) []conversationTurn {
	// A round that heard the person and answered in the same model round carries both sides on one span
	// (a voice module after a session-cap fix); it is a whole turn, so it is kept here even though
	// it is not a single-sided "turn side".
	var turnSpans []*tracepkg.Span
	for _, s := range llmSpans {
		if hasOpenInferenceText(s) {
			turnSpans = append(turnSpans, s)
		}
	}
	if len(turnSpans) == 0 {
		return nil
	}
	sortByStart(turnSpans)

	var turns []conversationTurn
	var pendingUser *string
	pendingStart := turnSpans[0].StartTime

	flush := func(assistantText string) {
		idx := len(turns) + 1
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
			StartTimeMicro: pendingStart,
		})
		pendingUser = nil
	}

	for _, s := range turnSpans {
		inVal := s.TagString(OpenInferenceInputValue)
		outVal := s.TagString(OpenInferenceOutputValue)
		if inVal != "" {
			if pendingUser != nil {
				flush("")
			}
			v := inVal
			pendingUser = &v
			pendingStart = s.StartTime
			if outVal != "" {
				// The same round answered: the turn is complete.
				flush(outVal)
			}
		} else if outVal != "" {
			if pendingUser == nil {
				pendingStart = s.StartTime
			}
			flush(outVal)
		}
	}

	if pendingUser != nil {
		flush("")
	}

	// Every LLM span is a round, including the ones that carried no text (a round that only called tools).
	rounds := append([]*tracepkg.Span(nil), llmSpans...)
	sortByStart(rounds)
	toolSpans := findGenAIToolSpansIn(invSpan)
	sortByStart(toolSpans)
	toolsByTurn := make([][]*tracepkg.Span, len(turns))
	for _, t := range toolSpans {
		roundStart := rounds[len(rounds)-1].StartTime
		for _, s := range rounds {
			if s.StartTime >= t.StartTime {
				roundStart = s.StartTime
				break
			}
		}
		i := 0
		for j := range turns {
			if turns[j].StartTimeMicro <= roundStart {
				i = j
			}
		}
		toolsByTurn[i] = append(toolsByTurn[i], t)
	}
	for i := range turns {
		turns[i].ToolCalls, turns[i].ToolResponses = extractGenAIToolCalls(toolsByTurn[i], nil)
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
		assistantText := ExtractAgentResponseFromAttrs(lastAnswerSpan(llmSpans).Tags)
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

// extractGenAIToolCalls extracts the tool calls and results of one invocation. A call can be seen twice: in the
// model's gen_ai.output.messages (with the model's id and arguments) and as a tool span (with the id and arguments
// the framework recorded, or none). They are one call. The model's is the call as made, so its arguments win
// and the span fills what the model left out; a span with no id joins the first call of the same name that no
// span has claimed yet, and a span with no model call behind it is a call of its own.
func extractGenAIToolCalls(toolSpans, llmSpans []*tracepkg.Span) ([]turnToolCall, []turnToolResponse) {
	type call struct {
		turnToolCall
		at      int64 // when it happened: the model's answer for a model call, the span's start otherwise
		claimed bool  // a tool span is already this call
	}
	var calls []*call
	byID := map[string]*call{}

	for _, llmSpan := range llmSpans {
		messages, ok := parseJSON(llmSpan.Tags[GenAIOutputMessages]).([]any)
		if !ok {
			continue
		}
		at := llmSpan.StartTime + llmSpan.Duration
		for _, m := range trimCumulativeOutput(llmSpan, messages) {
			mm := asMap(m)
			if mm == nil || !assistantRoles[stringField(mm, "role")] {
				continue
			}
			for _, tc := range extractToolCallsFromMessage(mm) {
				if existing := byID[tc.ID]; tc.ID != "" && existing != nil {
					if len(existing.Args) == 0 {
						existing.Args = tc.Arguments
					}
					continue
				}
				c := &call{turnToolCall: turnToolCall{Name: tc.Name, Args: tc.Arguments, ID: tc.ID}, at: at}
				calls = append(calls, c)
				if tc.ID != "" {
					byID[tc.ID] = c
				}
			}
		}
	}

	var toolResponses []turnToolResponse
	for _, toolSpan := range toolSpans {
		info := ExtractToolCallFromAttrs(toolSpan.Tags, toolSpan.OperationName, toolSpan.SpanID)
		if info == nil {
			continue
		}
		id := info.ID
		if id == "unknown" || id == toolSpan.SpanID {
			id = ""
		}

		var c *call
		if id != "" {
			c = byID[id]
		} else {
			for _, cand := range calls {
				if !cand.claimed && cand.Name == info.Name {
					c = cand
					break
				}
			}
		}
		if c == nil {
			c = &call{turnToolCall: turnToolCall{Name: info.Name, ID: id}, at: toolSpan.StartTime}
			calls = append(calls, c)
			if id != "" {
				byID[id] = c
			}
		}
		c.claimed = true
		if len(c.Args) == 0 {
			c.Args = info.Args
		}

		if toolResult := ExtractToolResultFromAttrs(toolSpan.Tags); toolResult != nil {
			toolResponses = append(toolResponses, turnToolResponse{Name: info.Name, Response: toolResult.Response, ID: c.ID})
		}
	}

	sort.SliceStable(calls, func(i, j int) bool { return calls[i].at < calls[j].at })
	toolCalls := make([]turnToolCall, 0, len(calls))
	for _, c := range calls {
		toolCalls = append(toolCalls, c.turnToolCall)
	}
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
		Pending:           turn.Pending,
	}
}
