package api

import (
	"sort"
	"strings"

	"github.com/triageagent-dev/ego/internal/adk"
	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

// Un.md); everything here derives from spans alone.

// ToolCallDTO is one tool invocation as the UI expects it (matches
// StreamingInvocation.toolCalls in ui/src/lib/types.ts).
type ToolCallDTO struct {
	Name        string `json:"name"`
	Args        any    `json:"args"`
	ID          string `json:"id,omitempty"`
	LatencyMs   *int64 `json:"latencyMs,omitempty"`
	ResultBytes *int64 `json:"resultBytes,omitempty"`
}

// ToolResponseDTO is one tool result as the UI expects it (matches
// StreamingInvocation.toolResponses).
type ToolResponseDTO struct {
	Name     string `json:"name"`
	Response any    `json:"response"`
	ID       string `json:"id,omitempty"`
}

type ModelInfoDTO struct {
	Models        []string `json:"models,omitempty"`
	InputTokens   int64    `json:"inputTokens,omitempty"`
	OutputTokens  int64    `json:"outputTokens,omitempty"`
	TurnLatencyMs float64  `json:"turnLatencyMs,omitempty"`
	Provider      string   `json:"provider,omitempty"`
}

// InvocationDTO matches ui/src/lib/types.ts's StreamingInvocation exactly.
type InvocationDTO struct {
	InvocationID  string            `json:"invocationId"`
	UserText      string            `json:"userText"`
	AgentText     string            `json:"agentText"`
	ToolCalls     []ToolCallDTO     `json:"toolCalls"`
	ToolResponses []ToolResponseDTO `json:"toolResponses"`
	ModelInfo     *ModelInfoDTO     `json:"modelInfo,omitempty"`
}

// extractInvocationsForUI converts tr into UI-shaped invocation DTOs via adk.ConvertTrace, then enriches each
// with per-invocation tool latency/result-size and model/token metrics bucketed from tr's spans by time window.
func extractInvocationsForUI(tr *tracepkg.Trace) []InvocationDTO {
	if tr == nil {
		return nil
	}

	result := adk.ConvertTrace(tr)
	if len(result.Invocations) == 0 {
		return nil
	}

	buckets := bucketSpansByInvocation(tr, result.Invocations)

	dtos := make([]InvocationDTO, 0, len(result.Invocations))
	for _, inv := range result.Invocations {
		bucket := buckets[inv.InvocationID]

		userText := ""
		if inv.UserContent != nil {
			userText = inv.UserContent.Text()
		}
		agentText := ""
		if inv.FinalResponse != nil {
			agentText = inv.FinalResponse.Text()
		}

		toolCalls := make([]ToolCallDTO, 0)
		var toolResponses []ToolResponseDTO
		if inv.IntermediateData != nil {
			for _, tu := range inv.IntermediateData.ToolUses {
				toolCalls = append(toolCalls, ToolCallDTO{Name: tu.Name, Args: tu.Args, ID: tu.ID})
			}
			for _, tr := range inv.IntermediateData.ToolResponses {
				toolResponses = append(toolResponses, ToolResponseDTO{Name: tr.Name, Response: tr.Response, ID: tr.ID})
			}
		}

		// Positional pairing: both toolCalls and bucket-derived metrics are
		// naturally ordered by call time, and raw tool spans carry no ID
		// that reliably survives conversion into ADK's tool_uses.
		metrics := extractToolMetrics(bucket)
		for i := range toolCalls {
			if i >= len(metrics) {
				break
			}
			if metrics[i].latencyMs != nil {
				toolCalls[i].LatencyMs = metrics[i].latencyMs
			}
			if metrics[i].resultBytes != nil {
				toolCalls[i].ResultBytes = metrics[i].resultBytes
			}
		}

		dtos = append(dtos, InvocationDTO{
			InvocationID:  inv.InvocationID,
			UserText:      userText,
			AgentText:     agentText,
			ToolCalls:     toolCalls,
			ToolResponses: toolResponses,
			ModelInfo:     extractModelInfoFromSpans(bucket),
		})
	}

	return dtos
}

// bucketSpansByInvocation partitions every span in tr into the invocation whose time window contains it.
// Neither converter returns a span-to-invocation mapping, so this is the only way to scope per-turn metrics
// without threading new fields through internal/adk. Windows are built from each Invocation's CreationTimestamp
// (seconds since epoch, set by the converter from the same underlying span's start time): a window starts there
// and runs until the next invocation's window starts.
func bucketSpansByInvocation(tr *tracepkg.Trace, invocations []adk.Invocation) map[string][]*tracepkg.Span {
	ordered := make([]adk.Invocation, len(invocations))
	copy(ordered, invocations)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].CreationTimestamp < ordered[j].CreationTimestamp })

	type window struct {
		id      string
		startUs int64
		endUs   int64
		hasEnd  bool
	}
	windows := make([]window, 0, len(ordered))
	for i, inv := range ordered {
		w := window{id: inv.InvocationID, startUs: int64(inv.CreationTimestamp * 1_000_000)}
		if i+1 < len(ordered) {
			w.endUs = int64(ordered[i+1].CreationTimestamp * 1_000_000)
			w.hasEnd = true
		}
		windows = append(windows, w)
	}

	buckets := make(map[string][]*tracepkg.Span, len(invocations))
	for _, inv := range invocations {
		buckets[inv.InvocationID] = nil
	}
	for _, span := range tr.AllSpans {
		for _, w := range windows {
			if span.StartTime >= w.startUs && (!w.hasEnd || span.StartTime < w.endUs) {
				buckets[w.id] = append(buckets[w.id], span)
				break
			}
		}
	}
	return buckets
}

type toolSpanMetrics struct {
	latencyMs   *int64
	resultBytes *int64
}

// extractToolMetrics returns per-tool-call latency/result size, in call order, for spans already scoped to one
// invocation by bucketSpansByInvocation. Only triage-core's voice module (internal/voice/session.go) emits
// tool.latency_ms/tool.result_bytes today; other sources simply won't have these tags.
func extractToolMetrics(spans []*tracepkg.Span) []toolSpanMetrics {
	var toolSpans []*tracepkg.Span
	for _, s := range spans {
		if adk.IsToolSpan(s) || strings.HasPrefix(s.OperationName, "execute_tool") {
			toolSpans = append(toolSpans, s)
		}
	}
	sort.Slice(toolSpans, func(i, j int) bool { return toolSpans[i].StartTime < toolSpans[j].StartTime })

	metrics := make([]toolSpanMetrics, 0, len(toolSpans))
	for _, s := range toolSpans {
		var m toolSpanMetrics
		if v := numericTag(s, adk.OpenInferenceToolLatencyMs); v != nil {
			m.latencyMs = v
		}
		if v := numericTag(s, adk.OpenInferenceToolResultBytes); v != nil {
			m.resultBytes = v
		}
		metrics = append(metrics, m)
	}
	return metrics
}

// extractModelInfoFromSpans extracts model info + turn latency from spans already scoped to one invocation by
// bucketSpansByInvocation.
func extractModelInfoFromSpans(spans []*tracepkg.Span) *ModelInfoDTO {
	if len(spans) == 0 {
		return nil
	}

	info := &ModelInfoDTO{}

	turnStart, turnEnd := spans[0].StartTime, spans[0].EndTime()
	for _, s := range spans {
		if s.StartTime < turnStart {
			turnStart = s.StartTime
		}
		if s.EndTime() > turnEnd {
			turnEnd = s.EndTime()
		}
	}
	if turnEnd > turnStart {
		info.TurnLatencyMs = float64(turnEnd-turnStart) / 1000.0
	}

	var llmSpans []*tracepkg.Span
	for _, s := range spans {
		if adk.IsLLMSpan(s) || strings.Contains(s.OperationName, "call_llm") {
			llmSpans = append(llmSpans, s)
		}
	}
	sort.Slice(llmSpans, func(i, j int) bool { return llmSpans[i].StartTime < llmSpans[j].StartTime })

	modelsUsed := map[string]bool{}
	var totalIn, totalOut int64
	provider := ""

	for _, s := range llmSpans {
		in, out, model := adk.ExtractTokenUsageFromAttrs(s.Tags)
		if model != "" && model != "unknown" {
			modelsUsed[model] = true
		} else if m := s.TagString(adk.GenAIRequestModel); m != "" {
			modelsUsed[m] = true
		}
		totalIn += in
		totalOut += out

		if provider == "" {
			if p := s.TagString(adk.GenAIProviderName); p != "" {
				provider = p
			} else if p := s.TagString(adk.GenAISystem); p != "" {
				provider = p
			}
		}
	}

	if len(modelsUsed) > 0 {
		models := make([]string, 0, len(modelsUsed))
		for m := range modelsUsed {
			models = append(models, m)
		}
		sort.Strings(models)
		info.Models = models
	}
	if totalIn > 0 {
		info.InputTokens = totalIn
	}
	if totalOut > 0 {
		info.OutputTokens = totalOut
	}
	if provider != "" {
		info.Provider = provider
	}

	if len(info.Models) == 0 && info.InputTokens == 0 && info.OutputTokens == 0 && info.Provider == "" && info.TurnLatencyMs == 0 {
		return nil
	}
	return info
}

func numericTag(s *tracepkg.Span, key string) *int64 {
	v, ok := s.Tags[key]
	if !ok {
		return nil
	}
	switch n := v.(type) {
	case int64:
		return &n
	case int:
		i := int64(n)
		return &i
	case float64:
		i := int64(n)
		return &i
	}
	return nil
}
