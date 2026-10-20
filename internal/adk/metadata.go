package adk

import (
	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

// TraceMetadata is extract_trace_metadata's return value.
type TraceMetadata struct {
	AgentName          string
	AgentID            string
	ServiceName        string
	Model              string
	StartTime          int64 // microseconds since epoch; 0 means unknown
	UserInputPreview   string
	FinalOutputPreview string
}

const previewMaxRunes = 200

// truncatePreview.
func truncatePreview(text string) string {
	r := []rune(text)
	if len(r) <= previewMaxRunes {
		return text
	}
	return string(r[:previewMaxRunes]) + "..."
}

// firstServiceName.name resource attribute, the stable cross-framework identifier for grouping runs by agent.
func firstServiceName(tr *tracepkg.Trace) string {
	for _, span := range tr.AllSpans {
		if v := span.TagString(OtelServiceName); v != "" {
			return v
		}
	}
	return ""
}

// findInvocationSpans returns the spans this trace's format treats as
// invocation boundaries, using the same format-detection order ConvertTrace
// uses (ADK first, then GenAI-semconv/OpenInference, else ADK by default)
// so metadata and conversion agree on what an "invocation" is.
func findInvocationSpans(tr *tracepkg.Trace) []*tracepkg.Span {
	for _, span := range tr.AllSpans {
		if IsADKScope(span) {
			return findADKSpans(tr, "invoke_agent")
		}
	}
	if DetectGenAIFormat(tr) {
		return findGenAIInvocationSpans(tr)
	}
	return findADKSpans(tr, "invoke_agent")
}

// findLLMSpansIn returns the LLM-call spans within an invocation span,
// ADK-first then GenAI-semconv/OpenInference, falling back to the
// invocation span itself when it's directly an LLM span (a root LLM span
// with no separate invocation wrapper).
func findLLMSpansIn(invSpan *tracepkg.Span) []*tracepkg.Span {
	if llm := FindADKLLMSpansIn(invSpan); len(llm) > 0 {
		return llm
	}
	if llm := findGenAILLMSpansIn(invSpan); len(llm) > 0 {
		return llm
	}
	if isLLMSpan(invSpan) {
		return []*tracepkg.Span{invSpan}
	}
	return nil
}

// ExtractTraceMetadata extracts agent name, model, timing, and preview text from a trace.
//
// invocations, when non-empty, should be the result of ConvertTrace(tr) -
// the same turn-pairing ConvertTrace uses to build the Conversation detail
// view. UserInputPreview/FinalOutputPreview are derived from it rather than
// independently re-deriving from raw spans: for multi-turn OpenInference/
// voice traces (alternating single-sided input.value/output.value spans),
// a naive "first/last LLM span" span heuristic can land on a span with no
// usable text (e.g. an assistant-only opening greeting), leaving the
// preview empty even though ConvertTrace's invocation pairing already
// extracted the real first user message and final agent response
// correctly. AgentName/Model/StartTime are still derived from raw spans
// since those already extract correctly.
func ExtractTraceMetadata(tr *tracepkg.Trace, invocations []Invocation) TraceMetadata {
	meta := TraceMetadata{ServiceName: firstServiceName(tr)}

	invSpans := findInvocationSpans(tr)
	if len(invSpans) > 0 {
		first := invSpans[0]
		meta.AgentName = first.TagString(GenAIAgentName)
		meta.AgentID = first.TagString(GenAIAgentID)
		meta.StartTime = first.StartTime

		if llmSpans := findLLMSpansIn(first); len(llmSpans) > 0 {
			_, _, model := ExtractTokenUsageFromAttrs(llmSpans[0].Tags)
			if model != "" && model != "unknown" {
				meta.Model = model
			}
		}
	}

	if meta.AgentName == "" && len(tr.RootSpans) > 0 {
		meta.AgentName = tr.RootSpans[0].OperationName
	}

	if meta.Model == "" {
		for _, span := range tr.AllSpans {
			if m := span.TagString(GenAIRequestModel); m != "" {
				meta.Model = m
				break
			}
		}
	}

	// Scan every invocation (not just the first/last) for preview text:
	// a leading invocation can be assistant-only (e.g. a voice agent's
	// opening greeting before the user has said anything), which would
	// leave invocations[0].UserContent empty even though a later
	// invocation has the real first user message. Symmetrically, scan
	// backwards for the final agent response in case the last invocation
	// is a trailing user message/tool call with no reply yet.
	for _, inv := range invocations {
		if userText := inv.UserContent.Text(); userText != "" {
			meta.UserInputPreview = truncatePreview(userText)
			break
		}
	}
	for i := len(invocations) - 1; i >= 0; i-- {
		if agentText := invocations[i].FinalResponse.Text(); agentText != "" {
			meta.FinalOutputPreview = truncatePreview(agentText)
			break
		}
	}

	return meta
}
