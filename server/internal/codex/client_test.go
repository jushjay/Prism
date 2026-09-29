package codex

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jushjay/prism/internal/config"
)

func TestCreateResponsePostsToSub2APIResponses(t *testing.T) {
	var gotPath, gotAuth, gotUA, gotOriginator, gotAccept string
	var gotBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotUA = r.Header.Get("User-Agent")
		gotOriginator = r.Header.Get("Originator")
		gotAccept = r.Header.Get("Accept")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.completed\ndata: {}\n\n")
	}))
	defer upstream.Close()

	client := NewClient(config.UpstreamConfig{BaseURL: upstream.URL, UserAgent: "codex_cli_rs/0.1", Originator: "codex_cli_rs"}, http.DefaultTransport)
	header := http.Header{}
	header.Set("Authorization", "Bearer sk-sub2api")
	header.Set("User-Agent", "Cursor/1.0")
	request := PrepareRequest(ResponsesRequest{
		Model:       "gpt-5.4",
		Input:       []InputItem{{Role: "user", Content: "hi"}},
		Reasoning:   &Reasoning{Effort: "high"},
		ServiceTier: "priority",
		Store:       true,
	}, true)
	resp, body, err := client.CreateResponse(context.Background(), request, header)
	if err != nil {
		t.Fatalf("CreateResponse: %v", err)
	}
	_ = resp.Body.Close()
	if len(body) == 0 {
		t.Fatal("expected sent body to be returned for auditing")
	}
	if gotPath != "/v1/responses" || gotAuth != "Bearer sk-sub2api" || gotAccept != "text/event-stream" {
		t.Fatalf("unexpected request path=%q auth=%q accept=%q", gotPath, gotAuth, gotAccept)
	}
	if gotUA != "codex_cli_rs/0.1" || gotOriginator != "codex_cli_rs" {
		t.Fatalf("expected configured UA/originator, got %q %q", gotUA, gotOriginator)
	}
	if gotBody["stream"] != true || gotBody["store"] != false {
		t.Fatalf("expected stream=true store=false, got %#v", gotBody)
	}
	if _, ok := gotBody["service_tier"]; ok {
		t.Fatalf("service_tier should be stripped, got %#v", gotBody)
	}
	include, _ := gotBody["include"].([]any)
	if len(include) != 1 || include[0] != "reasoning.encrypted_content" {
		t.Fatalf("expected reasoning include, got %#v", gotBody["include"])
	}
}

func TestCreateResponseReturnsUpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid api key"}}`)
	}))
	defer upstream.Close()

	client := NewClient(config.UpstreamConfig{BaseURL: upstream.URL}, http.DefaultTransport)
	_, _, err := client.CreateResponse(context.Background(), ResponsesRequest{Model: "gpt-5.4"}, http.Header{})
	upstreamErr, ok := err.(*UpstreamError)
	if !ok {
		t.Fatalf("expected UpstreamError, got %T %v", err, err)
	}
	if upstreamErr.StatusCode != http.StatusUnauthorized || !strings.Contains(upstreamErr.Body, "invalid api key") {
		t.Fatalf("unexpected upstream error %#v", upstreamErr)
	}
}

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
	raw, err := MarshalResponsesRequest(request)
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

	raw, err := MarshalResponsesRequest(request)
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

	raw, err := MarshalResponsesRequest(request)
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
	raw, err := MarshalResponsesRequest(request)
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
	raw, err := MarshalResponsesRequest(request)
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

	raw, err := MarshalResponsesRequest(request)
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
