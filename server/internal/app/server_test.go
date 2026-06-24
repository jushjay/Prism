package app

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
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

type fakeOAuthService struct {
	startAuthURL string
	startState   string
	startErr     error
	exchangeResp auth.TokenResponse
	exchangeErr  error
	refreshResp  auth.TokenResponse
	refreshErr   error
	refreshCalls int
	lastHost     string
	lastTargetID string
}

func (f *fakeOAuthService) Start(returnHost string, targetAccountID string) (string, string, error) {
	f.lastHost = returnHost
	f.lastTargetID = targetAccountID
	if f.startErr != nil {
		return "", "", f.startErr
	}
	return f.startAuthURL, f.startState, nil
}

func (f *fakeOAuthService) TryAcquire(string) (*auth.Session, bool) {
	return &auth.Session{
		CodeVerifier:    "verifier",
		RedirectURI:     "http://localhost:1455/auth/callback",
		TargetAccountID: f.lastTargetID,
		CreatedAt:       time.Now(),
	}, true
}

func (f *fakeOAuthService) Release(string)          {}
func (f *fakeOAuthService) Complete(string)         {}
func (f *fakeOAuthService) IsCompleted(string) bool { return false }
func (f *fakeOAuthService) ExchangeCode(string, string, string) (auth.TokenResponse, error) {
	if f.exchangeErr != nil {
		return auth.TokenResponse{}, f.exchangeErr
	}
	return f.exchangeResp, nil
}
func (f *fakeOAuthService) Refresh(string) (auth.TokenResponse, error) {
	f.refreshCalls++
	if f.refreshErr != nil {
		return auth.TokenResponse{}, f.refreshErr
	}
	return f.refreshResp, nil
}

func TestCollectReasoningText(t *testing.T) {
	events := []codex.SSEEvent{
		testEvent("response.reasoning_summary_text.delta", map[string]any{"delta": "step "}),
		testEvent("response.reasoning_summary_text.delta", map[string]any{"delta": "by step"}),
		testEvent("response.reasoning_summary_text.done", map[string]any{"text": "ignored"}),
	}
	if got := collectReasoningText(events); got != "step by step" {
		t.Fatalf("unexpected reasoning text %q", got)
	}

	doneOnly := []codex.SSEEvent{
		testEvent("response.reasoning_summary_text.done", map[string]any{"text": "final summary"}),
	}
	if got := collectReasoningText(doneOnly); got != "final summary" {
		t.Fatalf("unexpected fallback reasoning text %q", got)
	}
}

func TestParseOAuthRelayInput(t *testing.T) {
	tests := []struct {
		name  string
		input string
		state string
		code  string
	}{
		{
			name:  "full callback url",
			input: "http://localhost:1455/auth/callback?code=abc&state=xyz",
			state: "xyz",
			code:  "abc",
		},
		{
			name:  "query string only",
			input: "?code=abc&state=xyz",
			state: "xyz",
			code:  "abc",
		},
		{
			name:  "plain text pairs",
			input: "code=abc\nstate=xyz",
			state: "xyz",
			code:  "abc",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			state, code, err := parseOAuthRelayInput(tc.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if state != tc.state || code != tc.code {
				t.Fatalf("unexpected parse result state=%q code=%q", state, code)
			}
		})
	}
}

func TestBuildResponsesPayloadPreservesOutputAndReconvertTuple(t *testing.T) {
	tupleSchema := map[string]any{
		"type": "array",
		"prefixItems": []any{
			map[string]any{"type": "string"},
			map[string]any{"type": "integer"},
		},
	}
	events := []codex.SSEEvent{
		testEvent("response.completed", map[string]any{
			"response": map[string]any{
				"id":    "resp_1",
				"model": "gpt-5.4",
				"usage": map[string]any{
					"input_tokens":  10,
					"output_tokens": 5,
				},
				"output": []any{
					map[string]any{
						"type": "message",
						"content": []any{
							map[string]any{
								"type": "output_text",
								"text": `{"0":"go","1":7}`,
							},
						},
					},
					map[string]any{
						"type":      "function_call",
						"call_id":   "call_1",
						"name":      "lookup",
						"arguments": "{}",
					},
				},
			},
		}),
	}

	payload, usage, responseID, err := buildResponsesPayload(events, "fallback-model", tupleSchema)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if responseID != "resp_1" {
		t.Fatalf("unexpected response id %s", responseID)
	}
	if usage.InputTokens != 10 || usage.OutputTokens != 5 {
		t.Fatalf("unexpected usage %+v", usage)
	}

	output := payload["output"].([]any)
	message := output[0].(map[string]any)
	content := message["content"].([]any)
	part := content[0].(map[string]any)
	if got := part["text"].(string); got != `["go",7]` {
		t.Fatalf("unexpected reconverted text %s", got)
	}
	if got := output[1].(map[string]any)["type"].(string); got != "function_call" {
		t.Fatalf("expected function_call item, got %s", got)
	}
}

func TestBuildResponsesPayloadParsesCompatibleUsageFields(t *testing.T) {
	events := []codex.SSEEvent{
		testEvent("response.completed", map[string]any{
			"response": map[string]any{
				"id":    "resp_compat",
				"model": "glm-5.1",
				"usage": map[string]any{
					"prompt_tokens":     7,
					"completion_tokens": 3,
					"prompt_tokens_details": map[string]any{
						"cached_tokens": 2,
					},
					"completion_tokens_details": map[string]any{
						"reasoning_tokens": 1,
					},
				},
				"output": []any{
					map[string]any{
						"type": "message",
						"content": []any{
							map[string]any{
								"type": "output_text",
								"text": "ok",
							},
						},
					},
				},
			},
		}),
	}

	_, usage, responseID, err := buildResponsesPayload(events, "fallback-model", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if responseID != "resp_compat" {
		t.Fatalf("unexpected response id %s", responseID)
	}
	if usage.InputTokens != 7 || usage.OutputTokens != 3 || usage.CachedTokens != 2 || usage.ReasoningTokens != 1 {
		t.Fatalf("unexpected usage %+v", usage)
	}
}

func TestBuildResponsesPayloadParsesInputTokenDetailsCachedTokens(t *testing.T) {
	events := []codex.SSEEvent{
		testEvent("response.completed", map[string]any{
			"response": map[string]any{
				"id":    "resp_cache_hit",
				"model": "gpt-5.4",
				"usage": map[string]any{
					"input_tokens":  9411,
					"output_tokens": 6,
					"input_tokens_details": map[string]any{
						"cached_tokens": 8960,
					},
					"output_tokens_details": map[string]any{
						"reasoning_tokens": 0,
					},
					"total_tokens": 9417,
				},
				"output": []any{
					map[string]any{
						"type": "message",
						"content": []any{
							map[string]any{
								"type": "output_text",
								"text": "CACHE_OK",
							},
						},
					},
				},
			},
		}),
	}

	payload, usage, _, err := buildResponsesPayload(events, "fallback-model", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if usage.CachedTokens != 8960 || usage.TotalTokens != 9417 {
		t.Fatalf("unexpected usage %+v", usage)
	}
	usagePayload, ok := payload["usage"].(gin.H)
	if !ok {
		t.Fatalf("expected gin.H usage payload, got %#v", payload["usage"])
	}
	inputDetails, ok := usagePayload["input_tokens_details"].(gin.H)
	if !ok || inputDetails["cached_tokens"] != 8960 {
		t.Fatalf("expected input token details cached tokens, got %#v", usagePayload["input_tokens_details"])
	}
	if usagePayload["total_tokens"] != 9417 {
		t.Fatalf("expected total_tokens=9417, got %#v", usagePayload["total_tokens"])
	}
}

func TestApplyUpstreamErrorMarksRateLimited(t *testing.T) {
	resetAt := time.Now().Add(2 * time.Minute).Unix()
	pool, err := auth.NewAccountPool(
		filepath.Join(t.TempDir(), "accounts.json"),
		store.NewJSONStore(),
		config.AuthConfig{},
	)
	if err != nil {
		t.Fatalf("new account pool: %v", err)
	}
	account, err := pool.AddAccount(testJWT(map[string]any{
		"sub":                "user-1",
		"chatgpt_account_id": "acct-1",
		"email":              "acct-1@example.com",
		"exp":                float64(time.Now().Add(time.Hour).Unix()),
	}), "refresh-token")
	if err != nil {
		t.Fatalf("add account: %v", err)
	}
	server := &Server{accounts: pool}
	decision := server.applyUpstreamError(account.ID, &codex.UpstreamError{
		StatusCode: http.StatusTooManyRequests,
		Body:       `{"error":{"resets_in_seconds":120}}`,
		Header: http.Header{
			"x-codex-primary-reset-at": []string{strconv.FormatInt(resetAt, 10)},
		},
	})

	if !decision.Retry || !decision.UseRateLimitType || decision.Status != http.StatusTooManyRequests {
		t.Fatalf("unexpected decision %+v", decision)
	}
	updated, ok := pool.Get(account.ID)
	if !ok {
		t.Fatalf("expected account to exist")
	}
	if updated.Status != auth.StatusRateLimited {
		t.Fatalf("expected rate limited status, got %s", updated.Status)
	}
	if updated.RateLimitedUntil == nil {
		t.Fatalf("expected rate limited until to be set")
	}
}

func TestApplyRateLimitHeadersUpdatesCachedQuotaWindows(t *testing.T) {
	pool, err := auth.NewAccountPool(
		filepath.Join(t.TempDir(), "accounts.json"),
		store.NewJSONStore(),
		config.AuthConfig{},
	)
	if err != nil {
		t.Fatalf("new account pool: %v", err)
	}
	account, err := pool.AddAccount(testJWT(map[string]any{
		"sub":                "user-1",
		"chatgpt_account_id": "acct-1",
		"email":              "acct-1@example.com",
		"exp":                float64(time.Now().Add(time.Hour).Unix()),
	}), "refresh-token")
	if err != nil {
		t.Fatalf("add account: %v", err)
	}
	server := &Server{accounts: pool}
	now := time.Now()
	primaryReset := now.Add(5 * time.Hour).Unix()
	secondaryReset := now.Add(7 * 24 * time.Hour).Unix()

	server.applyRateLimitHeaders(account.ID, http.Header{
		"X-Codex-Primary-Used-Percent":     []string{"42.5"},
		"X-Codex-Primary-Window-Minutes":   []string{"300"},
		"X-Codex-Primary-Reset-At":         []string{strconv.FormatInt(primaryReset, 10)},
		"X-Codex-Secondary-Used-Percent":   []string{"13"},
		"X-Codex-Secondary-Window-Minutes": []string{"10080"},
		"X-Codex-Secondary-Reset-At":       []string{strconv.FormatInt(secondaryReset, 10)},
	})

	updated, ok := pool.Get(account.ID)
	if !ok {
		t.Fatalf("expected account to exist")
	}
	if updated.Quota == nil {
		t.Fatalf("expected cached quota to be populated")
	}
	if updated.QuotaFetchedAt == nil {
		t.Fatalf("expected quota fetched time to be populated")
	}
	if updated.Quota.PrimaryRateLimit.Window.UsedPercent == nil || *updated.Quota.PrimaryRateLimit.Window.UsedPercent != 42.5 {
		t.Fatalf("unexpected primary used percent %+v", updated.Quota.PrimaryRateLimit.Window.UsedPercent)
	}
	if updated.Quota.PrimaryRateLimit.Window.LimitWindowSeconds == nil || *updated.Quota.PrimaryRateLimit.Window.LimitWindowSeconds != 5*60*60 {
		t.Fatalf("unexpected primary window seconds %+v", updated.Quota.PrimaryRateLimit.Window.LimitWindowSeconds)
	}
	if updated.Quota.PrimaryRateLimit.Window.ResetAt == nil || *updated.Quota.PrimaryRateLimit.Window.ResetAt != primaryReset {
		t.Fatalf("unexpected primary reset %+v", updated.Quota.PrimaryRateLimit.Window.ResetAt)
	}
	if updated.Quota.SecondaryRateLimit == nil {
		t.Fatalf("expected secondary quota window")
	}
	if updated.Quota.SecondaryRateLimit.Window.UsedPercent == nil || *updated.Quota.SecondaryRateLimit.Window.UsedPercent != 13 {
		t.Fatalf("unexpected secondary used percent %+v", updated.Quota.SecondaryRateLimit.Window.UsedPercent)
	}
	if updated.Quota.SecondaryRateLimit.Window.LimitWindowSeconds == nil || *updated.Quota.SecondaryRateLimit.Window.LimitWindowSeconds != 7*24*60*60 {
		t.Fatalf("unexpected secondary window seconds %+v", updated.Quota.SecondaryRateLimit.Window.LimitWindowSeconds)
	}
	if updated.Quota.SecondaryRateLimit.Window.ResetAt == nil || *updated.Quota.SecondaryRateLimit.Window.ResetAt != secondaryReset {
		t.Fatalf("unexpected secondary reset %+v", updated.Quota.SecondaryRateLimit.Window.ResetAt)
	}
}

func TestHandleUpdateAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)

	pool, err := auth.NewAccountPool(
		filepath.Join(t.TempDir(), "accounts.json"),
		store.NewJSONStore(),
		config.AuthConfig{},
	)
	if err != nil {
		t.Fatalf("new account pool: %v", err)
	}
	account, err := pool.AddAccount(testJWT(map[string]any{
		"sub":                "user-1",
		"chatgpt_account_id": "acct-1",
		"email":              "acct-1@example.com",
		"exp":                float64(time.Now().Add(time.Hour).Unix()),
	}), "refresh-token")
	if err != nil {
		t.Fatalf("add account: %v", err)
	}

	server := &Server{engine: gin.New(), accounts: pool}
	server.engine.PUT("/auth/accounts/:id", server.handleUpdateAccount)

	body := strings.NewReader(`{
		"label":"Main account",
		"email":"main@example.com",
		"plan_type":"plus",
		"proxy_api_key":"proxy-updated",
		"enabled":false
	}`)
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/auth/accounts/"+account.ID, body)
	req.Header.Set("Content-Type", "application/json")
	server.engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Success bool            `json:"success"`
		Account accountResponse `json:"account"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !payload.Success {
		t.Fatalf("expected success response")
	}
	if payload.Account.Label != "Main account" || payload.Account.Email != "main@example.com" {
		t.Fatalf("unexpected account view: %+v", payload.Account)
	}
	if payload.Account.Enabled {
		t.Fatalf("expected account to be disabled")
	}

	recorder = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/auth/accounts/"+account.ID, strings.NewReader(`{"proxy_api_key":" "}`))
	req.Header.Set("Content-Type", "application/json")
	server.engine.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected empty proxy key to return 400, got %d", recorder.Code)
	}
}

func TestHandleSetAccountEnabledRefreshesOpenAIToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	pool, err := auth.NewAccountPool(
		filepath.Join(t.TempDir(), "accounts.json"),
		store.NewJSONStore(),
		config.AuthConfig{},
	)
	if err != nil {
		t.Fatalf("new account pool: %v", err)
	}
	account, err := pool.AddAccount(testJWT(map[string]any{
		"sub":                "user-1",
		"chatgpt_account_id": "acct-1",
		"email":              "acct-1@example.com",
		"exp":                float64(time.Now().Add(-time.Hour).Unix()),
	}), "refresh-token")
	if err != nil {
		t.Fatalf("add account: %v", err)
	}
	if err := pool.SetEnabled(account.ID, false); err != nil {
		t.Fatalf("disable account: %v", err)
	}

	refreshedToken := testJWT(map[string]any{
		"sub":                "user-1",
		"chatgpt_account_id": "acct-1",
		"email":              "acct-1@example.com",
		"exp":                float64(time.Now().Add(2 * time.Hour).Unix()),
	})
	oauth := &fakeOAuthService{
		refreshResp: auth.TokenResponse{
			AccessToken:  refreshedToken,
			RefreshToken: "refresh-new",
			ExpiresIn:    3600,
		},
	}
	server := &Server{engine: gin.New(), accounts: pool, oauth: oauth}
	server.engine.POST("/auth/accounts/:id/enabled", server.handleSetAccountEnabled)

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/accounts/"+account.ID+"/enabled", strings.NewReader(`{"enabled":true}`))
	req.Header.Set("Content-Type", "application/json")
	server.engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	if oauth.refreshCalls != 1 {
		t.Fatalf("expected one refresh call, got %d", oauth.refreshCalls)
	}
	updated, ok := pool.Get(account.ID)
	if !ok {
		t.Fatalf("expected account to exist")
	}
	if updated.DisabledByUser {
		t.Fatalf("expected account to be enabled")
	}
	if updated.RefreshToken != "refresh-new" || updated.AccessToken != refreshedToken {
		t.Fatalf("expected tokens to refresh, got %+v", updated)
	}
	if updated.LastRefreshAt == nil {
		t.Fatalf("expected last refresh timestamp to be set")
	}
}

func TestHandleSetAccountEnabledKeepsDisabledWhenRefreshFails(t *testing.T) {
	gin.SetMode(gin.TestMode)

	pool, err := auth.NewAccountPool(
		filepath.Join(t.TempDir(), "accounts.json"),
		store.NewJSONStore(),
		config.AuthConfig{},
	)
	if err != nil {
		t.Fatalf("new account pool: %v", err)
	}
	account, err := pool.AddAccount(testJWT(map[string]any{
		"sub":                "user-1",
		"chatgpt_account_id": "acct-1",
		"email":              "acct-1@example.com",
		"exp":                float64(time.Now().Add(-time.Hour).Unix()),
	}), "refresh-token")
	if err != nil {
		t.Fatalf("add account: %v", err)
	}
	if err := pool.SetEnabled(account.ID, false); err != nil {
		t.Fatalf("disable account: %v", err)
	}

	oauth := &fakeOAuthService{refreshErr: errors.New("refresh failed")}
	server := &Server{engine: gin.New(), accounts: pool, oauth: oauth}
	server.engine.POST("/auth/accounts/:id/enabled", server.handleSetAccountEnabled)

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/accounts/"+account.ID+"/enabled", strings.NewReader(`{"enabled":true}`))
	req.Header.Set("Content-Type", "application/json")
	server.engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("expected status 502, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	updated, ok := pool.Get(account.ID)
	if !ok {
		t.Fatalf("expected account to exist")
	}
	if !updated.DisabledByUser {
		t.Fatalf("expected account to remain disabled")
	}
	if updated.Status != auth.StatusExpired {
		t.Fatalf("expected account status expired, got %s", updated.Status)
	}
}

func TestHandleResetAccountStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)

	pool, err := auth.NewAccountPool(
		filepath.Join(t.TempDir(), "accounts.json"),
		store.NewJSONStore(),
		config.AuthConfig{},
	)
	if err != nil {
		t.Fatalf("new account pool: %v", err)
	}
	account, err := pool.AddCustomAccount(auth.CustomAccountInput{
		Label:              "fflycode",
		CustomBaseURL:      "https://example.com",
		CustomAPIKey:       "secret",
		CustomEndpointType: "/v1/chat/completions",
		Enabled:            true,
	})
	if err != nil {
		t.Fatalf("add custom account: %v", err)
	}
	if err := pool.UpdateStatus(account.ID, auth.StatusBanned); err != nil {
		t.Fatalf("mark banned: %v", err)
	}

	server := &Server{engine: gin.New(), accounts: pool}
	server.engine.POST("/auth/accounts/:id/reset-status", server.handleResetAccountStatus)

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/accounts/"+account.ID+"/reset-status", nil)
	server.engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	updated, ok := pool.Get(account.ID)
	if !ok {
		t.Fatalf("expected account to exist")
	}
	if updated.Status != auth.StatusActive {
		t.Fatalf("expected account status active, got %s", updated.Status)
	}
}

func TestHandleLoginStartWithTargetAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)

	pool, err := auth.NewAccountPool(
		filepath.Join(t.TempDir(), "accounts.json"),
		store.NewJSONStore(),
		config.AuthConfig{},
	)
	if err != nil {
		t.Fatalf("new account pool: %v", err)
	}
	account, err := pool.AddAccount(testJWT(map[string]any{
		"sub":                "user-1",
		"chatgpt_account_id": "acct-1",
		"email":              "acct-1@example.com",
		"exp":                float64(time.Now().Add(time.Hour).Unix()),
	}), "refresh-token")
	if err != nil {
		t.Fatalf("add account: %v", err)
	}

	oauth := &fakeOAuthService{
		startAuthURL: "https://auth.openai.test/authorize",
		startState:   "state-123",
	}
	server := &Server{
		engine:   gin.New(),
		accounts: pool,
		oauth:    oauth,
		cfg:      config.Config{Server: config.ServerConfig{Port: 3000}},
	}
	server.engine.POST("/auth/login-start", server.handleLoginStart)

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/login-start", strings.NewReader(`{"accountId":"`+account.ID+`"}`))
	req.Host = "localhost:3000"
	req.Header.Set("Content-Type", "application/json")
	server.engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		AuthURL   string `json:"authUrl"`
		State     string `json:"state"`
		AccountID string `json:"accountId"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.AuthURL != oauth.startAuthURL || payload.State != oauth.startState || payload.AccountID != account.ID {
		t.Fatalf("unexpected payload %+v", payload)
	}
	if oauth.lastTargetID != account.ID {
		t.Fatalf("expected oauth start target account %q, got %q", account.ID, oauth.lastTargetID)
	}
}

func TestHandleCodeRelayReauthenticatesExistingAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)

	pool, err := auth.NewAccountPool(
		filepath.Join(t.TempDir(), "accounts.json"),
		store.NewJSONStore(),
		config.AuthConfig{},
	)
	if err != nil {
		t.Fatalf("new account pool: %v", err)
	}
	account, err := pool.AddAccount(testJWT(map[string]any{
		"sub":                "user-old",
		"chatgpt_account_id": "acct-old",
		"email":              "old@example.com",
		"exp":                float64(time.Now().Add(20 * time.Minute).Unix()),
	}), "refresh-old")
	if err != nil {
		t.Fatalf("add account: %v", err)
	}

	oauth := &fakeOAuthService{
		lastTargetID: account.ID,
		exchangeResp: auth.TokenResponse{
			AccessToken: testJWT(map[string]any{
				"sub":                "user-new",
				"chatgpt_account_id": "acct-new",
				"email":              "new@example.com",
				"exp":                float64(time.Now().Add(2 * time.Hour).Unix()),
			}),
			RefreshToken: "refresh-new",
		},
	}
	server := &Server{engine: gin.New(), accounts: pool, oauth: oauth}
	server.engine.POST("/auth/code-relay", server.handleCodeRelay)

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/code-relay", strings.NewReader(`{"callbackUrl":"http://localhost:1455/auth/callback?code=abc&state=xyz"}`))
	req.Header.Set("Content-Type", "application/json")
	server.engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	updated, ok := pool.Get(account.ID)
	if !ok {
		t.Fatalf("expected account to exist")
	}
	if updated.Email != "new@example.com" || updated.AccountID != "acct-new" {
		t.Fatalf("expected refreshed identity, got %+v", updated)
	}
	if updated.RefreshToken != "refresh-new" {
		t.Fatalf("expected refresh token to update, got %q", updated.RefreshToken)
	}
	if len(pool.List()) != 1 {
		t.Fatalf("expected reauthentication to keep single account record")
	}
}

func TestHandleCustomAccountCreateAndUpdatePreservesSecret(t *testing.T) {
	gin.SetMode(gin.TestMode)

	pool, err := auth.NewAccountPool(
		filepath.Join(t.TempDir(), "accounts.json"),
		store.NewJSONStore(),
		config.AuthConfig{},
	)
	if err != nil {
		t.Fatalf("new account pool: %v", err)
	}
	server := &Server{engine: gin.New(), accounts: pool}
	server.engine.POST("/auth/accounts/custom", server.handleCustomAccountCreate)
	server.engine.PUT("/auth/accounts/:id", server.handleUpdateAccount)

	body := strings.NewReader(`{
		"label":"Custom",
		"custom_base_url":"https://api.example.com",
		"custom_api_key":"secret",
		"custom_endpoint_type":"v1/chat",
		"custom_user_agent":"Custom-UA/1.0"
	}`)
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/accounts/custom", body)
	req.Header.Set("Content-Type", "application/json")
	server.engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	var createPayload struct {
		Account accountResponse `json:"account"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &createPayload); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if createPayload.Account.Provider != auth.ProviderCustom {
		t.Fatalf("expected custom provider, got %q", createPayload.Account.Provider)
	}
	if !createPayload.Account.CustomAPIKeySet {
		t.Fatalf("expected api key set flag")
	}
	if createPayload.Account.CustomEndpointType != "/v1/chat/completions" {
		t.Fatalf("expected endpoint path /v1/chat/completions, got %q", createPayload.Account.CustomEndpointType)
	}
	if createPayload.Account.CustomUserAgent != "Custom-UA/1.0" {
		t.Fatalf("expected custom user agent to round-trip, got %q", createPayload.Account.CustomUserAgent)
	}

	recorder = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/auth/accounts/"+createPayload.Account.ID, strings.NewReader(`{
		"provider":"custom",
		"custom_base_url":"https://api.example.com",
		"custom_api_key":"",
		"custom_endpoint_type":"responses",
		"custom_user_agent":"Custom-UA/2.0"
	}`))
	req.Header.Set("Content-Type", "application/json")
	server.engine.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected update status 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	updated, ok := pool.Get(createPayload.Account.ID)
	if !ok {
		t.Fatalf("expected custom account to exist")
	}
	if updated.CustomAPIKey != "secret" {
		t.Fatalf("expected empty update key to preserve secret")
	}
	if updated.CustomEndpointType != "/v1/responses" {
		t.Fatalf("expected endpoint path to update, got %q", updated.CustomEndpointType)
	}
	if updated.CustomUserAgent != "Custom-UA/2.0" {
		t.Fatalf("expected user agent to update, got %q", updated.CustomUserAgent)
	}
}

func TestHandleResponsesRoutesByPersistedAccountModels(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dir := t.TempDir()
	sqliteStore, err := store.NewSQLiteStore(filepath.Join(dir, "state.db"), dir)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer sqliteStore.Close()

	pool, err := auth.NewAccountPool(
		filepath.Join(dir, "accounts.json"),
		sqliteStore,
		config.AuthConfig{},
	)
	if err != nil {
		t.Fatalf("NewAccountPool() error = %v", err)
	}
	openAIAccount, err := pool.AddAccount(testJWT(map[string]any{
		"sub":                "user-openai",
		"chatgpt_account_id": "acct-openai",
		"email":              "openai@example.com",
		"exp":                float64(time.Now().Add(time.Hour).Unix()),
	}), "refresh-token")
	if err != nil {
		t.Fatalf("AddAccount() error = %v", err)
	}
	fflycodeAccount, err := pool.AddCustomAccount(auth.CustomAccountInput{
		Label:              "fflycode",
		CustomBaseURL:      "https://fflycode.example.com",
		CustomAPIKey:       "fflycode-key",
		CustomEndpointType: "responses",
		Enabled:            true,
	})
	if err != nil {
		t.Fatalf("AddCustomAccount() error = %v", err)
	}

	customHits := 0
	customUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"glm-5.1","object":"model","created":1753632000,"owned_by":"z-ai"}]}`))
			return
		}
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("unexpected custom upstream path %q", r.URL.Path)
		}
		customHits++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte(`data: {"response":{"id":"resp_custom","model":"glm-5.1","usage":{"input_tokens":1,"output_tokens":2},"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}}` + "\n\n"))
	}))
	defer customUpstream.Close()

	fflycodeAccount.CustomBaseURL = customUpstream.URL
	if err := pool.ReplaceAccount(fflycodeAccount); err != nil {
		t.Fatalf("ReplaceAccount() error = %v", err)
	}

	now := time.Now().UTC()
	expires := now.Add(time.Hour)
	if _, err := sqliteStore.DB().Exec(`
		INSERT INTO openai_account_models (account_id, model_id, display_name, model_object, owned_by, created_unix, source_payload, fetched_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, openAIAccount.ID, "gpt-5.4", "GPT-5.4", "model", "openai", now.Unix(), `{}`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert openai_account_models: %v", err)
	}
	if _, err := sqliteStore.DB().Exec(`
		INSERT INTO openai_account_model_sync_state (account_id, fetched_at, expires_at, last_error, updated_at)
		VALUES (?, ?, ?, '', ?)
	`, openAIAccount.ID, now.Format(time.RFC3339Nano), expires.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert openai_account_model_sync_state: %v", err)
	}
	if _, err := sqliteStore.DB().Exec(`
		INSERT INTO custom_account_models (account_id, model_id, display_name, model_object, owned_by, created_unix, source_payload, fetched_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, fflycodeAccount.ID, "glm-5.1", "GLM-5.1", "model", "z-ai", now.Unix(), `{}`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert custom_account_models: %v", err)
	}
	if _, err := sqliteStore.DB().Exec(`
		INSERT INTO custom_account_model_sync_state (account_id, fetched_at, expires_at, last_error, updated_at)
		VALUES (?, ?, ?, '', ?)
	`, fflycodeAccount.ID, now.Format(time.RFC3339Nano), expires.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
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
				DefaultModel: "gpt-5.4",
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
				DefaultModel: "gpt-5.4",
			},
		},
		engine:     gin.New(),
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

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"glm-5.1","stream":false,"input":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer proxy-key")
	server.engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	if customHits != 1 {
		t.Fatalf("expected request to route to custom account exactly once, got %d", customHits)
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got, _ := payload["model"].(string); got != "glm-5.1" {
		t.Fatalf("expected response model glm-5.1, got %q", got)
	}
}

func TestHandleChatCompletionsPreservesPreviousResponseIDForCustomResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dir := t.TempDir()
	sqliteStore, err := store.NewSQLiteStore(filepath.Join(dir, "state.db"), dir)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer sqliteStore.Close()

	pool, err := auth.NewAccountPool(
		filepath.Join(dir, "accounts.json"),
		sqliteStore,
		config.AuthConfig{},
	)
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

	var upstreamPreviousResponseIDs []string
	customUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"deepseek-chat","object":"model","created":1753632000,"owned_by":"deepseek"}]}`))
			return
		case "/v1/responses":
			var payload codex.ResponsesRequest
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode custom request: %v", err)
			}
			upstreamPreviousResponseIDs = append(upstreamPreviousResponseIDs, payload.PreviousResponseID)
			w.Header().Set("Content-Type", "text/event-stream")
			responseID := "resp_first"
			text := "first"
			if payload.PreviousResponseID != "" {
				responseID = "resp_second"
				text = "second"
			}
			_, _ = w.Write([]byte("event: response.completed\n"))
			_, _ = w.Write([]byte(`data: {"response":{"id":"` + responseID + `","model":"deepseek-chat","usage":{"input_tokens":1,"output_tokens":1},"output":[{"type":"message","content":[{"type":"output_text","text":"` + text + `"}]}]}}` + "\n\n"))
			return
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

	firstBody := `{"model":"deepseek-chat","messages":[{"role":"user","content":"hello"}],"stream":false}`
	firstRecorder := httptest.NewRecorder()
	firstReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(firstBody))
	firstReq.Header.Set("Content-Type", "application/json")
	firstReq.Header.Set("Authorization", "Bearer test-key")
	firstReq.Header.Set("User-Agent", "Cursor/1.0")
	server.engine.ServeHTTP(firstRecorder, firstReq)
	if firstRecorder.Code != http.StatusOK {
		t.Fatalf("expected first status 200, got %d body=%s", firstRecorder.Code, firstRecorder.Body.String())
	}

	secondBody := `{
		"model":"deepseek-chat",
		"input":[{"role":"user","content":"continue"}],
		"previous_response_id":"resp_first",
		"stream":false,
		"tools":[{"type":"function","name":"Shell","description":"Exec shell","parameters":{"type":"object","properties":{"command":{"type":"string"}}}}]
	}`
	secondRecorder := httptest.NewRecorder()
	secondReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(secondBody))
	secondReq.Header.Set("Content-Type", "application/json")
	secondReq.Header.Set("Authorization", "Bearer test-key")
	secondReq.Header.Set("User-Agent", "Cursor/1.0")
	server.engine.ServeHTTP(secondRecorder, secondReq)
	if secondRecorder.Code != http.StatusOK {
		t.Fatalf("expected second status 200, got %d body=%s", secondRecorder.Code, secondRecorder.Body.String())
	}

	if len(upstreamPreviousResponseIDs) != 2 {
		t.Fatalf("expected 2 upstream requests, got %d", len(upstreamPreviousResponseIDs))
	}
	if upstreamPreviousResponseIDs[0] != "" {
		t.Fatalf("expected first upstream request to have empty previous_response_id, got %q", upstreamPreviousResponseIDs[0])
	}
	if upstreamPreviousResponseIDs[1] != "resp_first" {
		t.Fatalf("expected second upstream request to preserve previous_response_id, got %q", upstreamPreviousResponseIDs[1])
	}
}

func TestHandleChatCompletionsAppliesMappedReasoningEffortWhenRequestDoesNotSpecifyOne(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dir := t.TempDir()
	sqliteStore, err := store.NewSQLiteStore(filepath.Join(dir, "state.db"), dir)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer sqliteStore.Close()

	pool, err := auth.NewAccountPool(
		filepath.Join(dir, "accounts.json"),
		sqliteStore,
		config.AuthConfig{},
	)
	if err != nil {
		t.Fatalf("NewAccountPool() error = %v", err)
	}
	account, err := pool.AddCustomAccount(auth.CustomAccountInput{
		Label:              "reasoning",
		CustomBaseURL:      "https://example.invalid",
		CustomAPIKey:       "key",
		CustomEndpointType: "responses",
		Enabled:            true,
	})
	if err != nil {
		t.Fatalf("AddCustomAccount() error = %v", err)
	}

	var upstreamRequests []codex.ResponsesRequest
	customUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			var payload codex.ResponsesRequest
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode custom request: %v", err)
			}
			upstreamRequests = append(upstreamRequests, payload)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: response.output_text.delta\n"))
			_, _ = w.Write([]byte(`data: {"delta":"ok"}` + "\n\n"))
			_, _ = w.Write([]byte("event: response.completed\n"))
			_, _ = w.Write([]byte(`data: {"response":{"id":"resp_1","model":"gpt-5.4","usage":{"input_tokens":1,"output_tokens":1},"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}}` + "\n\n"))
		default:
			t.Fatalf("unexpected custom upstream path %q", r.URL.Path)
		}
	}))
	defer customUpstream.Close()

	account.CustomBaseURL = customUpstream.URL
	if err := pool.ReplaceAccount(account); err != nil {
		t.Fatalf("ReplaceAccount() error = %v", err)
	}

	now := time.Now().UTC()
	expires := now.Add(time.Hour)
	if _, err := sqliteStore.DB().Exec(`
		INSERT INTO custom_account_models (account_id, model_id, display_name, model_object, owned_by, created_unix, source_payload, fetched_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, account.ID, "gpt-5.4", "GPT-5.4", "model", "openai", now.Unix(), `{}`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert custom_account_models: %v", err)
	}
	if _, err := sqliteStore.DB().Exec(`
		INSERT INTO custom_account_model_sync_state (account_id, fetched_at, expires_at, last_error, updated_at)
		VALUES (?, ?, ?, '', ?)
	`, account.ID, now.Format(time.RFC3339Nano), expires.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
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
				DefaultModel: "mapped-chat",
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
	if _, err := modelService.UpsertModelMapping(models.ModelMappingInput{
		ModelName:       "mapped-chat",
		TargetModel:     "gpt-5.4",
		ReasoningEffort: "high",
		ApplyGlobal:     true,
	}); err != nil {
		t.Fatalf("UpsertModelMapping() error = %v", err)
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
				DefaultModel:           "mapped-chat",
				DefaultReasoningEffort: "medium",
			},
		},
		engine:     gin.New(),
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

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"mapped-chat","messages":[{"role":"user","content":"hi"}],"stream":false}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer proxy-key")
	server.engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(upstreamRequests) != 1 {
		t.Fatalf("expected 1 upstream request, got %d", len(upstreamRequests))
	}
	if upstreamRequests[0].Model != "gpt-5.4" {
		t.Fatalf("expected mapped upstream model gpt-5.4, got %q", upstreamRequests[0].Model)
	}
	if upstreamRequests[0].Reasoning == nil || upstreamRequests[0].Reasoning.Effort != "high" {
		t.Fatalf("expected mapped reasoning effort high, got %+v", upstreamRequests[0].Reasoning)
	}
}

func TestHandleChatCompletionsDoesNotOverrideExplicitReasoningEffort(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dir := t.TempDir()
	sqliteStore, err := store.NewSQLiteStore(filepath.Join(dir, "state.db"), dir)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer sqliteStore.Close()

	pool, err := auth.NewAccountPool(
		filepath.Join(dir, "accounts.json"),
		sqliteStore,
		config.AuthConfig{},
	)
	if err != nil {
		t.Fatalf("NewAccountPool() error = %v", err)
	}
	account, err := pool.AddCustomAccount(auth.CustomAccountInput{
		Label:              "reasoning-explicit",
		CustomBaseURL:      "https://example.invalid",
		CustomAPIKey:       "key",
		CustomEndpointType: "responses",
		Enabled:            true,
	})
	if err != nil {
		t.Fatalf("AddCustomAccount() error = %v", err)
	}

	var upstreamRequest codex.ResponsesRequest
	customUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("unexpected custom upstream path %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&upstreamRequest); err != nil {
			t.Fatalf("decode custom request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.output_text.delta\n"))
		_, _ = w.Write([]byte(`data: {"delta":"ok"}` + "\n\n"))
		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte(`data: {"response":{"id":"resp_1","model":"gpt-5.4","usage":{"input_tokens":1,"output_tokens":1},"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}}` + "\n\n"))
	}))
	defer customUpstream.Close()

	account.CustomBaseURL = customUpstream.URL
	if err := pool.ReplaceAccount(account); err != nil {
		t.Fatalf("ReplaceAccount() error = %v", err)
	}

	now := time.Now().UTC()
	expires := now.Add(time.Hour)
	if _, err := sqliteStore.DB().Exec(`
		INSERT INTO custom_account_models (account_id, model_id, display_name, model_object, owned_by, created_unix, source_payload, fetched_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, account.ID, "gpt-5.4", "GPT-5.4", "model", "openai", now.Unix(), `{}`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert custom_account_models: %v", err)
	}
	if _, err := sqliteStore.DB().Exec(`
		INSERT INTO custom_account_model_sync_state (account_id, fetched_at, expires_at, last_error, updated_at)
		VALUES (?, ?, ?, '', ?)
	`, account.ID, now.Format(time.RFC3339Nano), expires.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
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
				DefaultModel: "mapped-chat",
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
	if _, err := modelService.UpsertModelMapping(models.ModelMappingInput{
		ModelName:       "mapped-chat",
		TargetModel:     "gpt-5.4",
		ReasoningEffort: "high",
		ApplyGlobal:     true,
	}); err != nil {
		t.Fatalf("UpsertModelMapping() error = %v", err)
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
				DefaultModel:           "mapped-chat",
				DefaultReasoningEffort: "medium",
			},
		},
		engine:     gin.New(),
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

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"mapped-chat","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"low","stream":false}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer proxy-key")
	server.engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	if upstreamRequest.Reasoning == nil || upstreamRequest.Reasoning.Effort != "low" {
		t.Fatalf("expected explicit reasoning effort low to win, got %+v", upstreamRequest.Reasoning)
	}
}

func TestHandleResponsesReturnsModelNotFoundWhenNoSyncedAccountSupportsModel(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dir := t.TempDir()
	sqliteStore, err := store.NewSQLiteStore(filepath.Join(dir, "state.db"), dir)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer sqliteStore.Close()

	pool, err := auth.NewAccountPool(
		filepath.Join(dir, "accounts.json"),
		sqliteStore,
		config.AuthConfig{},
	)
	if err != nil {
		t.Fatalf("NewAccountPool() error = %v", err)
	}
	openAIAccount, err := pool.AddAccount(testJWT(map[string]any{
		"sub":                "user-openai",
		"chatgpt_account_id": "acct-openai",
		"email":              "openai@example.com",
		"exp":                float64(time.Now().Add(time.Hour).Unix()),
	}), "refresh-token")
	if err != nil {
		t.Fatalf("AddAccount() error = %v", err)
	}

	now := time.Now().UTC()
	expires := now.Add(time.Hour)
	if _, err := sqliteStore.DB().Exec(`
		INSERT INTO openai_account_models (account_id, model_id, display_name, model_object, owned_by, created_unix, source_payload, fetched_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, openAIAccount.ID, "gpt-5.4", "GPT-5.4", "model", "openai", now.Unix(), `{}`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert openai_account_models: %v", err)
	}
	if _, err := sqliteStore.DB().Exec(`
		INSERT INTO openai_account_model_sync_state (account_id, fetched_at, expires_at, last_error, updated_at)
		VALUES (?, ?, ?, '', ?)
	`, openAIAccount.ID, now.Format(time.RFC3339Nano), expires.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert openai_account_model_sync_state: %v", err)
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
				DefaultModel: "gpt-5.4",
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
				DefaultModel: "gpt-5.4",
			},
		},
		engine:     gin.New(),
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

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"glm-5.1","stream":false,"input":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer proxy-key")
	server.engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	errPayload, _ := payload["error"].(map[string]any)
	if errPayload["type"] != "invalid_request_error" {
		t.Fatalf("expected invalid_request_error, got %#v", errPayload["type"])
	}
	if errPayload["param"] != "model" {
		t.Fatalf("expected param model, got %#v", errPayload["param"])
	}
	if errPayload["code"] != "model_not_found" {
		t.Fatalf("expected code model_not_found, got %#v", errPayload["code"])
	}
}

func TestChatStreamStateEmitsReasoningChunk(t *testing.T) {
	state := newChatStreamState("gpt-5.4", nil)
	chunks := state.consume(testEvent("response.reasoning_summary_text.delta", map[string]any{
		"delta": "thinking",
	}))
	if len(chunks) != 1 {
		t.Fatalf("expected one reasoning chunk, got %d", len(chunks))
	}
	chunk := chunks[0].(gin.H)
	choices := chunk["choices"].([]any)
	choice := choices[0].(gin.H)
	delta := choice["delta"].(gin.H)
	if delta["reasoning_content"] != "thinking" {
		t.Fatalf("unexpected reasoning delta %#v", delta)
	}
}

func TestWriteSSEComment(t *testing.T) {
	var builder strings.Builder
	writeSSEComment(&builder, "keepalive")
	if builder.String() != ": keepalive\n\n" {
		t.Fatalf("unexpected SSE comment %q", builder.String())
	}
}

func TestStartUpstreamSSEReader(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("event: keepalive\ndata: {}\n\n"))
	stop := make(chan struct{})
	ch := startUpstreamSSEReader(reader, stop)
	result, ok := <-ch
	if !ok {
		t.Fatal("expected one result")
	}
	if result.err != nil || result.done {
		t.Fatalf("unexpected reader result %#v", result)
	}
	if result.event.Event != "keepalive" {
		t.Fatalf("unexpected event %#v", result.event)
	}
}

func TestChatStreamStateEmitsUsageChunkOnCompleted(t *testing.T) {
	state := newChatStreamState("gpt-5.4", nil)
	chunks := state.consume(testEvent("response.completed", map[string]any{
		"response": map[string]any{
			"id":    "resp_1",
			"model": "gpt-5.4",
			"usage": map[string]any{
				"input_tokens":     10,
				"output_tokens":    3,
				"cached_tokens":    7,
				"reasoning_tokens": 2,
			},
		},
	}))

	if len(chunks) != 2 {
		t.Fatalf("expected finish and usage chunks, got %d", len(chunks))
	}
	finishChunk := chunks[0].(gin.H)
	finishChoice := finishChunk["choices"].([]any)[0].(gin.H)
	if finishChoice["finish_reason"] != "stop" {
		t.Fatalf("expected stop finish reason, got %#v", finishChoice["finish_reason"])
	}
	usageChunk := chunks[1].(gin.H)
	if choices := usageChunk["choices"].([]any); len(choices) != 0 {
		t.Fatalf("expected empty choices in usage chunk, got %#v", choices)
	}
	usagePayload := usageChunk["usage"].(gin.H)
	if usagePayload["prompt_tokens"] != 10 || usagePayload["completion_tokens"] != 3 || usagePayload["total_tokens"] != 13 {
		t.Fatalf("unexpected usage payload %#v", usagePayload)
	}
	promptDetails := usagePayload["prompt_tokens_details"].(gin.H)
	if promptDetails["cached_tokens"] != 7 {
		t.Fatalf("expected cached tokens 7, got %#v", promptDetails)
	}
	completionDetails := usagePayload["completion_tokens_details"].(gin.H)
	if completionDetails["reasoning_tokens"] != 2 {
		t.Fatalf("expected reasoning tokens 2, got %#v", completionDetails)
	}
}

func TestChatStreamStateMapsCustomToolItemIDToCallID(t *testing.T) {
	state := newChatStreamState("gpt-5.4", nil)

	startChunks := state.consume(testEvent("response.output_item.added", map[string]any{
		"output_index": 2,
		"item": map[string]any{
			"type": "custom_tool_call",
			"id":   "ctc_1",
			"name": "run_terminal_cmd",
		},
	}))
	if len(startChunks) != 1 {
		t.Fatalf("expected one start chunk, got %d", len(startChunks))
	}
	startDelta := startChunks[0].(gin.H)["choices"].([]any)[0].(gin.H)["delta"].(gin.H)
	toolCall := startDelta["tool_calls"].([]any)[0].(gin.H)
	if toolCall["id"] != "ctc_1" {
		t.Fatalf("expected tool call id ctc_1, got %#v", toolCall["id"])
	}

	deltaChunks := state.consume(testEvent("response.custom_tool_call_input.delta", map[string]any{
		"item_id":      "ctc_1",
		"delta":        "ls",
		"output_index": 2,
	}))
	if len(deltaChunks) != 1 {
		t.Fatalf("expected one delta chunk, got %d", len(deltaChunks))
	}
	deltaToolCall := deltaChunks[0].(gin.H)["choices"].([]any)[0].(gin.H)["delta"].(gin.H)["tool_calls"].([]any)[0].(gin.H)
	if deltaToolCall["index"] != 0 {
		t.Fatalf("expected tool call index 0, got %#v", deltaToolCall["index"])
	}
}

func TestCollectToolCallsMapsCustomToolItemIDToCallID(t *testing.T) {
	events := []codex.SSEEvent{
		testEvent("response.output_item.added", map[string]any{
			"output_index": 2,
			"item": map[string]any{
				"type": "custom_tool_call",
				"id":   "ctc_1",
				"name": "run_terminal_cmd",
			},
		}),
		testEvent("response.custom_tool_call_input.delta", map[string]any{
			"item_id":      "ctc_1",
			"delta":        "ls",
			"output_index": 2,
		}),
		testEvent("response.custom_tool_call_input.done", map[string]any{
			"item_id": "ctc_1",
			"input":   "ls -la",
		}),
	}

	toolCalls := collectToolCalls(events)
	if len(toolCalls) != 1 {
		t.Fatalf("expected one tool call, got %d", len(toolCalls))
	}
	if toolCalls[0]["id"] != "ctc_1" {
		t.Fatalf("expected tool call id ctc_1, got %#v", toolCalls[0]["id"])
	}
	function := toolCalls[0]["function"].(gin.H)
	if function["name"] != "run_terminal_cmd" {
		t.Fatalf("expected tool call name run_terminal_cmd, got %#v", function["name"])
	}
	if function["arguments"] != "ls -la" {
		t.Fatalf("expected tool call arguments ls -la, got %#v", function["arguments"])
	}
}

func TestBuildAccessLogLine(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses?stream=true", strings.NewReader(`{"input":"hi"}`))
	req.RemoteAddr = "127.0.0.1:45678"
	req.Host = "api.example.test"
	req.Header.Set("X-Real-IP", "183.192.84.113")
	req.Header.Set("X-Forwarded-For", "183.192.84.113, 127.0.0.1")
	req.Header.Set("Forwarded", `for=183.192.84.113;proto=https`)
	req.Header.Set("Referer", "https://console.example.test")
	req.Header.Set("User-Agent", "vibe-test/1.0")
	c.Request = req
	c.Status(http.StatusForbidden)
	c.Set(contextKeyIPGuardAction, "deny")
	c.Set(contextKeyIPGuardReason, "whitelist")

	line := buildAccessLogLine(c, time.Now().Add(-15*time.Millisecond))

	for _, fragment := range []string{
		`remote_addr="127.0.0.1"`,
		`remote_addr_raw="127.0.0.1:45678"`,
		`client_ip="183.192.84.113"`,
		`client_ip_source="x_real_ip"`,
		`trust_forwarded=true`,
		`method="POST"`,
		`uri="/v1/responses?stream=true"`,
		`status=403`,
		`host="api.example.test"`,
		`referer="https://console.example.test"`,
		`ua="vibe-test/1.0"`,
		`xff="183.192.84.113, 127.0.0.1"`,
		`ip_guard="deny"`,
		`ip_reason="whitelist"`,
	} {
		if !strings.Contains(line, fragment) {
			t.Fatalf("log line missing %s: %s", fragment, line)
		}
	}

	if resolved := security.ResolveClientIP(c.Request.Header, c.Request.RemoteAddr); resolved != "183.192.84.113" {
		t.Fatalf("ResolveClientIP() = %q, want %q", resolved, "183.192.84.113")
	}
}

func testEvent(name string, payload map[string]any) codex.SSEEvent {
	raw, _ := json.Marshal(payload)
	return codex.SSEEvent{
		Event: name,
		Data:  raw,
	}
}

func testJWT(claims map[string]any) string {
	raw, _ := json.Marshal(claims)
	return "test." + base64.RawURLEncoding.EncodeToString(raw) + ".sig"
}
