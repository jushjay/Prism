package app

import (
	"bufio"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jushjay/prism/internal/codex"
	"github.com/jushjay/prism/internal/reasoning"
)

func TestCollectReasoningText(t *testing.T) {
	events := []codex.SSEEvent{
		testEvent("response.reasoning_summary_text.delta", map[string]any{"delta": "step "}),
		testEvent("response.reasoning_summary_text.delta", map[string]any{"delta": "by step"}),
		testEvent("response.reasoning_summary_text.done", map[string]any{"text": "ignored"}),
	}
	if got := collectReasoningText(events); got != "step by step" {
		t.Fatalf("unexpected reasoning text %q", got)
	}

	doneOnly := []codex.SSEEvent{
		testEvent("response.reasoning_summary_text.done", map[string]any{"text": "final summary"}),
	}
	if got := collectReasoningText(doneOnly); got != "final summary" {
		t.Fatalf("unexpected fallback reasoning text %q", got)
	}
}

func TestApplyModelReasoningEffort(t *testing.T) {
	tests := []struct {
		name           string
		resolution     reasoning.Resolution
		initialEffort  string
		expectedEffort string
	}{
		{
			name:           "configured model effort is injected",
			resolution:     reasoning.Resolution{ReasoningEffort: "high"},
			expectedEffort: "high",
		},
		{
			name:           "configured effort overrides request effort",
			initialEffort:  "medium",
			resolution:     reasoning.Resolution{ReasoningEffort: "low"},
			expectedEffort: "low",
		},
		{
			name:           "unlisted model keeps request effort",
			initialEffort:  "medium",
			expectedEffort: "medium",
		},
		{
			name:           "unlisted model gets no effort",
			expectedEffort: "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := codex.ResponsesRequest{}
			if test.initialEffort != "" {
				request.Reasoning = &codex.Reasoning{Effort: test.initialEffort}
			}
			applyModelReasoningEffort(&request, test.resolution)
			got := ""
			if request.Reasoning != nil {
				got = request.Reasoning.Effort
			}
			if got != test.expectedEffort {
				t.Fatalf("reasoning = %+v, want %q", request.Reasoning, test.expectedEffort)
			}
		})
	}
}

func TestBuildResponsesPayloadPreservesOutputAndReconvertTuple(t *testing.T) {
	tupleSchema := map[string]any{
		"type": "array",
		"prefixItems": []any{
			map[string]any{"type": "string"},
			map[string]any{"type": "integer"},
		},
	}
	events := []codex.SSEEvent{
		testEvent("response.completed", map[string]any{
			"response": map[string]any{
				"id":    "resp_1",
				"model": "gpt-5.4",
				"usage": map[string]any{
					"input_tokens":  10,
					"output_tokens": 5,
				},
				"output": []any{
					map[string]any{
						"type": "message",
						"content": []any{
							map[string]any{
								"type": "output_text",
								"text": `{"0":"go","1":7}`,
							},
						},
					},
					map[string]any{
						"type":      "function_call",
						"call_id":   "call_1",
						"name":      "lookup",
						"arguments": "{}",
					},
				},
			},
		}),
	}

	payload, usage, responseID, err := buildResponsesPayload(events, "fallback-model", tupleSchema)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if responseID != "resp_1" {
		t.Fatalf("unexpected response id %s", responseID)
	}
	if usage.InputTokens != 10 || usage.OutputTokens != 5 {
		t.Fatalf("unexpected usage %+v", usage)
	}

	output := payload["output"].([]any)
	message := output[0].(map[string]any)
	content := message["content"].([]any)
	part := content[0].(map[string]any)
	if got := part["text"].(string); got != `["go",7]` {
		t.Fatalf("unexpected reconverted text %s", got)
	}
	if got := output[1].(map[string]any)["type"].(string); got != "function_call" {
		t.Fatalf("expected function_call item, got %s", got)
	}
}

func TestBuildResponsesPayloadParsesCompatibleUsageFields(t *testing.T) {
	events := []codex.SSEEvent{
		testEvent("response.completed", map[string]any{
			"response": map[string]any{
				"id":    "resp_compat",
				"model": "glm-5.1",
				"usage": map[string]any{
					"prompt_tokens":     7,
					"completion_tokens": 3,
					"prompt_tokens_details": map[string]any{
						"cached_tokens": 2,
					},
					"completion_tokens_details": map[string]any{
						"reasoning_tokens": 1,
					},
				},
				"output": []any{
					map[string]any{
						"type": "message",
						"content": []any{
							map[string]any{
								"type": "output_text",
								"text": "ok",
							},
						},
					},
				},
			},
		}),
	}

	_, usage, responseID, err := buildResponsesPayload(events, "fallback-model", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if responseID != "resp_compat" {
		t.Fatalf("unexpected response id %s", responseID)
	}
	if usage.InputTokens != 7 || usage.OutputTokens != 3 || usage.CachedTokens != 2 || usage.ReasoningTokens != 1 {
		t.Fatalf("unexpected usage %+v", usage)
	}
}

func TestBuildResponsesPayloadParsesInputTokenDetailsCachedTokens(t *testing.T) {
	events := []codex.SSEEvent{
		testEvent("response.completed", map[string]any{
			"response": map[string]any{
				"id":    "resp_cache_hit",
				"model": "gpt-5.4",
				"usage": map[string]any{
					"input_tokens":  9411,
					"output_tokens": 6,
					"input_tokens_details": map[string]any{
						"cached_tokens": 8960,
					},
					"output_tokens_details": map[string]any{
						"reasoning_tokens": 0,
					},
					"total_tokens": 9417,
				},
				"output": []any{
					map[string]any{
						"type": "message",
						"content": []any{
							map[string]any{
								"type": "output_text",
								"text": "CACHE_OK",
							},
						},
					},
				},
			},
		}),
	}

	payload, usage, _, err := buildResponsesPayload(events, "fallback-model", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if usage.CachedTokens != 8960 || usage.TotalTokens != 9417 {
		t.Fatalf("unexpected usage %+v", usage)
	}
	usagePayload, ok := payload["usage"].(gin.H)
	if !ok {
		t.Fatalf("expected gin.H usage payload, got %#v", payload["usage"])
	}
	inputDetails, ok := usagePayload["input_tokens_details"].(gin.H)
	if !ok || inputDetails["cached_tokens"] != 8960 {
		t.Fatalf("expected input token details cached tokens, got %#v", usagePayload["input_tokens_details"])
	}
	if usagePayload["total_tokens"] != 9417 {
		t.Fatalf("expected total_tokens=9417, got %#v", usagePayload["total_tokens"])
	}
}

func TestChatStreamStateEmitsReasoningChunk(t *testing.T) {
	state := newChatStreamState("gpt-5.4", nil)
	chunks := state.consume(testEvent("response.reasoning_summary_text.delta", map[string]any{
		"delta": "thinking",
	}))
	if len(chunks) != 1 {
		t.Fatalf("expected one reasoning chunk, got %d", len(chunks))
	}
	chunk := chunks[0].(gin.H)
	choices := chunk["choices"].([]any)
	choice := choices[0].(gin.H)
	delta := choice["delta"].(gin.H)
	if delta["reasoning_content"] != "thinking" {
		t.Fatalf("unexpected reasoning delta %#v", delta)
	}
}

func TestWriteSSEComment(t *testing.T) {
	var builder strings.Builder
	writeSSEComment(&builder, "keepalive")
	if builder.String() != ": keepalive\n\n" {
		t.Fatalf("unexpected SSE comment %q", builder.String())
	}
}

func TestStartUpstreamSSEReader(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("event: keepalive\ndata: {}\n\n"))
	stop := make(chan struct{})
	ch := startUpstreamSSEReader(reader, stop)
	result, ok := <-ch
	if !ok {
		t.Fatal("expected one result")
	}
	if result.err != nil || result.done {
		t.Fatalf("unexpected reader result %#v", result)
	}
	if result.event.Event != "keepalive" {
		t.Fatalf("unexpected event %#v", result.event)
	}
}

func TestChatStreamStateEmitsUsageChunkOnCompleted(t *testing.T) {
	state := newChatStreamState("gpt-5.4", nil)
	chunks := state.consume(testEvent("response.completed", map[string]any{
		"response": map[string]any{
			"id":    "resp_1",
			"model": "gpt-5.4",
			"usage": map[string]any{
				"input_tokens":     10,
				"output_tokens":    3,
				"cached_tokens":    7,
				"reasoning_tokens": 2,
			},
		},
	}))

	if len(chunks) != 2 {
		t.Fatalf("expected finish and usage chunks, got %d", len(chunks))
	}
	finishChunk := chunks[0].(gin.H)
	finishChoice := finishChunk["choices"].([]any)[0].(gin.H)
	if finishChoice["finish_reason"] != "stop" {
		t.Fatalf("expected stop finish reason, got %#v", finishChoice["finish_reason"])
	}
	usageChunk := chunks[1].(gin.H)
	if choices := usageChunk["choices"].([]any); len(choices) != 0 {
		t.Fatalf("expected empty choices in usage chunk, got %#v", choices)
	}
	usagePayload := usageChunk["usage"].(gin.H)
	if usagePayload["prompt_tokens"] != 10 || usagePayload["completion_tokens"] != 3 || usagePayload["total_tokens"] != 13 {
		t.Fatalf("unexpected usage payload %#v", usagePayload)
	}
	promptDetails := usagePayload["prompt_tokens_details"].(gin.H)
	if promptDetails["cached_tokens"] != 7 {
		t.Fatalf("expected cached tokens 7, got %#v", promptDetails)
	}
	completionDetails := usagePayload["completion_tokens_details"].(gin.H)
	if completionDetails["reasoning_tokens"] != 2 {
		t.Fatalf("expected reasoning tokens 2, got %#v", completionDetails)
	}
}

func TestChatStreamStateMapsCustomToolItemIDToCallID(t *testing.T) {
	state := newChatStreamState("gpt-5.4", nil)

	startChunks := state.consume(testEvent("response.output_item.added", map[string]any{
		"output_index": 2,
		"item": map[string]any{
			"type": "custom_tool_call",
			"id":   "ctc_1",
			"name": "run_terminal_cmd",
		},
	}))
	if len(startChunks) != 1 {
		t.Fatalf("expected one start chunk, got %d", len(startChunks))
	}
	startDelta := startChunks[0].(gin.H)["choices"].([]any)[0].(gin.H)["delta"].(gin.H)
	toolCall := startDelta["tool_calls"].([]any)[0].(gin.H)
	if toolCall["id"] != "ctc_1" {
		t.Fatalf("expected tool call id ctc_1, got %#v", toolCall["id"])
	}

	deltaChunks := state.consume(testEvent("response.custom_tool_call_input.delta", map[string]any{
		"item_id":      "ctc_1",
		"delta":        "ls",
		"output_index": 2,
	}))
	if len(deltaChunks) != 1 {
		t.Fatalf("expected one delta chunk, got %d", len(deltaChunks))
	}
	deltaToolCall := deltaChunks[0].(gin.H)["choices"].([]any)[0].(gin.H)["delta"].(gin.H)["tool_calls"].([]any)[0].(gin.H)
	if deltaToolCall["index"] != 0 {
		t.Fatalf("expected tool call index 0, got %#v", deltaToolCall["index"])
	}
}

func TestCollectToolCallsMapsCustomToolItemIDToCallID(t *testing.T) {
	events := []codex.SSEEvent{
		testEvent("response.output_item.added", map[string]any{
			"output_index": 2,
			"item": map[string]any{
				"type": "custom_tool_call",
				"id":   "ctc_1",
				"name": "run_terminal_cmd",
			},
		}),
		testEvent("response.custom_tool_call_input.delta", map[string]any{
			"item_id":      "ctc_1",
			"delta":        "ls",
			"output_index": 2,
		}),
		testEvent("response.custom_tool_call_input.done", map[string]any{
			"item_id": "ctc_1",
			"input":   "ls -la",
		}),
	}

	toolCalls := collectToolCalls(events)
	if len(toolCalls) != 1 {
		t.Fatalf("expected one tool call, got %d", len(toolCalls))
	}
	if toolCalls[0]["id"] != "ctc_1" {
		t.Fatalf("expected tool call id ctc_1, got %#v", toolCalls[0]["id"])
	}
	function := toolCalls[0]["function"].(gin.H)
	if function["name"] != "run_terminal_cmd" {
		t.Fatalf("expected tool call name run_terminal_cmd, got %#v", function["name"])
	}
	if function["arguments"] != "ls -la" {
		t.Fatalf("expected tool call arguments ls -la, got %#v", function["arguments"])
	}
}

func TestChatStreamStateSanitizesLocalSubagentCloudBaseBranch(t *testing.T) {
	state := newChatStreamState("gpt-5.6-sol", nil)
	callID := "call_subagent"
	events := []codex.SSEEvent{
		testEvent("response.output_item.added", map[string]any{
			"item": map[string]any{
				"type":    "function_call",
				"id":      callID,
				"call_id": callID,
				"name":    "Subagent",
			},
		}),
		testEvent("response.function_call_arguments.delta", map[string]any{
			"call_id": callID,
			"delta":   `{"cloud_base_branch":"","environment":"local",`,
		}),
		testEvent("response.function_call_arguments.delta", map[string]any{
			"call_id": callID,
			"delta":   `"description":"explore","prompt":"read only"}`,
		}),
		testEvent("response.function_call_arguments.done", map[string]any{
			"call_id": callID,
		}),
	}

	var chunks []any
	for _, event := range events {
		chunks = append(chunks, state.consume(event)...)
	}

	if len(chunks) != 2 {
		t.Fatalf("expected initial tool call and sanitized arguments chunks, got %d", len(chunks))
	}
	raw, err := json.Marshal(chunks[1])
	if err != nil {
		t.Fatalf("marshal chunk: %v", err)
	}
	if strings.Contains(string(raw), "cloud_base_branch") {
		t.Fatalf("expected cloud_base_branch to be stripped, got %s", raw)
	}
	if !strings.Contains(string(raw), `\"environment\":\"local\"`) {
		t.Fatalf("expected sanitized arguments to preserve environment, got %s", raw)
	}
}

func testEvent(name string, payload map[string]any) codex.SSEEvent {
	raw, _ := json.Marshal(payload)
	return codex.SSEEvent{
		Event: name,
		Data:  raw,
	}
}
