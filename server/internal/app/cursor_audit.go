package app

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jushjay/prism/internal/codex"
	"github.com/jushjay/prism/internal/config"
)

const auditLogMaxBytes int64 = 100 * 1024 * 1024

type auditFileLogger struct {
	mu   sync.Mutex
	file *os.File
	path string
	size int64
}

type cursorAuditEntry struct {
	Time             string   `json:"time"`
	Method           string   `json:"method"`
	URI              string   `json:"uri"`
	Host             string   `json:"host"`
	UserAgent        string   `json:"user_agent"`
	ClientIP         string   `json:"client_ip"`
	RequestModel     string   `json:"request_model,omitempty"`
	RequestStream    bool     `json:"request_stream"`
	MessageCount     int      `json:"message_count,omitempty"`
	InputCount       int      `json:"input_count,omitempty"`
	RequestToolCount int      `json:"request_tool_count,omitempty"`
	RequestToolNames []string `json:"request_tool_names,omitempty"`
	Body             string   `json:"body"`
}

type upstreamAuditEntry struct {
	Time             string            `json:"time"`
	SourceMethod     string            `json:"source_method"`
	SourceURI        string            `json:"source_uri"`
	SourceUserAgent  string            `json:"source_user_agent"`
	SourceClientIP   string            `json:"source_client_ip"`
	TargetURL        string            `json:"target_url"`
	RequestModel     string            `json:"request_model,omitempty"`
	ReasoningEffort  string            `json:"reasoning_effort,omitempty"`
	RequestStream    bool              `json:"request_stream"`
	InputCount       int               `json:"input_count,omitempty"`
	RequestToolCount int               `json:"request_tool_count,omitempty"`
	RequestToolNames []string          `json:"request_tool_names,omitempty"`
	RequestBody      string            `json:"request_body"`
	RequestHeaders   map[string]string `json:"request_headers,omitempty"`
	ResponseStatus   int               `json:"response_status"`
	ResponseHeaders  map[string]string `json:"response_headers,omitempty"`
	ResponseBody     string            `json:"response_body,omitempty"`
	DurationMs       int64             `json:"duration_ms"`
	Error            string            `json:"error,omitempty"`
}

type requestAuditSummary struct {
	Model     string
	Stream    bool
	Messages  int
	Input     int
	ToolNames []string
}

func newAuditFileLogger(enabled bool, path string) (*auditFileLogger, error) {
	if !enabled {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if info, err := os.Stat(path); err == nil && info.Size() >= auditLogMaxBytes {
		rotatedPath, rotateErr := rotateAuditFile(path)
		if rotateErr != nil {
			return nil, rotateErr
		}
		startGzipAuditLog(rotatedPath)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	size := int64(0)
	if info, err := file.Stat(); err == nil {
		size = info.Size()
	}
	return &auditFileLogger{file: file, path: path, size: size}, nil
}

func (l *auditFileLogger) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	return l.file.Close()
}

func (l *auditFileLogger) log(entry any) {
	if l == nil || l.file == nil {
		return
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	line := append(raw, '\n')
	if l.size > 0 && l.size+int64(len(line)) > auditLogMaxBytes {
		if err := l.rotateLocked(); err != nil {
			fmt.Fprintf(gin.DefaultErrorWriter, "[audit] rotate %s failed: %v\n", l.path, err)
		}
	}
	n, err := l.file.Write(line)
	if err != nil {
		fmt.Fprintf(gin.DefaultErrorWriter, "[audit] write %s failed: %v\n", l.path, err)
		return
	}
	l.size += int64(n)
}

func (l *auditFileLogger) rotateLocked() error {
	if l == nil || l.file == nil {
		return nil
	}
	if err := l.file.Close(); err != nil {
		return err
	}
	rotatedPath, err := rotateAuditFile(l.path)
	if err != nil {
		file, openErr := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if openErr == nil {
			l.file = file
			if info, statErr := file.Stat(); statErr == nil {
				l.size = info.Size()
			}
		}
		return err
	}
	startGzipAuditLog(rotatedPath)
	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	l.file = file
	l.size = 0
	return nil
}

func rotateAuditFile(path string) (string, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	rotatedPath := rotatedAuditPath(path, time.Now())
	if err := os.Rename(path, rotatedPath); err != nil {
		return "", err
	}
	return rotatedPath, nil
}

func rotatedAuditPath(path string, at time.Time) string {
	dir := filepath.Dir(path)
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(filepath.Base(path), ext)
	stamp := at.Format("20060102T150405.000000000")
	candidate := filepath.Join(dir, base+"-"+stamp+ext)
	for i := 1; ; i++ {
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
		candidate = filepath.Join(dir, fmt.Sprintf("%s-%s.%d%s", base, stamp, i, ext))
	}
}

func startGzipAuditLog(path string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	go func() {
		if err := gzipAuditLog(path); err != nil {
			fmt.Fprintf(gin.DefaultErrorWriter, "[audit] gzip %s failed: %v\n", path, err)
		}
	}()
}

func gzipAuditLog(path string) error {
	input, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer input.Close()

	tmpPath := path + ".gz.tmp"
	output, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	gzipWriter := gzip.NewWriter(output)
	_, copyErr := io.Copy(gzipWriter, input)
	closeErr := gzipWriter.Close()
	fileCloseErr := output.Close()
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		return closeErr
	}
	if fileCloseErr != nil {
		_ = os.Remove(tmpPath)
		return fileCloseErr
	}
	if err := os.Rename(tmpPath, path+".gz"); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return os.Remove(path)
}

type auditManager struct {
	cursor   *auditFileLogger
	upstream *auditFileLogger
}

func newAuditManager(cfg config.AuditConfig) (*auditManager, error) {
	cursor, err := newAuditFileLogger(cfg.CursorRequestLogEnabled, cfg.CursorRequestLogFile)
	if err != nil {
		return nil, err
	}
	upstream, err := newAuditFileLogger(cfg.UpstreamLogEnabled, cfg.UpstreamLogFile)
	if err != nil {
		_ = cursor.Close()
		return nil, err
	}
	return &auditManager{cursor: cursor, upstream: upstream}, nil
}

func (m *auditManager) Close() error {
	if m == nil {
		return nil
	}
	return errors.Join(m.cursor.Close(), m.upstream.Close())
}

func (s *Server) auditCursorRequest(c *gin.Context, body []byte) {
	if s == nil || s.audit == nil {
		return
	}
	s.audit.LogCursorRequest(c, body)
}

func (s *Server) auditUpstream(c *gin.Context, requestBody []byte, requestHeaders http.Header, resp *http.Response, err error, duration time.Duration) {
	if s == nil || s.audit == nil {
		return
	}
	s.audit.LogUpstream(c, s.upstream.ResponsesURL(), requestBody, requestHeaders, resp, err, duration)
}

func (m *auditManager) LogCursorRequest(c *gin.Context, body []byte) {
	if m == nil || m.cursor == nil || !isCursorUserAgent(c.Request.UserAgent()) {
		return
	}
	summary := summarizeAuditRequestBody(body)
	m.cursor.log(cursorAuditEntry{
		Time:             time.Now().Format(time.RFC3339),
		Method:           c.Request.Method,
		URI:              c.Request.URL.RequestURI(),
		Host:             c.Request.Host,
		UserAgent:        c.Request.UserAgent(),
		ClientIP:         clientIP(c.Request),
		RequestModel:     summary.Model,
		RequestStream:    summary.Stream,
		MessageCount:     summary.Messages,
		InputCount:       summary.Input,
		RequestToolCount: len(summary.ToolNames),
		RequestToolNames: summary.ToolNames,
		Body:             string(body),
	})
}

func (m *auditManager) LogUpstream(c *gin.Context, targetURL string, requestBody []byte, requestHeaders http.Header, resp *http.Response, err error, duration time.Duration) {
	if m == nil || m.upstream == nil || c == nil {
		return
	}
	entry := upstreamAuditEntry{
		Time:            time.Now().Format(time.RFC3339),
		SourceMethod:    c.Request.Method,
		SourceURI:       c.Request.URL.RequestURI(),
		SourceUserAgent: c.Request.UserAgent(),
		SourceClientIP:  clientIP(c.Request),
		TargetURL:       targetURL,
		RequestBody:     string(requestBody),
		RequestHeaders:  auditHeaders(requestHeaders),
		DurationMs:      duration.Milliseconds(),
	}
	if resp != nil {
		entry.ResponseStatus = resp.StatusCode
		entry.ResponseHeaders = auditHeaders(resp.Header)
	}
	var upstreamErr *codex.UpstreamError
	if errors.As(err, &upstreamErr) {
		entry.ResponseStatus = upstreamErr.StatusCode
		entry.ResponseHeaders = auditHeaders(upstreamErr.Header)
		entry.ResponseBody = upstreamErr.Body
	}
	if err != nil {
		entry.Error = err.Error()
	}
	var requestPayload map[string]any
	if json.Unmarshal(requestBody, &requestPayload) == nil {
		summary := summarizeAuditRequestPayload(requestPayload)
		entry.RequestModel = summary.Model
		entry.RequestStream = summary.Stream
		entry.InputCount = summary.Input
		entry.RequestToolCount = len(summary.ToolNames)
		entry.RequestToolNames = summary.ToolNames
		if reasoning, ok := requestPayload["reasoning"].(map[string]any); ok {
			entry.ReasoningEffort, _ = reasoning["effort"].(string)
		}
	}
	m.upstream.log(entry)
}

var redactedAuditHeaders = map[string]struct{}{
	"authorization": {},
	"x-api-key":     {},
	"api-key":       {},
	"cookie":        {},
	"set-cookie":    {},
}

func auditHeaders(header http.Header) map[string]string {
	if len(header) == 0 {
		return nil
	}
	out := make(map[string]string, len(header))
	for key, values := range header {
		if len(values) == 0 {
			continue
		}
		if _, redact := redactedAuditHeaders[strings.ToLower(key)]; redact {
			out[key] = "[redacted]"
			continue
		}
		out[key] = strings.Join(values, ", ")
	}
	return out
}

func summarizeAuditRequestBody(body []byte) requestAuditSummary {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return requestAuditSummary{}
	}
	return summarizeAuditRequestPayload(payload)
}

func summarizeAuditRequestPayload(payload map[string]any) requestAuditSummary {
	summary := requestAuditSummary{}
	if model, _ := payload["model"].(string); model != "" {
		summary.Model = model
	}
	if stream, ok := payload["stream"].(bool); ok {
		summary.Stream = stream
	}
	if messages, ok := payload["messages"].([]any); ok {
		summary.Messages = len(messages)
	}
	if input, ok := payload["input"].([]any); ok {
		summary.Input = len(input)
	}
	if tools, ok := payload["tools"].([]any); ok {
		summary.ToolNames = summarizeAuditToolNames(tools)
	}
	return summary
}

func summarizeAuditToolNames(tools []any) []string {
	names := make([]string, 0, len(tools))
	for _, rawTool := range tools {
		tool, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		name, _ := tool["name"].(string)
		if name == "" {
			if function, ok := tool["function"].(map[string]any); ok {
				name, _ = function["name"].(string)
			}
		}
		if name == "" {
			name, _ = tool["type"].(string)
		}
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}
