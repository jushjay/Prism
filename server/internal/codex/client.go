package codex

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jushjay/prism/internal/config"
)

type Client struct {
	cfg        config.Config
	httpClient *http.Client
}

type UpstreamError struct {
	StatusCode int
	Body       string
	Header     http.Header
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("upstream error %d: %s", e.StatusCode, strings.TrimSpace(e.Body))
}

func NewClient(cfg config.Config) (*Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &Client{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: 0,
			Jar:     jar,
		},
	}, nil
}

func (c *Client) GetModels(ctx context.Context, token, accountID, clientVersion string) ([]map[string]any, error) {
	endpoint := c.cfg.API.BaseURL + "/codex/models?client_version=" + url.QueryEscape(clientVersion)
	req, err := c.newRequest(ctx, http.MethodGet, endpoint, nil, token, accountID)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	payload, err := decodeModelList(resp)
	if err != nil {
		return nil, err
	}
	if len(payload) == 0 {
		return nil, errors.New("upstream returned empty model catalog")
	}
	return payload, nil
}

func (c *Client) GetUsage(ctx context.Context, token, accountID string) (UsageResponse, error) {
	req, err := c.newRequest(ctx, http.MethodGet, c.cfg.API.BaseURL+"/codex/usage", nil, token, accountID)
	if err != nil {
		return UsageResponse{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return UsageResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return UsageResponse{}, &UpstreamError{
			StatusCode: resp.StatusCode,
			Body:       strings.TrimSpace(string(body)),
			Header:     resp.Header.Clone(),
		}
	}

	var payload UsageResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return UsageResponse{}, err
	}
	return payload, nil
}

func (c *Client) GetCustomModels(ctx context.Context, baseURL, apiKey, endpointPath, userAgent string) ([]map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, TargetURL(baseURL, customModelsPath(endpointPath)), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	if strings.TrimSpace(userAgent) != "" {
		req.Header.Set("User-Agent", strings.TrimSpace(userAgent))
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	return decodeModelList(resp)
}

func (c *Client) CreateResponse(ctx context.Context, token, accountID string, request ResponsesRequest) (*http.Response, error) {
	request.Store = false
	if request.Stream {
		request.Include = ensureReasoningInclude(request.Include, request.Reasoning)
	}
	if request.PreviousResponseID != "" {
		return c.createResponseViaWebSocket(ctx, token, accountID, request)
	}
	return c.createResponseViaHTTP(ctx, token, accountID, request)
}

func (c *Client) CreateCustomResponse(ctx context.Context, baseURL, apiKey, endpointPath, userAgent string, request ResponsesRequest) (*http.Response, error) {
	request.Store = false
	request.Stream = true
	request.ServiceTier = ""
	targetPath := normalizeCustomEndpointPath(endpointPath)
	body, err := marshalHTTPResponsesRequest(request)
	if customEndpointType(targetPath) == "chat_completions" {
		body, err = marshalCustomChatCompletionsRequest(request)
	}
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, TargetURL(baseURL, targetPath), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(userAgent) != "" {
		req.Header.Set("User-Agent", strings.TrimSpace(userAgent))
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return nil, &UpstreamError{
			StatusCode: resp.StatusCode,
			Body:       strings.TrimSpace(string(body)),
			Header:     resp.Header.Clone(),
		}
	}
	if customEndpointType(targetPath) == "chat_completions" {
		return convertChatCompletionsStream(resp), nil
	}
	return resp, nil
}

func customEndpointType(value string) string {
	switch {
	case strings.HasSuffix(normalizeCustomEndpointPath(value), "/chat/completions"):
		return "chat_completions"
	default:
		return "responses"
	}
}

func normalizeCustomEndpointPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "/v1/responses"
	}
	if parsed, err := url.Parse(value); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		value = parsed.Path
	}
	switch strings.Trim(strings.ToLower(strings.TrimSpace(value)), "/") {
	case "responses", "v1/responses", "response", "v1/response":
		return "/v1/responses"
	case "chat", "chat_completions", "chat/completions", "v1/chat", "v1/chat/completions":
		return "/v1/chat/completions"
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	return "/" + strings.Trim(strings.ToLower(strings.TrimSpace(value)), "/")
}

func customModelsPath(endpointPath string) string {
	path := normalizeCustomEndpointPath(endpointPath)
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, "/chat/completions"):
		return path[:len(path)-len("/chat/completions")] + "/models"
	case strings.HasSuffix(lower, "/responses"):
		return path[:len(path)-len("/responses")] + "/models"
	case strings.HasSuffix(lower, "/response"):
		return path[:len(path)-len("/response")] + "/models"
	default:
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) <= 1 {
			return "/v1/models"
		}
		return "/" + strings.Join(append(parts[:len(parts)-1], "models"), "/")
	}
}

func marshalCustomChatCompletionsRequest(request ResponsesRequest) ([]byte, error) {
	return MarshalAuditRequestBody(request, "chat_completions")
}

func MarshalAuditRequestBody(request ResponsesRequest, endpointType string) ([]byte, error) {
	if customEndpointType(endpointType) == "chat_completions" {
		return marshalAuditChatCompletionsRequest(request)
	}
	return marshalHTTPResponsesRequest(request)
}

func marshalAuditChatCompletionsRequest(request ResponsesRequest) ([]byte, error) {
	messages := []map[string]any{}
	pendingAssistantToolCalls := map[string][]map[string]any{}
	if strings.TrimSpace(request.Instructions) != "" {
		messages = append(messages, map[string]any{
			"role":    "system",
			"content": request.Instructions,
		})
	}
	for _, item := range request.Input {
		switch item.Type {
		case "function_call", "custom_tool_call":
			callID := strings.TrimSpace(item.CallID)
			if callID == "" {
				callID = "call_" + StableConversationKey(ResponsesRequest{
					Model:        request.Model,
					Instructions: request.Instructions,
					Input:        []InputItem{item},
				})
			}
			name := strings.TrimSpace(item.Name)
			if name == "" {
				continue
			}
			arguments := item.Arguments
			if item.Type == "custom_tool_call" && arguments == "" {
				arguments = item.Input
			}
			pendingAssistantToolCalls[callID] = append(pendingAssistantToolCalls[callID], map[string]any{
				"id":   callID,
				"type": "function",
				"function": map[string]any{
					"name":      name,
					"arguments": arguments,
				},
			})
		case "function_call_output", "custom_tool_call_output":
			callID := strings.TrimSpace(item.CallID)
			if callID != "" {
				if toolCalls := pendingAssistantToolCalls[callID]; len(toolCalls) > 0 {
					messages = append(messages, map[string]any{
						"role":       "assistant",
						"content":    "",
						"tool_calls": toolCalls,
					})
					delete(pendingAssistantToolCalls, callID)
				}
			}
			messages = append(messages, map[string]any{
				"role":         "tool",
				"tool_call_id": callID,
				"content":      normalizeChatToolOutput(item.Output),
			})
		default:
			role := item.Role
			if role == "" {
				continue
			}
			messages = append(messages, map[string]any{
				"role":    role,
				"content": normalizeChatCompletionContent(item.Content),
			})
		}
	}
	for _, toolCalls := range pendingAssistantToolCalls {
		if len(toolCalls) == 0 {
			continue
		}
		messages = append(messages, map[string]any{
			"role":       "assistant",
			"content":    "",
			"tool_calls": toolCalls,
		})
	}
	if len(messages) == 0 {
		messages = append(messages, map[string]any{"role": "user", "content": ""})
	}
	payload := map[string]any{
		"model":    request.Model,
		"stream":   true,
		"messages": messages,
		"stream_options": map[string]any{
			"include_usage": true,
		},
	}
	if tools := normalizeChatCompletionTools(request.Tools); len(tools) > 0 {
		payload["tools"] = tools
	}
	if request.ToolChoice != nil {
		payload["tool_choice"] = request.ToolChoice
	}
	if request.Reasoning != nil && request.Reasoning.Effort != "" {
		payload["reasoning_effort"] = request.Reasoning.Effort
	}
	return json.Marshal(payload)
}

func normalizeChatCompletionTools(tools []Tool) []map[string]any {
	normalized := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		toolType := strings.TrimSpace(tool.Type)
		function := tool.Function
		if function == nil {
			function = &ToolFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.Parameters,
			}
		}
		if toolType == "" {
			toolType = "function"
		}
		switch toolType {
		case "function":
			name := strings.TrimSpace(function.Name)
			if name == "" {
				name = strings.TrimSpace(tool.Name)
			}
			if name == "" {
				continue
			}
			description := strings.TrimSpace(function.Description)
			if description == "" {
				description = strings.TrimSpace(tool.Description)
			}
			parameters := function.Parameters
			if len(parameters) == 0 {
				parameters = tool.Parameters
			}
			entry := map[string]any{
				"type": "function",
				"function": map[string]any{
					"name": name,
				},
			}
			if description != "" {
				entry["function"].(map[string]any)["description"] = description
			}
			if len(parameters) > 0 {
				entry["function"].(map[string]any)["parameters"] = parameters
			}
			normalized = append(normalized, entry)
		default:
			entry := map[string]any{
				"type": toolType,
			}
			if strings.TrimSpace(tool.Name) != "" {
				entry["name"] = strings.TrimSpace(tool.Name)
			}
			if strings.TrimSpace(tool.Description) != "" {
				entry["description"] = strings.TrimSpace(tool.Description)
			}
			if len(tool.Parameters) > 0 {
				entry["parameters"] = tool.Parameters
			}
			if len(tool.Format) > 0 {
				entry["format"] = tool.Format
			}
			if function != nil && strings.TrimSpace(function.Name) != "" {
				entry["function"] = map[string]any{
					"name":        function.Name,
					"description": function.Description,
					"parameters":  function.Parameters,
				}
			}
			normalized = append(normalized, entry)
		}
	}
	return normalized
}

func normalizeChatToolOutput(output any) string {
	switch typed := output.(type) {
	case string:
		return typed
	case nil:
		return ""
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			return ""
		}
		return string(raw)
	}
}

func normalizeChatCompletionContent(content any) any {
	switch typed := content.(type) {
	case []map[string]any:
		parts := make([]any, 0, len(typed))
		for _, part := range typed {
			parts = append(parts, part)
		}
		return normalizeChatCompletionContent(parts)
	case []any:
		normalized := make([]map[string]any, 0, len(typed))
		for _, raw := range typed {
			part, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			partType, _ := part["type"].(string)
			switch partType {
			case "input_text", "output_text", "text":
				normalized = append(normalized, map[string]any{
					"type": "text",
					"text": part["text"],
				})
			case "input_image", "image_url":
				imageURL := part["image_url"]
				switch imageValue := imageURL.(type) {
				case nil:
					if inputImage, ok := part["image"].(string); ok {
						imageURL = inputImage
					}
				case map[string]any:
					imageURL = imageValue["url"]
				}
				if imageURL != nil && imageURL != "" {
					normalized = append(normalized, map[string]any{
						"type":      "image_url",
						"image_url": imageURL,
					})
				}
			}
		}
		if len(normalized) == 0 {
			return ""
		}
		return normalized
	default:
		return content
	}
}

func convertChatCompletionsStream(resp *http.Response) *http.Response {
	reader, writer := io.Pipe()
	out := &http.Response{
		StatusCode: resp.StatusCode,
		Header:     resp.Header.Clone(),
		Body:       reader,
	}
	out.Header.Set("Content-Type", "text/event-stream")
	go func() {
		defer writer.Close()
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		responseID := ""
		model := ""
		var usage Usage
		toolCalls := map[int]*openAIChatToolCallState{}
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			if data == "" {
				continue
			}
			if data == "[DONE]" {
				flushOpenAIChatToolCallDone(writer, toolCalls)
				writeConvertedResponseCompleted(writer, responseID, model, usage)
				return
			}
			var chunk map[string]any
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				continue
			}
			if id, _ := chunk["id"].(string); id != "" {
				responseID = id
			}
			if chunkModel, _ := chunk["model"].(string); chunkModel != "" {
				model = chunkModel
			}
			if rawUsage, ok := chunk["usage"].(map[string]any); ok {
				usage = ParseUsage(rawUsage)
			}
			choices, _ := chunk["choices"].([]any)
			for _, rawChoice := range choices {
				choice, _ := rawChoice.(map[string]any)
				delta, _ := choice["delta"].(map[string]any)
				emitOpenAIChatToolCallEvents(writer, toolCalls, delta)
				if text, _ := delta["content"].(string); text != "" {
					writeConvertedEvent(writer, "response.output_text.delta", map[string]any{"delta": text})
				}
				if text, _ := delta["reasoning_content"].(string); text != "" {
					writeConvertedEvent(writer, "response.reasoning_summary_text.delta", map[string]any{"delta": text})
				}
				if finishReason, _ := choice["finish_reason"].(string); finishReason == "tool_calls" {
					flushOpenAIChatToolCallDone(writer, toolCalls)
				}
			}
		}
		flushOpenAIChatToolCallDone(writer, toolCalls)
		writeConvertedResponseCompleted(writer, responseID, model, usage)
	}()
	return out
}

type openAIChatToolCallState struct {
	ID        string
	Name      string
	Arguments strings.Builder
	Started   bool
	Done      bool
}

func emitOpenAIChatToolCallEvents(w io.Writer, states map[int]*openAIChatToolCallState, delta map[string]any) {
	rawToolCalls, _ := delta["tool_calls"].([]any)
	for _, raw := range rawToolCalls {
		toolCall, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		indexValue, ok := toolCall["index"].(float64)
		if !ok {
			continue
		}
		index := int(indexValue)
		state, ok := states[index]
		if !ok {
			state = &openAIChatToolCallState{}
			states[index] = state
		}
		if id, _ := toolCall["id"].(string); id != "" {
			state.ID = id
		}
		function, _ := toolCall["function"].(map[string]any)
		if name, _ := function["name"].(string); name != "" {
			state.Name = name
		}
		if state.ID == "" {
			state.ID = fmt.Sprintf("call_%d", index)
		}
		if !state.Started && state.Name != "" {
			writeConvertedEvent(w, "response.output_item.added", map[string]any{
				"item": map[string]any{
					"type":      "function_call",
					"id":        state.ID,
					"call_id":   state.ID,
					"name":      state.Name,
					"arguments": "",
				},
			})
			state.Started = true
		}
		if argumentsDelta, _ := function["arguments"].(string); argumentsDelta != "" {
			if !state.Started {
				writeConvertedEvent(w, "response.output_item.added", map[string]any{
					"item": map[string]any{
						"type":      "function_call",
						"id":        state.ID,
						"call_id":   state.ID,
						"name":      state.Name,
						"arguments": "",
					},
				})
				state.Started = true
			}
			state.Arguments.WriteString(argumentsDelta)
			writeConvertedEvent(w, "response.function_call_arguments.delta", map[string]any{
				"item_id": state.ID,
				"call_id": state.ID,
				"delta":   argumentsDelta,
			})
		}
	}
}

func flushOpenAIChatToolCallDone(w io.Writer, states map[int]*openAIChatToolCallState) {
	for _, state := range states {
		if state == nil || state.Done || !state.Started {
			continue
		}
		state.Done = true
		writeConvertedEvent(w, "response.function_call_arguments.done", map[string]any{
			"item_id":   state.ID,
			"call_id":   state.ID,
			"name":      state.Name,
			"arguments": state.Arguments.String(),
		})
	}
}

func writeConvertedResponseCompleted(w io.Writer, responseID, model string, usage Usage) {
	if responseID == "" {
		responseID = "resp_custom"
	}
	totalTokens := usage.TotalTokens
	if totalTokens == 0 {
		totalTokens = usage.InputTokens + usage.OutputTokens
	}
	writeConvertedEvent(w, "response.completed", map[string]any{
		"response": map[string]any{
			"id":    responseID,
			"model": model,
			"usage": map[string]any{
				"input_tokens": usage.InputTokens,
				"input_tokens_details": map[string]any{
					"cached_tokens": usage.CachedTokens,
				},
				"output_tokens": usage.OutputTokens,
				"output_tokens_details": map[string]any{
					"reasoning_tokens": usage.ReasoningTokens,
				},
				"total_tokens": totalTokens,
			},
		},
	})
}

func writeConvertedEvent(w io.Writer, event string, payload map[string]any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = io.WriteString(w, "event: "+event+"\n")
	_, _ = io.WriteString(w, "data: "+string(raw)+"\n\n")
}

func (c *Client) createResponseViaHTTP(ctx context.Context, token, accountID string, request ResponsesRequest) (*http.Response, error) {
	payload := request
	payload.PreviousResponseID = ""
	payload.ServiceTier = ""
	// The upstream Codex responses endpoint now requires SSE transport semantics
	// even when the caller wants a non-streaming JSON response. We still collect
	// the SSE body server-side and reassemble JSON for non-stream callers.
	payload.Stream = true
	body, err := marshalHTTPResponsesRequest(payload)
	if err != nil {
		return nil, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, c.cfg.API.BaseURL+"/codex/responses", bytes.NewReader(body), token, accountID)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("OpenAI-Beta", "responses_websockets=2026-02-06")
	req.Header.Set("x-openai-internal-codex-residency", "us")
	if request.TurnState != "" {
		req.Header.Set("x-codex-turn-state", request.TurnState)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return nil, &UpstreamError{
			StatusCode: resp.StatusCode,
			Body:       strings.TrimSpace(string(body)),
			Header:     resp.Header.Clone(),
		}
	}
	return resp, nil
}

func marshalHTTPResponsesRequest(request ResponsesRequest) ([]byte, error) {
	request = sanitizeUnsupportedUpstreamFields(request)
	if !request.ForceInstructions {
		return json.Marshal(request)
	}

	payload := map[string]any{
		"model":        request.Model,
		"instructions": request.Instructions,
		"input":        request.Input,
		"stream":       request.Stream,
		"store":        request.Store,
	}
	if request.Reasoning != nil {
		payload["reasoning"] = request.Reasoning
	}
	if request.PreviousResponseID != "" {
		payload["previous_response_id"] = request.PreviousResponseID
	}
	if len(request.Tools) > 0 {
		payload["tools"] = request.Tools
	}
	if request.ToolChoice != nil {
		payload["tool_choice"] = request.ToolChoice
	}
	if request.Text != nil {
		payload["text"] = request.Text
	}
	if request.PromptCacheKey != "" {
		payload["prompt_cache_key"] = request.PromptCacheKey
	}
	if len(request.Include) > 0 {
		payload["include"] = request.Include
	}
	return json.Marshal(payload)
}

func (c *Client) createResponseViaWebSocket(ctx context.Context, token, accountID string, request ResponsesRequest) (*http.Response, error) {
	request = sanitizeUnsupportedUpstreamFields(request)
	wsURL := strings.Replace(c.cfg.API.BaseURL, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1) + "/codex/responses"

	headers := c.defaultHeaders(token, accountID)
	headers.Set("OpenAI-Beta", "responses_websockets=2026-02-06")
	headers.Set("x-openai-internal-codex-residency", "us")
	if request.TurnState != "" {
		headers.Set("x-codex-turn-state", request.TurnState)
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: 20 * time.Second,
	}
	conn, _, err := dialer.DialContext(ctx, wsURL, headers)
	if err != nil {
		return nil, err
	}

	wsPayload := map[string]any{
		"type":         "response.create",
		"model":        request.Model,
		"instructions": request.Instructions,
		"input":        request.Input,
	}
	if request.PreviousResponseID != "" {
		wsPayload["previous_response_id"] = request.PreviousResponseID
	}
	if request.Reasoning != nil {
		wsPayload["reasoning"] = request.Reasoning
	}
	if len(request.Tools) > 0 {
		wsPayload["tools"] = request.Tools
	}
	if request.ToolChoice != nil {
		wsPayload["tool_choice"] = request.ToolChoice
	}
	if request.Text != nil {
		wsPayload["text"] = request.Text
	}
	if request.PromptCacheKey != "" {
		wsPayload["prompt_cache_key"] = request.PromptCacheKey
	}
	if len(request.Include) > 0 {
		wsPayload["include"] = ensureReasoningInclude(request.Include, request.Reasoning)
	}
	if err := conn.WriteJSON(wsPayload); err != nil {
		_ = conn.Close()
		return nil, err
	}

	reader, writer := io.Pipe()
	go func() {
		defer writer.Close()
		defer conn.Close()
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err) {
					_ = writer.CloseWithError(err)
				}
				return
			}
			var typed map[string]any
			if err := json.Unmarshal(msg, &typed); err != nil {
				_, _ = writer.Write([]byte("data: " + string(msg) + "\n\n"))
				continue
			}
			eventName, _ := typed["type"].(string)
			if eventName == "" {
				eventName = "message"
			}
			_, _ = writer.Write([]byte("event: " + eventName + "\n"))
			_, _ = writer.Write([]byte("data: " + string(msg) + "\n\n"))
			if eventName == "response.completed" || eventName == "response.failed" || eventName == "error" {
				return
			}
		}
	}()

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       reader,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}
	return resp, nil
}

func ParseSSE(resp *http.Response) ([]SSEEvent, error) {
	defer resp.Body.Close()
	var events []SSEEvent
	scanner := bufio.NewScanner(resp.Body)
	var currentEvent SSEEvent
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			currentEvent.Event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			currentEvent.Data = append(currentEvent.Data, []byte(strings.TrimPrefix(line, "data: "))...)
		case line == "":
			if len(currentEvent.Data) > 0 {
				if currentEvent.Event == "" {
					currentEvent.Event = "message"
				}
				eventCopy := currentEvent
				events = append(events, eventCopy)
			}
			currentEvent = SSEEvent{}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func StableConversationKey(request ResponsesRequest) string {
	canonical := map[string]any{
		"instructions": strings.TrimSpace(request.Instructions),
		"first_user":   firstUserText(request.Input),
		"model":        request.Model,
	}
	raw, _ := json.Marshal(canonical)
	sum := sha1.Sum(raw)
	return "conv_" + hex.EncodeToString(sum[:16])
}

func firstUserText(input []InputItem) string {
	for _, item := range input {
		if item.Role != "user" {
			continue
		}
		switch content := item.Content.(type) {
		case string:
			return content
		case []any:
			var parts []string
			for _, raw := range content {
				part, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				if typeName, _ := part["type"].(string); typeName == "input_text" {
					if text, ok := part["text"].(string); ok {
						parts = append(parts, text)
					}
				}
			}
			return strings.Join(parts, "")
		}
	}
	return ""
}

func (c *Client) newRequest(ctx context.Context, method, target string, body io.Reader, token, accountID string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	headers := c.defaultHeaders(token, accountID)
	for key, values := range headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	return req, nil
}

func (c *Client) defaultHeaders(token, accountID string) http.Header {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+token)
	if accountID != "" {
		headers.Set("ChatGPT-Account-Id", accountID)
	}
	headers.Set("User-Agent", c.cfg.API.UserAgent)
	headers.Set("Originator", c.cfg.API.Originator)
	headers.Set("Accept-Language", "en-US,en;q=0.9")
	headers.Set("Sec-Fetch-Dest", "empty")
	headers.Set("Sec-Fetch-Mode", "cors")
	headers.Set("Sec-Fetch-Site", "same-origin")
	return headers
}

func decodeModelList(resp *http.Response) ([]map[string]any, error) {
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, &UpstreamError{
			StatusCode: resp.StatusCode,
			Body:       strings.TrimSpace(string(body)),
			Header:     resp.Header.Clone(),
		}
	}

	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}

	if nested, ok := payload["chat_models"].(map[string]any); ok {
		if flattened := flattenModelEntries(nested["models"]); len(flattened) > 0 {
			return flattened, nil
		}
	}

	for _, key := range []string{"models", "data", "categories"} {
		if flattened := flattenModelEntries(payload[key]); len(flattened) > 0 {
			return flattened, nil
		}
	}

	return nil, nil
}

func flattenModelEntries(raw any) []map[string]any {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}

	flattened := make([]map[string]any, 0, len(items))
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if nested, ok := entry["models"].([]any); ok {
			flattened = append(flattened, flattenModelEntries(nested)...)
			continue
		}
		flattened = append(flattened, entry)
	}
	return flattened
}

func ensureReasoningInclude(existing []string, reasoning *Reasoning) []string {
	if reasoning == nil {
		return existing
	}
	for _, item := range existing {
		if item == "reasoning.encrypted_content" {
			return existing
		}
	}
	return append(existing, "reasoning.encrypted_content")
}

func sanitizeUnsupportedUpstreamFields(request ResponsesRequest) ResponsesRequest {
	request.User = ""
	request.PromptCacheRetention = ""
	request.Metadata = nil
	request.StreamOptions = nil
	request.Input = normalizeUpstreamCallIDs(request.Input)
	return request
}

func normalizeUpstreamCallIDs(input []InputItem) []InputItem {
	normalizedByOriginal := map[string]string{}
	originalByNormalized := map[string]string{}
	result := make([]InputItem, len(input))
	copy(result, input)
	for i := range result {
		callID := result[i].CallID
		if callID == "" {
			continue
		}
		normalized, ok := normalizedByOriginal[callID]
		if !ok {
			normalized = upstreamCallID(callID, originalByNormalized)
			normalizedByOriginal[callID] = normalized
			originalByNormalized[normalized] = callID
		}
		result[i].CallID = normalized
	}
	return result
}

func upstreamCallID(callID string, originalByNormalized map[string]string) string {
	if isValidUpstreamCallID(callID) {
		if original, exists := originalByNormalized[callID]; !exists || original == callID {
			return callID
		}
	}
	for _, candidate := range strings.Fields(callID) {
		if !isValidUpstreamCallID(candidate) {
			continue
		}
		if original, exists := originalByNormalized[candidate]; !exists || original == callID {
			return candidate
		}
	}
	sum := sha1.Sum([]byte(callID))
	return "call_" + hex.EncodeToString(sum[:16])
}

func isValidUpstreamCallID(callID string) bool {
	return callID != "" && len(callID) <= 64 && !strings.ContainsAny(callID, " \t\r\n")
}

func ReadResponseText(events []SSEEvent) (string, Usage, string, error) {
	var builder strings.Builder
	var usage Usage
	var responseID string
	for _, event := range events {
		var payload map[string]any
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			continue
		}
		switch event.Event {
		case "response.created", "response.in_progress", "response.completed":
			if response, ok := payload["response"].(map[string]any); ok {
				if id, ok := response["id"].(string); ok {
					responseID = id
				}
				if rawUsage, ok := response["usage"].(map[string]any); ok {
					usage = ParseUsage(rawUsage)
				}
			}
		case "response.output_text.delta":
			if delta, ok := payload["delta"].(string); ok {
				builder.WriteString(delta)
			}
		case "error", "response.failed":
			return "", usage, responseID, errors.New(string(event.Data))
		}
	}
	return builder.String(), usage, responseID, nil
}

func ParseUsage(raw map[string]any) Usage {
	inputTokens := intFromAny(raw["input_tokens"])
	if inputTokens == 0 {
		inputTokens = intFromAny(raw["prompt_tokens"])
	}
	outputTokens := intFromAny(raw["output_tokens"])
	if outputTokens == 0 {
		outputTokens = intFromAny(raw["completion_tokens"])
	}
	cachedTokens := intFromAny(raw["cached_tokens"])
	promptDetailsCached := 0
	if details, ok := raw["prompt_tokens_details"].(map[string]any); ok {
		promptDetailsCached = intFromAny(details["cached_tokens"])
	}
	inputDetailsCached := 0
	if details, ok := raw["input_tokens_details"].(map[string]any); ok {
		inputDetailsCached = intFromAny(details["cached_tokens"])
	}
	if cachedTokens == 0 {
		switch {
		case inputDetailsCached > 0:
			cachedTokens = inputDetailsCached
		case promptDetailsCached > 0:
			cachedTokens = promptDetailsCached
		}
	}
	reasoningTokens := intFromAny(raw["reasoning_tokens"])
	completionReasoning := 0
	if details, ok := raw["completion_tokens_details"].(map[string]any); ok {
		completionReasoning = intFromAny(details["reasoning_tokens"])
	}
	if completionReasoning == 0 {
		if details, ok := raw["output_tokens_details"].(map[string]any); ok {
			completionReasoning = intFromAny(details["reasoning_tokens"])
		}
	}
	if completionReasoning == 0 {
		completionReasoning = intFromAny(raw["reasoning_tokens"])
	}
	if reasoningTokens == 0 {
		reasoningTokens = completionReasoning
	}
	totalTokens := intFromAny(raw["total_tokens"])
	if totalTokens == 0 {
		totalTokens = inputTokens + outputTokens
	}
	return Usage{
		InputTokens:         inputTokens,
		OutputTokens:        outputTokens,
		CachedTokens:        cachedTokens,
		ReasoningTokens:     reasoningTokens,
		TotalTokens:         totalTokens,
		InputDetailsCached:  inputDetailsCached,
		PromptDetailsCached: promptDetailsCached,
		CompletionReasoning: completionReasoning,
	}
}

func intFromAny(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	default:
		return 0
	}
}

func TargetURL(baseURL, path string) string {
	parsed, _ := url.Parse(baseURL)
	parsed.Path = strings.TrimSuffix(parsed.Path, "/") + "/" + strings.TrimPrefix(path, "/")
	return parsed.String()
}
