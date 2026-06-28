package codex

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStablePromptCacheKeyIsDeterministic(t *testing.T) {
	request := ResponsesRequest{
		Model:        "gpt-5.4",
		Instructions: "test instructions",
		Input: []InputItem{
			{Role: "user", Content: "hello"},
		},
	}

	key1 := StableConversationKey(request)
	key2 := StableConversationKey(request)

	if key1 == "" {
		t.Fatal("expected non-empty prompt cache key")
	}
	if key1 != key2 {
		t.Fatalf("expected deterministic prompt cache key, got %q and %q", key1, key2)
	}
}

func TestStableConversationKeyUsesConversationSeed(t *testing.T) {
	requestA := ResponsesRequest{
		Model:        "gpt-5.4",
		Instructions: "same instructions",
		Input: []InputItem{
			{Role: "user", Content: "first"},
			{Role: "assistant", Content: "old"},
			{Role: "user", Content: "new"},
		},
	}
	requestB := ResponsesRequest{
		Model:        "gpt-5.4",
		Instructions: "same instructions",
		Input: []InputItem{
			{Role: "user", Content: "first"},
			{Role: "assistant", Content: "different old"},
			{Role: "user", Content: "different new"},
		},
	}
	if StableConversationKey(requestA) != StableConversationKey(requestB) {
		t.Fatal("expected same conversation key when instructions and first user message match")
	}
}

func TestDefaultHeadersDoNotForceAcceptEncoding(t *testing.T) {
	client := &Client{}

	headers := client.defaultHeaders("token", "account-id")

	if got := headers.Get("Accept-Encoding"); got != "" {
		t.Fatalf("expected Accept-Encoding to be unset so Go can auto-decompress, got %q", got)
	}
}

func TestCreateCustomResponseUsesOpenAICompatibleResponsesEndpoint(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotUserAgent string
	var gotModel string
	var gotPreviousResponseID string
	var gotPromptCacheKey string
	var gotStream bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotUserAgent = r.Header.Get("User-Agent")
		var payload ResponsesRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		gotModel = payload.Model
		gotPreviousResponseID = payload.PreviousResponseID
		gotPromptCacheKey = payload.PromptCacheKey
		gotStream = payload.Stream
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte(`data: {"response":{"id":"resp_1","usage":{"input_tokens":1,"output_tokens":2}}}` + "\n\n"))
	}))
	defer server.Close()

	client := &Client{httpClient: server.Client()}
	resp, err := client.CreateCustomResponse(t.Context(), server.URL+"/compat", "custom-key", "responses", "Custom-UA/1.0", ResponsesRequest{
		Model:              "custom-model",
		Input:              []InputItem{{Role: "user", Content: "hello"}},
		PreviousResponseID: "resp_prev_123",
		PromptCacheKey:     "conv_123",
	})
	if err != nil {
		t.Fatalf("CreateCustomResponse() error = %v", err)
	}
	_ = resp.Body.Close()

	if gotPath != "/compat/v1/responses" {
		t.Fatalf("expected /compat/v1/responses path, got %q", gotPath)
	}
	if gotAuth != "Bearer custom-key" {
		t.Fatalf("unexpected authorization header %q", gotAuth)
	}
	if gotUserAgent != "Custom-UA/1.0" {
		t.Fatalf("expected user agent Custom-UA/1.0, got %q", gotUserAgent)
	}
	if gotModel != "custom-model" {
		t.Fatalf("expected custom model, got %q", gotModel)
	}
	if gotPreviousResponseID != "resp_prev_123" {
		t.Fatalf("expected previous_response_id to be preserved, got %q", gotPreviousResponseID)
	}
	if gotPromptCacheKey != "conv_123" {
		t.Fatalf("expected prompt_cache_key to be preserved, got %q", gotPromptCacheKey)
	}
	if !gotStream {
		t.Fatalf("expected upstream responses request to force stream=true")
	}
}

func TestCreateCustomResponseUsesChatCompletionsEndpoint(t *testing.T) {
	var gotPath string
	var gotAcceptEncoding string
	var gotUserAgent string
	var gotMessages []map[string]any
	var gotStreamOptions map[string]any
	var gotTools []map[string]any
	var gotToolChoice any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAcceptEncoding = r.Header.Get("Accept-Encoding")
		gotUserAgent = r.Header.Get("User-Agent")
		var payload struct {
			Messages      []map[string]any `json:"messages"`
			StreamOptions map[string]any   `json:"stream_options"`
			Tools         []map[string]any `json:"tools"`
			ToolChoice    any              `json:"tool_choice"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		gotMessages = payload.Messages
		gotStreamOptions = payload.StreamOptions
		gotTools = payload.Tools
		gotToolChoice = payload.ToolChoice
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"id":"chat_1","model":"chat-model","choices":[{"delta":{"content":"hello"}}]}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"id":"chat_1","model":"chat-model","usage":{"prompt_tokens":1,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":3},"completion_tokens_details":{"reasoning_tokens":4}},"choices":[]}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := &Client{httpClient: server.Client()}
	resp, err := client.CreateCustomResponse(t.Context(), server.URL+"/compat", "custom-key", "/api/paas/v4/chat/completions", "Custom-UA/2.0", ResponsesRequest{
		Model:        "chat-model",
		Instructions: "system prompt",
		Input:        []InputItem{{Role: "user", Content: "hello"}},
		Tools: []Tool{
			{
				Type:        "function",
				Name:        "Read",
				Description: "Read a file",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{"type": "string"},
					},
				},
			},
		},
		ToolChoice: map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": "Read",
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateCustomResponse() error = %v", err)
	}
	events, err := ParseSSE(resp)
	if err != nil {
		t.Fatalf("ParseSSE() error = %v", err)
	}
	if gotPath != "/compat/api/paas/v4/chat/completions" {
		t.Fatalf("expected chat completions path, got %q", gotPath)
	}
	if gotAcceptEncoding != "identity" {
		t.Fatalf("expected identity accept-encoding, got %q", gotAcceptEncoding)
	}
	if gotUserAgent != "Custom-UA/2.0" {
		t.Fatalf("expected user agent Custom-UA/2.0, got %q", gotUserAgent)
	}
	if len(gotMessages) != 2 {
		t.Fatalf("expected system and user messages, got %#v", gotMessages)
	}
	if len(gotTools) != 1 {
		t.Fatalf("expected one tool, got %#v", gotTools)
	}
	if gotTools[0]["type"] != "function" {
		t.Fatalf("expected function tool, got %#v", gotTools[0])
	}
	function, _ := gotTools[0]["function"].(map[string]any)
	if function["name"] != "Read" {
		t.Fatalf("expected tool function name Read, got %#v", function)
	}
	toolChoiceMap, ok := gotToolChoice.(map[string]any)
	if !ok {
		t.Fatalf("expected tool_choice object, got %#v", gotToolChoice)
	}
	toolChoiceFunction, _ := toolChoiceMap["function"].(map[string]any)
	if toolChoiceFunction["name"] != "Read" {
		t.Fatalf("expected tool_choice function Read, got %#v", gotToolChoice)
	}
	if got := gotStreamOptions["include_usage"]; got != true {
		t.Fatalf("expected include_usage stream option, got %#v", gotStreamOptions)
	}
	text, usage, responseID, err := ReadResponseText(events)
	if err != nil {
		t.Fatalf("ReadResponseText() error = %v", err)
	}
	if responseID != "chat_1" || text != "hello" || usage.InputTokens != 1 || usage.OutputTokens != 2 || usage.CachedTokens != 3 || usage.ReasoningTokens != 4 {
		t.Fatalf("unexpected converted stream responseID=%q text=%q usage=%+v", responseID, text, usage)
	}
}

func TestParseUsagePrefersInputTokenDetailsCachedTokens(t *testing.T) {
	usage := ParseUsage(map[string]any{
		"input_tokens":  9411,
		"output_tokens": 6,
		"input_tokens_details": map[string]any{
			"cached_tokens": 8960,
		},
		"output_tokens_details": map[string]any{
			"reasoning_tokens": 0,
		},
		"total_tokens": 9417,
	})

	if usage.InputTokens != 9411 || usage.OutputTokens != 6 || usage.CachedTokens != 8960 || usage.TotalTokens != 9417 {
		t.Fatalf("unexpected usage %+v", usage)
	}
}

func TestCreateCustomResponseConvertsChatCompletionsToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chat_1\",\"model\":\"chat-model\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"Read\",\"arguments\":\"{\\\"path\\\":\"}}]}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"chat_1\",\"model\":\"chat-model\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"/tmp/test\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := &Client{httpClient: server.Client()}
	resp, err := client.CreateCustomResponse(t.Context(), server.URL, "custom-key", "/v1/chat/completions", "", ResponsesRequest{
		Model: "chat-model",
		Input: []InputItem{{Role: "user", Content: "read file"}},
	})
	if err != nil {
		t.Fatalf("CreateCustomResponse() error = %v", err)
	}
	events, err := ParseSSE(resp)
	if err != nil {
		t.Fatalf("ParseSSE() error = %v", err)
	}

	if len(events) < 3 {
		t.Fatalf("expected converted tool call events, got %d", len(events))
	}
	if events[0].Event != "response.output_item.added" {
		t.Fatalf("expected first event response.output_item.added, got %q", events[0].Event)
	}
	if events[1].Event != "response.function_call_arguments.delta" {
		t.Fatalf("expected second event response.function_call_arguments.delta, got %q", events[1].Event)
	}
	if events[2].Event != "response.function_call_arguments.delta" {
		t.Fatalf("expected third event response.function_call_arguments.delta, got %q", events[2].Event)
	}
	foundDone := false
	for _, event := range events {
		if event.Event == "response.function_call_arguments.done" {
			foundDone = true
			break
		}
	}
	if !foundDone {
		t.Fatal("expected response.function_call_arguments.done event")
	}
}

func TestGetCustomModelsUsesDerivedModelsPath(t *testing.T) {
	var gotPath string
	var gotUserAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotUserAgent = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"glm-4.5","object":"model","created":1753632000,"owned_by":"z-ai"}]}`))
	}))
	defer server.Close()

	client := &Client{httpClient: server.Client()}
	models, err := client.GetCustomModels(t.Context(), server.URL+"/compat", "custom-key", "/api/paas/v4/chat/completions", "Custom-UA/3.0")
	if err != nil {
		t.Fatalf("GetCustomModels() error = %v", err)
	}
	if gotPath != "/compat/api/paas/v4/models" {
		t.Fatalf("expected derived models path, got %q", gotPath)
	}
	if gotUserAgent != "Custom-UA/3.0" {
		t.Fatalf("expected user agent Custom-UA/3.0, got %q", gotUserAgent)
	}
	if len(models) != 1 || models[0]["id"] != "glm-4.5" {
		t.Fatalf("unexpected models payload %#v", models)
	}
}

func TestCreateCustomResponseNormalizesStructuredChatContent(t *testing.T) {
	var gotMessages []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []map[string]any `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		gotMessages = payload.Messages
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"id":"chat_1","model":"chat-model","choices":[]}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := &Client{httpClient: server.Client()}
	_, err := client.CreateCustomResponse(t.Context(), server.URL, "custom-key", "chat_completions", "", ResponsesRequest{
		Model: "chat-model",
		Input: []InputItem{
			{
				Role: "user",
				Content: []any{
					map[string]any{"type": "input_text", "text": "hello"},
					map[string]any{"type": "input_image", "image_url": "https://example.com/image.png"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateCustomResponse() error = %v", err)
	}

	if len(gotMessages) != 1 {
		t.Fatalf("expected one message, got %#v", gotMessages)
	}
	content, ok := gotMessages[0]["content"].([]any)
	if !ok {
		t.Fatalf("expected structured content, got %#v", gotMessages[0]["content"])
	}
	first, _ := content[0].(map[string]any)
	second, _ := content[1].(map[string]any)
	if first["type"] != "text" || first["text"] != "hello" {
		t.Fatalf("expected input_text normalized to text, got %#v", first)
	}
	if second["type"] != "image_url" || second["image_url"] != "https://example.com/image.png" {
		t.Fatalf("expected input_image normalized to image_url, got %#v", second)
	}
}

func TestCreateCustomResponseBuildsStandardToolCallHistoryForChatCompletions(t *testing.T) {
	var gotMessages []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []map[string]any `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		gotMessages = payload.Messages
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"id":"chat_1","model":"chat-model","choices":[]}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := &Client{httpClient: server.Client()}
	_, err := client.CreateCustomResponse(t.Context(), server.URL, "custom-key", "chat_completions", "", ResponsesRequest{
		Model: "chat-model",
		Input: []InputItem{
			{Role: "user", Content: "hi"},
			{Type: "function_call", CallID: "call_1", Name: "Read", Arguments: "{\"filePath\":\"/tmp/README.md\"}"},
			{Type: "function_call_output", CallID: "call_1", Output: "file contents"},
			{Role: "user", Content: "continue"},
		},
	})
	if err != nil {
		t.Fatalf("CreateCustomResponse() error = %v", err)
	}

	if len(gotMessages) != 4 {
		t.Fatalf("expected 4 messages, got %#v", gotMessages)
	}
	if gotMessages[0]["role"] != "user" || gotMessages[0]["content"] != "hi" {
		t.Fatalf("unexpected first message %#v", gotMessages[0])
	}
	if gotMessages[1]["role"] != "assistant" {
		t.Fatalf("expected assistant tool call message, got %#v", gotMessages[1])
	}
	toolCalls, ok := gotMessages[1]["tool_calls"].([]any)
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("expected one tool call, got %#v", gotMessages[1]["tool_calls"])
	}
	toolCall, _ := toolCalls[0].(map[string]any)
	function, _ := toolCall["function"].(map[string]any)
	if toolCall["id"] != "call_1" || function["name"] != "Read" || function["arguments"] != "{\"filePath\":\"/tmp/README.md\"}" {
		t.Fatalf("unexpected tool call %#v", toolCall)
	}
	if gotMessages[2]["role"] != "tool" || gotMessages[2]["tool_call_id"] != "call_1" || gotMessages[2]["content"] != "file contents" {
		t.Fatalf("unexpected tool result message %#v", gotMessages[2])
	}
	if gotMessages[3]["role"] != "user" || gotMessages[3]["content"] != "continue" {
		t.Fatalf("unexpected final message %#v", gotMessages[3])
	}
}

func TestResponsesRequestMarshalIncludesEmptyInstructions(t *testing.T) {
	request := ResponsesRequest{
		Model:        "gpt-5.4",
		Instructions: "",
		Input: []InputItem{
			{Role: "user", Content: "hello"},
		},
		Stream:             true,
		Store:              false,
		PreviousResponseID: "resp_prev_123",
	}

	request.ForceInstructions = true
	raw, err := marshalHTTPResponsesRequest(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	jsonBody := string(raw)
	if !strings.Contains(jsonBody, `"instructions":""`) {
		t.Fatalf("expected marshaled request to include empty instructions, got %s", jsonBody)
	}
	if !strings.Contains(jsonBody, `"previous_response_id":"resp_prev_123"`) {
		t.Fatalf("expected marshaled request to include previous_response_id, got %s", jsonBody)
	}
}

func TestResponsesRequestMarshalOmitsEmptyInstructionsByDefault(t *testing.T) {
	request := ResponsesRequest{
		Model:        "gpt-5.4",
		Instructions: "",
		Input: []InputItem{
			{Role: "user", Content: "hello"},
		},
		Stream: true,
		Store:  false,
	}

	raw, err := marshalHTTPResponsesRequest(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	jsonBody := string(raw)
	if strings.Contains(jsonBody, `"instructions":""`) {
		t.Fatalf("expected default marshaling to omit empty instructions, got %s", jsonBody)
	}
}

func TestResponsesRequestMarshalPreservesStructuredFunctionOutput(t *testing.T) {
	request := ResponsesRequest{
		Model:        "gpt-5.4",
		Instructions: "",
		Input: []InputItem{
			{
				Type:   "function_call_output",
				CallID: "call_1",
				Output: []any{
					map[string]any{"type": "input_text", "text": "tool result"},
				},
			},
		},
		Stream: true,
		Store:  false,
	}

	raw, err := marshalHTTPResponsesRequest(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	jsonBody := string(raw)
	if !strings.Contains(jsonBody, `"output":[{"text":"tool result","type":"input_text"}]`) &&
		!strings.Contains(jsonBody, `"output":[{"type":"input_text","text":"tool result"}]`) {
		t.Fatalf("expected marshaled request to preserve structured tool output, got %s", jsonBody)
	}
}

func TestResponsesRequestMarshalStripsUnsupportedCursorCompatFields(t *testing.T) {
	request := ResponsesRequest{
		Model:                "gpt-5.4",
		User:                 "cursor-user",
		Instructions:         "",
		Input:                []InputItem{{Role: "user", Content: "hello"}},
		Stream:               true,
		Store:                false,
		PromptCacheRetention: "24h",
		Metadata: map[string]any{
			"cursorConversationId": "conv_123",
		},
		StreamOptions: map[string]any{
			"include_usage": true,
		},
		Tools: []Tool{
			{
				Type:        "custom",
				Name:        "ApplyPatch",
				Description: "Edit files",
				Format: map[string]any{
					"type":   "grammar",
					"syntax": "lark",
				},
			},
		},
	}

	request.ForceInstructions = true
	raw, err := marshalHTTPResponsesRequest(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	jsonBody := string(raw)
	if !strings.Contains(jsonBody, `"format":{"syntax":"lark","type":"grammar"}`) {
		t.Fatalf("expected marshaled request to preserve custom tool format, got %s", jsonBody)
	}
	for _, unwanted := range []string{
		`"user":"cursor-user"`,
		`"prompt_cache_retention":"24h"`,
		`"cursorConversationId":"conv_123"`,
		`"include_usage":true`,
		`"function":`,
	} {
		if strings.Contains(jsonBody, unwanted) {
			t.Fatalf("expected marshaled request to strip %s, got %s", unwanted, jsonBody)
		}
	}
}

func TestResponsesRequestMarshalFlattensFunctionTools(t *testing.T) {
	request := ResponsesRequest{
		Model:  "gpt-5.4",
		Input:  []InputItem{{Role: "user", Content: "hello"}},
		Stream: true,
		Store:  false,
		Tools: []Tool{
			{
				Type: "function",
				Function: &ToolFunction{
					Name:        "Read",
					Description: "Read a file",
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"path": map[string]any{"type": "string"},
						},
					},
				},
			},
		},
	}

	request.ForceInstructions = true
	raw, err := marshalHTTPResponsesRequest(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	jsonBody := string(raw)
	for _, expected := range []string{
		`"type":"function"`,
		`"name":"Read"`,
		`"description":"Read a file"`,
	} {
		if !strings.Contains(jsonBody, expected) {
			t.Fatalf("expected marshaled request to contain %s, got %s", expected, jsonBody)
		}
	}
	if strings.Contains(jsonBody, `"function":`) {
		t.Fatalf("expected marshaled request to flatten function tools, got %s", jsonBody)
	}
}

func TestResponsesRequestMarshalNormalizesLongCompositeCallIDs(t *testing.T) {
	compositeCallID := "call_T6o3gY3owUFyC6ZFLYrcXnS9\nfc_031fac4a847dd9a60169fd722fa4e881a0aa0d4862cca44904"
	request := ResponsesRequest{
		Model: "gpt-5.4",
		Input: []InputItem{
			{
				Type:      "function_call",
				CallID:    compositeCallID,
				Name:      "Shell",
				Arguments: "{}",
			},
			{
				Type:   "function_call_output",
				CallID: compositeCallID,
				Output: "ok",
			},
		},
		Stream: true,
		Store:  false,
	}

	raw, err := marshalHTTPResponsesRequest(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if strings.Contains(string(raw), compositeCallID) {
		t.Fatalf("expected marshaled request to omit composite call id, got %s", string(raw))
	}

	var payload ResponsesRequest
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if got := payload.Input[0].CallID; got != "call_T6o3gY3owUFyC6ZFLYrcXnS9" {
		t.Fatalf("expected first call id segment, got %q", got)
	}
	if payload.Input[1].CallID != payload.Input[0].CallID {
		t.Fatalf("expected call and output IDs to match, got %q and %q", payload.Input[0].CallID, payload.Input[1].CallID)
	}
}
