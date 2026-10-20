package adk

// OTel span attribute key constants.
const (
	OtelServiceName = "service.name"
	OtelScope       = "otel.scope.name"
	AdkScopeValue   = "gcp.vertex.agent"

	GenAIOp                = "gen_ai.operation.name"
	GenAIAgentName         = "gen_ai.agent.name"
	GenAIAgentID           = "gen_ai.agent.id"
	GenAIRequestModel      = "gen_ai.request.model"
	GenAIInputMessages     = "gen_ai.input.messages"
	GenAIOutputMessages    = "gen_ai.output.messages"
	GenAIToolName          = "gen_ai.tool.name"
	GenAIToolCallID        = "gen_ai.tool.call.id"
	GenAIToolCallArguments = "gen_ai.tool.call.arguments"
	GenAIToolCallResult    = "gen_ai.tool.call.result"
	GenAISystem            = "gen_ai.system"
	GenAIProviderName      = "gen_ai.provider.name"
	GenAIUsageInputTokens  = "gen_ai.usage.input_tokens"
	GenAIUsageOutputTokens = "gen_ai.usage.output_tokens"

	OpenInferenceInputValue      = "input.value"
	OpenInferenceOutputValue     = "output.value"
	OpenInferenceToolName        = "tool.name"
	OpenInferenceToolLatencyMs   = "tool.latency_ms"
	OpenInferenceToolResultBytes = "tool.result_bytes"
	OpenInferenceSpanKind        = "openinference.span.kind"
	OpenInferenceModelName       = "llm.model_name"
	OpenInferenceTokenPrompt     = "llm.token_count.prompt"
	OpenInferenceTokenCompletion = "llm.token_count.completion"

	AdkLLMRequest   = "gcp.vertex.agent.llm_request"
	AdkLLMResponse  = "gcp.vertex.agent.llm_response"
	AdkToolCallArgs = "gcp.vertex.agent.tool_call_args"
	AdkToolResponse = "gcp.vertex.agent.tool_response"
	AdkInvocationID = "gcp.vertex.agent.invocation_id"
	AdkSessionID    = "gcp.vertex.agent.session_id"
	AdkEventID      = "gcp.vertex.agent.event_id"
)

// adkMarkerTags are the gcp.vertex.agent.* attributes whose mere presence signals ADK instrumentation.
var adkMarkerTags = []string{
	AdkLLMRequest,
	AdkLLMResponse,
	AdkToolCallArgs,
	AdkToolResponse,
	AdkInvocationID,
	AdkSessionID,
	AdkEventID,
}

var userRoles = map[string]bool{"user": true, "human": true}
var assistantRoles = map[string]bool{"assistant": true, "model": true, "ai": true}
