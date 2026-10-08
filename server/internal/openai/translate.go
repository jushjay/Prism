package openai

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/jushjay/prism/internal/codex"
	"github.com/jushjay/prism/internal/schema"
)

type TranslationResult struct {
	Request     codex.ResponsesRequest
	TupleSchema map[string]any
}

func ToCodex(req ChatCompletionRequest) TranslationResult {
	instructions := "You are a helpful assistant."
	var systemParts []string
	var input []codex.InputItem
	customToolNames := customToolNameSet(req.Tools)
	customCallIDs := map[string]struct{}{}
	for _, message := range req.Messages {
		switch message.Role {
		case "system", "developer":
			systemParts = append(systemParts, extractText(message.Content))
		case "assistant":
			text := extractText(message.Content)
			if text != "" || strings.TrimSpace(message.ReasoningContent) != "" {
				input = append(input, codex.InputItem{
					Role:             "assistant",
					Content:          text,
					ReasoningContent: message.ReasoningContent,
				})
			}
			for _, toolCall := range message.ToolCalls {
				if item, ok := customToolCallItem(toolCall, customToolNames); ok {
					customCallIDs[toolCall.ID] = struct{}{}
					input = append(input, item)
					continue
				}
				input = append(input, codex.InputItem{
					Type:      "function_call",
					CallID:    toolCall.ID,
					Name:      toolCall.Function.Name,
					Arguments: toolCall.Function.Arguments,
				})
			}
			if message.FunctionCall != nil {
				input = append(input, codex.InputItem{
					Type:      "function_call",
					CallID:    "fc_" + message.FunctionCall.Name,
					Name:      message.FunctionCall.Name,
					Arguments: message.FunctionCall.Arguments,
				})
			}
		case "tool", "function":
			outputType := "function_call_output"
			if _, ok := customCallIDs[message.ToolCallID]; ok && message.ToolCallID != "" {
				outputType = "custom_tool_call_output"
			}
			input = append(input, codex.InputItem{
				Type:   outputType,
				CallID: message.ToolCallID,
				Output: extractText(message.Content),
			})
		default:
			input = append(input, codex.InputItem{Role: "user", Content: normalizeContent(message.Content)})
		}
	}
	if len(systemParts) > 0 {
		instructions = strings.Join(systemParts, "\n\n")
	}
	request := codex.ResponsesRequest{
		Model:              strings.TrimSpace(req.Model),
		Instructions:       instructions,
		Input:              input,
		Stream:             req.Stream,
		Store:              false,
		PreviousResponseID: strings.TrimSpace(req.PreviousResponseID),
		PromptCacheKey:     derivePromptCacheKey(req.Messages),
	}
	explicitReasoning := strings.TrimSpace(req.ReasoningEffort)
	if explicitReasoning == "" && req.Reasoning != nil {
		explicitReasoning = strings.TrimSpace(req.Reasoning.Effort)
	}
	if explicitReasoning != "" {
		request.Reasoning = &codex.Reasoning{
			Effort:  explicitReasoning,
			Summary: "auto",
		}
	}
	if req.ServiceTier != "" {
		request.ServiceTier = req.ServiceTier
	}
	if len(req.Tools) > 0 {
		tools := make([]codex.Tool, 0, len(req.Tools))
		for _, tool := range req.Tools {
			if isCustomTool(tool) {
				tools = append(tools, customTool(tool))
				continue
			}
			toolName := strings.TrimSpace(tool.Function.Name)
			if toolName == "" {
				toolName = strings.TrimSpace(tool.Name)
			}
			toolDescription := strings.TrimSpace(tool.Function.Description)
			if toolDescription == "" {
				toolDescription = strings.TrimSpace(tool.Description)
			}
			toolParameters := tool.Function.Parameters
			if len(toolParameters) == 0 {
				toolParameters = tool.Parameters
			}
			functionParameters := tool.Function.Parameters
			if len(functionParameters) == 0 {
				functionParameters = tool.Parameters
			}
			tools = append(tools, codex.Tool{
				Type:        tool.Type,
				Name:        toolName,
				Description: toolDescription,
				Parameters:  toolParameters,
				Function: &codex.ToolFunction{
					Name:        toolName,
					Description: toolDescription,
					Parameters:  functionParameters,
				},
			})
		}
		request.Tools = tools
		request.ToolChoice = req.ToolChoice
	}
	if req.ResponseFormat != nil && req.ResponseFormat.Type != "text" {
		format := map[string]any{"type": req.ResponseFormat.Type}
		if req.ResponseFormat.JSONSchema != nil {
			prepared, tupleSchema := schema.Prepare(req.ResponseFormat.JSONSchema.Schema)
			format["name"] = req.ResponseFormat.JSONSchema.Name
			format["schema"] = prepared
			format["strict"] = req.ResponseFormat.JSONSchema.Strict
			request.Text = &codex.TextConfig{Format: format}
			if len(request.Input) == 0 {
				request.Input = []codex.InputItem{{Role: "user", Content: ""}}
			}
			return TranslationResult{
				Request:     request,
				TupleSchema: tupleSchema,
			}
		}
		request.Text = &codex.TextConfig{Format: format}
	}
	if len(request.Input) == 0 {
		request.Input = []codex.InputItem{{Role: "user", Content: ""}}
	}
	return TranslationResult{
		Request:     request,
		TupleSchema: nil,
	}
}

func isCustomTool(tool Tool) bool {
	return strings.TrimSpace(tool.Type) == "custom"
}

func customTool(tool Tool) codex.Tool {
	name := strings.TrimSpace(tool.Name)
	description := strings.TrimSpace(tool.Description)
	format := tool.Format
	if tool.Custom != nil {
		if name == "" {
			name = strings.TrimSpace(tool.Custom.Name)
		}
		if description == "" {
			description = strings.TrimSpace(tool.Custom.Description)
		}
		if len(format) == 0 {
			format = tool.Custom.Format
		}
	}
	return codex.Tool{
		Type:        "custom",
		Name:        name,
		Description: description,
		Format:      format,
	}
}

func customToolNameSet(tools []Tool) map[string]struct{} {
	names := map[string]struct{}{}
	for _, tool := range tools {
		if !isCustomTool(tool) {
			continue
		}
		if name := customTool(tool).Name; name != "" {
			names[name] = struct{}{}
		}
	}
	return names
}

// customToolCallItem maps an assistant tool call to a custom_tool_call input
// item. Cursor may echo custom tool calls either in the OpenAI "custom" shape
// or as a "function" call whose arguments hold the raw tool input.
func customToolCallItem(toolCall ToolCall, customToolNames map[string]struct{}) (codex.InputItem, bool) {
	if toolCall.Custom != nil || strings.TrimSpace(toolCall.Type) == "custom" {
		name := strings.TrimSpace(toolCall.Function.Name)
		inputText := toolCall.Function.Arguments
		if toolCall.Custom != nil {
			if customName := strings.TrimSpace(toolCall.Custom.Name); customName != "" {
				name = customName
			}
			inputText = toolCall.Custom.Input
		}
		return codex.InputItem{Type: "custom_tool_call", CallID: toolCall.ID, Name: name, Input: inputText}, true
	}
	name := strings.TrimSpace(toolCall.Function.Name)
	if _, ok := customToolNames[name]; !ok {
		return codex.InputItem{}, false
	}
	return codex.InputItem{Type: "custom_tool_call", CallID: toolCall.ID, Name: name, Input: toolCall.Function.Arguments}, true
}

func extractText(content any) string {
	switch typed := content.(type) {
	case string:
		return typed
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			part, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if part["type"] == "text" {
				if text, ok := part["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

func normalizeContent(content any) any {
	switch typed := content.(type) {
	case string:
		return typed
	case []any:
		output := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			part, ok := item.(map[string]any)
			if !ok {
				continue
			}
			switch part["type"] {
			case "text":
				output = append(output, map[string]any{
					"type": "input_text",
					"text": part["text"],
				})
			case "image_url":
				switch imageValue := part["image_url"].(type) {
				case string:
					output = append(output, map[string]any{"type": "input_image", "image_url": imageValue})
				case map[string]any:
					output = append(output, map[string]any{"type": "input_image", "image_url": imageValue["url"]})
				}
			}
		}
		if len(output) == 0 {
			return ""
		}
		return output
	default:
		return ""
	}
}

func derivePromptCacheKey(messages []ChatMessage) string {
	firstUserText := ""
	for _, message := range messages {
		if message.Role != "user" {
			continue
		}
		firstUserText = extractText(message.Content)
		if firstUserText != "" {
			break
		}
	}
	payload := map[string]any{
		"first_user":   firstUserText,
		"instructions": extractInstructions(messages),
	}
	raw, _ := json.Marshal(payload)
	sum := sha1.Sum(raw)
	return "conv_" + hex.EncodeToString(sum[:16])
}

func extractInstructions(messages []ChatMessage) string {
	var parts []string
	for _, message := range messages {
		if message.Role == "system" || message.Role == "developer" {
			parts = append(parts, extractText(message.Content))
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}
