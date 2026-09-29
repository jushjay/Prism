package codex

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/jushjay/prism/internal/config"
)

const ResponsesPath = "/v1/responses"

type Client struct {
	cfg        config.UpstreamConfig
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

func NewClient(cfg config.UpstreamConfig, transport http.RoundTripper) *Client {
	return &Client{
		cfg:        cfg,
		httpClient: &http.Client{Transport: transport},
	}
}

func (c *Client) ResponsesURL() string {
	return TargetURL(c.cfg.BaseURL, ResponsesPath)
}

// PrepareRequest applies the fixed upstream semantics: the sidecar always
// consumes SSE from sub2api (and re-assembles JSON for non-stream callers)
// and never asks upstream to persist responses.
func PrepareRequest(request ResponsesRequest, clientStream bool) ResponsesRequest {
	request.Store = false
	request.Stream = true
	request.ServiceTier = ""
	if clientStream {
		request.Include = ensureReasoningInclude(request.Include, request.Reasoning)
	}
	return request
}

// CreateResponse posts an already prepared request to sub2api /v1/responses.
// The returned body is the exact payload sent upstream, for auditing.
func (c *Client) CreateResponse(ctx context.Context, request ResponsesRequest, header http.Header) (*http.Response, []byte, error) {
	body, err := MarshalResponsesRequest(request)
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.ResponsesURL(), bytes.NewReader(body))
	if err != nil {
		return nil, body, err
	}
	for key, values := range header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Accept-Encoding", "identity")
	if ua := strings.TrimSpace(c.cfg.UserAgent); ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	if originator := strings.TrimSpace(c.cfg.Originator); originator != "" {
		req.Header.Set("Originator", originator)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, body, err
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return nil, body, &UpstreamError{
			StatusCode: resp.StatusCode,
			Body:       strings.TrimSpace(string(raw)),
			Header:     resp.Header.Clone(),
		}
	}
	return resp, body, nil
}

func MarshalResponsesRequest(request ResponsesRequest) ([]byte, error) {
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
	request.Tools = normalizeResponsesTools(request.Tools)
	return request
}

func normalizeResponsesTools(tools []Tool) []Tool {
	normalized := make([]Tool, 0, len(tools))
	for _, tool := range tools {
		toolType := strings.TrimSpace(tool.Type)
		if toolType == "" {
			toolType = "function"
		}

		name := strings.TrimSpace(tool.Name)
		description := strings.TrimSpace(tool.Description)
		parameters := tool.Parameters
		if function := tool.Function; function != nil {
			if name == "" {
				name = strings.TrimSpace(function.Name)
			}
			if description == "" {
				description = strings.TrimSpace(function.Description)
			}
			if len(parameters) == 0 {
				parameters = function.Parameters
			}
		}

		entry := Tool{
			Type:        toolType,
			Name:        name,
			Description: description,
			Parameters:  parameters,
			Format:      tool.Format,
			Strict:      tool.Strict,
			Function:    nil,
		}
		if toolType == "function" && entry.Name == "" {
			continue
		}
		normalized = append(normalized, entry)
	}
	return normalized
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
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return strings.TrimRight(baseURL, "/") + "/" + strings.TrimPrefix(path, "/")
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/") + "/" + strings.TrimPrefix(path, "/")
	return parsed.String()
}
