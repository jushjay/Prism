package app

import (
	"bytes"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jushjay/prism/internal/affinity"
	"github.com/jushjay/prism/internal/auth"
	"github.com/jushjay/prism/internal/codex"
	"github.com/jushjay/prism/internal/config"
	"github.com/jushjay/prism/internal/models"
	"github.com/jushjay/prism/internal/requestlog"
	"github.com/jushjay/prism/internal/security"
	"github.com/jushjay/prism/internal/store"
	"github.com/jushjay/prism/internal/usage"
)

type closeNotifyRecorder struct {
	*httptest.ResponseRecorder
}

func (r *closeNotifyRecorder) CloseNotify() <-chan bool {
	ch := make(chan bool, 1)
	return ch
}

func TestHandleResponsesRecordsRequestEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server, sqliteStore := newRequestLogTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"deepseek-chat","object":"model","created":1753632000,"owned_by":"deepseek"}]}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Request-Id", "req-success-1")
		_, _ = w.Write([]byte("event: response.output_text.delta\n"))
		_, _ = w.Write([]byte(`data: {"delta":"hello"}` + "\n\n"))
		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte(`data: {"response":{"id":"resp_success_1","model":"deepseek-chat","usage":{"input_tokens":3,"output_tokens":5},"output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}]}}` + "\n\n"))
	})
	defer sqliteStore.Close()

	body := `{"model":"deepseek-chat","stream":false,"input":[{"role":"user","content":"hi"}]}`
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer proxy-key")
	server.engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}

	var count int
	if err := sqliteStore.DB().QueryRow(`SELECT COUNT(1) FROM request_events`).Scan(&count); err != nil {
		t.Fatalf("count request_events: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 request event, got %d", count)
	}

	var sourcePath, requestedModel, routedModel, upstreamRequestID, responseID string
	var success, requestStream, durationMs int
	var firstTokenMs sql.NullInt64
	if err := sqliteStore.DB().QueryRow(`
		SELECT source_path, requested_model, routed_model, upstream_request_id, response_id, success, request_stream, duration_ms, first_token_ms
		FROM request_events
		LIMIT 1
	`).Scan(&sourcePath, &requestedModel, &routedModel, &upstreamRequestID, &responseID, &success, &requestStream, &durationMs, &firstTokenMs); err != nil {
		t.Fatalf("select request_events: %v", err)
	}
	if sourcePath != "/v1/responses" || requestedModel != "deepseek-chat" || routedModel != "deepseek-chat" {
		t.Fatalf("unexpected model/source fields source=%q requested=%q routed=%q", sourcePath, requestedModel, routedModel)
	}
	if upstreamRequestID != "req-success-1" || responseID != "resp_success_1" {
		t.Fatalf("unexpected request/response ids req=%q resp=%q", upstreamRequestID, responseID)
	}
	if success != 1 || requestStream != 0 || durationMs < 0 || !firstTokenMs.Valid {
		t.Fatalf("unexpected scalar fields success=%d stream=%d duration=%d firstTokenValid=%v", success, requestStream, durationMs, firstTokenMs.Valid)
	}
}

func TestHandleResponsesRecordsEmptyRetryAttempts(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server, sqliteStore := newTwoAccountRequestLogTestServer(t, []http.HandlerFunc{
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"deepseek-chat","object":"model","created":1753632000,"owned_by":"deepseek"}]}`))
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("X-Request-Id", "req-retry-1")
			_, _ = w.Write([]byte("event: response.completed\n"))
			_, _ = w.Write([]byte(`data: {"response":{"id":"resp_empty","model":"deepseek-chat","usage":{"input_tokens":0,"output_tokens":0},"output":[]}}` + "\n\n"))
		},
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"deepseek-chat","object":"model","created":1753632000,"owned_by":"deepseek"}]}`))
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("X-Request-Id", "req-retry-2")
			_, _ = w.Write([]byte("event: response.output_text.delta\n"))
			_, _ = w.Write([]byte(`data: {"delta":"done"}` + "\n\n"))
			_, _ = w.Write([]byte("event: response.completed\n"))
			_, _ = w.Write([]byte(`data: {"response":{"id":"resp_retry_success","model":"deepseek-chat","usage":{"input_tokens":2,"output_tokens":4},"output":[{"type":"message","content":[{"type":"output_text","text":"done"}]}]}}` + "\n\n"))
		},
	})
	defer sqliteStore.Close()

	body := `{"model":"deepseek-chat","stream":false,"input":[{"role":"user","content":"retry"}]}`
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer proxy-key")
	server.engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}

	var count int
	if err := sqliteStore.DB().QueryRow(`SELECT COUNT(1) FROM request_events`).Scan(&count); err != nil {
		t.Fatalf("count request_events: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 request events, got %d", count)
	}

	rows, err := sqliteStore.DB().Query(`SELECT retry_attempt, success, response_id, input_tokens, output_tokens FROM request_events ORDER BY id ASC`)
	if err != nil {
		t.Fatalf("query request_events: %v", err)
	}
	defer rows.Close()
	var got [][]any
	for rows.Next() {
		var retryAttempt, success int
		var responseID string
		var inputTokens, outputTokens sql.NullInt64
		if err := rows.Scan(&retryAttempt, &success, &responseID, &inputTokens, &outputTokens); err != nil {
			t.Fatalf("scan request_events row: %v", err)
		}
		got = append(got, []any{retryAttempt, success, responseID, inputTokens.Int64, outputTokens.Int64})
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(got))
	}
	if got[0][0] != 0 || got[0][1] != 1 || got[0][2] != "resp_empty" {
		t.Fatalf("unexpected first attempt row %#v", got[0])
	}
	if got[1][0] != 1 || got[1][1] != 1 || got[1][2] != "resp_retry_success" {
		t.Fatalf("unexpected second attempt row %#v", got[1])
	}
}

func TestHandleResponsesLogsEmptyResponseDiagnostics(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server, sqliteStore := newRequestLogTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"deepseek-chat","object":"model","created":1753632000,"owned_by":"deepseek"}]}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Request-Id", "req-empty-log")
		_, _ = w.Write([]byte("event: response.created\n"))
		_, _ = w.Write([]byte(`data: {"response":{"id":"resp_empty_log","model":"deepseek-chat"}}` + "\n\n"))
		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte(`data: {"response":{"id":"resp_empty_log","model":"deepseek-chat","usage":{"input_tokens":0,"output_tokens":0},"output":[]}}` + "\n\n"))
	})
	defer sqliteStore.Close()

	originalErrorWriter := gin.DefaultErrorWriter
	var logBuffer bytes.Buffer
	gin.DefaultErrorWriter = &logBuffer
	defer func() {
		gin.DefaultErrorWriter = originalErrorWriter
	}()

	body := `{"model":"deepseek-chat","stream":false,"input":[{"role":"user","content":"hi"}]}`
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer proxy-key")
	server.engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("expected status 502 after exhausting retries, got %d body=%s", recorder.Code, recorder.Body.String())
	}

	logs := logBuffer.String()
	if !strings.Contains(logs, `[upstream-attempt] source=/v1/responses`) {
		t.Fatalf("expected diagnostics log, got %q", logs)
	}
	if !strings.Contains(logs, `response_id="resp_empty_log"`) {
		t.Fatalf("expected response_id in diagnostics log, got %q", logs)
	}
	if !strings.Contains(logs, `empty_response=true`) {
		t.Fatalf("expected empty_response=true in diagnostics log, got %q", logs)
	}
	if !strings.Contains(logs, `events="response.completed=1,response.created=1"`) {
		t.Fatalf("expected event summary in diagnostics log, got %q", logs)
	}
}

func TestHandleResponsesSkipsSuccessfulDiagnostics(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server, sqliteStore := newRequestLogTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"deepseek-chat","object":"model","created":1753632000,"owned_by":"deepseek"}]}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Request-Id", "req-success-log")
		_, _ = w.Write([]byte("event: response.output_text.delta\n"))
		_, _ = w.Write([]byte(`data: {"delta":"hello"}` + "\n\n"))
		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte(`data: {"response":{"id":"resp_success_log","model":"deepseek-chat","usage":{"input_tokens":3,"output_tokens":5},"output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}]}}` + "\n\n"))
	})
	defer sqliteStore.Close()

	originalErrorWriter := gin.DefaultErrorWriter
	var logBuffer bytes.Buffer
	gin.DefaultErrorWriter = &logBuffer
	defer func() {
		gin.DefaultErrorWriter = originalErrorWriter
	}()

	body := `{"model":"deepseek-chat","stream":false,"input":[{"role":"user","content":"hi"}]}`
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer proxy-key")
	server.engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}

	logs := logBuffer.String()
	if logs != "" {
		t.Fatalf("expected no diagnostics log for successful request, got %q", logs)
	}
}

func TestHandleChatCompletionsStreamSkipsSuccessfulDiagnostics(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dir := t.TempDir()
	sqliteStore, err := store.NewSQLiteStore(filepath.Join(dir, "state.db"), dir)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer sqliteStore.Close()

	pool, err := auth.NewAccountPool(filepath.Join(dir, "accounts.json"), sqliteStore, config.AuthConfig{})
	if err != nil {
		t.Fatalf("NewAccountPool() error = %v", err)
	}
	customAccount, err := pool.AddCustomAccount(auth.CustomAccountInput{
		Label:              "deepseek",
		CustomBaseURL:      "https://example.invalid",
		CustomAPIKey:       "deepseek-key",
		CustomEndpointType: "responses",
		Enabled:            true,
	})
	if err != nil {
		t.Fatalf("AddCustomAccount() error = %v", err)
	}

	customUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"deepseek-chat","object":"model","created":1753632000,"owned_by":"deepseek"}]}`))
		case "/v1/responses":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: response.created\n"))
			_, _ = w.Write([]byte(`data: {"response":{"id":"resp_stream_diag","model":"deepseek-chat"}}` + "\n\n"))
			_, _ = w.Write([]byte("event: response.output_text.delta\n"))
			_, _ = w.Write([]byte(`data: {"delta":"hello"}` + "\n\n"))
			_, _ = w.Write([]byte("event: response.completed\n"))
			_, _ = w.Write([]byte(`data: {"response":{"id":"resp_stream_diag","model":"deepseek-chat","usage":{"input_tokens":3,"output_tokens":5},"output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}]}}` + "\n\n"))
		default:
			t.Fatalf("unexpected custom upstream path %q", r.URL.Path)
		}
	}))
	defer customUpstream.Close()

	customAccount.CustomBaseURL = customUpstream.URL
	if err := pool.ReplaceAccount(customAccount); err != nil {
		t.Fatalf("ReplaceAccount() error = %v", err)
	}

	now := time.Now().UTC()
	expires := now.Add(time.Hour)
	if _, err := sqliteStore.DB().Exec(`
		INSERT INTO custom_account_models (account_id, model_id, display_name, model_object, owned_by, created_unix, source_payload, fetched_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, customAccount.ID, "deepseek-chat", "DeepSeek Chat", "model", "deepseek", now.Unix(), `{}`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert custom_account_models: %v", err)
	}
	if _, err := sqliteStore.DB().Exec(`
		INSERT INTO custom_account_model_sync_state (account_id, fetched_at, expires_at, last_error, updated_at)
		VALUES (?, ?, ?, '', ?)
	`, customAccount.ID, now.Format(time.RFC3339Nano), expires.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert custom_account_model_sync_state: %v", err)
	}

	client, err := codex.NewClient(config.Config{
		API: config.APIConfig{
			BaseURL: "https://chatgpt.example.test/backend-api",
		},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	modelService, err := models.NewService(
		config.Config{
			Model: config.ModelConfig{
				DefaultModel: "deepseek-chat",
			},
			Storage: config.StorageConfig{
				ModelCacheFile:    filepath.Join(dir, "model-cache.json"),
				ManualModelsFile:  filepath.Join(dir, "manual-models.json"),
				ModelMappingsFile: filepath.Join(dir, "model-mappings.json"),
			},
		},
		models.Catalog{},
		pool,
		client,
		nil,
		sqliteStore.DB(),
		sqliteStore,
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	securityService, err := security.NewService(sqliteStore.DB())
	if err != nil {
		t.Fatalf("NewService() security error = %v", err)
	}
	usageService, err := usage.NewService(config.Config{}, sqliteStore.DB(), sqliteStore)
	if err != nil {
		t.Fatalf("NewService() usage error = %v", err)
	}
	requestLogService, err := requestlog.NewService(sqliteStore.DB())
	if err != nil {
		t.Fatalf("NewService() requestlog error = %v", err)
	}

	server := &Server{
		cfg: config.Config{
			Server: config.ServerConfig{
				ProxyAPIKey: "test-key",
			},
			Model: config.ModelConfig{
				DefaultModel: "deepseek-chat",
			},
		},
		engine:     gin.New(),
		state:      sqliteStore,
		accounts:   pool,
		codex:      client,
		models:     modelService,
		security:   securityService,
		usage:      usageService,
		requestLog: requestLogService,
		affinity:   affinity.NewStore(),
	}
	server.engine.Use(gin.Recovery())
	server.engine.POST("/v1/chat/completions", server.requireAPIKey(), server.handleChatCompletions)

	originalErrorWriter := gin.DefaultErrorWriter
	var logBuffer bytes.Buffer
	gin.DefaultErrorWriter = &logBuffer
	defer func() {
		gin.DefaultErrorWriter = originalErrorWriter
	}()

	body := `{"model":"deepseek-chat","messages":[{"role":"user","content":"hello"}],"stream":true}`
	recorder := &closeNotifyRecorder{ResponseRecorder: httptest.NewRecorder()}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("User-Agent", "Cursor/1.0")
	server.engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}

	logs := logBuffer.String()
	if logs != "" {
		t.Fatalf("expected no diagnostics log for successful stream, got %q", logs)
	}
}

func TestHandleChatCompletionsStreamCanceledIsRecordedAsFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dir := t.TempDir()
	sqliteStore, err := store.NewSQLiteStore(filepath.Join(dir, "state.db"), dir)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer sqliteStore.Close()

	pool, err := auth.NewAccountPool(filepath.Join(dir, "accounts.json"), sqliteStore, config.AuthConfig{})
	if err != nil {
		t.Fatalf("NewAccountPool() error = %v", err)
	}
	customAccount, err := pool.AddCustomAccount(auth.CustomAccountInput{
		Label:              "deepseek",
		CustomBaseURL:      "https://example.invalid",
		CustomAPIKey:       "deepseek-key",
		CustomEndpointType: "responses",
		Enabled:            true,
	})
	if err != nil {
		t.Fatalf("AddCustomAccount() error = %v", err)
	}

	customUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"deepseek-chat","object":"model","created":1753632000,"owned_by":"deepseek"}]}`))
		case "/v1/responses":
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			_, _ = w.Write([]byte("event: response.created\n"))
			_, _ = w.Write([]byte(`data: {"response":{"id":"resp_stream_cancel","model":"deepseek-chat"}}` + "\n\n"))
			_, _ = w.Write([]byte("event: response.output_text.delta\n"))
			_, _ = w.Write([]byte(`data: {"delta":"hello"}` + "\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatalf("response writer does not support hijack")
			}
			conn, _, hijackErr := hj.Hijack()
			if hijackErr != nil {
				t.Fatalf("Hijack() error = %v", hijackErr)
			}
			_ = conn.Close()
		default:
			t.Fatalf("unexpected custom upstream path %q", r.URL.Path)
		}
	}))
	defer customUpstream.Close()

	customAccount.CustomBaseURL = customUpstream.URL
	if err := pool.ReplaceAccount(customAccount); err != nil {
		t.Fatalf("ReplaceAccount() error = %v", err)
	}

	now := time.Now().UTC()
	expires := now.Add(time.Hour)
	if _, err := sqliteStore.DB().Exec(`
		INSERT INTO custom_account_models (account_id, model_id, display_name, model_object, owned_by, created_unix, source_payload, fetched_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, customAccount.ID, "deepseek-chat", "DeepSeek Chat", "model", "deepseek", now.Unix(), `{}`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert custom_account_models: %v", err)
	}
	if _, err := sqliteStore.DB().Exec(`
		INSERT INTO custom_account_model_sync_state (account_id, fetched_at, expires_at, last_error, updated_at)
		VALUES (?, ?, ?, '', ?)
	`, customAccount.ID, now.Format(time.RFC3339Nano), expires.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert custom_account_model_sync_state: %v", err)
	}

	client, err := codex.NewClient(config.Config{
		API: config.APIConfig{
			BaseURL: "https://chatgpt.example.test/backend-api",
		},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	modelService, err := models.NewService(
		config.Config{
			Model: config.ModelConfig{
				DefaultModel: "deepseek-chat",
			},
			Storage: config.StorageConfig{
				ModelCacheFile:    filepath.Join(dir, "model-cache.json"),
				ManualModelsFile:  filepath.Join(dir, "manual-models.json"),
				ModelMappingsFile: filepath.Join(dir, "model-mappings.json"),
			},
		},
		models.Catalog{},
		pool,
		client,
		nil,
		sqliteStore.DB(),
		sqliteStore,
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	securityService, err := security.NewService(sqliteStore.DB())
	if err != nil {
		t.Fatalf("NewService() security error = %v", err)
	}
	usageService, err := usage.NewService(config.Config{}, sqliteStore.DB(), sqliteStore)
	if err != nil {
		t.Fatalf("NewService() usage error = %v", err)
	}
	requestLogService, err := requestlog.NewService(sqliteStore.DB())
	if err != nil {
		t.Fatalf("NewService() requestlog error = %v", err)
	}

	server := &Server{
		cfg: config.Config{
			Server: config.ServerConfig{
				ProxyAPIKey: "test-key",
			},
			Model: config.ModelConfig{
				DefaultModel: "deepseek-chat",
			},
		},
		engine:     gin.New(),
		state:      sqliteStore,
		accounts:   pool,
		codex:      client,
		models:     modelService,
		security:   securityService,
		usage:      usageService,
		requestLog: requestLogService,
		affinity:   affinity.NewStore(),
	}
	server.engine.Use(gin.Recovery())
	server.engine.POST("/v1/chat/completions", server.requireAPIKey(), server.handleChatCompletions)

	originalErrorWriter := gin.DefaultErrorWriter
	var logBuffer bytes.Buffer
	gin.DefaultErrorWriter = &logBuffer
	defer func() {
		gin.DefaultErrorWriter = originalErrorWriter
	}()

	body := `{"model":"deepseek-chat","messages":[{"role":"user","content":"hello"}],"stream":true}`
	recorder := &closeNotifyRecorder{ResponseRecorder: httptest.NewRecorder()}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("User-Agent", "Cursor/1.0")
	server.engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "hello") {
		t.Fatalf("expected partial streamed output, got %q", recorder.Body.String())
	}

	var success int
	var outputTokens, inputTokens sql.NullInt64
	var errorMessage string
	if err := sqliteStore.DB().QueryRow(`
		SELECT success, input_tokens, output_tokens, error_message
		FROM request_events
		ORDER BY id DESC
		LIMIT 1
	`).Scan(&success, &inputTokens, &outputTokens, &errorMessage); err != nil {
		t.Fatalf("select request_events: %v", err)
	}
	if success != 0 {
		t.Fatalf("expected failure request event, got success=%d", success)
	}
	if !strings.Contains(errorMessage, "read_error:") {
		t.Fatalf("expected read_error in error message, got %q", errorMessage)
	}
	if !inputTokens.Valid || inputTokens.Int64 != 0 || !outputTokens.Valid || outputTokens.Int64 != 0 {
		t.Fatalf("expected zero usage snapshot on failed request event, got input=%v output=%v", inputTokens, outputTokens)
	}

	var usageCount int
	if err := sqliteStore.DB().QueryRow(`SELECT COUNT(1) FROM usage_events`).Scan(&usageCount); err != nil {
		t.Fatalf("count usage_events: %v", err)
	}
	if usageCount != 0 {
		t.Fatalf("expected no usage events, got %d", usageCount)
	}

	logs := logBuffer.String()
	if !strings.Contains(logs, `response_id="resp_stream_cancel"`) {
		t.Fatalf("expected response id in diagnostics log, got %q", logs)
	}
	if !strings.Contains(logs, `stream_end_reason="read_error:`) {
		t.Fatalf("expected read_error stream end reason in diagnostics log, got %q", logs)
	}
	if !strings.Contains(logs, `error="read_error:`) {
		t.Fatalf("expected diagnostics error field, got %q", logs)
	}
}

func TestHandleResponsesRecordsUpstreamFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server, sqliteStore := newRequestLogTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"deepseek-chat","object":"model","created":1753632000,"owned_by":"deepseek"}]}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"busy"}}`))
	})
	defer sqliteStore.Close()

	body := `{"model":"deepseek-chat","stream":false,"input":[{"role":"user","content":"fail"}]}`
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer proxy-key")
	server.engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("expected status 429, got %d body=%s", recorder.Code, recorder.Body.String())
	}

	var success int
	var statusCode sql.NullInt64
	var errorMessage string
	if err := sqliteStore.DB().QueryRow(`
		SELECT success, status_code, error_message
		FROM request_events
		LIMIT 1
	`).Scan(&success, &statusCode, &errorMessage); err != nil {
		t.Fatalf("select request_events: %v", err)
	}
	if success != 0 || !statusCode.Valid || statusCode.Int64 != 429 {
		t.Fatalf("unexpected failure row success=%d status=%v", success, statusCode)
	}
	if !strings.Contains(errorMessage, "upstream error 429") {
		t.Fatalf("unexpected error message %q", errorMessage)
	}
}

func newRequestLogTestServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) (*Server, *store.SQLiteStore) {
	t.Helper()

	dir := t.TempDir()
	sqliteStore, err := store.NewSQLiteStore(filepath.Join(dir, "state.db"), dir)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}

	pool, err := auth.NewAccountPool(filepath.Join(dir, "accounts.json"), sqliteStore, config.AuthConfig{})
	if err != nil {
		t.Fatalf("NewAccountPool() error = %v", err)
	}
	customAccount, err := pool.AddCustomAccount(auth.CustomAccountInput{
		Label:              "deepseek",
		CustomBaseURL:      "https://example.invalid",
		CustomAPIKey:       "deepseek-key",
		CustomEndpointType: "responses",
		Enabled:            true,
	})
	if err != nil {
		t.Fatalf("AddCustomAccount() error = %v", err)
	}

	upstream := httptest.NewServer(http.HandlerFunc(handler))
	t.Cleanup(upstream.Close)
	customAccount.CustomBaseURL = upstream.URL
	if err := pool.ReplaceAccount(customAccount); err != nil {
		t.Fatalf("ReplaceAccount() error = %v", err)
	}

	now := time.Now().UTC()
	expires := now.Add(time.Hour)
	if _, err := sqliteStore.DB().Exec(`
		INSERT INTO custom_account_models (account_id, model_id, display_name, model_object, owned_by, created_unix, source_payload, fetched_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, customAccount.ID, "deepseek-chat", "DeepSeek Chat", "model", "deepseek", now.Unix(), `{}`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert custom_account_models: %v", err)
	}
	if _, err := sqliteStore.DB().Exec(`
		INSERT INTO custom_account_model_sync_state (account_id, fetched_at, expires_at, last_error, updated_at)
		VALUES (?, ?, ?, '', ?)
	`, customAccount.ID, now.Format(time.RFC3339Nano), expires.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert custom_account_model_sync_state: %v", err)
	}

	client, err := codex.NewClient(config.Config{
		API: config.APIConfig{
			BaseURL: "https://chatgpt.example.test/backend-api",
		},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	modelService, err := models.NewService(
		config.Config{
			Model: config.ModelConfig{
				DefaultModel: "deepseek-chat",
			},
			Storage: config.StorageConfig{
				ModelCacheFile:    filepath.Join(dir, "model-cache.json"),
				ManualModelsFile:  filepath.Join(dir, "manual-models.json"),
				ModelMappingsFile: filepath.Join(dir, "model-mappings.json"),
			},
		},
		models.Catalog{},
		pool,
		client,
		nil,
		sqliteStore.DB(),
		sqliteStore,
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	securityService, err := security.NewService(sqliteStore.DB())
	if err != nil {
		t.Fatalf("NewService() security error = %v", err)
	}
	usageService, err := usage.NewService(config.Config{}, sqliteStore.DB(), sqliteStore)
	if err != nil {
		t.Fatalf("NewService() usage error = %v", err)
	}
	requestLogService, err := requestlog.NewService(sqliteStore.DB())
	if err != nil {
		t.Fatalf("NewService() requestlog error = %v", err)
	}

	server := &Server{
		cfg: config.Config{
			Server: config.ServerConfig{
				ProxyAPIKey: "proxy-key",
			},
			Model: config.ModelConfig{
				DefaultModel: "deepseek-chat",
			},
		},
		engine:     gin.New(),
		state:      sqliteStore,
		accounts:   pool,
		codex:      client,
		models:     modelService,
		security:   securityService,
		usage:      usageService,
		requestLog: requestLogService,
		affinity:   affinity.NewStore(),
	}
	server.engine.Use(gin.Recovery())
	server.engine.POST("/v1/responses", server.requireAPIKey(), server.handleResponses)
	return server, sqliteStore
}

func newTwoAccountRequestLogTestServer(t *testing.T, handlers []http.HandlerFunc) (*Server, *store.SQLiteStore) {
	t.Helper()

	if len(handlers) != 2 {
		t.Fatalf("expected exactly 2 handlers, got %d", len(handlers))
	}

	dir := t.TempDir()
	sqliteStore, err := store.NewSQLiteStore(filepath.Join(dir, "state.db"), dir)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}

	pool, err := auth.NewAccountPool(filepath.Join(dir, "accounts.json"), sqliteStore, config.AuthConfig{})
	if err != nil {
		t.Fatalf("NewAccountPool() error = %v", err)
	}

	client, err := codex.NewClient(config.Config{
		API: config.APIConfig{
			BaseURL: "https://chatgpt.example.test/backend-api",
		},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	var accounts []auth.Account
	for idx, handler := range handlers {
		account, addErr := pool.AddCustomAccount(auth.CustomAccountInput{
			Label:              "deepseek-" + string(rune('a'+idx)),
			CustomBaseURL:      "https://example.invalid",
			CustomAPIKey:       "deepseek-key",
			CustomEndpointType: "responses",
			Enabled:            true,
		})
		if addErr != nil {
			t.Fatalf("AddCustomAccount() error = %v", addErr)
		}
		upstream := httptest.NewServer(handler)
		t.Cleanup(upstream.Close)
		account.CustomBaseURL = upstream.URL
		if err := pool.ReplaceAccount(account); err != nil {
			t.Fatalf("ReplaceAccount() error = %v", err)
		}
		accounts = append(accounts, account)
	}

	now := time.Now().UTC()
	expires := now.Add(time.Hour)
	for _, account := range accounts {
		if _, err := sqliteStore.DB().Exec(`
			INSERT INTO custom_account_models (account_id, model_id, display_name, model_object, owned_by, created_unix, source_payload, fetched_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, account.ID, "deepseek-chat", "DeepSeek Chat", "model", "deepseek", now.Unix(), `{}`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
			t.Fatalf("insert custom_account_models: %v", err)
		}
		if _, err := sqliteStore.DB().Exec(`
			INSERT INTO custom_account_model_sync_state (account_id, fetched_at, expires_at, last_error, updated_at)
			VALUES (?, ?, ?, '', ?)
		`, account.ID, now.Format(time.RFC3339Nano), expires.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
			t.Fatalf("insert custom_account_model_sync_state: %v", err)
		}
	}

	modelService, err := models.NewService(
		config.Config{
			Model: config.ModelConfig{
				DefaultModel: "deepseek-chat",
			},
			Storage: config.StorageConfig{
				ModelCacheFile:    filepath.Join(dir, "model-cache.json"),
				ManualModelsFile:  filepath.Join(dir, "manual-models.json"),
				ModelMappingsFile: filepath.Join(dir, "model-mappings.json"),
			},
		},
		models.Catalog{},
		pool,
		client,
		nil,
		sqliteStore.DB(),
		sqliteStore,
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	securityService, err := security.NewService(sqliteStore.DB())
	if err != nil {
		t.Fatalf("NewService() security error = %v", err)
	}
	usageService, err := usage.NewService(config.Config{}, sqliteStore.DB(), sqliteStore)
	if err != nil {
		t.Fatalf("NewService() usage error = %v", err)
	}
	requestLogService, err := requestlog.NewService(sqliteStore.DB())
	if err != nil {
		t.Fatalf("NewService() requestlog error = %v", err)
	}

	server := &Server{
		cfg: config.Config{
			Server: config.ServerConfig{
				ProxyAPIKey: "proxy-key",
			},
			Model: config.ModelConfig{
				DefaultModel: "deepseek-chat",
			},
		},
		engine:     gin.New(),
		state:      sqliteStore,
		accounts:   pool,
		codex:      client,
		models:     modelService,
		security:   securityService,
		usage:      usageService,
		requestLog: requestLogService,
		affinity:   affinity.NewStore(),
	}
	server.engine.Use(gin.Recovery())
	server.engine.POST("/v1/responses", server.requireAPIKey(), server.handleResponses)
	return server, sqliteStore
}
