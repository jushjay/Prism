package openai

import "encoding/json"

type ChatCompletionRequest struct {
	Model              string          `json:"model"`
	Messages           []ChatMessage   `json:"messages"`
	Stream             bool            `json:"stream"`
	PreviousResponseID string          `json:"previous_response_id,omitempty"`
	ReasoningEffort    string          `json:"reasoning_effort,omitempty"`
	Reasoning          *Reasoning      `json:"reasoning,omitempty"`
	ServiceTier        string          `json:"service_tier,omitempty"`
	Tools              []Tool          `json:"tools,omitempty"`
	ToolChoice         any             `json:"tool_choice,omitempty"`
	ResponseFormat     *ResponseFormat `json:"response_format,omitempty"`
}

type Reasoning struct {
	Effort string `json:"effort,omitempty"`
}

// UnmarshalJSON accepts both {"effort":"high"} and the bare string form
// "high" that Cursor sends for some models.
func (r *Reasoning) UnmarshalJSON(data []byte) error {
	var effort string
	if err := json.Unmarshal(data, &effort); err == nil {
		r.Effort = effort
		return nil
	}
	type reasoningObject Reasoning
	var object reasoningObject
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	*r = Reasoning(object)
	return nil
}

type ChatMessage struct {
	Role             string      `json:"role"`
	Content          any         `json:"content"`
	ReasoningContent string      `json:"reasoning_content,omitempty"`
	Name             string      `json:"name,omitempty"`
	ToolCalls        []ToolCall  `json:"tool_calls,omitempty"`
	ToolCallID       string      `json:"tool_call_id,omitempty"`
	FunctionCall     *ToolCallFn `json:"function_call,omitempty"`
}

type Tool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  map[string]any  `json:"parameters,omitempty"`
	Format      map[string]any  `json:"format,omitempty"`
	Function    ToolSpec        `json:"function"`
	Custom      *CustomToolSpec `json:"custom,omitempty"`
}

type ToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type CustomToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Format      map[string]any `json:"format,omitempty"`
}

type ToolCall struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Function ToolCallFn      `json:"function"`
	Custom   *CustomToolCall `json:"custom,omitempty"`
}

type ToolCallFn struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type CustomToolCall struct {
	Name  string `json:"name"`
	Input string `json:"input"`
}

type ResponseFormat struct {
	Type       string           `json:"type"`
	JSONSchema *JSONSchemaBlock `json:"json_schema,omitempty"`
}

type JSONSchemaBlock struct {
	Name   string         `json:"name"`
	Schema map[string]any `json:"schema"`
	Strict bool           `json:"strict,omitempty"`
}
