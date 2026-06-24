package openai

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/jushjay/prism/internal/codex"
	"github.com/jushjay/prism/internal/config"
	"github.com/jushjay/prism/internal/schema"
)

type TranslationResult struct {
	Request                 codex.ResponsesRequest
	TupleSchema             map[string]any
	HasExplicitReasoning    bool
	ExplicitReasoningEffort string
}

func ToCodex(cfg config.Config, req ChatCompletionRequest) TranslationResult {
	instructions := "You are a helpful assistant."
	var systemParts []string
	var input []codex.InputItem
	for _, message := range req.Messages {
		switch message.Role {
		case "system", "developer":
			systemParts = append(systemParts, extractText(message.Content))
		case "assistant":
			text := extractText(message.Content)
			if text != "" {
				input = append(input, codex.InputItem{Role: "assistant", Content: text})
			}
			for _, toolCall := range message.ToolCalls {
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
			input = append(input, codex.InputItem{
				Type:   "function_call_output",
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
	if cfg.Model.InjectDesktopContext {
		instructions = "You are running behind the Prism compatibility layer.\n\n" + instructions
	}
	request := codex.ResponsesRequest{
		Model:          resolveModel(cfg, req.Model),
		Instructions:   instructions,
		Input:          input,
		Stream:         req.Stream,
		Store:          false,
		PromptCacheKey: derivePromptCacheKey(req.Messages),
	}
	effort := req.ReasoningEffort
	explicitReasoning := strings.TrimSpace(req.ReasoningEffort)
	if effort == "" {
		effort = cfg.Model.DefaultReasoningEffort
	}
	if effort != "" {
		request.Reasoning = &codex.Reasoning{
			Effort:  effort,
			Summary: "auto",
		}
	}
	if req.ServiceTier != "" {
		request.ServiceTier = req.ServiceTier
	}
	if len(req.Tools) > 0 {
		tools := make([]codex.Tool, 0, len(req.Tools))
		for _, tool := range req.Tools {
			tools = append(tools, codex.Tool{
				Type:        tool.Type,
				Name:        tool.Function.Name,
				Description: tool.Function.Description,
				Parameters:  tool.Function.Parameters,
				Function: &codex.ToolFunction{
					Name:        tool.Function.Name,
					Description: tool.Function.Description,
					Parameters:  tool.Function.Parameters,
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
				Request:                 request,
				TupleSchema:             tupleSchema,
				HasExplicitReasoning:    explicitReasoning != "",
				ExplicitReasoningEffort: explicitReasoning,
			}
		}
		request.Text = &codex.TextConfig{Format: format}
	}
	if len(request.Input) == 0 {
		request.Input = []codex.InputItem{{Role: "user", Content: ""}}
	}
	return TranslationResult{
		Request:                 request,
		TupleSchema:             nil,
		HasExplicitReasoning:    explicitReasoning != "",
		ExplicitReasoningEffort: explicitReasoning,
	}
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

func resolveModel(cfg config.Config, model string) string {
	if strings.TrimSpace(model) == "" {
		return cfg.Model.DefaultModel
	}
	return model
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
