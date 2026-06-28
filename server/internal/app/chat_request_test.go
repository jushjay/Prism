package app

import (
	"testing"

	"github.com/jushjay/prism/internal/config"
	"github.com/jushjay/prism/internal/openai"
)

func TestParseChatCompletionRequestNormalizesCursorTools(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5.4",
		"messages": [{"role":"user","content":"hi"}],
		"tools": [{
			"type": "function",
			"name": "search_docs",
			"description": "Search docs",
			"parameters": {
				"type": "object",
				"properties": {
					"query": {"type": "string"}
				}
			}
		}]
	}`)

	request, err := parseChatCompletionRequest(body, "Cursor/1.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(request.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(request.Tools))
	}
	if got := request.Tools[0].Function.Name; got != "search_docs" {
		t.Fatalf("expected function name to be normalized, got %q", got)
	}
	if got := request.Tools[0].Function.Description; got != "Search docs" {
		t.Fatalf("expected function description to be normalized, got %q", got)
	}
	if request.Tools[0].Function.Parameters == nil {
		t.Fatal("expected function parameters to be normalized")
	}
}

func TestParseChatCompletionRequestDoesNotNormalizeNonCursorTools(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5.4",
		"messages": [{"role":"user","content":"hi"}],
		"tools": [{
			"type": "function",
			"name": "search_docs"
		}]
	}`)

	request, err := parseChatCompletionRequest(body, "curl/8.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(request.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(request.Tools))
	}
	if got := request.Tools[0].Function.Name; got != "" {
		t.Fatalf("expected non-Cursor request to remain unchanged, got function name %q", got)
	}
}

func TestParseChatProxyRequestSupportsResponsesCompatTools(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5.4",
		"input": [
			{"role":"user","content":"hi"},
			{
				"type":"function_call_output",
				"call_id":"call_1",
				"output":[{"type":"input_text","text":"tool result"}]
			}
		],
		"stream": true,
		"tools": [
			{
				"type": "function",
				"name": "Shell",
				"description": "Exec shell command",
				"parameters": {
					"type": "object",
					"properties": {
						"command": {"type": "string"}
					}
				}
			},
			{
				"type": "custom",
				"name": "ApplyPatch",
				"description": "Edit files",
				"format": {
					"type": "grammar",
					"syntax": "lark"
				}
			}
		],
		"user": "cursor-user",
		"metadata": {
			"cursorConversationId": "conv_123"
		},
		"prompt_cache_retention": "24h",
		"stream_options": {
			"include_usage": true
		}
	}`)

	proxyRequest, err := parseChatProxyRequest(config.Config{
		Model: config.ModelConfig{DefaultModel: "gpt-5.4"},
	}, body, "Cursor/1.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !proxyRequest.Stream {
		t.Fatal("expected compat request to preserve stream flag")
	}
	if got := len(proxyRequest.CodexRequest.Tools); got != 2 {
		t.Fatalf("expected 2 tools, got %d", got)
	}
	if got := proxyRequest.CodexRequest.Tools[0].Name; got != "Shell" {
		t.Fatalf("expected first tool name Shell, got %q", got)
	}
	if got := proxyRequest.CodexRequest.Tools[0].Description; got != "Exec shell command" {
		t.Fatalf("expected first tool description to be preserved, got %q", got)
	}
	if proxyRequest.CodexRequest.Tools[0].Parameters == nil {
		t.Fatal("expected first tool parameters to be preserved")
	}
	if got := proxyRequest.CodexRequest.Tools[1].Type; got != "custom" {
		t.Fatalf("expected second tool type custom, got %q", got)
	}
	if got := proxyRequest.CodexRequest.Tools[1].Name; got != "ApplyPatch" {
		t.Fatalf("expected second tool name ApplyPatch, got %q", got)
	}
	if got := proxyRequest.CodexRequest.Tools[1].Format["type"]; got != "grammar" {
		t.Fatalf("expected custom tool format to be preserved, got %#v", got)
	}
	if got := proxyRequest.CodexRequest.User; got != "cursor-user" {
		t.Fatalf("expected user to be preserved, got %q", got)
	}
	if got := proxyRequest.CodexRequest.Metadata["cursorConversationId"]; got != "conv_123" {
		t.Fatalf("expected metadata to be preserved, got %#v", got)
	}
	if got := proxyRequest.CodexRequest.PromptCacheRetention; got != "24h" {
		t.Fatalf("expected prompt cache retention to be preserved, got %q", got)
	}
	if got := proxyRequest.CodexRequest.StreamOptions["include_usage"]; got != true {
		t.Fatalf("expected stream options to be preserved, got %#v", got)
	}
	if !proxyRequest.CodexRequest.ForceInstructions {
		t.Fatal("expected compat request to force instructions serialization")
	}
	outputItem := proxyRequest.CodexRequest.Input[1]
	if got, ok := outputItem.Output.(string); !ok || got != "tool result" {
		t.Fatalf("expected function_call_output array to normalize to text, got %#v", outputItem.Output)
	}
}

func TestParseChatProxyRequestPreservesCustomToolCallInput(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5.4",
		"input": [
			{"role":"user","content":"hi"},
			{
				"type":"custom_tool_call",
				"call_id":"call_1",
				"name":"ApplyPatch",
				"input":"*** Begin Patch\n*** End Patch\n"
			},
			{
				"type":"custom_tool_call_output",
				"call_id":"call_1",
				"output":[{"type":"input_text","text":"updated"}]
			}
		],
		"stream": true
	}`)

	proxyRequest, err := parseChatProxyRequest(config.Config{
		Model: config.ModelConfig{DefaultModel: "gpt-5.4"},
	}, body, "Cursor/1.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := proxyRequest.CodexRequest.Input[1].Input; got != "*** Begin Patch\n*** End Patch\n" {
		t.Fatalf("expected custom_tool_call input to be preserved, got %q", got)
	}
}

func TestNormalizeFunctionCallOutputFallsBackToJSON(t *testing.T) {
	output := normalizeFunctionCallOutput([]any{
		map[string]any{"type": "input_image", "image_url": "data:image/png;base64,abc"},
	})
	got, ok := output.(string)
	if !ok {
		t.Fatalf("expected normalized output to be string, got %#v", output)
	}
	if got != `[{"image_url":"data:image/png;base64,abc","type":"input_image"}]` &&
		got != `[{"type":"input_image","image_url":"data:image/png;base64,abc"}]` {
		t.Fatalf("unexpected normalized output %q", got)
	}
}

func TestParseChatProxyRequestDoesNotApplyCompatForNonCursor(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5.4",
		"input": [{"role":"user","content":"hi"}],
		"tools": [{"type":"custom","name":"ApplyPatch"}]
	}`)

	_, err := parseChatProxyRequest(config.Config{
		Model: config.ModelConfig{DefaultModel: "gpt-5.4"},
	}, body, "curl/8.0")
	if err == nil {
		t.Fatal("expected non-Cursor compat payload to be rejected by chat parser")
	}
}

func TestToCodexSupportsRootToolNameForFunctionTool(t *testing.T) {
	request, err := parseChatCompletionRequest([]byte(`{
		"model":"gpt-5.4",
		"stream":true,
		"tools":[{
			"type":"function",
			"name":"search_docs",
			"description":"Search docs",
			"parameters":{"type":"object","properties":{"query":{"type":"string"}}}
		}]
	}`), "Cursor/1.0")
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	request.Tools[0].Function = openai.ToolSpec{}
	translated := openai.ToCodex(config.Config{Model: config.ModelConfig{DefaultModel: "gpt-5.4"}}, request)
	if len(translated.Request.Tools) != 1 {
		t.Fatalf("expected one tool, got %d", len(translated.Request.Tools))
	}
	if translated.Request.Tools[0].Name != "search_docs" {
		t.Fatalf("expected root name fallback to be preserved, got %q", translated.Request.Tools[0].Name)
	}
	if translated.Request.Tools[0].Function == nil || translated.Request.Tools[0].Function.Name != "search_docs" {
		t.Fatalf("expected function name fallback to be preserved, got %#v", translated.Request.Tools[0].Function)
	}
	if translated.Request.Tools[0].Description != "Search docs" {
		t.Fatalf("expected description fallback to be preserved, got %q", translated.Request.Tools[0].Description)
	}
	if translated.Request.Tools[0].Parameters == nil || translated.Request.Tools[0].Parameters["type"] != "object" {
		t.Fatalf("expected parameters fallback to be preserved, got %#v", translated.Request.Tools[0].Parameters)
	}
}
