package app

import (
	"bufio"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jushjay/prism/internal/codex"
	"github.com/jushjay/prism/internal/reasoning"
)

const errModelRequired = "model is required"

// resolveUpstreamRequest rewrites configured model names and applies the
// model's configured reasoning effort, overriding any effort Cursor sent.
func (s *Server) resolveUpstreamRequest(request codex.ResponsesRequest) codex.ResponsesRequest {
	if strings.TrimSpace(request.PromptCacheKey) == "" {
		request.PromptCacheKey = codex.StableConversationKey(request)
	}
	resolution := s.resolver.Resolve(request.Model)
	if resolution.TargetModel != "" {
		request.Model = resolution.TargetModel
	}
	applyModelReasoningEffort(&request, resolution)
	return request
}

func applyModelReasoningEffort(request *codex.ResponsesRequest, resolution reasoning.Resolution) {
	effort := strings.TrimSpace(resolution.ReasoningEffort)
	if effort == "" {
		return
	}
	if request.Reasoning == nil {
		request.Reasoning = &codex.Reasoning{
			Effort:  effort,
			Summary: "auto",
		}
		return
	}
	request.Reasoning.Effort = effort
	if strings.TrimSpace(request.Reasoning.Summary) == "" {
		request.Reasoning.Summary = "auto"
	}
}

func reasoningEffortForRequest(request codex.ResponsesRequest) string {
	if request.Reasoning == nil {
		return ""
	}
	return strings.TrimSpace(request.Reasoning.Effort)
}

func (s *Server) createUpstreamResponse(c *gin.Context, requestedModel string, request codex.ResponsesRequest) (*http.Response, bool) {
	fmt.Fprintf(gin.DefaultWriter, "[cursor] path=%s requested_model=%s upstream_model=%s reasoning_effort=%s stream=%t\n",
		c.Request.URL.Path, requestedModel, request.Model, reasoningEffortForRequest(request), c.GetBool(contextKeyClientStream))
	headers := upstreamRequestHeaders(c.Request.Header)
	start := time.Now()
	resp, body, err := s.upstream.CreateResponse(c.Request.Context(), request, headers)
	s.auditUpstream(c, body, headers, resp, err, time.Since(start))
	if err != nil {
		if c.Request.Context().Err() != nil {
			return nil, false
		}
		writeUpstreamError(c, err)
		return nil, false
	}
	return resp, true
}

const contextKeyClientStream = "sidecar.client_stream"

var skippedUpstreamRequestHeaders = map[string]struct{}{
	"connection":          {},
	"keep-alive":          {},
	"proxy-authenticate":  {},
	"proxy-authorization": {},
	"proxy-connection":    {},
	"te":                  {},
	"trailer":             {},
	"transfer-encoding":   {},
	"upgrade":             {},
	"host":                {},
	"content-length":      {},
	"content-type":        {},
	"accept":              {},
	"accept-encoding":     {},
}

// upstreamRequestHeaders forwards the client headers (notably Authorization,
// which carries the sub2api key) minus hop-by-hop and body-specific ones.
func upstreamRequestHeaders(in http.Header) http.Header {
	out := http.Header{}
	for key, values := range in {
		if _, skip := skippedUpstreamRequestHeaders[strings.ToLower(key)]; skip {
			continue
		}
		out[key] = append([]string(nil), values...)
	}
	return out
}

func writeUpstreamError(c *gin.Context, err error) {
	var upstreamErr *codex.UpstreamError
	if !errors.As(err, &upstreamErr) {
		writeProxyError(c, http.StatusBadGateway, "sub2api request failed: "+err.Error())
		return
	}
	copyForwardResponseHeaders(c.Writer.Header(), upstreamErr.Header)
	contentType := upstreamErr.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json; charset=utf-8"
	}
	c.Data(upstreamErr.StatusCode, contentType, []byte(upstreamErr.Body))
}

func writeProxyError(c *gin.Context, status int, message string) {
	errType := "server_error"
	switch {
	case status == http.StatusTooManyRequests:
		errType = "rate_limit_error"
	case status >= 400 && status < 500:
		errType = "invalid_request_error"
	}
	c.JSON(status, gin.H{
		"error": gin.H{
			"message": message,
			"type":    errType,
		},
	})
}

func copyForwardResponseHeaders(dst, src http.Header) {
	for key, values := range src {
		if shouldSkipForwardHeader(key) {
			continue
		}
		dst.Del(key)
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func shouldSkipForwardHeader(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailers", "transfer-encoding", "upgrade", "content-length", "content-encoding":
		return true
	default:
		return false
	}
}

type streamOutcome struct {
	ended     bool
	completed bool
	reason    string
}

func (o *streamOutcome) end(reason string) {
	o.ended = true
	o.reason = reason
}

// markStreamTerminalEvent records response.completed and reports whether the
// event terminates the stream as a failure.
func markStreamTerminalEvent(outcome *streamOutcome, event codex.SSEEvent) bool {
	switch event.Event {
	case "response.completed":
		outcome.completed = true
	case "response.failed", "error":
		outcome.end(streamFailureMessage(event))
		return true
	}
	return false
}

func readSSEEvents(resp *http.Response) ([]codex.SSEEvent, error) {
	reader := bufio.NewReader(resp.Body)
	var events []codex.SSEEvent
	for {
		event, done, err := readOneSSEEvent(reader)
		if err != nil {
			return nil, err
		}
		if done {
			return events, nil
		}
		events = append(events, event)
	}
}

func isCursorUpstreamNoise(event codex.SSEEvent) bool {
	return event.Event == "codex.rate_limits" || event.Event == "keepalive"
}
