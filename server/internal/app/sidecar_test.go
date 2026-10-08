package app

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jushjay/prism/internal/config"
	"github.com/jushjay/prism/internal/reasoning"
)

const cursorUA = "Cursor/2.0.0 (darwin arm64)"

type capturedRequest struct {
	Method string
	Path   string
	Query  string
	Host   string
	Header http.Header
	Body   []byte
}

type fakeSub2API struct {
	mu       sync.Mutex
	requests []capturedRequest
	handler  http.HandlerFunc
}

func (f *fakeSub2API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.requests = append(f.requests, capturedRequest{
		Method: r.Method,
		Path:   r.URL.Path,
		Query:  r.URL.RawQuery,
		Host:   r.Host,
		Header: r.Header.Clone(),
		Body:   body,
	})
	f.mu.Unlock()
	f.handler(w, r)
}

func (f *fakeSub2API) last(t *testing.T) capturedRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		t.Fatal("sub2api received no request")
	}
	return f.requests[len(f.requests)-1]
}

func (f *fakeSub2API) lastJSON(t *testing.T) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(f.last(t).Body, &payload); err != nil {
		t.Fatalf("upstream body is not JSON: %v", err)
	}
	return payload
}

func newTestSidecar(t *testing.T, handler http.HandlerFunc, fileCfg reasoning.FileConfig) (*httptest.Server, *fakeSub2API) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	sub2api := &fakeSub2API{handler: handler}
	upstream := httptest.NewServer(sub2api)
	t.Cleanup(upstream.Close)

	resolver, err := reasoning.New(fileCfg)
	if err != nil {
		t.Fatalf("reasoning.New: %v", err)
	}
	cfg := config.Config{
		Upstream: config.UpstreamConfig{BaseURL: upstream.URL, PreserveHost: true},
	}
	s, err := newServer(cfg, resolver, nil)
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	sidecar := httptest.NewServer(s.Handler())
	t.Cleanup(sidecar.Close)
	return sidecar, sub2api
}

func postJSON(t *testing.T, url, userAgent, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-sub2api-user")
	req.Header.Set("User-Agent", userAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	return resp
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(raw)
}

func writeSSE(w http.ResponseWriter, events ...[2]string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, event := range events {
		_, _ = io.WriteString(w, "event: "+event[0]+"\ndata: "+event[1]+"\n\n")
	}
}

func completedTextStream(w http.ResponseWriter, r *http.Request) {
	writeSSE(w,
		[2]string{"response.created", `{"response":{"id":"resp_1","model":"gpt-5.4"}}`},
		[2]string{"response.reasoning_summary_text.delta", `{"delta":"thinking"}`},
		[2]string{"response.output_text.delta", `{"delta":"Hello"}`},
		[2]string{"response.output_text.delta", `{"delta":" world"}`},
		[2]string{"response.completed", `{"response":{"id":"resp_1","model":"gpt-5.4","usage":{"input_tokens":10,"output_tokens":5}}}`},
	)
}

func reasoningEffortOf(t *testing.T, payload map[string]any) string {
	t.Helper()
	reasoningValue, ok := payload["reasoning"].(map[string]any)
	if !ok {
		t.Fatalf("upstream request has no reasoning: %#v", payload)
	}
	effort, _ := reasoningValue["effort"].(string)
	return effort
}

func TestNonCursorRequestIsForwardedVerbatim(t *testing.T) {
	sidecar, sub2api := newTestSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Upstream", "sub2api")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"chatcmpl-raw"}`)
	}, reasoning.FileConfig{})

	body := `{"model":"gpt-5.4-high","messages":[{"role":"user","content":"hi"}],"stream":false}`
	req, _ := http.NewRequest(http.MethodPost, sidecar.URL+"/v1/chat/completions?foo=bar", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-sub2api-user")
	req.Header.Set("User-Agent", "curl/8.0")
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	req.Host = "api.example.com"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	respBody := readBody(t, resp)

	if resp.StatusCode != http.StatusCreated || respBody != `{"id":"chatcmpl-raw"}` || resp.Header.Get("X-Upstream") != "sub2api" {
		t.Fatalf("response not passed through: %d %q %v", resp.StatusCode, respBody, resp.Header)
	}
	got := sub2api.last(t)
	if got.Path != "/v1/chat/completions" || got.Query != "foo=bar" {
		t.Fatalf("unexpected upstream target %s?%s", got.Path, got.Query)
	}
	if string(got.Body) != body {
		t.Fatalf("body was modified:\n got %s\nwant %s", got.Body, body)
	}
	if got.Header.Get("Authorization") != "Bearer sk-sub2api-user" || got.Header.Get("User-Agent") != "curl/8.0" {
		t.Fatalf("headers not forwarded: %v", got.Header)
	}
	if got.Host != "api.example.com" {
		t.Fatalf("expected inbound Host to be preserved, got %q", got.Host)
	}
	if got.Header.Get("X-Forwarded-For") != "203.0.113.9" {
		t.Fatalf("expected X-Forwarded-For to be kept verbatim, got %q", got.Header.Get("X-Forwarded-For"))
	}
}

func TestNonCursorStreamIsForwardedVerbatim(t *testing.T) {
	upstreamStream := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"
	sidecar, _ := newTestSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, upstreamStream)
	}, reasoning.FileConfig{})

	resp := postJSON(t, sidecar.URL+"/v1/chat/completions", "OpenAI/Python 1.0", `{"model":"gpt-5.4","stream":true,"messages":[]}`)
	if got := readBody(t, resp); got != upstreamStream {
		t.Fatalf("stream modified:\n got %q\nwant %q", got, upstreamStream)
	}
}

func TestCursorNonRewritePathsAreProxied(t *testing.T) {
	sidecar, sub2api := newTestSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"object":"list","data":[]}`)
	}, reasoning.FileConfig{})

	req, _ := http.NewRequest(http.MethodGet, sidecar.URL+"/v1/models", nil)
	req.Header.Set("User-Agent", cursorUA)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if body := readBody(t, resp); body != `{"object":"list","data":[]}` {
		t.Fatalf("unexpected body %q", body)
	}
	if got := sub2api.last(t); got.Method != http.MethodGet || got.Path != "/v1/models" {
		t.Fatalf("unexpected upstream request %s %s", got.Method, got.Path)
	}
}

func TestCursorChatInjectsModelReasoningAndConvertsStream(t *testing.T) {
	sidecar, sub2api := newTestSidecar(t, completedTextStream, reasoning.FileConfig{
		Models: map[string]reasoning.Model{"gpt-5.4": {ReasoningEffort: "high"}},
	})

	resp := postJSON(t, sidecar.URL+"/v1/chat/completions", cursorUA, `{"model":"gpt-5.4","stream":true,"messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", resp.StatusCode, readBody(t, resp))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("unexpected content type %q", ct)
	}
	body := readBody(t, resp)

	got := sub2api.last(t)
	if got.Path != "/v1/responses" {
		t.Fatalf("expected conversion to /v1/responses, got %s", got.Path)
	}
	if got.Header.Get("Authorization") != "Bearer sk-sub2api-user" {
		t.Fatalf("authorization not forwarded: %v", got.Header)
	}
	payload := sub2api.lastJSON(t)
	if effort := reasoningEffortOf(t, payload); effort != "high" {
		t.Fatalf("expected configured model effort high, got %q", effort)
	}
	if payload["stream"] != true || payload["store"] != false || payload["instructions"] != "be brief" {
		t.Fatalf("unexpected upstream payload %#v", payload)
	}
	if _, ok := payload["messages"]; ok {
		t.Fatalf("upstream payload should be Responses format, got %#v", payload)
	}

	var content, reasoningContent strings.Builder
	var finishReason string
	sawDone := false
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			sawDone = true
			continue
		}
		var chunk struct {
			Object  string `json:"object"`
			Model   string `json:"model"`
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			t.Fatalf("bad chunk %q: %v", data, err)
		}
		if chunk.Object != "chat.completion.chunk" || chunk.Model != "gpt-5.4" {
			t.Fatalf("unexpected chunk %q", data)
		}
		for _, choice := range chunk.Choices {
			content.WriteString(choice.Delta.Content)
			reasoningContent.WriteString(choice.Delta.ReasoningContent)
			if choice.FinishReason != nil {
				finishReason = *choice.FinishReason
			}
		}
	}
	if content.String() != "Hello world" || reasoningContent.String() != "thinking" || finishReason != "stop" || !sawDone {
		t.Fatalf("unexpected converted stream content=%q reasoning=%q finish=%q done=%t\n%s", content.String(), reasoningContent.String(), finishReason, sawDone, body)
	}
}

func TestCursorChatUnlistedModelHasNoInjectedReasoning(t *testing.T) {
	sidecar, sub2api := newTestSidecar(t, completedTextStream, reasoning.FileConfig{
		Models: map[string]reasoning.Model{"gpt-5.4": {ReasoningEffort: "high"}},
	})

	resp := postJSON(t, sidecar.URL+"/v1/chat/completions", cursorUA, `{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"}]}`)
	readBody(t, resp)
	payload := sub2api.lastJSON(t)
	if payload["model"] != "gpt-5.5" {
		t.Fatalf("unlisted model should pass through, got %v", payload["model"])
	}
	if _, ok := payload["reasoning"]; ok {
		t.Fatalf("unlisted model should not get a reasoning effort, got %#v", payload["reasoning"])
	}
}

func TestCursorRequestWithoutModelIsRejected(t *testing.T) {
	sidecar, sub2api := newTestSidecar(t, completedTextStream, reasoning.FileConfig{})

	for _, target := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"messages":[{"role":"user","content":"hi"}]}`},
		{"/v1/responses", `{"input":[{"role":"user","content":"hi"}]}`},
	} {
		resp := postJSON(t, sidecar.URL+target.path, cursorUA, target.body)
		body := readBody(t, resp)
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, errModelRequired) {
			t.Fatalf("%s: expected 400 %q, got %d %s", target.path, errModelRequired, resp.StatusCode, body)
		}
	}
	if len(sub2api.requests) != 0 {
		t.Fatalf("requests without model must not reach sub2api")
	}
}

func TestCursorChatConfiguredEffortOverridesRequestEffort(t *testing.T) {
	sidecar, sub2api := newTestSidecar(t, completedTextStream, reasoning.FileConfig{
		Models: map[string]reasoning.Model{
			"gpt-5.4-high": {Target: "gpt-5.4", ReasoningEffort: "high"},
			"gpt-5.4":      {ReasoningEffort: "xhigh"},
		},
	})

	for _, test := range []struct{ body, want string }{
		{`{"model":"gpt-5.4-high","reasoning_effort":"low","messages":[{"role":"user","content":"hi"}]}`, "high"},
		{`{"model":"gpt-5.4","reasoning":{"effort":"low"},"messages":[{"role":"user","content":"hi"}]}`, "xhigh"},
		{`{"model":"gpt-5.4","reasoning":"low","messages":[{"role":"user","content":"hi"}]}`, "xhigh"},
		{`{"model":"gpt-5.5","reasoning":"low","messages":[{"role":"user","content":"hi"}]}`, "low"},
	} {
		resp := postJSON(t, sidecar.URL+"/v1/chat/completions", cursorUA, test.body)
		readBody(t, resp)
		if effort := reasoningEffortOf(t, sub2api.lastJSON(t)); effort != test.want {
			t.Fatalf("%s: got effort %q, want %q", test.body, effort, test.want)
		}
	}
}

func TestCursorChatAcceptsStringReasoningAndCustomTool(t *testing.T) {
	sidecar, sub2api := newTestSidecar(t, completedTextStream, reasoning.FileConfig{
		Models: map[string]reasoning.Model{"gpt-6-astra": {ReasoningEffort: "low"}},
	})

	resp := postJSON(t, sidecar.URL+"/v1/chat/completions", cursorUA, `{
		"model": "gpt-6-astra",
		"messages": [{"role":"user","content":[{"type":"text","text":"hi"}]}],
		"stream": true,
		"tools": [{"type":"custom","name":"ApplyPatch","description":"Edit files","format":{"type":"grammar","syntax":"lark","definition":"start: x"}}],
		"user": "cursor-user",
		"reasoning": "medium",
		"stream_options": {"include_usage": true}
	}`)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d %s", resp.StatusCode, body)
	}
	payload := sub2api.lastJSON(t)
	if effort := reasoningEffortOf(t, payload); effort != "low" {
		t.Fatalf("expected configured effort low to override request effort, got %q", effort)
	}
	tools, _ := payload["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("expected 1 upstream tool, got %#v", payload["tools"])
	}
	tool := tools[0].(map[string]any)
	format, _ := tool["format"].(map[string]any)
	if tool["type"] != "custom" || tool["name"] != "ApplyPatch" || format["definition"] != "start: x" {
		t.Fatalf("expected custom tool with grammar format upstream, got %#v", tool)
	}
	if _, ok := tool["function"]; ok {
		t.Fatalf("custom tool must not carry a function block, got %#v", tool)
	}
	if _, ok := tool["parameters"]; ok {
		t.Fatalf("custom tool must not carry parameters, got %#v", tool)
	}
}

func TestCursorResponsesAcceptsStringReasoning(t *testing.T) {
	sidecar, sub2api := newTestSidecar(t, completedTextStream, reasoning.FileConfig{})

	resp := postJSON(t, sidecar.URL+"/v1/responses", cursorUA, `{"model":"gpt-6-astra","reasoning":"medium","input":[{"role":"user","content":"hi"}]}`)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d %s", resp.StatusCode, body)
	}
	if effort := reasoningEffortOf(t, sub2api.lastJSON(t)); effort != "medium" {
		t.Fatalf("expected effort medium, got %q", effort)
	}
}

func TestCursorChatAppliesVirtualModelEffort(t *testing.T) {
	sidecar, sub2api := newTestSidecar(t, completedTextStream, reasoning.FileConfig{
		Models: map[string]reasoning.Model{
			"gpt-5.4-xhigh": {Target: "gpt-5.4", ReasoningEffort: "xhigh"},
			"gpt-5.4":       {ReasoningEffort: "low"},
		},
	})

	resp := postJSON(t, sidecar.URL+"/v1/chat/completions", cursorUA, `{"model":"gpt-5.4-xhigh","messages":[{"role":"user","content":"hi"}]}`)
	body := readBody(t, resp)
	payload := sub2api.lastJSON(t)
	if payload["model"] != "gpt-5.4" {
		t.Fatalf("expected mapped upstream model gpt-5.4, got %v", payload["model"])
	}
	if effort := reasoningEffortOf(t, payload); effort != "xhigh" {
		t.Fatalf("expected mapped effort xhigh, got %q", effort)
	}

	var completion struct {
		Object  string `json:"object"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(body), &completion); err != nil {
		t.Fatalf("non-stream response is not JSON: %v\n%s", err, body)
	}
	if completion.Object != "chat.completion" || completion.Model != "gpt-5.4-xhigh" || len(completion.Choices) != 1 {
		t.Fatalf("unexpected completion %s", body)
	}
	if completion.Choices[0].Message.Content != "Hello world" || completion.Choices[0].Message.ReasoningContent != "thinking" {
		t.Fatalf("unexpected message %s", body)
	}
	if completion.Usage.PromptTokens != 10 || completion.Usage.CompletionTokens != 5 {
		t.Fatalf("unexpected usage %s", body)
	}
}

func TestCursorResponsesShapedBodyOnChatPathIsConverted(t *testing.T) {
	sidecar, sub2api := newTestSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w,
			[2]string{"response.output_item.added", `{"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file"}}`},
			[2]string{"response.function_call_arguments.delta", `{"item_id":"fc_1","delta":"{\"path\":"}`},
			[2]string{"response.function_call_arguments.delta", `{"item_id":"fc_1","delta":"\"a.go\"}"}`},
			[2]string{"response.function_call_arguments.done", `{"item_id":"fc_1","arguments":"{\"path\":\"a.go\"}"}`},
			[2]string{"response.completed", `{"response":{"id":"resp_2","usage":{"input_tokens":3,"output_tokens":2}}}`},
		)
	}, reasoning.FileConfig{Models: map[string]reasoning.Model{"gpt-5.4": {ReasoningEffort: "medium"}}})

	body := `{"model":"gpt-5.4","stream":true,"instructions":"sys","input":[{"role":"user","content":[{"type":"input_text","text":"read a.go"}]},{"type":"function_call_output","call_id":"call_0","output":[{"type":"input_text","text":"ok"}]}],"tools":[{"type":"function","name":"read_file","parameters":{"type":"object"}}],"metadata":{"x":1}}`
	resp := postJSON(t, sidecar.URL+"/v1/chat/completions", cursorUA, body)
	stream := readBody(t, resp)

	payload := sub2api.lastJSON(t)
	if payload["instructions"] != "sys" || payload["model"] != "gpt-5.4" {
		t.Fatalf("unexpected upstream payload %#v", payload)
	}
	if _, ok := payload["metadata"]; ok {
		t.Fatalf("unsupported fields should be dropped, got %#v", payload)
	}
	input, _ := payload["input"].([]any)
	if len(input) != 2 {
		t.Fatalf("unexpected input %#v", payload["input"])
	}
	if output := input[1].(map[string]any)["output"]; output != "ok" {
		t.Fatalf("function_call_output should be flattened to text, got %#v", output)
	}
	if effort := reasoningEffortOf(t, payload); effort != "medium" {
		t.Fatalf("expected model effort on compat body, got %q", effort)
	}
	for _, want := range []string{`"tool_calls"`, `"name":"read_file"`, `"finish_reason":"tool_calls"`, "data: [DONE]"} {
		if !strings.Contains(stream, want) {
			t.Fatalf("converted stream missing %s:\n%s", want, stream)
		}
	}
}

func TestCursorResponsesEndpointNonStream(t *testing.T) {
	sidecar, sub2api := newTestSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w,
			[2]string{"response.created", `{"response":{"id":"resp_3","model":"gpt-5.4"}}`},
			[2]string{"response.output_text.delta", `{"delta":"done"}`},
			[2]string{"response.completed", `{"response":{"id":"resp_3","model":"gpt-5.4","output":[{"type":"message","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":4,"output_tokens":1}}}`},
		)
	}, reasoning.FileConfig{
		Models: map[string]reasoning.Model{"gpt-5.4-high": {Target: "gpt-5.4", ReasoningEffort: "high"}},
	})

	resp := postJSON(t, sidecar.URL+"/v1/responses", cursorUA, `{"model":"gpt-5.4-high","input":[{"role":"user","content":"hi"}]}`)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", resp.StatusCode, body)
	}
	payload := sub2api.lastJSON(t)
	if payload["model"] != "gpt-5.4" || reasoningEffortOf(t, payload) != "high" || payload["stream"] != true {
		t.Fatalf("unexpected upstream payload %#v", payload)
	}
	var result struct {
		ID     string `json:"id"`
		Object string `json:"object"`
		Output []any  `json:"output"`
	}
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatalf("bad response JSON: %v", err)
	}
	if result.ID != "resp_3" || result.Object != "response" || len(result.Output) != 1 {
		t.Fatalf("unexpected aggregated response %s", body)
	}
}

func TestCursorResponsesEndpointStreamPassesEvents(t *testing.T) {
	sidecar, _ := newTestSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w,
			[2]string{"codex.rate_limits", `{}`},
			[2]string{"response.output_text.delta", `{"delta":"hi"}`},
			[2]string{"response.completed", `{"response":{"id":"resp_4"}}`},
		)
	}, reasoning.FileConfig{})

	resp := postJSON(t, sidecar.URL+"/v1/responses", cursorUA, `{"model":"gpt-5.4","stream":true,"input":[]}`)
	body := readBody(t, resp)
	if strings.Contains(body, "codex.rate_limits") {
		t.Fatalf("internal events should be dropped:\n%s", body)
	}
	if !strings.Contains(body, "event: response.output_text.delta") || !strings.Contains(body, "event: response.completed") {
		t.Fatalf("expected responses events to be forwarded:\n%s", body)
	}
}

func TestCursorUpstreamErrorIsPassedThrough(t *testing.T) {
	sidecar, _ := newTestSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"quota exceeded","type":"rate_limit_error"}}`)
	}, reasoning.FileConfig{})

	resp := postJSON(t, sidecar.URL+"/v1/chat/completions", cursorUA, `{"model":"gpt-5.4","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusTooManyRequests || body != `{"error":{"message":"quota exceeded","type":"rate_limit_error"}}` {
		t.Fatalf("upstream error not passed through: %d %s", resp.StatusCode, body)
	}
	if resp.Header.Get("Retry-After") != "30" {
		t.Fatalf("expected Retry-After to be forwarded, got %v", resp.Header)
	}
}

func TestCursorChatStreamFailureBeforeOutputReturnsError(t *testing.T) {
	sidecar, _ := newTestSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w, [2]string{"response.failed", `{"response":{"error":{"message":"model overloaded"}}}`})
	}, reasoning.FileConfig{})

	resp := postJSON(t, sidecar.URL+"/v1/chat/completions", cursorUA, `{"model":"gpt-5.4","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(body, "model overloaded") {
		t.Fatalf("expected 502 with upstream message, got %d %s", resp.StatusCode, body)
	}
}

func TestSidecarHealth(t *testing.T) {
	sidecar, sub2api := newTestSidecar(t, completedTextStream, reasoning.FileConfig{
		Models: map[string]reasoning.Model{"a": {Target: "b", ReasoningEffort: "low"}},
	})
	resp, err := http.Get(sidecar.URL + healthPath)
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"models":1`) {
		t.Fatalf("unexpected health %d %s", resp.StatusCode, body)
	}
	if len(sub2api.requests) != 0 {
		t.Fatalf("health must not hit sub2api")
	}
}

func TestIsCursorRewriteRequest(t *testing.T) {
	cases := []struct {
		method, path, ua string
		want             bool
	}{
		{http.MethodPost, "/v1/chat/completions", cursorUA, true},
		{http.MethodPost, "/v1/responses", cursorUA, true},
		{http.MethodGet, "/v1/chat/completions", cursorUA, false},
		{http.MethodPost, "/v1/embeddings", cursorUA, false},
		{http.MethodPost, "/v1/chat/completions", "codex_cli_rs/0.1", false},
		{http.MethodPost, "/v1/chat/completions", "", false},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("User-Agent", tc.ua)
		if got := isCursorRewriteRequest(req); got != tc.want {
			t.Fatalf("%s %s ua=%q: got %t want %t", tc.method, tc.path, tc.ua, got, tc.want)
		}
	}
}
