package app

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jushjay/prism/internal/auth"
	"github.com/jushjay/prism/internal/config"
	"github.com/jushjay/prism/internal/security"
)

type auditFileLogger struct {
	mu   sync.Mutex
	file *os.File
}

type cursorAuditEntry struct {
	Time         string `json:"time"`
	Method       string `json:"method"`
	URI          string `json:"uri"`
	Host         string `json:"host"`
	UserAgent    string `json:"user_agent"`
	RemoteAddr   string `json:"remote_addr"`
	ClientIP     string `json:"client_ip"`
	ClientIPFrom string `json:"client_ip_source"`
	Body         string `json:"body"`
}

type upstreamAuditEntry struct {
	Time            string            `json:"time"`
	UpstreamType    string            `json:"upstream_type"`
	SourceMethod    string            `json:"source_method"`
	SourceURI       string            `json:"source_uri"`
	SourceUserAgent string            `json:"source_user_agent"`
	SourceClientIP  string            `json:"source_client_ip"`
	AccountID       string            `json:"account_id"`
	AccountLabel    string            `json:"account_label,omitempty"`
	AccountProvider string            `json:"account_provider"`
	TargetURL       string            `json:"target_url"`
	TargetHost      string            `json:"target_host"`
	TargetPath      string            `json:"target_path"`
	EndpointStyle   string            `json:"endpoint_style"`
	RequestModel    string            `json:"request_model,omitempty"`
	RequestStream   bool              `json:"request_stream"`
	RequestBody     string            `json:"request_body"`
	RequestHeaders  map[string]string `json:"request_headers,omitempty"`
	ResponseStatus  int               `json:"response_status"`
	ResponseHeaders map[string]string `json:"response_headers,omitempty"`
	ResponseBody    string            `json:"response_body,omitempty"`
	DurationMs      int64             `json:"duration_ms"`
	Error           string            `json:"error,omitempty"`
}

func newAuditFileLogger(enabled bool, path string) (*auditFileLogger, error) {
	if !enabled {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &auditFileLogger{file: file}, nil
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
	_, _ = l.file.Write(append(raw, '\n'))
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
	m.cursor.log(cursorAuditEntry{
		Time:         time.Now().Format(time.RFC3339),
		Method:       c.Request.Method,
		URI:          c.Request.URL.RequestURI(),
		Host:         c.Request.Host,
		UserAgent:    c.Request.UserAgent(),
		RemoteAddr:   info.RemoteIP,
		ClientIP:     info.ClientIP,
		ClientIPFrom: info.Source,
		Body:         string(body),
	})
}

func (m *auditManager) LogOpenAIEgress(c *gin.Context, account auth.Account, targetURL, endpointStyle string, requestBody []byte, requestHeaders map[string]string, responseStatus int, responseHeaders map[string]string, responseBody string, duration time.Duration, err error) {
	m.logUpstreamEgress(m.openai, "openai", c, account, targetURL, endpointStyle, requestBody, requestHeaders, responseStatus, responseHeaders, responseBody, duration, err)
}

func (m *auditManager) LogCustomEgress(c *gin.Context, account auth.Account, targetURL, endpointStyle string, requestBody []byte, requestHeaders map[string]string, responseStatus int, responseHeaders map[string]string, responseBody string, duration time.Duration, err error) {
	m.logUpstreamEgress(m.custom, "custom", c, account, targetURL, endpointStyle, requestBody, requestHeaders, responseStatus, responseHeaders, responseBody, duration, err)
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
	}
	if err != nil {
		entry.Error = err.Error()
	}
	logger.log(entry)
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
