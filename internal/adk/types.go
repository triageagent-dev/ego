package adk

// Part is one part of a Content: exactly one of Text, FunctionCall, or
// FunctionResponse is set, matching genai_types.Part's discriminated shape.
type Part struct {
	Text             string            `json:"text,omitempty"`
	FunctionCall     *FunctionCall     `json:"function_call,omitempty"`
	FunctionResponse *FunctionResponse `json:"function_response,omitempty"`
}

// Content is a single turn of conversation content (genai_types.Content).
type Content struct {
	Role  string `json:"role"`
	Parts []Part `json:"parts"`
}

// TextOnly returns a Content with a single text part.
func TextOnly(role, text string) *Content {
	return &Content{Role: role, Parts: []Part{{Text: text}}}
}

// Text concatenates every text-bearing part with a newline.
func (c *Content) Text() string {
	if c == nil {
		return ""
	}
	out := ""
	for i, p := range c.Parts {
		if p.Text == "" {
			continue
		}
		if i > 0 && out != "" {
			out += "\n"
		}
		out += p.Text
	}
	return out
}

// FunctionCall is a single tool invocation (genai_types.FunctionCall).
type FunctionCall struct {
	ID   string         `json:"id,omitempty"`
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

// FunctionResponse is a single tool result (genai_types.FunctionResponse).
type FunctionResponse struct {
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

// IntermediateData holds the tool calls/responses between user input and
// final response for one invocation.
type IntermediateData struct {
	ToolUses      []FunctionCall     `json:"tool_uses,omitempty"`
	ToolResponses []FunctionResponse `json:"tool_responses,omitempty"`
}

// Invocation is a single agent turn: the language-agnostic unit every
// evaluator scores. Mirrors google.adk.evaluation.eval_case.Invocation.
type Invocation struct {
	InvocationID      string            `json:"invocation_id,omitempty"`
	UserContent       *Content          `json:"user_content,omitempty"`
	FinalResponse     *Content          `json:"final_response,omitempty"`
	IntermediateData  *IntermediateData `json:"intermediate_data,omitempty"`
	CreationTimestamp float64           `json:"creation_timestamp,omitempty"`
}

// UserText concatenates every text part of UserContent with a space.
func (inv Invocation) UserText() string {
	if inv.UserContent == nil {
		return ""
	}
	out := ""
	for _, p := range inv.UserContent.Parts {
		if p.Text == "" {
			continue
		}
		if out != "" {
			out += " "
		}
		out += p.Text
	}
	return out
}

// ToolCalls returns the invocation's tool calls, or nil if none.
func (inv Invocation) ToolCalls() []FunctionCall {
	if inv.IntermediateData == nil {
		return nil
	}
	return inv.IntermediateData.ToolUses
}

// EvalCase is one golden scenario: a conversation of expected invocations.
type EvalCase struct {
	EvalID       string       `json:"eval_id"`
	Conversation []Invocation `json:"conversation"`
}

// EvalSet is a golden reference file, portable with ADK's own EvalSet JSON
// schema (docs/eval-set-format.md).
type EvalSet struct {
	EvalSetID         string     `json:"eval_set_id"`
	Name              string     `json:"name,omitempty"`
	Description       string     `json:"description,omitempty"`
	EvalCases         []EvalCase `json:"eval_cases"`
	CreationTimestamp float64    `json:"creation_timestamp,omitempty"`
}
