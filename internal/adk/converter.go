package adk

import (
	"fmt"
	"strings"

	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

// ConversionResult is one trace's converted invocations plus any per-invocation conversion warnings.
type ConversionResult struct {
	TraceID     string
	Invocations []Invocation
	Warnings    []string
}

// findADKSpans finds ADK-instrumented spans whose operation name has the given prefix. For "invoke_agent" it
// also accepts spans whose own scope markers were lost but whose subtree is still ADK-instrumented (a Tempo
// round-trip artifact).
func findADKSpans(tr *tracepkg.Trace, operation string) []*tracepkg.Span {
	var matches []*tracepkg.Span
	for _, span := range tr.AllSpans {
		if !strings.HasPrefix(span.OperationName, operation) {
			continue
		}
		if IsADKScope(span) {
			matches = append(matches, span)
			continue
		}
		if operation == "invoke_agent" && HasADKDescendant(span) {
			matches = append(matches, span)
		}
	}
	sortByStart(matches)
	return matches
}

// ConvertADKTrace converts a Jaeger/OTLP-loaded trace emitted by Google ADK instrumentation (gcp.vertex.agent.*
// attributes) into Invocations.
func ConvertADKTrace(tr *tracepkg.Trace) ConversionResult {
	result := ConversionResult{TraceID: tr.TraceID}

	invokeSpans := findADKSpans(tr, "invoke_agent")
	if len(invokeSpans) == 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("trace %s: no invoke_agent spans found", tr.TraceID))
		return result
	}

	for _, invokeSpan := range invokeSpans {
		inv, err := convertInvokeSpan(invokeSpan)
		if err != nil {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("trace %s: failed to convert invoke_agent span %s: %v", tr.TraceID, invokeSpan.SpanID, err))
			continue
		}
		result.Invocations = append(result.Invocations, inv)
	}

	return result
}

func convertInvokeSpan(invokeSpan *tracepkg.Span) (Invocation, error) {
	llmSpans := FindADKLLMSpansIn(invokeSpan)
	if len(llmSpans) == 0 {
		return Invocation{}, fmt.Errorf(
			"invoke_agent span %s has no converter-compatible ADK LLM descendants; expected call_llm or ADK generate_content spans",
			invokeSpan.SpanID)
	}

	toolSpans := FindChildrenByOp(invokeSpan, "execute_tool")

	userContent, err := extractUserContent(llmSpans[0])
	if err != nil {
		return Invocation{}, err
	}
	finalResponse, err := extractFinalResponse(llmSpans[len(llmSpans)-1])
	if err != nil {
		return Invocation{}, err
	}
	toolUses, toolResponses := extractToolTrajectory(llmSpans, toolSpans)

	invocationID := invokeSpan.TagString(AdkInvocationID)
	if invocationID == "" {
		invocationID = invokeSpan.SpanID
	}

	return Invocation{
		InvocationID:      invocationID,
		UserContent:       userContent,
		FinalResponse:     finalResponse,
		IntermediateData:  &IntermediateData{ToolUses: toolUses, ToolResponses: toolResponses},
		CreationTimestamp: float64(invokeSpan.StartTime) / 1_000_000.0,
	}, nil
}

// extractUserContent extracts user input from the first call_llm span's attributes.
func extractUserContent(firstCallLLM *tracepkg.Span) (*Content, error) {
	if text := ExtractUserTextFromAttrs(firstCallLLM.Tags); text != "" {
		return TextOnly("user", text), nil
	}
	llmRequest := asMap(parseJSON(firstCallLLM.Tag(AdkLLMRequest)))
	for _, c := range getContents(llmRequest) {
		cd := asMap(c)
		if cd != nil && cd["role"] == "user" {
			return contentFromDict(cd), nil
		}
	}
	return nil, fmt.Errorf("call_llm span %s: no user content found in llm_request", firstCallLLM.SpanID)
}

// extractFinalResponse extracts the final text response from the last call_llm span's attributes.
func extractFinalResponse(lastCallLLM *tracepkg.Span) (*Content, error) {
	if text := ExtractAgentResponseFromAttrs(lastCallLLM.Tags); text != "" {
		return TextOnly("model", text), nil
	}
	llmResponse := asMap(parseJSON(lastCallLLM.Tag(AdkLLMResponse)))
	contentDict := getContentField(llmResponse)
	if contentDict == nil {
		return nil, fmt.Errorf("call_llm span %s: no content in llm_response", lastCallLLM.SpanID)
	}
	return contentFromDict(contentDict), nil
}

// extractToolTrajectory prefers execute_tool spans (which have actual execution results) over function_call
// parts in call_llm responses (which only have the LLM's request to call the tool).
func extractToolTrajectory(llmSpans, toolSpans []*tracepkg.Span) ([]FunctionCall, []FunctionResponse) {
	var toolUses []FunctionCall
	var toolResponses []FunctionResponse

	if len(toolSpans) > 0 {
		for _, toolSpan := range toolSpans {
			fc, fr := extractFromToolSpan(toolSpan)
			if fc != nil {
				toolUses = append(toolUses, *fc)
			}
			if fr != nil {
				toolResponses = append(toolResponses, *fr)
			}
		}
	} else {
		for _, callLLM := range llmSpans {
			toolUses = append(toolUses, extractFunctionCallsFromLLMResponse(callLLM)...)
		}
	}

	return toolUses, toolResponses
}

func extractFromToolSpan(toolSpan *tracepkg.Span) (*FunctionCall, *FunctionResponse) {
	toolCall := ExtractToolCallFromAttrs(toolSpan.Tags, toolSpan.OperationName, toolSpan.SpanID)
	if toolCall == nil {
		return nil, nil
	}

	fc := &FunctionCall{Name: toolCall.Name, Args: toolCall.Args, ID: toolCall.ID}

	var fr *FunctionResponse
	if result := ExtractToolResultFromAttrs(toolSpan.Tags); result != nil {
		fr = &FunctionResponse{Name: toolCall.Name, Response: result.Response, ID: toolCall.ID}
	}

	return fc, fr
}

func extractFunctionCallsFromLLMResponse(callLLM *tracepkg.Span) []FunctionCall {
	llmResponse := asMap(parseJSON(callLLM.Tag(AdkLLMResponse)))
	contentDict := getContentField(llmResponse)
	parts := asSlice(contentDict["parts"])

	var calls []FunctionCall
	for _, p := range parts {
		part := asMap(p)
		if part == nil {
			continue
		}
		fcDict := asMap(part["function_call"])
		if fcDict == nil {
			fcDict = asMap(part["functionCall"])
		}
		if fcDict == nil {
			continue
		}
		name, _ := fcDict["name"].(string)
		id, _ := fcDict["id"].(string)
		calls = append(calls, FunctionCall{Name: name, Args: asMap(fcDict["args"]), ID: id})
	}
	return calls
}

// contentFromDict builds a Content from a raw parsed llm_request/llm_response dict, handling text,
// function_call, and function_response parts.
func contentFromDict(cd map[string]any) *Content {
	role, _ := cd["role"].(string)
	if role == "" {
		role = "user"
	}

	var parts []Part
	for _, p := range asSlice(cd["parts"]) {
		pm := asMap(p)
		if pm == nil {
			continue
		}
		if text, ok := pm["text"].(string); ok {
			parts = append(parts, Part{Text: text})
			continue
		}
		if fc := asMap(pm["function_call"]); fc != nil || pm["functionCall"] != nil {
			if fc == nil {
				fc = asMap(pm["functionCall"])
			}
			name, _ := fc["name"].(string)
			id, _ := fc["id"].(string)
			parts = append(parts, Part{FunctionCall: &FunctionCall{Name: name, Args: asMap(fc["args"]), ID: id}})
			continue
		}
		if fr := asMap(pm["function_response"]); fr != nil || pm["functionResponse"] != nil {
			if fr == nil {
				fr = asMap(pm["functionResponse"])
			}
			name, _ := fr["name"].(string)
			id, _ := fr["id"].(string)
			parts = append(parts, Part{FunctionResponse: &FunctionResponse{Name: name, Response: asMap(fr["response"]), ID: id}})
		}
	}

	return &Content{Role: role, Parts: parts}
}
