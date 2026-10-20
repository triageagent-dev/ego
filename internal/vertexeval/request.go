package vertexeval

import (
	"context"

	"google.golang.org/genai"
)

// Result is one metric's outcome from the Vertex Managed Eval Service,
// already unwrapped from the RPC's response envelope.
type Result struct {
	// Score is nil if Vertex didn't return one (e.g. NOT_EVALUATED turns in
	// a multi-turn conversation, or a metric error - check Error/logs).
	Score       *float64
	Explanation string
}

// predefinedMetricSpec and metricSpec mirror vertexai._genai.types.PredefinedMetricSpec / Metric.
type predefinedMetricSpec struct {
	MetricSpecName       string `json:"metric_spec_name"`
	MetricSpecParameters any    `json:"metric_spec_parameters"`
}

type metricSpec struct {
	PredefinedMetricSpec predefinedMetricSpec `json:"predefined_metric_spec"`
}

// instanceDataContents / instanceData mirror
// vertexai._genai.types.evals.InstanceDataContents / InstanceData: a
// single genai.Content wrapped twice ({"contents":{"contents":[content]}}),
// exactly as google-adk's _content_to_instance_data builds it.
type instanceDataContents struct {
	Contents []*genai.Content `json:"contents,omitempty"`
}

type instanceData struct {
	Contents *instanceDataContents `json:"contents,omitempty"`
}

func contentInstanceData(c *genai.Content) *instanceData {
	if c == nil {
		return nil
	}
	return &instanceData{Contents: &instanceDataContents{Contents: []*genai.Content{c}}}
}

// agentEvent, conversationTurn, agentConfig, and agentEvalData mirror vertexai._genai.types.evals.AgentEvent /
// ConversationTurn / AgentConfig / AgentData.
type agentEvent struct {
	Author  string         `json:"author"`
	Content *genai.Content `json:"content,omitempty"`
}

type conversationTurn struct {
	TurnIndex int          `json:"turn_index"`
	TurnID    string       `json:"turn_id"`
	Events    []agentEvent `json:"events"`
}

type agentConfig struct {
	AgentID     string        `json:"agent_id,omitempty"`
	Instruction string        `json:"instruction,omitempty"`
	Tools       []*genai.Tool `json:"tools,omitempty"`
}

// agentEvalData is EvaluationInstance's "agent_data" field renamed to its actual wire name "agent_eval_data"
// -.cloud.aiplatform_v1beta1.types.evaluation_service.
type agentEvalData struct {
	Agents map[string]agentConfig `json:"agents,omitempty"`
	Turns  []conversationTurn     `json:"turns,omitempty"`
}

// evaluationInstance mirrors vertexai._genai.types.EvaluationInstance as
// sent on the wire (all four field names are unchanged snake_case except
// agent_data -> agent_eval_data, per _EvaluationInstance_to_vertex).
type evaluationInstance struct {
	Prompt        *instanceData  `json:"prompt,omitempty"`
	Response      *instanceData  `json:"response,omitempty"`
	Reference     *instanceData  `json:"reference,omitempty"`
	AgentEvalData *agentEvalData `json:"agent_eval_data,omitempty"`
}

type evaluateInstancesRequest struct {
	Metrics  []metricSpec       `json:"metrics"`
	Instance evaluationInstance `json:"instance"`
}

func singleMetricSpec(metricSpecName string) []metricSpec {
	return []metricSpec{{PredefinedMetricSpec: predefinedMetricSpec{MetricSpecName: metricSpecName}}}
}

// singleTurnAgentEvalData replicates google-adk's
// _eval_case_to_agent_data for the single-turn case: Vertex's single-turn
// predefined metrics (safety_v1, etc.) are unconditionally given a
// synthetic one-turn conversation (user prompt event, then model response
// event) alongside the flat prompt/response/reference fields - both are
// sent together, not one or the other.
func singleTurnAgentEvalData(prompt, response *genai.Content) *agentEvalData {
	var events []agentEvent
	if prompt != nil {
		events = append(events, agentEvent{Author: "user", Content: prompt})
	}
	if response != nil {
		events = append(events, agentEvent{Author: "model", Content: response})
	}
	if len(events) == 0 {
		return nil
	}
	return &agentEvalData{Turns: []conversationTurn{{TurnIndex: 0, TurnID: "turn_0", Events: events}}}
}

// EvaluateSingleTurn scores one prompt/response pair (optionally against a
// reference) with a Vertex predefined single-turn metric (e.g. safety_v1).
// reference may be nil.
func (c *Client) EvaluateSingleTurn(ctx context.Context, metricSpecName string, prompt, response, reference *genai.Content) (*Result, error) {
	req := evaluateInstancesRequest{
		Metrics: singleMetricSpec(metricSpecName),
		Instance: evaluationInstance{
			Prompt:        contentInstanceData(prompt),
			Response:      contentInstanceData(response),
			Reference:     contentInstanceData(reference),
			AgentEvalData: singleTurnAgentEvalData(prompt, response),
		},
	}
	mr, err := c.evaluateInstances(ctx, req)
	if err != nil {
		return nil, err
	}
	return &Result{Score: mr.Score, Explanation: mr.Explanation}, nil
}

// EvaluateMultiTurn scores a full conversation with a Vertex predefined multi-turn metric. turns is built by
// the caller (see internal/vertexeval's metrics.go, which maps adk.Invocation slices into this shape);
// toolNames (may be empty) synthesizes an "agent" entry in the agents map with bare FunctionDeclarations.
func (c *Client) EvaluateMultiTurn(ctx context.Context, metricSpecName string, turns []conversationTurn, toolNames []string) (*Result, error) {
	data := &agentEvalData{Turns: turns}
	if len(toolNames) > 0 {
		decls := make([]*genai.FunctionDeclaration, len(toolNames))
		for i, name := range toolNames {
			decls[i] = &genai.FunctionDeclaration{Name: name}
		}
		data.Agents = map[string]agentConfig{
			"agent": {AgentID: "agent", Tools: []*genai.Tool{{FunctionDeclarations: decls}}},
		}
	}
	req := evaluateInstancesRequest{
		Metrics: singleMetricSpec(metricSpecName),
		Instance: evaluationInstance{
			AgentEvalData: data,
		},
	}
	mr, err := c.evaluateInstances(ctx, req)
	if err != nil {
		return nil, err
	}
	return &Result{Score: mr.Score, Explanation: mr.Explanation}, nil
}
