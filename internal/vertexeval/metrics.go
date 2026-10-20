package vertexeval

import (
	"context"
	"fmt"

	"google.golang.org/genai"

	"github.com/triageagent-dev/ego/internal/adk"
	"github.com/triageagent-dev/ego/internal/eval"
)

// toGenaiContent converts our adk.Content into the *genai.Content type
// this package's request builders (and their JSON tags, which match
// google.golang.org/genai's own hand-written wire format) expect.
func toGenaiContent(c *adk.Content) *genai.Content {
	if c == nil {
		return nil
	}
	parts := make([]*genai.Part, 0, len(c.Parts))
	for _, p := range c.Parts {
		gp := &genai.Part{Text: p.Text}
		if p.FunctionCall != nil {
			gp.FunctionCall = &genai.FunctionCall{ID: p.FunctionCall.ID, Name: p.FunctionCall.Name, Args: p.FunctionCall.Args}
		}
		if p.FunctionResponse != nil {
			gp.FunctionResponse = &genai.FunctionResponse{ID: p.FunctionResponse.ID, Name: p.FunctionResponse.Name, Response: p.FunctionResponse.Response}
		}
		parts = append(parts, gp)
	}
	return &genai.Content{Role: c.Role, Parts: parts}
}

// SafetyV1 scores each actual invocation's prompt/response pair with Vertex's safety_v1 predefined metric and
// averages the per-invocation scores.
func SafetyV1(ctx context.Context, client *Client, actual, expected []adk.Invocation, threshold float64) (eval.Result, error) {
	return runSingleTurnMetric(ctx, client, "safety_v1", actual, expected, threshold)
}

// runSingleTurnMetric implements _SingleTurnVertexAiEvalFacade.evaluate_invocations: one :evaluateInstances
// call. The overall score is the mean over invocations that did get scored.
func runSingleTurnMetric(ctx context.Context, client *Client, metricSpecName string, actual, expected []adk.Invocation, threshold float64) (eval.Result, error) {
	var total float64
	var numScored int
	scores := make([]float64, len(actual))
	for i, inv := range actual {
		if inv.UserText() == "" {
			continue
		}
		var reference *genai.Content
		if i < len(expected) {
			reference = toGenaiContent(expected[i].FinalResponse)
		}
		result, err := client.EvaluateSingleTurn(ctx, metricSpecName, toGenaiContent(inv.UserContent), toGenaiContent(inv.FinalResponse), reference)
		if err != nil {
			return eval.Result{}, fmt.Errorf("invocation %d: %w", i, err)
		}
		if result.Score != nil {
			scores[i] = *result.Score
			total += *result.Score
			numScored++
		}
	}

	if numScored == 0 {
		return eval.Result{MetricName: metricSpecName, Status: eval.StatusNotEvaluated}, nil
	}
	overall := total / float64(numScored)
	status := eval.StatusFailed
	if overall >= threshold {
		status = eval.StatusPassed
	}
	return eval.Result{MetricName: metricSpecName, Score: overall, Status: status, PerInvocationScores: scores}, nil
}

func MultiTurnTaskSuccessV1(ctx context.Context, client *Client, actual []adk.Invocation, threshold float64) (eval.Result, error) {
	return runMultiTurnMetric(ctx, client, "multi_turn_task_success_v1", actual, threshold)
}

func MultiTurnTrajectoryQualityV1(ctx context.Context, client *Client, actual []adk.Invocation, threshold float64) (eval.Result, error) {
	return runMultiTurnMetric(ctx, client, "multi_turn_trajectory_quality_v1", actual, threshold)
}

func MultiTurnToolUseQualityV1(ctx context.Context, client *Client, actual []adk.Invocation, threshold float64) (eval.Result, error) {
	return runMultiTurnMetric(ctx, client, "multi_turn_tool_use_quality_v1", actual, threshold)
}

// runMultiTurnMetric implements _MultiTurnVertexiAiEvalFacade.evaluate_invocations: a single :evaluateInstances
// call over the whole conversation. Only the last invocation is actually scored - Vertex's multi-turn metrics
// judge the conversation.
func runMultiTurnMetric(ctx context.Context, client *Client, metricSpecName string, actual []adk.Invocation, threshold float64) (eval.Result, error) {
	if len(actual) == 0 {
		return eval.Result{MetricName: metricSpecName, Status: eval.StatusNotEvaluated}, nil
	}

	turns := buildConversationTurns(actual)
	toolNames := collectToolNames(actual)

	result, err := client.EvaluateMultiTurn(ctx, metricSpecName, turns, toolNames)
	if err != nil {
		return eval.Result{}, err
	}

	scores := make([]float64, len(actual))
	if result.Score == nil {
		return eval.Result{MetricName: metricSpecName, Status: eval.StatusNotEvaluated, PerInvocationScores: scores}, nil
	}
	scores[len(scores)-1] = *result.Score
	status := eval.StatusFailed
	if *result.Score >= threshold {
		status = eval.StatusPassed
	}
	return eval.Result{MetricName: metricSpecName, Score: *result.Score, Status: status, PerInvocationScores: scores}, nil
}

// buildConversationTurns replicates
// _MultiTurnVertexiAiEvalFacade._get_turns/_map_invocation_turn: one
// ConversationTurn per invocation (turn_index = position in the list,
// turn_id = the invocation's own ID), each holding a user event, that
// invocation's tool-call/tool-response events (see toolEvents), and a
// final agent-response event, in that order.
func buildConversationTurns(invocations []adk.Invocation) []conversationTurn {
	turns := make([]conversationTurn, len(invocations))
	for i, inv := range invocations {
		var events []agentEvent
		events = append(events, agentEvent{Author: "user", Content: toGenaiContent(inv.UserContent)})
		events = append(events, toolEvents(inv)...)
		events = append(events, agentEvent{Author: "agent", Content: toGenaiContent(inv.FinalResponse)})

		turnID := inv.InvocationID
		if turnID == "" {
			turnID = fmt.Sprintf("turn_%d", i)
		}
		turns[i] = conversationTurn{TurnIndex: i, TurnID: turnID, Events: events}
	}
	return turns
}

// ADK's native runtime authors both calls and responses with the agent name (no separate "tool" actor); we use
// "agent" to match that convention so the Vertex judges see the dialog in the shape they expect.
func toolEvents(inv adk.Invocation) []agentEvent {
	if inv.IntermediateData == nil {
		return nil
	}
	data := inv.IntermediateData

	responseByID := make(map[string]adk.FunctionResponse, len(data.ToolResponses))
	for _, tr := range data.ToolResponses {
		if tr.ID != "" {
			responseByID[tr.ID] = tr
		}
	}

	var events []agentEvent
	for i, tc := range data.ToolUses {
		events = append(events, agentEvent{
			Author: "agent",
			Content: &genai.Content{Role: "model", Parts: []*genai.Part{{
				FunctionCall: &genai.FunctionCall{ID: tc.ID, Name: tc.Name, Args: tc.Args},
			}}},
		})

		var match *adk.FunctionResponse
		if tc.ID != "" {
			if r, ok := responseByID[tc.ID]; ok {
				match = &r
			}
		} else if i < len(data.ToolResponses) && data.ToolResponses[i].ID == "" {
			match = &data.ToolResponses[i]
		}
		if match != nil {
			events = append(events, agentEvent{
				Author: "agent",
				Content: &genai.Content{Role: "user", Parts: []*genai.Part{{
					FunctionResponse: &genai.FunctionResponse{ID: match.ID, Name: match.Name, Response: match.Response},
				}}},
			})
		}
	}
	return events
}

func collectToolNames(invocations []adk.Invocation) []string {
	seen := map[string]bool{}
	var names []string
	for _, inv := range invocations {
		if inv.IntermediateData == nil {
			continue
		}
		for _, tc := range inv.IntermediateData.ToolUses {
			if tc.Name != "" && !seen[tc.Name] {
				seen[tc.Name] = true
				names = append(names, tc.Name)
			}
		}
	}
	return names
}
