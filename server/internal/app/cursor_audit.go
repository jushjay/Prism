package app

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jushjay/prism/internal/auth"
	"github.com/jushjay/prism/internal/config"
	"github.com/jushjay/prism/internal/security"
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
	RemoteAddr       string   `json:"remote_addr"`
	ClientIP         string   `json:"client_ip"`
	ClientIPFrom     string   `json:"client_ip_source"`
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
	UpstreamType     string            `json:"upstream_type"`
	SourceMethod     string            `json:"source_method"`
	SourceURI        string            `json:"source_uri"`
	SourceUserAgent  string            `json:"source_user_agent"`
	SourceClientIP   string            `json:"source_client_ip"`
	AccountID        string            `json:"account_id"`
	AccountLabel     string            `json:"account_label,omitempty"`
	AccountProvider  string            `json:"account_provider"`
	TargetURL        string            `json:"target_url"`
	TargetHost       string            `json:"target_host"`
	TargetPath       string            `json:"target_path"`
	EndpointStyle    string            `json:"endpoint_style"`
	RequestModel     string            `json:"request_model,omitempty"`
	RequestStream    bool              `json:"request_stream"`
	MessageCount     int               `json:"message_count,omitempty"`
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

type toolTraceCall struct {
	ID             string `json:"id,omitempty"`
	Name           string `json:"name,omitempty"`
	ArgumentsBytes int    `json:"arguments_bytes,omitempty"`
}

type toolTraceSummary struct {
	EventTypeCounts     map[string]int  `json:"event_type_counts,omitempty"`
	UpstreamToolCalls   []toolTraceCall `json:"upstream_tool_calls,omitempty"`
	DownstreamToolCalls []toolTraceCall `json:"downstream_tool_calls,omitempty"`
	FinishReasons       []string        `json:"finish_reasons,omitempty"`
	ResponseID          string          `json:"response_id,omitempty"`
}

type toolTraceAuditEntry struct {
	Time            string           `json:"time"`
	EntryType       string           `json:"entry_type"`
	Phase           string           `json:"phase"`
	UpstreamType    string           `json:"upstream_type"`
	SourceURI       string           `json:"source_uri"`
	SourceUserAgent string           `json:"source_user_agent"`
	SourceClientIP  string           `json:"source_client_ip"`
	AccountID       string           `json:"account_id"`
	AccountLabel    string           `json:"account_label,omitempty"`
	AccountProvider string           `json:"account_provider"`
	EndpointStyle   string           `json:"endpoint_style"`
	RequestedModel  string           `json:"requested_model"`
	RoutedModel     string           `json:"routed_model"`
	StreamEndReason string           `json:"stream_end_reason,omitempty"`
	Error           string           `json:"error,omitempty"`
	Summary         toolTraceSummary `json:"summary"`
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
	cursor *auditFileLogger
	openai *auditFileLogger
	custom *auditFileLogger
}

func newAuditManager(cfg config.AuditConfig) (*auditManager, error) {
	cursor, err := newAuditFileLogger(cfg.CursorRequestLogEnabled, cfg.CursorRequestLogFile)
	if err != nil {
		return nil, err
	}
	openai, err := newAuditFileLogger(cfg.OpenAIEgressLogEnabled, cfg.OpenAIEgressLogFile)
	if err != nil {
		_ = cursor.Close()
		return nil, err
	}
	custom, err := newAuditFileLogger(cfg.CustomEgressLogEnabled, cfg.CustomEgressLogFile)
	if err != nil {
		_ = cursor.Close()
		_ = openai.Close()
		return nil, err
	}
	return &auditManager{
		cursor: cursor,
		openai: openai,
		custom: custom,
	}, nil
}

func (m *auditManager) Close() error {
	if m == nil {
		return nil
	}
	if err := m.cursor.Close(); err != nil {
		return err
	}
	if err := m.openai.Close(); err != nil {
		return err
	}
	return m.custom.Close()
}

func (m *auditManager) LogCursorRequest(c *gin.Context, body []byte) {
	if m == nil || m.cursor == nil || !isCursorUserAgent(c.Request.UserAgent()) {
		return
	}
	info := security.InspectClientIP(c.Request.Header, c.Request.RemoteAddr)
	summary := summarizeAuditRequestBody(body)
	m.cursor.log(cursorAuditEntry{
		Time:             time.Now().Format(time.RFC3339),
		Method:           c.Request.Method,
		URI:              c.Request.URL.RequestURI(),
		Host:             c.Request.Host,
		UserAgent:        c.Request.UserAgent(),
		RemoteAddr:       info.RemoteIP,
		ClientIP:         info.ClientIP,
		ClientIPFrom:     info.Source,
		RequestModel:     summary.Model,
		RequestStream:    summary.Stream,
		MessageCount:     summary.Messages,
		InputCount:       summary.Input,
		RequestToolCount: len(summary.ToolNames),
		RequestToolNames: summary.ToolNames,
		Body:             string(body),
	})
}

func (m *auditManager) LogOpenAIEgress(c *gin.Context, account auth.Account, targetURL, endpointStyle string, requestBody []byte, requestHeaders map[string]string, responseStatus int, responseHeaders map[string]string, responseBody string, duration time.Duration, err error) {
	m.logUpstreamEgress(m.openai, "openai", c, account, targetURL, endpointStyle, requestBody, requestHeaders, responseStatus, responseHeaders, responseBody, duration, err)
}

func (m *auditManager) LogCustomEgress(c *gin.Context, account auth.Account, targetURL, endpointStyle string, requestBody []byte, requestHeaders map[string]string, responseStatus int, responseHeaders map[string]string, responseBody string, duration time.Duration, err error) {
	m.logUpstreamEgress(m.custom, "custom", c, account, targetURL, endpointStyle, requestBody, requestHeaders, responseStatus, responseHeaders, responseBody, duration, err)
}

func (m *auditManager) LogToolTrace(c *gin.Context, account auth.Account, phase, requestedModel, routedModel, endpointStyle, streamEndReason, errMessage string, summary toolTraceSummary) {
	if m == nil || c == nil {
		return
	}
	logger := m.openai
	upstreamType := "openai"
	if account.Provider == auth.ProviderCustom {
		logger = m.custom
		upstreamType = "custom"
	}
	if logger == nil {
		return
	}
	info := security.InspectClientIP(c.Request.Header, c.Request.RemoteAddr)
	entry := toolTraceAuditEntry{
		Time:            time.Now().Format(time.RFC3339),
		EntryType:       "tool_trace",
		Phase:           phase,
		UpstreamType:    upstreamType,
		SourceURI:       c.Request.URL.RequestURI(),
		SourceUserAgent: c.Request.UserAgent(),
		SourceClientIP:  info.ClientIP,
		AccountID:       account.ID,
		AccountLabel:    account.Label,
		AccountProvider: string(account.Provider),
		EndpointStyle:   endpointStyle,
		RequestedModel:  requestedModel,
		RoutedModel:     routedModel,
		StreamEndReason: streamEndReason,
		Error:           errMessage,
		Summary:         summary,
	}
	if entry.AccountLabel == "" {
		entry.AccountLabel = account.Email
	}
	logger.log(entry)
}

func (m *auditManager) logUpstreamEgress(logger *auditFileLogger, upstreamType string, c *gin.Context, account auth.Account, targetURL, endpointStyle string, requestBody []byte, requestHeaders map[string]string, responseStatus int, responseHeaders map[string]string, responseBody string, duration time.Duration, err error) {
	if m == nil || logger == nil || c == nil {
		return
	}
	info := security.InspectClientIP(c.Request.Header, c.Request.RemoteAddr)
	entry := upstreamAuditEntry{
		Time:            time.Now().Format(time.RFC3339),
		UpstreamType:    upstreamType,
		SourceMethod:    c.Request.Method,
		SourceURI:       c.Request.URL.RequestURI(),
		SourceUserAgent: c.Request.UserAgent(),
		SourceClientIP:  info.ClientIP,
		AccountID:       account.ID,
		AccountLabel:    account.Label,
		AccountProvider: string(account.Provider),
		TargetURL:       targetURL,
		EndpointStyle:   endpointStyle,
		RequestBody:     string(requestBody),
		RequestHeaders:  requestHeaders,
		ResponseStatus:  responseStatus,
		ResponseHeaders: responseHeaders,
		ResponseBody:    responseBody,
		DurationMs:      duration.Milliseconds(),
	}
	if entry.AccountLabel == "" {
		entry.AccountLabel = account.Email
	}
	if parsed, err := filepathToURLParts(targetURL); err == nil {
		entry.TargetHost = parsed.Host
		entry.TargetPath = parsed.Path
	}
	var requestPayload map[string]any
	if err := json.Unmarshal(requestBody, &requestPayload); err == nil {
		if model, _ := requestPayload["model"].(string); model != "" {
			entry.RequestModel = model
		}
		if stream, ok := requestPayload["stream"].(bool); ok {
			entry.RequestStream = stream
		}
		summary := summarizeAuditRequestPayload(requestPayload)
		entry.MessageCount = summary.Messages
		entry.InputCount = summary.Input
		entry.RequestToolCount = len(summary.ToolNames)
		entry.RequestToolNames = summary.ToolNames
	}
	if err != nil {
		entry.Error = err.Error()
	}
	logger.log(entry)
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

type parsedURLParts struct {
	Host string
	Path string
}

func filepathToURLParts(raw string) (parsedURLParts, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return parsedURLParts{}, err
	}
	return parsedURLParts{Host: parsed.Host, Path: parsed.Path}, nil
}
