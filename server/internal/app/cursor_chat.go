package app

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jushjay/prism/internal/codex"
)

func (s *Server) handleCursorChatCompletions(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		writeProxyError(c, http.StatusBadRequest, err.Error())
		return
	}
	s.auditCursorRequest(c, body)
	chatRequest, err := parseChatProxyRequest(body, c.Request.UserAgent())
	if err != nil {
		writeProxyError(c, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(chatRequest.CodexRequest.Model) == "" {
		writeProxyError(c, http.StatusBadRequest, errModelRequired)
		return
	}
	c.Set(contextKeyClientStream, chatRequest.Stream)
	upstreamRequest := s.resolveUpstreamRequest(chatRequest.CodexRequest)
	upstreamRequest = codex.PrepareRequest(upstreamRequest, chatRequest.Stream)
	requestedModel := chatRequest.RequestedModel
	if strings.TrimSpace(requestedModel) == "" {
		requestedModel = upstreamRequest.Model
	}

	resp, ok := s.createUpstreamResponse(c, requestedModel, upstreamRequest)
	if !ok {
		return
	}
	defer resp.Body.Close()
	if chatRequest.Stream {
		streamChatCompletion(c, resp, requestedModel, chatRequest.TupleSchema)
		return
	}
	writeChatCompletion(c, resp, requestedModel, chatRequest.TupleSchema)
}

func streamChatCompletion(c *gin.Context, resp *http.Response, requestedModel string, tupleSchema map[string]any) {
	ctx := c.Request.Context()
	stopReader := make(chan struct{})
	defer close(stopReader)
	eventCh := startUpstreamSSEReader(bufio.NewReader(resp.Body), stopReader)
	streamState := newChatStreamState(requestedModel, tupleSchema)
	outcome := &streamOutcome{}
	processEvent := func(event codex.SSEEvent) ([]any, bool) {
		if isCursorUpstreamNoise(event) {
			return nil, false
		}
		if markStreamTerminalEvent(outcome, event) {
			return nil, true
		}
		return streamState.consume(event), false
	}

	heartbeat := time.NewTicker(downstreamHeartbeatInterval)
	defer heartbeat.Stop()

	// Do not commit a downstream 200 until upstream has produced output or
	// completed, so an early response.failed surfaces as a real HTTP error.
	var pendingChunks [][]any
	streamStarted := false
	streamReady := false
	for !streamReady && !outcome.ended {
		select {
		case result, ok := <-eventCh:
			switch {
			case !ok:
				outcome.end("stream_reader_closed")
			case result.err != nil:
				outcome.end("read_error: " + result.err.Error())
			case result.done:
				if outcome.completed {
					streamReady = true
					outcome.end("done")
				} else {
					outcome.end("stream_ended_without_completion")
				}
			default:
				chunks, failed := processEvent(result.event)
				if failed {
					break
				}
				pendingChunks = append(pendingChunks, chunks)
				streamReady = chatChunksCarryVisibleOutput(chunks) || outcome.completed
			}
		case <-heartbeat.C:
			if !streamStarted {
				prepareSSEStreamResponse(c)
				streamStarted = true
			}
			writeSSECommentAndFlush(c.Writer, "keepalive")
		case <-ctx.Done():
			outcome.end("request_context: " + ctx.Err().Error())
		}
	}

	if !streamReady {
		if ctx.Err() != nil {
			return
		}
		fmt.Fprintf(gin.DefaultErrorWriter, "[cursor] chat stream failed before output: %s\n", outcome.reason)
		if streamStarted {
			writeChatStreamChunk(c.Writer, gin.H{"error": gin.H{"message": outcome.reason, "type": "server_error"}})
			writeSSECommentAndFlush(c.Writer, "error")
			return
		}
		writeProxyError(c, http.StatusBadGateway, outcome.reason)
		return
	}

	if !streamStarted {
		prepareSSEStreamResponse(c)
	}
	w := c.Writer
	writeChatStreamChunk(w, streamState.initialRoleChunk())
	for _, chunks := range pendingChunks {
		for _, chunk := range chunks {
			writeChatStreamChunk(w, chunk)
		}
	}
	if outcome.ended {
		if outcome.reason == "done" {
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		}
		w.Flush()
		return
	}
	w.Flush()

	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			writeSSECommentAndFlush(w, "keepalive")
		case result, ok := <-eventCh:
			switch {
			case !ok:
				fmt.Fprintln(gin.DefaultErrorWriter, "[cursor] chat stream reader closed before done")
				return
			case result.err != nil:
				fmt.Fprintf(gin.DefaultErrorWriter, "[cursor] chat stream read error: %v\n", result.err)
				return
			case result.done:
				if outcome.completed {
					_, _ = io.WriteString(w, "data: [DONE]\n\n")
				} else {
					fmt.Fprintln(gin.DefaultErrorWriter, "[cursor] chat stream ended without response.completed")
				}
				w.Flush()
				return
			}
			if result.event.Event == "keepalive" {
				writeSSECommentAndFlush(w, "keepalive")
				continue
			}
			chunks, failed := processEvent(result.event)
			if failed {
				fmt.Fprintf(gin.DefaultErrorWriter, "[cursor] chat stream failed: %s\n", outcome.reason)
				return
			}
			for _, chunk := range chunks {
				writeChatStreamChunk(w, chunk)
			}
			w.Flush()
		}
	}
}

func writeChatCompletion(c *gin.Context, resp *http.Response, requestedModel string, tupleSchema map[string]any) {
	events, err := readSSEEvents(resp)
	if err != nil {
		writeProxyError(c, http.StatusBadGateway, err.Error())
		return
	}
	text, usage, responseID, err := codex.ReadResponseText(events)
	if err != nil {
		writeProxyError(c, http.StatusBadGateway, err.Error())
		return
	}
	if tupleSchema != nil {
		text = reconvertJSONText(text, tupleSchema)
	}
	toolCalls := collectToolCalls(events)
	reasoningContent := collectReasoningText(events)
	finishReason := "stop"
	messageBody := gin.H{
		"role":    "assistant",
		"content": text,
	}
	if reasoningContent != "" {
		messageBody["reasoning_content"] = reasoningContent
	}
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
		messageBody["tool_calls"] = toolCalls
		if text == "" {
			messageBody["content"] = nil
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"id":      "chatcmpl-" + responseID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   requestedModel,
		"choices": []any{
			gin.H{
				"index":         0,
				"message":       messageBody,
				"finish_reason": finishReason,
			},
		},
		"usage": chatUsageChunk(usage),
	})
}
