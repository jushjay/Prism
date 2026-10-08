package codex

import "encoding/json"

type ResponsesRequest struct {
	Model                string         `json:"model"`
	User                 string         `json:"user,omitempty"`
	Instructions         string         `json:"instructions,omitempty"`
	Input                []InputItem    `json:"input"`
	Stream               bool           `json:"stream"`
	Store                bool           `json:"store"`
	PreviousResponseID   string         `json:"previous_response_id,omitempty"`
	Reasoning            *Reasoning     `json:"reasoning,omitempty"`
	Tools                []Tool         `json:"tools,omitempty"`
	ToolChoice           any            `json:"tool_choice,omitempty"`
	Text                 *TextConfig    `json:"text,omitempty"`
	PromptCacheKey       string         `json:"prompt_cache_key,omitempty"`
	PromptCacheRetention string         `json:"prompt_cache_retention,omitempty"`
	Include              []string       `json:"include,omitempty"`
	Metadata             map[string]any `json:"metadata,omitempty"`
	StreamOptions        map[string]any `json:"stream_options,omitempty"`
	ServiceTier          string         `json:"service_tier,omitempty"`
	ForceInstructions    bool           `json:"-"`
}

type Reasoning struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
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

type TextConfig struct {
	Format map[string]any `json:"format"`
}

type Tool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	Format      map[string]any `json:"format,omitempty"`
	Strict      bool           `json:"strict,omitempty"`
	Function    *ToolFunction  `json:"function,omitempty"`
}

type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type InputItem struct {
	Role             string `json:"role,omitempty"`
	Type             string `json:"type,omitempty"`
	Content          any    `json:"content,omitempty"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
	CallID           string `json:"call_id,omitempty"`
	Name             string `json:"name,omitempty"`
	Arguments        string `json:"arguments,omitempty"`
	Input            string `json:"input,omitempty"`
	Output           any    `json:"output,omitempty"`
}

type Usage struct {
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	CachedTokens        int `json:"cached_tokens,omitempty"`
	ReasoningTokens     int `json:"reasoning_tokens,omitempty"`
	TotalTokens         int `json:"total_tokens,omitempty"`
	InputDetailsCached  int `json:"-"`
	PromptDetailsCached int `json:"-"`
	CompletionReasoning int `json:"-"`
}

type SSEEvent struct {
	Event string
	Data  []byte
}
