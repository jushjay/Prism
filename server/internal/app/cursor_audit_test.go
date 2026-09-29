package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jushjay/prism/internal/codex"
	"github.com/jushjay/prism/internal/config"
)

func TestCursorAuditLoggerWritesCursorRequestsOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)

	logPath := filepath.Join(t.TempDir(), "cursor-audit.jsonl")
	audit, err := newAuditManager(config.AuditConfig{
		CursorRequestLogEnabled: true,
		CursorRequestLogFile:    logPath,
	})
	if err != nil {
		t.Fatalf("newAuditManager: %v", err)
	}
	defer func() { _ = audit.Close() }()

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("User-Agent", "Cursor/1.0")
	req.Header.Set("X-Forwarded-For", "203.0.113.51")
	req.RemoteAddr = "127.0.0.1:12345"
	ctx.Request = req

	audit.LogCursorRequest(ctx, []byte(`{"model":"gpt-5.4"}`))
	audit.LogCursorRequest(ctx, []byte(`{"model":"gpt-5.5"}`))

	otherReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	otherReq.Header.Set("User-Agent", "curl/8.0")
	otherReq.RemoteAddr = "127.0.0.1:12345"
	ctx.Request = otherReq
	audit.LogCursorRequest(ctx, []byte(`{"model":"ignored"}`))

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	lines := splitNonEmptyLines(string(raw))
	if len(lines) != 2 {
		t.Fatalf("expected 2 cursor log lines, got %d", len(lines))
	}

	var entry cursorAuditEntry
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("unmarshal log entry: %v", err)
	}
	if entry.UserAgent != "Cursor/1.0" {
		t.Fatalf("unexpected user agent %q", entry.UserAgent)
	}
	if entry.ClientIP != "203.0.113.51" {
		t.Fatalf("unexpected client ip %q", entry.ClientIP)
	}
	if entry.Body != `{"model":"gpt-5.4"}` {
		t.Fatalf("unexpected body %q", entry.Body)
	}
}

func splitNonEmptyLines(raw string) []string {
	lines := []string{}
	start := 0
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\n' {
			continue
		}
		if i > start {
			lines = append(lines, raw[start:i])
		}
		start = i + 1
	}
	if start < len(raw) {
		lines = append(lines, raw[start:])
	}
	return lines
}

func TestAuditManagerLogsUpstreamWithRedactedSecrets(t *testing.T) {
	gin.SetMode(gin.TestMode)

	logPath := filepath.Join(t.TempDir(), "upstream-audit.jsonl")
	audit, err := newAuditManager(config.AuditConfig{
		UpstreamLogEnabled: true,
		UpstreamLogFile:    logPath,
	})
	if err != nil {
		t.Fatalf("newAuditManager: %v", err)
	}
	defer func() { _ = audit.Close() }()

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("User-Agent", "Cursor/1.0")
	req.Header.Set("X-Real-IP", "203.0.113.51")
	ctx.Request = req

	headers := http.Header{}
	headers.Set("Authorization", "Bearer sk-secret")
	headers.Set("X-Trace", "abc")
	upstreamErr := &codex.UpstreamError{StatusCode: 429, Body: `{"error":"slow down"}`, Header: http.Header{"Content-Type": {"application/json"}}}
	audit.LogUpstream(ctx, "http://127.0.0.1:8081/v1/responses", []byte(`{"model":"gpt-5.4","stream":true,"reasoning":{"effort":"high"},"input":[{"role":"user"}]}`), headers, nil, upstreamErr, 15*time.Millisecond)

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	lines := splitNonEmptyLines(string(raw))
	if len(lines) != 1 {
		t.Fatalf("expected 1 upstream log line, got %d", len(lines))
	}
	var entry upstreamAuditEntry
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("unmarshal entry: %v", err)
	}
	if entry.ResponseStatus != 429 || entry.ResponseBody != `{"error":"slow down"}` {
		t.Fatalf("unexpected response fields %#v", entry)
	}
	if entry.RequestModel != "gpt-5.4" || entry.ReasoningEffort != "high" || entry.InputCount != 1 {
		t.Fatalf("unexpected request summary %#v", entry)
	}
	if entry.RequestHeaders["Authorization"] != "[redacted]" || entry.RequestHeaders["X-Trace"] != "abc" {
		t.Fatalf("unexpected request headers %#v", entry.RequestHeaders)
	}
	if entry.SourceClientIP != "203.0.113.51" {
		t.Fatalf("unexpected client ip %q", entry.SourceClientIP)
	}
}
