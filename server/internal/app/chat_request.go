package app

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/jushjay/prism/internal/codex"
	"github.com/jushjay/prism/internal/config"
	"github.com/jushjay/prism/internal/openai"
	"github.com/jushjay/prism/internal/schema"
)

type chatProxyRequest struct {
	CodexRequest         codex.ResponsesRequest
	TupleSchema          map[string]any
	RequestedModel       string
	Stream               bool
	HasExplicitReasoning bool
}

func parseChatProxyRequest(cfg config.Config, body []byte, userAgent string) (chatProxyRequest, error) {
	compat, err := parseChatCompatRequest(cfg, body, userAgent)
	if err != nil {
		return chatProxyRequest{}, err
	}
	if compat != nil {
		return *compat, nil
	}

	request, err := parseChatCompletionRequest(body, userAgent)
	if err != nil {
		return chatProxyRequest{}, err
	}
	if len(request.Messages) == 0 {
		return chatProxyRequest{}, errors.New("chat completions requests require messages")
	}
	translation := openai.ToCodex(cfg, request)
	return chatProxyRequest{
		CodexRequest:         translation.Request,
		TupleSchema:          translation.TupleSchema,
		RequestedModel:       request.Model,
		Stream:               request.Stream,
		HasExplicitReasoning: translation.HasExplicitReasoning,
	}, nil
}

func parseChatCompletionRequest(body []byte, userAgent string) (openai.ChatCompletionRequest, error) {
	if isCursorUserAgent(userAgent) {
		body = normalizeCursorChatCompletionBody(body)
	}

	var request openai.ChatCompletionRequest
	if err := json.Unmarshal(body, &request); err != nil {
		return openai.ChatCompletionRequest{}, err
	}
	return request, nil
}

func parseChatCompatRequest(cfg config.Config, body []byte, userAgent string) (*chatProxyRequest, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	if !isCursorUserAgent(userAgent) {
		return nil, nil
	}
	if !looksLikeResponsesCompatBody(payload) {
		return nil, nil
	}

	var request codex.ResponsesRequest
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, err
	}
	normalizeFunctionCallOutputs(request.Input)

	var tupleSchema map[string]any
	if request.Text != nil {
		if formatType, _ := request.Text.Format["type"].(string); formatType == "json_schema" {
			if rawSchema, ok := request.Text.Format["schema"].(map[string]any); ok {
				prepared, original := schema.Prepare(rawSchema)
				request.Text.Format["schema"] = prepared
				tupleSchema = original
			}
		}
	}
	if request.Reasoning != nil && strings.TrimSpace(request.Reasoning.Summary) == "" {
		request.Reasoning.Summary = "auto"
	}
	if strings.TrimSpace(request.Model) == "" {
		request.Model = cfg.Model.DefaultModel
	}
	request.ForceInstructions = true
	request.Store = false
	if len(request.Input) == 0 {
		request.Input = []codex.InputItem{{Role: "user", Content: ""}}
	}
	if strings.TrimSpace(request.PromptCacheKey) == "" {
		// prompt_cache_key is a cache/affinity hint only. Continuations must stay
		// explicit via previous_response_id so Cursor-style full-history replay is preserved.
		request.PromptCacheKey = codex.StableConversationKey(request)
	}

	requestedModel := request.Model
	if rawModel, ok := payload["model"].(string); ok && strings.TrimSpace(rawModel) != "" {
		requestedModel = rawModel
	}

	return &chatProxyRequest{
		CodexRequest:         request,
		TupleSchema:          tupleSchema,
		RequestedModel:       requestedModel,
		Stream:               request.Stream,
		HasExplicitReasoning: request.Reasoning != nil && strings.TrimSpace(request.Reasoning.Effort) != "",
	}, nil
}

func normalizeFunctionCallOutputs(input []codex.InputItem) {
	for i := range input {
		item := &input[i]
		if item.Type != "function_call_output" {
			continue
		}
		item.Output = normalizeFunctionCallOutput(item.Output)
	}
}

func normalizeFunctionCallOutput(output any) any {
	switch typed := output.(type) {
	case []any:
		return extractToolOutputText(typed)
	case []map[string]any:
		parts := make([]any, 0, len(typed))
		for _, part := range typed {
			parts = append(parts, part)
		}
		return extractToolOutputText(parts)
	default:
		return output
	}
}

func extractToolOutputText(parts []any) string {
	var textParts []string
	for _, raw := range parts {
		part, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch partType, _ := part["type"].(string); partType {
		case "input_text", "output_text", "text":
			if text, ok := part["text"].(string); ok && text != "" {
				textParts = append(textParts, text)
			}
		}
	}
	if len(textParts) > 0 {
		return strings.Join(textParts, "\n")
	}
	raw, err := json.Marshal(parts)
	if err != nil {
		return ""
	}
	return string(raw)
}

func isCursorUserAgent(userAgent string) bool {
	return strings.Contains(userAgent, "Cursor/")
}

func looksLikeResponsesCompatBody(payload map[string]any) bool {
	if _, ok := payload["messages"].([]any); ok {
		return false
	}
	if _, ok := payload["input"].([]any); ok {
		return true
	}
	if instructions, ok := payload["instructions"].(string); ok && strings.TrimSpace(instructions) != "" {
		return true
	}
	rawTools, ok := payload["tools"].([]any)
	if !ok {
		return false
	}
	for _, rawTool := range rawTools {
		tool, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		if _, ok := tool["name"].(string); ok {
			return true
		}
		if toolType, _ := tool["type"].(string); toolType == "custom" {
			return true
		}
	}
	return false
}

func normalizeCursorChatCompletionBody(body []byte) []byte {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}

	rawTools, ok := payload["tools"].([]any)
	if !ok || len(rawTools) == 0 {
		return body
	}

	changed := false
	for _, rawTool := range rawTools {
		tool, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		toolType, _ := tool["type"].(string)
		if toolType != "function" {
			continue
		}

		function, _ := tool["function"].(map[string]any)
		if function == nil {
			function = map[string]any{}
		}

		rootName, _ := tool["name"].(string)
		rootDescription, _ := tool["description"].(string)
		functionName, _ := function["name"].(string)
		functionDescription, _ := function["description"].(string)

		if strings.TrimSpace(functionName) == "" && strings.TrimSpace(rootName) != "" {
			function["name"] = rootName
			changed = true
		}
		if strings.TrimSpace(rootName) == "" && strings.TrimSpace(functionName) != "" {
			tool["name"] = functionName
			changed = true
		}
		if strings.TrimSpace(functionDescription) == "" && strings.TrimSpace(rootDescription) != "" {
			function["description"] = rootDescription
			changed = true
		}
		if _, exists := function["parameters"]; !exists {
			if rootParameters, ok := tool["parameters"].(map[string]any); ok {
				function["parameters"] = rootParameters
				changed = true
			}
		}
		if len(function) > 0 {
			tool["function"] = function
		}
	}

	if !changed {
		return body
	}

	normalized, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return normalized
}
