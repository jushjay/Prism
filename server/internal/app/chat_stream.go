package app

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jushjay/prism/internal/codex"
	"github.com/jushjay/prism/internal/schema"
)

func readOneSSEEvent(reader *bufio.Reader) (codex.SSEEvent, bool, error) {
	var event codex.SSEEvent
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) && len(event.Data) == 0 {
				return codex.SSEEvent{}, true, nil
			}
			return codex.SSEEvent{}, false, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if len(event.Data) == 0 {
				continue
			}
			return event, false, nil
		}
		if strings.HasPrefix(line, "event: ") {
			event.Event = strings.TrimPrefix(line, "event: ")
			continue
		}
		if strings.HasPrefix(line, "data: ") {
			event.Data = append(event.Data, []byte(strings.TrimPrefix(line, "data: "))...)
		}
	}
}

func streamFailureMessage(event codex.SSEEvent) string {
	var payload struct {
		Message string `json:"message"`
		Error   struct {
			Message string `json:"message"`
		} `json:"error"`
		Response struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"response"`
	}
	if err := json.Unmarshal(event.Data, &payload); err == nil {
		if message := strings.TrimSpace(payload.Error.Message); message != "" {
			return "upstream_response_failed: " + message
		}
		if message := strings.TrimSpace(payload.Response.Error.Message); message != "" {
			return "upstream_response_failed: " + message
		}
		if message := strings.TrimSpace(payload.Message); message != "" {
			return "upstream_response_failed: " + message
		}
	}
	return "upstream_response_failed"
}

type upstreamSSEReadResult struct {
	event codex.SSEEvent
	done  bool
	err   error
}

func startUpstreamSSEReader(reader *bufio.Reader, stop <-chan struct{}) <-chan upstreamSSEReadResult {
	results := make(chan upstreamSSEReadResult, 1)
	go func() {
		defer close(results)
		for {
			event, done, err := readOneSSEEvent(reader)
			result := upstreamSSEReadResult{event: event, done: done, err: err}
			select {
			case results <- result:
			case <-stop:
				return
			}
			if done || err != nil {
				return
			}
		}
	}()
	return results
}

func writeRawSSEEvent(w io.Writer, event codex.SSEEvent) {
	if event.Event != "" {
		_, _ = io.WriteString(w, "event: "+event.Event+"\n")
	}
	_, _ = io.WriteString(w, "data: "+string(event.Data)+"\n\n")
}

func chatChunksCarryVisibleOutput(chunks []any) bool {
	for _, rawChunk := range chunks {
		chunk, ok := rawChunk.(gin.H)
		if !ok {
			continue
		}
		choices, ok := chunk["choices"].([]any)
		if !ok {
			continue
		}
		for _, rawChoice := range choices {
			choice, ok := rawChoice.(gin.H)
			if !ok {
				continue
			}
			delta, ok := choice["delta"].(gin.H)
			if !ok {
				continue
			}
			if content, ok := delta["content"].(string); ok && strings.TrimSpace(content) != "" {
				return true
			}
			if toolCalls, ok := delta["tool_calls"].([]any); ok && len(toolCalls) > 0 {
				return true
			}
		}
	}
	return false
}

func reconvertJSONText(text string, tupleSchema map[string]any) string {
	if tupleSchema == nil || strings.TrimSpace(text) == "" {
		return text
	}
	var parsed any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return text
	}
	reconverted := schema.ReconvertTupleValues(parsed, tupleSchema)
	raw, err := json.Marshal(reconverted)
	if err != nil {
		return text
	}
	return string(raw)
}

func firstOutputText(output []any) string {
	for _, item := range output {
		asMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		content, ok := asMap["content"].([]any)
		if !ok {
			continue
		}
		for _, raw := range content {
			part, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			typeName, _ := part["type"].(string)
			if (typeName == "output_text" || typeName == "text") && part["text"] != nil {
				if text, ok := part["text"].(string); ok {
					return text
				}
			}
		}
	}
	return ""
}

type chatStreamState struct {
	chunkID            string
	model              string
	created            int64
	tupleSchema        map[string]any
	toolCallIndexByID  map[string]int
	itemIDToCallID     map[string]string
	itemIDToName       map[string]string
	nextToolCallIndex  int
	callIDsWithDeltas  map[string]struct{}
	callIDToName       map[string]string
	argumentBuffers    map[string]*strings.Builder
	tupleTextBuffer    strings.Builder
	reasoningBuffer    strings.Builder
	hasTupleTextBuffer bool
	hasToolCalls       bool
}

func newChatStreamState(model string, tupleSchema map[string]any) *chatStreamState {
	return &chatStreamState{
		chunkID:           "chatcmpl-stream",
		model:             model,
		created:           time.Now().Unix(),
		tupleSchema:       tupleSchema,
		toolCallIndexByID: map[string]int{},
		itemIDToCallID:    map[string]string{},
		itemIDToName:      map[string]string{},
		callIDsWithDeltas: map[string]struct{}{},
		callIDToName:      map[string]string{},
		argumentBuffers:   map[string]*strings.Builder{},
	}
}

func (s *chatStreamState) initialRoleChunk() any {
	return s.chunkWithDelta(gin.H{
		"role": "assistant",
	})
}

func (s *chatStreamState) chatChunk(choices []any, usage any) gin.H {
	return gin.H{
		"id":                 s.chunkID,
		"object":             "chat.completion.chunk",
		"created":            s.created,
		"model":              s.model,
		"choices":            choices,
		"system_fingerprint": nil,
		"usage":              usage,
	}
}

func chatChoice(delta gin.H, finishReason any) gin.H {
	return gin.H{
		"index":         0,
		"delta":         delta,
		"logprobs":      nil,
		"finish_reason": finishReason,
	}
}

func chatUsageChunk(usage codex.Usage) gin.H {
	return gin.H{
		"prompt_tokens":     usage.InputTokens,
		"completion_tokens": usage.OutputTokens,
		"total_tokens":      usage.InputTokens + usage.OutputTokens,
		"prompt_tokens_details": gin.H{
			"cached_tokens": usage.CachedTokens,
		},
		"completion_tokens_details": gin.H{
			"reasoning_tokens": usage.ReasoningTokens,
		},
	}
}

func usageFromCompletedPayload(payload map[string]any) (codex.Usage, bool) {
	response, ok := payload["response"].(map[string]any)
	if !ok {
		return codex.Usage{}, false
	}
	rawUsage, ok := response["usage"].(map[string]any)
	if !ok {
		return codex.Usage{}, false
	}
	return codex.ParseUsage(rawUsage), true
}

func (s *chatStreamState) chunkWithDelta(delta gin.H) gin.H {
	return s.chatChunk([]any{chatChoice(delta, nil)}, nil)
}

func (s *chatStreamState) finishChunk(finishReason string) gin.H {
	return s.chatChunk([]any{chatChoice(gin.H{}, finishReason)}, nil)
}

func (s *chatStreamState) usageChunk(usage codex.Usage) gin.H {
	return s.chatChunk([]any{}, chatUsageChunk(usage))
}

func (s *chatStreamState) consume(event codex.SSEEvent) []any {
	var payload map[string]any
	if err := json.Unmarshal(event.Data, &payload); err != nil {
		return nil
	}
	switch event.Event {
	case "response.output_item.added":
		item, ok := payload["item"].(map[string]any)
		if !ok {
			return nil
		}
		itemType, _ := item["type"].(string)
		if itemType != "function_call" && itemType != "custom_tool_call" {
			return nil
		}
		callID := toolCallIDFromItem(item)
		if callID == "" {
			return nil
		}
		itemID, _ := item["id"].(string)
		name, _ := item["name"].(string)
		if itemID != "" {
			s.itemIDToCallID[itemID] = callID
			if name != "" {
				s.itemIDToName[itemID] = name
			}
		}
		if name != "" {
			s.callIDToName[callID] = name
		}
		index := s.nextToolCallIndex
		s.nextToolCallIndex++
		s.toolCallIndexByID[callID] = index
		s.hasToolCalls = true
		return []any{
			s.chunkWithDelta(gin.H{
				"tool_calls": []any{
					gin.H{
						"index": index,
						"id":    callID,
						"type":  "function",
						"function": gin.H{
							"name":      name,
							"arguments": "",
						},
					},
				},
			}),
		}
	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		callID, _ := payload["call_id"].(string)
		itemID, _ := payload["item_id"].(string)
		callID = s.resolveCallID(callID, itemID)
		if callID == "" {
			return nil
		}
		index, ok := s.toolCallIndexByID[callID]
		if !ok {
			index = 0
		}
		delta, _ := payload["delta"].(string)
		if s.buffersToolArguments(callID) {
			s.argumentBuffer(callID).WriteString(delta)
			s.callIDsWithDeltas[callID] = struct{}{}
			return nil
		}
		s.callIDsWithDeltas[callID] = struct{}{}
		return []any{
			s.chunkWithDelta(gin.H{
				"tool_calls": []any{
					gin.H{
						"index": index,
						"function": gin.H{
							"arguments": delta,
						},
					},
				},
			}),
		}
	case "response.function_call_arguments.done", "response.custom_tool_call_input.done":
		callID, _ := payload["call_id"].(string)
		itemID, _ := payload["item_id"].(string)
		callID = s.resolveCallID(callID, itemID)
		if callID == "" {
			return nil
		}
		index, ok := s.toolCallIndexByID[callID]
		if !ok {
			index = 0
		}
		arguments, _ := payload["arguments"].(string)
		if arguments == "" {
			arguments, _ = payload["input"].(string)
		}
		if s.buffersToolArguments(callID) {
			if buffer := s.argumentBuffers[callID]; buffer != nil && buffer.Len() > 0 {
				arguments = buffer.String()
			}
			delete(s.argumentBuffers, callID)
			delete(s.callIDsWithDeltas, callID)
		} else if _, ok := s.callIDsWithDeltas[callID]; ok {
			return nil
		}
		arguments = sanitizeCursorToolArguments(s.callIDToName[callID], arguments)
		return []any{
			s.chunkWithDelta(gin.H{
				"tool_calls": []any{
					gin.H{
						"index": index,
						"function": gin.H{
							"arguments": arguments,
						},
					},
				},
			}),
		}
	case "response.output_text.delta":
		delta, _ := payload["delta"].(string)
		if s.tupleSchema != nil {
			s.hasTupleTextBuffer = true
			s.tupleTextBuffer.WriteString(delta)
			return nil
		}
		return []any{
			s.chunkWithDelta(gin.H{"content": delta}),
		}
	case "response.reasoning_summary_text.delta":
		delta, _ := payload["delta"].(string)
		if delta == "" {
			return nil
		}
		s.reasoningBuffer.WriteString(delta)
		return []any{
			s.chunkWithDelta(gin.H{"reasoning_content": delta}),
		}
	case "response.reasoning_summary_text.done":
		text, _ := payload["text"].(string)
		if text == "" || s.reasoningBuffer.Len() > 0 {
			return nil
		}
		s.reasoningBuffer.WriteString(text)
		return []any{
			s.chunkWithDelta(gin.H{"reasoning_content": text}),
		}
	case "response.completed":
		var chunks []any
		if s.tupleSchema != nil {
			text := s.tupleTextBuffer.String()
			if text == "" {
				if response, ok := payload["response"].(map[string]any); ok {
					if output, ok := response["output"].([]any); ok {
						text = firstOutputText(output)
					}
				}
			}
			if text != "" {
				chunks = append(chunks, s.chunkWithDelta(gin.H{
					"content": reconvertJSONText(text, s.tupleSchema),
				}))
			}
		}
		finishReason := "stop"
		if s.hasToolCalls {
			finishReason = "tool_calls"
		}
		chunks = append(chunks, s.finishChunk(finishReason))
		if usage, ok := usageFromCompletedPayload(payload); ok {
			chunks = append(chunks, s.usageChunk(usage))
		}
		return chunks
	default:
		return nil
	}
}

func (s *chatStreamState) resolveCallID(callID, itemID string) string {
	return resolveToolCallID(callID, itemID, s.itemIDToCallID)
}

func (s *chatStreamState) buffersToolArguments(callID string) bool {
	return strings.EqualFold(strings.TrimSpace(s.callIDToName[callID]), "Subagent")
}

func (s *chatStreamState) argumentBuffer(callID string) *strings.Builder {
	buffer := s.argumentBuffers[callID]
	if buffer == nil {
		buffer = &strings.Builder{}
		s.argumentBuffers[callID] = buffer
	}
	return buffer
}

func sanitizeCursorToolArguments(name, arguments string) string {
	if !strings.EqualFold(strings.TrimSpace(name), "Subagent") || strings.TrimSpace(arguments) == "" {
		return arguments
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(arguments), &payload); err != nil {
		return arguments
	}
	environment, _ := payload["environment"].(string)
	cloudBaseBranch, hasCloudBaseBranch := payload["cloud_base_branch"]
	if hasCloudBaseBranch && !strings.EqualFold(strings.TrimSpace(environment), "cloud") {
		if text, ok := cloudBaseBranch.(string); ok && strings.TrimSpace(text) == "" {
			delete(payload, "cloud_base_branch")
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return arguments
	}
	return string(raw)
}

func writeChatStreamChunk(w io.Writer, chunk any) {
	raw, _ := json.Marshal(chunk)
	_, _ = io.WriteString(w, "data: "+string(raw)+"\n\n")
}

func prepareSSEStreamResponse(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache, no-transform")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
}

func writeSSEComment(w io.Writer, comment string) {
	comment = strings.TrimSpace(comment)
	if comment == "" {
		comment = "keepalive"
	}
	_, _ = io.WriteString(w, ": "+comment+"\n\n")
}

func writeSSECommentAndFlush(w io.Writer, comment string) {
	writeSSEComment(w, comment)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func collectReasoningText(events []codex.SSEEvent) string {
	var builder strings.Builder
	var fallback string
	for _, event := range events {
		var payload map[string]any
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			continue
		}
		switch event.Event {
		case "response.reasoning_summary_text.delta":
			if delta, _ := payload["delta"].(string); delta != "" {
				builder.WriteString(delta)
			}
		case "response.reasoning_summary_text.done":
			if text, _ := payload["text"].(string); text != "" && fallback == "" {
				fallback = text
			}
		}
	}
	if builder.Len() > 0 {
		return builder.String()
	}
	return fallback
}

func collectToolCalls(events []codex.SSEEvent) []gin.H {
	callByID := map[string]gin.H{}
	itemIDToCallID := map[string]string{}
	itemIDToName := map[string]string{}
	order := make([]string, 0)
	for _, event := range events {
		var payload map[string]any
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			continue
		}
		switch event.Event {
		case "response.output_item.added":
			item, ok := payload["item"].(map[string]any)
			if !ok {
				continue
			}
			itemType, _ := item["type"].(string)
			if itemType != "function_call" && itemType != "custom_tool_call" {
				continue
			}
			callID := toolCallIDFromItem(item)
			if callID == "" {
				continue
			}
			itemID, _ := item["id"].(string)
			name, _ := item["name"].(string)
			if itemID != "" {
				itemIDToCallID[itemID] = callID
				if name != "" {
					itemIDToName[itemID] = name
				}
			}
			if _, ok := callByID[callID]; !ok {
				callByID[callID] = gin.H{
					"id":   callID,
					"type": "function",
					"function": gin.H{
						"name":      name,
						"arguments": "",
					},
				}
				order = append(order, callID)
			}
		case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
			callID, _ := payload["call_id"].(string)
			itemID, _ := payload["item_id"].(string)
			callID = resolveToolCallID(callID, itemID, itemIDToCallID)
			if callID == "" {
				continue
			}
			entry, ok := callByID[callID]
			if !ok {
				entry = gin.H{
					"id":   callID,
					"type": "function",
					"function": gin.H{
						"arguments": "",
					},
				}
				order = append(order, callID)
			}
			function := entry["function"].(gin.H)
			delta, _ := payload["delta"].(string)
			function["arguments"] = function["arguments"].(string) + delta
			entry["function"] = function
			callByID[callID] = entry
		case "response.function_call_arguments.done", "response.custom_tool_call_input.done":
			callID, _ := payload["call_id"].(string)
			itemID, _ := payload["item_id"].(string)
			callID = resolveToolCallID(callID, itemID, itemIDToCallID)
			if callID == "" {
				continue
			}
			entry, ok := callByID[callID]
			if !ok {
				entry = gin.H{
					"id":       callID,
					"type":     "function",
					"function": gin.H{},
				}
				order = append(order, callID)
			}
			function := entry["function"].(gin.H)
			if name, _ := payload["name"].(string); name != "" {
				function["name"] = name
			} else if itemID != "" {
				if mappedName, ok := itemIDToName[itemID]; ok && mappedName != "" {
					function["name"] = mappedName
				}
			}
			if arguments, _ := payload["arguments"].(string); arguments != "" {
				function["arguments"] = arguments
			} else if input, _ := payload["input"].(string); input != "" {
				function["arguments"] = input
			}
			entry["function"] = function
			callByID[callID] = entry
		}
	}
	result := make([]gin.H, 0, len(order))
	for _, callID := range order {
		entry := callByID[callID]
		if function, ok := entry["function"].(gin.H); ok {
			name, _ := function["name"].(string)
			arguments, _ := function["arguments"].(string)
			function["arguments"] = sanitizeCursorToolArguments(name, arguments)
			entry["function"] = function
		}
		result = append(result, entry)
	}
	return result
}

func toolCallIDFromItem(item map[string]any) string {
	callID, _ := item["call_id"].(string)
	if callID != "" {
		return callID
	}
	id, _ := item["id"].(string)
	return id
}

func resolveToolCallID(callID, itemID string, itemIDToCallID map[string]string) string {
	if callID != "" {
		if resolved, ok := itemIDToCallID[callID]; ok {
			return resolved
		}
		return callID
	}
	if itemID == "" {
		return ""
	}
	if resolved, ok := itemIDToCallID[itemID]; ok {
		return resolved
	}
	return itemID
}

func buildResponsesPayload(events []codex.SSEEvent, model string, tupleSchema map[string]any) (gin.H, codex.Usage, string, error) {
	var usage codex.Usage
	responseID := ""
	resolvedModel := model
	var output []any
	for _, event := range events {
		var payload map[string]any
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			continue
		}
		switch event.Event {
		case "response.created", "response.in_progress", "response.completed":
			response, ok := payload["response"].(map[string]any)
			if !ok {
				continue
			}
			if id, ok := response["id"].(string); ok && id != "" {
				responseID = id
			}
			if responseModel, ok := response["model"].(string); ok && responseModel != "" {
				resolvedModel = responseModel
			}
			if rawUsage, ok := response["usage"].(map[string]any); ok {
				usage = codex.ParseUsage(rawUsage)
			}
			if event.Event == "response.completed" {
				if rawOutput, ok := response["output"].([]any); ok {
					output = rewriteResponseOutput(rawOutput, tupleSchema)
				}
			}
		case "error", "response.failed":
			return nil, usage, responseID, errors.New(string(event.Data))
		}
	}
	if len(output) == 0 {
		text, _, _, err := codex.ReadResponseText(events)
		if err != nil {
			return nil, usage, responseID, err
		}
		if tupleSchema != nil {
			text = reconvertJSONText(text, tupleSchema)
		}
		output = []any{
			gin.H{
				"type": "message",
				"content": []any{
					gin.H{
						"type": "output_text",
						"text": text,
					},
				},
			},
		}
	}
	return gin.H{
		"id":     responseID,
		"object": "response",
		"model":  resolvedModel,
		"output": output,
		"usage": gin.H{
			"input_tokens": usage.InputTokens,
			"input_tokens_details": gin.H{
				"cached_tokens": usage.CachedTokens,
			},
			"output_tokens": usage.OutputTokens,
			"output_tokens_details": gin.H{
				"reasoning_tokens": usage.ReasoningTokens,
			},
			"total_tokens": usage.TotalTokens,
		},
	}, usage, responseID, nil
}

func rewriteResponseOutput(output []any, tupleSchema map[string]any) []any {
	if tupleSchema == nil {
		return output
	}
	rewritten := make([]any, 0, len(output))
	for _, rawItem := range output {
		item, ok := rawItem.(map[string]any)
		if !ok {
			rewritten = append(rewritten, rawItem)
			continue
		}
		itemCopy := mapsClone(item)
		content, ok := item["content"].([]any)
		if !ok {
			rewritten = append(rewritten, itemCopy)
			continue
		}
		contentCopy := make([]any, 0, len(content))
		for _, rawContent := range content {
			part, ok := rawContent.(map[string]any)
			if !ok {
				contentCopy = append(contentCopy, rawContent)
				continue
			}
			partCopy := mapsClone(part)
			typeName, _ := partCopy["type"].(string)
			if (typeName == "output_text" || typeName == "text") && partCopy["text"] != nil {
				if text, ok := partCopy["text"].(string); ok {
					partCopy["text"] = reconvertJSONText(text, tupleSchema)
				}
			}
			contentCopy = append(contentCopy, partCopy)
		}
		itemCopy["content"] = contentCopy
		rewritten = append(rewritten, itemCopy)
	}
	return rewritten
}

func mapsClone(input map[string]any) map[string]any {
	cloned := make(map[string]any, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

func streamTupleAwareResponsesEvent(w io.Writer, event codex.SSEEvent, tupleSchema map[string]any) bool {
	if tupleSchema == nil {
		writeRawSSEEvent(w, event)
		return true
	}
	if event.Event == "response.output_text.delta" {
		return true
	}
	if event.Event == "response.completed" {
		var payload map[string]any
		if err := json.Unmarshal(event.Data, &payload); err == nil {
			if response, ok := payload["response"].(map[string]any); ok {
				if output, ok := response["output"].([]any); ok {
					if text := firstOutputText(output); text != "" {
						reconverted := reconvertJSONText(text, tupleSchema)
						_, _ = io.WriteString(w, "event: response.output_text.delta\n")
						_, _ = io.WriteString(w, "data: "+string(mustJSON(map[string]any{
							"type":  "response.output_text.delta",
							"delta": reconverted,
						}))+"\n\n")
					}
				}
			}
		}
	}
	writeRawSSEEvent(w, event)
	return true
}

func mustJSON(value any) []byte {
	raw, _ := json.Marshal(value)
	return raw
}
