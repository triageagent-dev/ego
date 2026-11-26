package adk

import (
	"encoding/json"

	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

// EgoInputTruncated is set to true on a span by a sender that did not put all of a model call's input on the trace.
const EgoInputTruncated = "ego.input.truncated"

// genAISystemInstructions holds a call's system prompt in the conventions that record it outside the messages.
const genAISystemInstructions = "gen_ai.system_instructions"

// cutTokenFloor is the input size, in tokens, below which a call is not guessed to be cut: a small call's token
// count is mostly the provider's own framing.
const cutTokenFloor = 2000

// inputCut reports whether the trace of one turn lacks part of the model's input. A sender that says so
// (EgoInputTruncated) is believed. Without that, a turn with no tool calls is cut when the model reports more input
// tokens than the trace holds bytes of input: a token is at least a byte of text, so the rest was not recorded. A
// turn with tool calls is left alone, since the tools offered to the model count as input and are rarely recorded.
func inputCut(invSpan *tracepkg.Span, llmSpans []*tracepkg.Span, toolCalls int) bool {
	if markedCut(invSpan) {
		return true
	}
	if toolCalls > 0 {
		return false
	}
	var tokens int64
	// Documents add to what the call's own span records; they are a part of the same input.
	visible, documents := 0, ContextBytes(turnContext(invSpan))
	// The call is often traced twice, one span with the messages and one with the usage, so the largest of each
	// over the turn's spans is what is compared.
	for _, s := range append([]*tracepkg.Span{invSpan}, llmSpans...) {
		if isLLMSpan(s) {
			if in, _, _ := ExtractTokenUsageFromAttrs(s.Tags); in > tokens {
				tokens = in
			}
		}
		if n := messageBytes(s); n > visible {
			visible = n
		}
	}
	if visible > 0 {
		visible += documents
	}
	// A span with no input at all is not a cut one: the sender records no content, and conversion says so itself.
	return tokens > cutTokenFloor && visible > 0 && tokens > int64(visible)
}

// markedCut reports whether span or any span under it says the input is incomplete.
func markedCut(span *tracepkg.Span) bool {
	if boolTag(span, EgoInputTruncated) {
		return true
	}
	for _, child := range span.Children {
		if markedCut(child) {
			return true
		}
	}
	return false
}

// messageBytes is how many bytes of model input span records in its messages.
func messageBytes(span *tracepkg.Span) int {
	n := 0
	for _, key := range []string{GenAIInputMessages, OpenInferenceInputValue, AdkLLMRequest} {
		n = max(n, attrBytes(span.Tags[key]))
	}
	return n + attrBytes(span.Tags[genAISystemInstructions])
}

func attrBytes(v any) int {
	switch s := v.(type) {
	case nil:
		return 0
	case string:
		return len(s)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(raw)
}
