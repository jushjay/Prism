package app

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jushjay/prism/internal/codex"
	"github.com/jushjay/prism/internal/schema"
)

func (s *Server) handleCursorResponses(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		writeProxyError(c, http.StatusBadRequest, err.Error())
		return
	}
	s.auditCursorRequest(c, body)

	var request codex.ResponsesRequest
	if err := json.Unmarshal(body, &request); err != nil {
		writeProxyError(c, http.StatusBadRequest, err.Error())
		return
	}
	var tupleSchema map[string]any
	if request.Text != nil {
		if formatType, _ := request.Text.Format["type"].(string); formatType == "json_schema" {
			if rawSchema, ok := request.Text.Format["schema"].(map[string]any); ok {
				prepared, original := schema.Prepare(rawSchema)
				request.Text.Format["schema"] = prepared
				tupleSchema = original
			}
		}
	}
	if strings.TrimSpace(request.Model) == "" {
		request.Model = s.cfg.Model.DefaultModel
	}
	requestedModel := strings.TrimSpace(request.Model)
	clientStream := request.Stream
	hasExplicitReasoning := request.Reasoning != nil && strings.TrimSpace(request.Reasoning.Effort) != ""
	c.Set(contextKeyClientStream, clientStream)

	upstreamRequest := s.resolveUpstreamRequest(request, hasExplicitReasoning)
	upstreamRequest = codex.PrepareRequest(upstreamRequest, clientStream)
	resp, ok := s.createUpstreamResponse(c, requestedModel, upstreamRequest)
	if !ok {
		return
	}
	defer resp.Body.Close()
	if clientStream {
		streamResponses(c, resp, tupleSchema)
		return
	}

	events, err := readSSEEvents(resp)
	if err != nil {
		writeProxyError(c, http.StatusBadGateway, err.Error())
		return
	}
	payload, _, _, err := buildResponsesPayload(events, requestedModel, tupleSchema)
	if err != nil {
		writeProxyError(c, http.StatusBadGateway, err.Error())
		return
	}
	c.JSON(http.StatusOK, payload)
}

func streamResponses(c *gin.Context, resp *http.Response, tupleSchema map[string]any) {
	ctx := c.Request.Context()
	prepareSSEStreamResponse(c)
	w := c.Writer
	w.Flush()
	stopReader := make(chan struct{})
	defer close(stopReader)
	eventCh := startUpstreamSSEReader(bufio.NewReader(resp.Body), stopReader)
	heartbeat := time.NewTicker(downstreamHeartbeatInterval)
	defer heartbeat.Stop()
	outcome := &streamOutcome{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			writeSSECommentAndFlush(w, "keepalive")
		case result, ok := <-eventCh:
			switch {
			case !ok:
				return
			case result.err != nil:
				fmt.Fprintf(gin.DefaultErrorWriter, "[cursor] responses stream read error: %v\n", result.err)
				return
			case result.done:
				if !outcome.completed {
					fmt.Fprintln(gin.DefaultErrorWriter, "[cursor] responses stream ended without response.completed")
				}
				return
			}
			event := result.event
			switch event.Event {
			case "codex.rate_limits":
				continue
			case "keepalive":
				writeSSECommentAndFlush(w, "keepalive")
				continue
			}
			failed := markStreamTerminalEvent(outcome, event)
			streamTupleAwareResponsesEvent(w, event, tupleSchema)
			w.Flush()
			if failed {
				fmt.Fprintf(gin.DefaultErrorWriter, "[cursor] responses stream failed: %s\n", outcome.reason)
				return
			}
		}
	}
}
