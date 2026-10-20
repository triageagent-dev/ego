// Package incremental extracts conversation elements (user input, agent responses, tool calls/results, token
// usage) from spans as they arrive, for real-time display without waiting for session completion.
package incremental

import (
	"strings"

	"github.com/triageagent-dev/ego/internal/adk"
	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

// Update is one conversation-element update to broadcast to UI clients over SSE. Not every field applies to
// every Type; unused fields are omitted from JSON.
type Update struct {
	Type         string         `json:"type"`
	InvocationID string         `json:"invocationId,omitempty"`
	Text         string         `json:"text,omitempty"`
	Timestamp    float64        `json:"timestamp,omitempty"`
	InputTokens  int64          `json:"inputTokens,omitempty"`
	OutputTokens int64          `json:"outputTokens,omitempty"`
	Model        string         `json:"model,omitempty"`
	ToolCall     map[string]any `json:"toolCall,omitempty"`
	ToolCallID   string         `json:"toolCallId,omitempty"`
	ToolName     string         `json:"toolName,omitempty"`
	Response     map[string]any `json:"response,omitempty"`
	IsError      bool           `json:"isError,omitempty"`
}

// Extractor holds the dedup state for one session, so it must not be shared across sessions.
type Extractor struct {
	seenUserInput       map[string]bool
	seenToolCalls       map[string]map[string]bool
	seenAgentResponse   map[string]bool
	tokenTotals         map[string]*tokenTotal
	currentInvocationID string
	seenMessageContents map[string]bool
	toolNamesByID       map[string]string
}

type tokenTotal struct {
	InputTokens  int64
	OutputTokens int64
	Model        string
}

func New() *Extractor {
	return &Extractor{
		seenUserInput:       map[string]bool{},
		seenToolCalls:       map[string]map[string]bool{},
		seenAgentResponse:   map[string]bool{},
		tokenTotals:         map[string]*tokenTotal{},
		seenMessageContents: map[string]bool{},
		toolNamesByID:       map[string]string{},
	}
}

// ProcessSpan processes one normalized span and returns conversation
// updates to broadcast. Adapted from process_span to take ego's
// already-normalized *trace.Span (Tags already flattened, IDs already
// resolved) rather than a raw OTLP JSON span dict - same extraction logic,
// just fed from the Go ingestion pipeline's own shape instead of
// re-parsing OTLP JSON a second time.
func (e *Extractor) ProcessSpan(span *tracepkg.Span) []Update {
	var updates []Update

	isADK := span.TagString(adk.OtelScope) == adk.AdkScopeValue
	isGenAILLM := (span.HasTag(adk.GenAIRequestModel) && span.TagString(adk.GenAIRequestModel) != "") ||
		span.TagString(adk.OpenInferenceSpanKind) == "LLM" || span.TagString(adk.OpenInferenceModelName) != ""
	isGenAITool := span.HasTag(adk.GenAIToolName) && span.TagString(adk.GenAIToolName) != ""

	if !isADK && !isGenAILLM && !isGenAITool {
		return updates
	}

	invocationID := e.invocationID(span)
	if invocationID == "" {
		return updates
	}
	e.currentInvocationID = invocationID

	// Divide by 1e6 here for the same unix-seconds-as-float timestamp.
	startSeconds := float64(span.StartTime) / 1e6
	endSeconds := float64(span.StartTime+span.Duration) / 1e6

	isLLM := strings.HasPrefix(span.OperationName, "call_llm") || isGenAILLM
	if isLLM {
		if !e.seenUserInput[invocationID] {
			if userText := adk.ExtractUserTextFromAttrs(span.Tags); userText != "" {
				key := "user:" + strings.TrimSpace(userText)
				if !e.seenMessageContents[key] {
					updates = append(updates, Update{
						Type:         "user_input",
						InvocationID: invocationID,
						Text:         userText,
						Timestamp:    startSeconds,
					})
					e.seenMessageContents[key] = true
				}
			}
			e.seenUserInput[invocationID] = true
		}

		if agentText := adk.ExtractAgentResponseFromAttrs(span.Tags); agentText != "" && !e.seenAgentResponse[invocationID] {
			key := "agent:" + strings.TrimSpace(agentText)
			if !e.seenMessageContents[key] {
				updates = append(updates, Update{
					Type:         "agent_response",
					InvocationID: invocationID,
					Text:         agentText,
					Timestamp:    endSeconds,
				})
				e.seenMessageContents[key] = true
			}
			e.seenAgentResponse[invocationID] = true
		}

		inToks, outToks, model := adk.ExtractTokenUsageFromAttrs(span.Tags)
		if inToks != 0 || outToks != 0 {
			total := e.tokenTotals[invocationID]
			if total == nil {
				total = &tokenTotal{Model: model}
				e.tokenTotals[invocationID] = total
			}
			total.InputTokens += inToks
			total.OutputTokens += outToks

			updates = append(updates, Update{
				Type:         "token_update",
				InvocationID: invocationID,
				InputTokens:  inToks,
				OutputTokens: outToks,
				Model:        model,
			})
		}
	} else if strings.HasPrefix(span.OperationName, "execute_tool") || isGenAITool {
		toolCall := adk.ExtractToolCallFromAttrs(span.Tags, span.OperationName, span.SpanID)
		if toolCall != nil {
			callID := toolCall.ID
			if e.seenToolCalls[invocationID] == nil {
				e.seenToolCalls[invocationID] = map[string]bool{}
			}
			e.toolNamesByID[callID] = toolCall.Name

			if !e.seenToolCalls[invocationID][callID] {
				updates = append(updates, Update{
					Type:         "tool_call",
					InvocationID: invocationID,
					ToolCall:     map[string]any{"id": toolCall.ID, "name": toolCall.Name, "args": toolCall.Args},
					Timestamp:    startSeconds,
				})
				e.seenToolCalls[invocationID][callID] = true

				if toolResult := adk.ExtractToolResultFromAttrs(span.Tags); toolResult != nil {
					updates = append(updates, Update{
						Type:         "tool_result",
						InvocationID: invocationID,
						ToolCallID:   callID,
						ToolName:     toolCall.Name,
						Response:     toolResult.Response,
						IsError:      toolResult.IsError,
						Timestamp:    endSeconds,
					})
				}
			}
		}
	}

	return updates
}

// invocationID resolves the invocation a span belongs to: the ADK invocation_id attribute if present, else the
// parent span ID, else the span's own ID.
func (e *Extractor) invocationID(span *tracepkg.Span) string {
	if id := span.TagString(adk.AdkInvocationID); id != "" {
		return id
	}
	if span.ParentSpanID != "" {
		return span.ParentSpanID
	}
	return span.SpanID
}
