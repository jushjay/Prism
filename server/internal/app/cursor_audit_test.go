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
	"github.com/jushjay/prism/internal/auth"
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
	req.Header.Set("X-Forwarded-For", "13.212.58.113")
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
	if entry.ClientIP != "13.212.58.113" {
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

func TestAuditManagerSeparatesOpenAIAndCustomEgress(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dir := t.TempDir()
	openaiPath := filepath.Join(dir, "openai-egress.jsonl")
	customPath := filepath.Join(dir, "custom-egress.jsonl")
	audit, err := newAuditManager(config.AuditConfig{
		OpenAIEgressLogEnabled: true,
		OpenAIEgressLogFile:    openaiPath,
		CustomEgressLogEnabled: true,
		CustomEgressLogFile:    customPath,
	})
	if err != nil {
		t.Fatalf("newAuditManager: %v", err)
	}
	defer func() { _ = audit.Close() }()

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("User-Agent", "Cursor/1.0")
	req.Header.Set("X-Forwarded-For", "13.212.58.113")
	req.RemoteAddr = "127.0.0.1:12345"
	ctx.Request = req

	audit.LogOpenAIEgress(ctx, auth.Account{ID: "openai-1", Provider: auth.ProviderOpenAI}, "https://chatgpt.com/backend-api/codex/responses", "/codex/responses", []byte(`{"model":"gpt-5.4","stream":true}`), map[string]string{"accept": "text/event-stream"}, 200, map[string]string{"Content-Type": "text/event-stream"}, "", 12*time.Millisecond, nil)
	audit.LogCustomEgress(ctx, auth.Account{ID: "custom-1", Provider: auth.ProviderCustom, Label: "fflycode"}, "https://example.com/v1/responses", "/v1/responses", []byte(`{"model":"gpt-5.5","stream":true}`), map[string]string{"accept": "text/event-stream"}, 502, map[string]string{"Content-Type": "application/json"}, `{"error":"bad gateway"}`, 15*time.Millisecond, nil)

	openaiRaw, err := os.ReadFile(openaiPath)
	if err != nil {
		t.Fatalf("read openai log: %v", err)
	}
	customRaw, err := os.ReadFile(customPath)
	if err != nil {
		t.Fatalf("read custom log: %v", err)
	}
	openaiLines := splitNonEmptyLines(string(openaiRaw))
	customLines := splitNonEmptyLines(string(customRaw))
	if len(openaiLines) != 1 {
		t.Fatalf("expected 1 openai log line, got %d", len(openaiLines))
	}
	if len(customLines) != 1 {
		t.Fatalf("expected 1 custom log line, got %d", len(customLines))
	}

	var openaiEntry upstreamAuditEntry
	if err := json.Unmarshal([]byte(openaiLines[0]), &openaiEntry); err != nil {
		t.Fatalf("unmarshal openai entry: %v", err)
	}
	if openaiEntry.UpstreamType != "openai" || openaiEntry.TargetPath != "/backend-api/codex/responses" {
		t.Fatalf("unexpected openai entry %#v", openaiEntry)
	}

	var customEntry upstreamAuditEntry
	if err := json.Unmarshal([]byte(customLines[0]), &customEntry); err != nil {
		t.Fatalf("unmarshal custom entry: %v", err)
	}
	if customEntry.UpstreamType != "custom" || customEntry.TargetPath != "/v1/responses" || customEntry.ResponseStatus != 502 {
		t.Fatalf("unexpected custom entry %#v", customEntry)
	}
}
