package app

import (
	"bufio"
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jushjay/prism/internal/affinity"
	"github.com/jushjay/prism/internal/auth"
	"github.com/jushjay/prism/internal/codex"
	"github.com/jushjay/prism/internal/config"
	"github.com/jushjay/prism/internal/models"
	"github.com/jushjay/prism/internal/requestlog"
	"github.com/jushjay/prism/internal/schema"
	"github.com/jushjay/prism/internal/security"
	"github.com/jushjay/prism/internal/store"
	"github.com/jushjay/prism/internal/usage"
	clientversion "github.com/jushjay/prism/internal/version"
)

//go:embed static/.keep
var embeddedStatic embed.FS

type Server struct {
	cfg        config.Config
	engine     *gin.Engine
	state      *store.SQLiteStore
	accounts   *auth.AccountPool
	oauth      oauthService
	dashboard  *auth.DashboardSessions
	refresh    *auth.RefreshScheduler
	codex      *codex.Client
	models     *models.Service
	security   *security.Service
	usage      *usage.Service
	requestLog *requestlog.Service
	version    *clientversion.Manager
	affinity   *affinity.Store
	audit      *auditManager
}

type oauthService interface {
	Start(returnHost string, targetAccountID string) (authURL string, state string, err error)
	TryAcquire(state string) (*auth.Session, bool)
	Release(state string)
	Complete(state string)
	IsCompleted(state string) bool
	ExchangeCode(code, verifier, redirectURI string) (auth.TokenResponse, error)
	Refresh(refreshToken string) (auth.TokenResponse, error)
}

type accountResponse struct {
	ID                 string               `json:"id"`
	Provider           auth.AccountProvider `json:"provider"`
	UserID             string               `json:"user_id,omitempty"`
	AccountID          string               `json:"account_id,omitempty"`
	Email              string               `json:"email,omitempty"`
	Enabled            bool                 `json:"enabled"`
	PlanType           string               `json:"plan_type,omitempty"`
	Status             auth.AccountStatus   `json:"status"`
	ProxyAPIKey        string               `json:"proxy_api_key"`
	Label              string               `json:"label,omitempty"`
	CustomBaseURL      string               `json:"custom_base_url,omitempty"`
	CustomEndpointType string               `json:"custom_endpoint_type,omitempty"`
	CustomUserAgent    string               `json:"custom_user_agent,omitempty"`
	CustomTransform    bool                 `json:"custom_transform"`
	CustomAPIKeySet    bool                 `json:"custom_api_key_set,omitempty"`
	Usage              auth.AccountUsage    `json:"usage"`
	Quota              *auth.AccountQuota   `json:"quota,omitempty"`
	QuotaFetchedAt     *time.Time           `json:"quota_fetched_at,omitempty"`
	CreatedAt          time.Time            `json:"created_at"`
	UpdatedAt          time.Time            `json:"updated_at"`
	ExpiresAt          time.Time            `json:"expires_at"`
	LastRefreshAt      *time.Time           `json:"last_refresh_at,omitempty"`
	RateLimitedUntil   *time.Time           `json:"rate_limited_until,omitempty"`
}

const maxEmptyResponseRetries = 1
const downstreamHeartbeatInterval = 10 * time.Second

const (
	contextKeyClientIP       = "request.client_ip"
	contextKeyClientIPSource = "request.client_ip_source"
	contextKeyRemoteIP       = "request.remote_ip"
	contextKeyTrustForwarded = "request.trust_forwarded_headers"
	contextKeyIPGuardAction  = "request.ip_guard_action"
	contextKeyIPGuardReason  = "request.ip_guard_reason"
)

func NewServer() (*Server, error) {
	cfg := config.Load()
	stateStore, err := store.NewSQLiteStore(cfg.Storage.DBFile, cfg.Storage.BaseDir)
	if err != nil {
		return nil, err
	}
	if err := stateStore.ImportAndArchiveJSONFiles([]string{
		cfg.Storage.AccountsFile,
		cfg.Storage.SettingsFile,
		cfg.Storage.ProxiesFile,
		cfg.Storage.ModelCacheFile,
		cfg.Storage.ManualModelsFile,
		cfg.Storage.ModelMappingsFile,
		cfg.Storage.UsageStatsFile,
		cfg.Storage.VersionStateFile,
	}, cfg.Storage.JSONArchiveDir); err != nil {
		_ = stateStore.Close()
		return nil, err
	}
	accounts, err := auth.NewAccountPool(cfg.Storage.AccountsFile, stateStore, cfg.Auth)
	if err != nil {
		_ = stateStore.Close()
		return nil, err
	}
	oauth := auth.NewOAuthService(cfg.Auth)
	codexClient, err := codex.NewClient(cfg)
	if err != nil {
		_ = stateStore.Close()
		return nil, err
	}
	modelCatalog, err := models.Load(cfg.Storage.ModelCatalogFile)
	if err != nil {
		_ = stateStore.Close()
		return nil, err
	}
	versionManager := clientversion.NewManager(cfg, stateStore)
	modelService, err := models.NewService(cfg, modelCatalog, accounts, codexClient, versionManager, stateStore.DB(), stateStore)
	if err != nil {
		_ = stateStore.Close()
		return nil, err
	}
	usageService, err := usage.NewService(cfg, stateStore.DB(), stateStore)
	if err != nil {
		_ = stateStore.Close()
		return nil, err
	}
	requestLogService, err := requestlog.NewService(stateStore.DB())
	if err != nil {
		_ = stateStore.Close()
		return nil, err
	}
	securityService, err := security.NewService(stateStore.DB())
	if err != nil {
		_ = stateStore.Close()
		return nil, err
	}
	if err := securityService.CleanupLocalhostStats(); err != nil {
		_ = stateStore.Close()
		return nil, err
	}
	if err := usageService.BootstrapFromAccounts(accounts.List()); err != nil {
		_ = stateStore.Close()
		return nil, err
	}
	if err := usageService.MigrateAccountUsageData(accounts.List()); err != nil {
		_ = stateStore.Close()
		return nil, err
	}
	audit, err := newAuditManager(cfg.Audit)
	if err != nil {
		_ = stateStore.Close()
		return nil, err
	}

	s := &Server{
		cfg:        cfg,
		engine:     gin.New(),
		state:      stateStore,
		accounts:   accounts,
		oauth:      oauth,
		dashboard:  auth.NewDashboardSessions(cfg.Auth.SessionTTL),
		refresh:    auth.NewRefreshScheduler(accounts, oauth, cfg.Auth.RefreshMargin),
		codex:      codexClient,
		models:     modelService,
		security:   securityService,
		usage:      usageService,
		requestLog: requestLogService,
		version:    versionManager,
		affinity:   affinity.NewStore(),
		audit:      audit,
	}
	s.engine.Use(gin.Recovery())
	s.engine.Use(s.requestLogger())
	s.engine.Use(s.ipAccessGuard())
	s.registerRoutes()
	return s, nil
}

func (s *Server) Run() error {
	defer func() {
		if s.state != nil {
			_ = s.state.Close()
		}
		if s.audit != nil {
			_ = s.audit.Close()
		}
	}()
	s.refresh.Start()
	defer s.refresh.Stop()
	s.models.Start()
	defer s.models.Stop()
	s.version.Start()
	defer s.version.Stop()
	addr := fmt.Sprintf("%s:%d", s.cfg.Server.Host, s.cfg.Server.Port)
	return s.engine.Run(addr)
}

func (s *Server) registerRoutes() {
	s.engine.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"ok":      true,
			"service": "prism",
			"time":    time.Now().UTC(),
		})
	})

	s.engine.GET("/auth/status", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"authenticated": s.accounts.IsAuthenticated(),
			"proxy_api_key": s.cfg.Server.ProxyAPIKey,
			"pool":          s.accounts.Summary(),
		})
	})

	s.engine.POST("/auth/login-start", s.handleLoginStart)
	s.engine.POST("/auth/code-relay", s.handleCodeRelay)
	s.engine.GET("/auth/callback", s.handleCallback)
	s.engine.POST("/auth/import-cli", s.handleCLIImport)
	s.engine.POST("/auth/token", s.handleManualToken)
	s.engine.POST("/auth/logout", func(c *gin.Context) {
		_ = s.accounts.Clear()
		c.JSON(http.StatusOK, gin.H{"success": true})
	})
	s.engine.GET("/auth/accounts", func(c *gin.Context) {
		c.JSON(http.StatusOK, accountResponses(s.accounts.List()))
	})
	s.engine.POST("/auth/accounts/custom", s.handleCustomAccountCreate)
	s.engine.DELETE("/auth/accounts/:id", func(c *gin.Context) {
		if err := s.accounts.Delete(c.Param("id")); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true})
	})
	s.engine.PUT("/auth/accounts/:id", s.handleUpdateAccount)
	s.engine.POST("/auth/accounts/:id/enabled", s.handleSetAccountEnabled)
	s.engine.POST("/auth/accounts/:id/reset-status", s.handleResetAccountStatus)
	s.engine.POST("/auth/accounts/:id/refresh", s.handleRefreshOne)

	s.engine.POST("/auth/dashboard-login", s.handleDashboardLogin)
	s.engine.POST("/auth/dashboard-logout", s.handleDashboardLogout)
	s.engine.GET("/auth/dashboard-status", func(c *gin.Context) {
		sessionID, _ := c.Cookie("prism_session")
		c.JSON(http.StatusOK, gin.H{"authenticated": s.dashboard.Valid(sessionID)})
	})

	s.engine.GET("/v1/models", func(c *gin.Context) {
		catalog := s.models.GlobalCatalog()
		c.JSON(http.StatusOK, gin.H{
			"object": "list",
			"data":   catalog.OpenAIModels(),
		})
	})
	s.engine.GET("/v1/models/catalog", func(c *gin.Context) {
		catalog := s.models.GlobalCatalog()
		c.JSON(http.StatusOK, catalog.Models)
	})
	s.engine.GET("/v1/models/:id", func(c *gin.Context) {
		catalog := s.models.GlobalCatalog()
		model, ok := catalog.Resolve(c.Param("id"))
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": gin.H{
				"message": "Model not found",
				"type":    "invalid_request_error",
				"param":   "model",
				"code":    "model_not_found",
			}})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"id":       model.ID,
			"object":   "model",
			"created":  models.Timestamp(),
			"owned_by": "openai",
		})
	})

	api := s.engine.Group("/")
	api.Use(s.requireAPIKey())
	api.POST("/v1/responses", s.handleResponses)
	api.POST("/v1/chat/completions", s.handleChatCompletions)

	s.engine.GET("/admin/overview", s.handleAdminOverview)
	s.engine.GET("/admin/usage/summary", s.handleAdminUsageSummary)
	s.engine.GET("/admin/usage/history", s.handleAdminUsageHistory)
	s.engine.GET("/admin/usage/events", s.handleAdminUsageEvents)
	s.engine.GET("/admin/request-events/summary", s.handleAdminRequestEventSummary)
	s.engine.GET("/admin/request-events", s.handleAdminRequestEvents)
	s.engine.GET("/admin/usage/breakdown/accounts", s.handleAdminUsageBreakdownAccounts)
	s.engine.GET("/admin/usage/breakdown/models", s.handleAdminUsageBreakdownModels)
	s.engine.GET("/admin/usage/breakdown/account-models", s.handleAdminUsageBreakdownAccountModels)
	s.engine.GET("/admin/settings", s.handleSettings)
	s.engine.GET("/admin/models/accounts/:id", s.handleAdminAccountModels)
	s.engine.GET("/admin/models/custom", s.handleAdminListCustomAccountModels)
	s.engine.POST("/admin/models/accounts/:id/refresh", s.handleAdminRefreshAccountModels)
	s.engine.POST("/admin/models/manual", s.handleAdminManualModelUpsert)
	s.engine.GET("/admin/models/mappings", s.handleAdminListModelMappings)
	s.engine.POST("/admin/models/mappings", s.handleAdminUpsertModelMapping)
	s.engine.PUT("/admin/models/mappings/:id", s.handleAdminUpdateModelMapping)
	s.engine.DELETE("/admin/models/mappings/:id", s.handleAdminDeleteModelMapping)
	s.engine.GET("/admin/security/ip-filter", s.handleAdminIPSecurityOverview)
	s.engine.POST("/admin/security/ip-filter", s.handleAdminIPSecuritySettings)
	s.engine.POST("/admin/security/ip-rules", s.handleAdminIPSecurityRuleUpsert)
	s.engine.DELETE("/admin/security/ip-rules/:id", s.handleAdminIPSecurityRuleDelete)

	s.mountStatic()
}

func accountResponses(accounts []auth.Account) []accountResponse {
	items := make([]accountResponse, 0, len(accounts))
	for _, account := range accounts {
		items = append(items, accountView(account))
	}
	return items
}

func accountView(account auth.Account) accountResponse {
	provider := account.Provider
	if provider == "" {
		provider = auth.ProviderOpenAI
	}
	return accountResponse{
		ID:                 account.ID,
		Provider:           provider,
		UserID:             account.UserID,
		AccountID:          account.AccountID,
		Email:              account.Email,
		Enabled:            !account.DisabledByUser,
		PlanType:           account.PlanType,
		Status:             account.Status,
		ProxyAPIKey:        account.ProxyAPIKey,
		Label:              account.Label,
		CustomBaseURL:      account.CustomBaseURL,
		CustomEndpointType: account.CustomEndpointType,
		CustomUserAgent:    account.CustomUserAgent,
		CustomTransform:    account.CustomProtocolTransformEnabled(),
		CustomAPIKeySet:    strings.TrimSpace(account.CustomAPIKey) != "",
		Usage:              account.Usage,
		Quota:              auth.CloneQuota(account.Quota),
		QuotaFetchedAt:     cloneTime(account.QuotaFetchedAt),
		CreatedAt:          account.CreatedAt,
		UpdatedAt:          account.UpdatedAt,
		ExpiresAt:          account.ExpiresAt,
		LastRefreshAt:      cloneTime(account.LastRefreshAt),
		RateLimitedUntil:   cloneTime(account.RateLimitedUntil),
	}
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func (s *Server) handleLoginStart(c *gin.Context) {
	var payload struct {
		AccountID string `json:"accountId"`
	}
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&payload); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	host := c.Request.Host
	if host == "" {
		host = fmt.Sprintf("localhost:%d", s.cfg.Server.Port)
	}
	targetAccountID := strings.TrimSpace(payload.AccountID)
	if targetAccountID != "" {
		account, ok := s.accounts.Get(targetAccountID)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "account not found"})
			return
		}
		if account.Provider == auth.ProviderCustom {
			c.JSON(http.StatusBadRequest, gin.H{"error": "custom account does not support oauth reauthentication"})
			return
		}
	}
	authURL, state, err := s.oauth.Start(host, targetAccountID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"authUrl":   authURL,
		"state":     state,
		"port":      auth.CallbackPort(),
		"accountId": targetAccountID,
	})
}

func (s *Server) handleCodeRelay(c *gin.Context) {
	var body struct {
		CallbackURL string `json:"callbackUrl"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	state, code, err := parseOAuthRelayInput(body.CallbackURL)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	session, ok := s.oauth.TryAcquire(state)
	if !ok {
		if s.oauth.IsCompleted(state) {
			c.JSON(http.StatusOK, gin.H{"success": true})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid or expired session"})
		return
	}
	tokens, err := s.oauth.ExchangeCode(code, session.CodeVerifier, session.RedirectURI)
	if err != nil {
		s.oauth.Release(state)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	var account auth.Account
	if session.TargetAccountID != "" {
		account, err = s.accounts.ReauthenticateAccount(session.TargetAccountID, tokens.AccessToken, tokens.RefreshToken)
	} else {
		account, err = s.accounts.AddAccount(tokens.AccessToken, tokens.RefreshToken)
	}
	if err != nil {
		s.oauth.Release(state)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	s.oauth.Complete(state)
	c.JSON(http.StatusOK, gin.H{"success": true, "account": account})
}

func parseOAuthRelayInput(raw string) (state string, code string, err error) {
	input := strings.TrimSpace(raw)
	if input == "" {
		return "", "", errors.New("callbackUrl is required")
	}
	tryParseValues := func(values url.Values) (string, string) {
		return strings.TrimSpace(values.Get("state")), strings.TrimSpace(values.Get("code"))
	}
	if strings.HasPrefix(input, "?") {
		if values, parseErr := url.ParseQuery(strings.TrimPrefix(input, "?")); parseErr == nil {
			state, code = tryParseValues(values)
		}
	} else if strings.Contains(input, "://") || strings.HasPrefix(input, "localhost:") || strings.HasPrefix(input, "/auth/callback") {
		target := input
		if strings.HasPrefix(target, "localhost:") {
			target = "http://" + target
		} else if strings.HasPrefix(target, "/") {
			target = "http://localhost" + target
		}
		if parsed, parseErr := url.Parse(target); parseErr == nil {
			state, code = tryParseValues(parsed.Query())
		}
	}
	if state == "" || code == "" {
		normalized := strings.NewReplacer("\n", "&", "\r", "&", " ", "&").Replace(input)
		if values, parseErr := url.ParseQuery(strings.TrimPrefix(normalized, "?&")); parseErr == nil {
			if parsedState, parsedCode := tryParseValues(values); parsedState != "" && parsedCode != "" {
				state, code = parsedState, parsedCode
			}
		}
	}
	if state == "" || code == "" {
		return "", "", errors.New("callback input must contain both code and state")
	}
	return state, code, nil
}

func (s *Server) handleCallback(c *gin.Context) {
	state := c.Query("state")
	code := c.Query("code")
	if state == "" || code == "" {
		c.Data(http.StatusBadRequest, "text/html; charset=utf-8", []byte(callbackHTML(false, "Missing code or state")))
		return
	}
	session, ok := s.oauth.TryAcquire(state)
	if !ok {
		if s.oauth.IsCompleted(state) {
			c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(callbackHTML(true, "")))
			return
		}
		c.Data(http.StatusBadRequest, "text/html; charset=utf-8", []byte(callbackHTML(false, "Invalid or expired session")))
		return
	}
	tokens, err := s.oauth.ExchangeCode(code, session.CodeVerifier, session.RedirectURI)
	if err != nil {
		s.oauth.Release(state)
		c.Data(http.StatusInternalServerError, "text/html; charset=utf-8", []byte(callbackHTML(false, err.Error())))
		return
	}
	if session.TargetAccountID != "" {
		if _, err := s.accounts.ReauthenticateAccount(session.TargetAccountID, tokens.AccessToken, tokens.RefreshToken); err != nil {
			s.oauth.Release(state)
			c.Data(http.StatusInternalServerError, "text/html; charset=utf-8", []byte(callbackHTML(false, err.Error())))
			return
		}
	} else if _, err := s.accounts.AddAccount(tokens.AccessToken, tokens.RefreshToken); err != nil {
		s.oauth.Release(state)
		c.Data(http.StatusInternalServerError, "text/html; charset=utf-8", []byte(callbackHTML(false, err.Error())))
		return
	}
	s.oauth.Complete(state)
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(callbackHTML(true, "")))
}

func (s *Server) handleCLIImport(c *gin.Context) {
	var payload struct {
		AuthJSON string `json:"auth_json"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	tokens, err := auth.ImportCLIAuth([]byte(payload.AuthJSON))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	account, err := s.accounts.AddAccount(tokens.AccessToken, tokens.RefreshToken)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "account": accountView(account)})
}

func (s *Server) handleSetAccountEnabled(c *gin.Context) {
	var payload struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	account, ok := s.accounts.Get(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "account not found"})
		return
	}
	provider := account.Provider
	if provider == "" {
		provider = auth.ProviderOpenAI
	}
	if payload.Enabled && account.DisabledByUser && provider == auth.ProviderOpenAI && strings.TrimSpace(account.RefreshToken) != "" {
		refreshed, status, err := s.refreshAccountTokens(account.ID)
		if err != nil {
			c.JSON(status, gin.H{"error": err.Error()})
			return
		}
		account = refreshed
	}
	if err := s.accounts.SetEnabled(c.Param("id"), payload.Enabled); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	account, ok = s.accounts.Get(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "account not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "account": accountView(account)})
}

func (s *Server) handleResetAccountStatus(c *gin.Context) {
	account, ok := s.accounts.Get(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "account not found"})
		return
	}
	if err := s.accounts.UpdateStatus(account.ID, auth.StatusActive); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	updated, ok := s.accounts.Get(account.ID)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "account not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "account": accountView(updated)})
}

func (s *Server) handleCustomAccountCreate(c *gin.Context) {
	var payload struct {
		Label              string `json:"label"`
		PlanType           string `json:"plan_type"`
		CustomBaseURL      string `json:"custom_base_url"`
		CustomAPIKey       string `json:"custom_api_key"`
		CustomEndpointType string `json:"custom_endpoint_type"`
		CustomUserAgent    string `json:"custom_user_agent"`
		CustomTransform    *bool  `json:"custom_transform"`
		Enabled            *bool  `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	enabled := true
	if payload.Enabled != nil {
		enabled = *payload.Enabled
	}
	account, err := s.accounts.AddCustomAccount(auth.CustomAccountInput{
		Label:              payload.Label,
		PlanType:           payload.PlanType,
		CustomBaseURL:      payload.CustomBaseURL,
		CustomAPIKey:       payload.CustomAPIKey,
		CustomEndpointType: payload.CustomEndpointType,
		CustomUserAgent:    payload.CustomUserAgent,
		CustomTransform:    payload.CustomTransform,
		Enabled:            enabled,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "account": accountView(account)})
}

func (s *Server) handleUpdateAccount(c *gin.Context) {
	var payload struct {
		Provider           *string `json:"provider"`
		UserID             *string `json:"user_id"`
		AccountID          *string `json:"account_id"`
		Email              *string `json:"email"`
		PlanType           *string `json:"plan_type"`
		ProxyAPIKey        *string `json:"proxy_api_key"`
		Label              *string `json:"label"`
		Enabled            *bool   `json:"enabled"`
		CustomBaseURL      *string `json:"custom_base_url"`
		CustomAPIKey       *string `json:"custom_api_key"`
		CustomEndpointType *string `json:"custom_endpoint_type"`
		CustomUserAgent    *string `json:"custom_user_agent"`
		CustomTransform    *bool   `json:"custom_transform"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if payload.CustomAPIKey != nil && strings.TrimSpace(*payload.CustomAPIKey) == "" {
		payload.CustomAPIKey = nil
	}
	account, err := s.accounts.UpdateProfile(c.Param("id"), auth.AccountProfileUpdate{
		Provider:           payload.Provider,
		UserID:             payload.UserID,
		AccountID:          payload.AccountID,
		Email:              payload.Email,
		PlanType:           payload.PlanType,
		ProxyAPIKey:        payload.ProxyAPIKey,
		Label:              payload.Label,
		Enabled:            payload.Enabled,
		CustomBaseURL:      payload.CustomBaseURL,
		CustomAPIKey:       payload.CustomAPIKey,
		CustomEndpointType: payload.CustomEndpointType,
		CustomUserAgent:    payload.CustomUserAgent,
		CustomTransform:    payload.CustomTransform,
	})
	if err != nil {
		status := http.StatusInternalServerError
		if err.Error() == "account not found" {
			status = http.StatusNotFound
		} else if err.Error() == "proxy_api_key is required" {
			status = http.StatusBadRequest
		} else if strings.HasPrefix(err.Error(), "custom_") {
			status = http.StatusBadRequest
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "account": accountView(account)})
}

func (s *Server) handleManualToken(c *gin.Context) {
	var payload struct {
		Token        string `json:"token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	account, err := s.accounts.AddAccount(payload.Token, payload.RefreshToken)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "account": accountView(account)})
}

func (s *Server) handleRefreshOne(c *gin.Context) {
	updated, status, err := s.refreshAccountTokens(c.Param("id"))
	if err != nil {
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "account": accountView(updated)})
}

func (s *Server) refreshAccountTokens(accountID string) (auth.Account, int, error) {
	account, ok := s.accounts.BeginRefresh(accountID)
	if !ok {
		return auth.Account{}, http.StatusNotFound, errors.New("account not found or refresh already in progress")
	}
	tokens, err := s.oauth.Refresh(account.RefreshToken)
	if err != nil {
		_ = s.accounts.UpdateStatus(account.ID, auth.StatusExpired)
		return auth.Account{}, http.StatusBadGateway, err
	}
	expiresAt := time.Now().Add(55 * time.Minute)
	if tokens.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second)
	}
	if err := s.accounts.UpdateTokens(account.ID, tokens.AccessToken, tokens.RefreshToken, expiresAt); err != nil {
		return auth.Account{}, http.StatusInternalServerError, err
	}
	updated, ok := s.accounts.Get(account.ID)
	if !ok {
		return auth.Account{}, http.StatusNotFound, errors.New("account not found")
	}
	return updated, http.StatusOK, nil
}

func (s *Server) handleDashboardLogin(c *gin.Context) {
	var payload struct {
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if payload.Password != s.cfg.Server.ProxyAPIKey {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid password"})
		return
	}
	session := s.dashboard.Create()
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     "prism_session",
		Value:    session.ID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  session.ExpiresAt,
	})
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (s *Server) handleDashboardLogout(c *gin.Context) {
	sessionID, _ := c.Cookie("prism_session")
	if sessionID != "" {
		s.dashboard.Delete(sessionID)
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     "prism_session",
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		HttpOnly: true,
		MaxAge:   -1,
	})
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (s *Server) handleResponses(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	s.auditCursorRequest(c, body)

	var request codex.ResponsesRequest
	if err := json.Unmarshal(body, &request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
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
	hasExplicitReasoning := request.Reasoning != nil && strings.TrimSpace(request.Reasoning.Effort) != ""
	preferredAccountID := ""
	if request.PreviousResponseID != "" {
		preferredAccountID = s.affinity.AccountForResponse(request.PreviousResponseID)
		if request.TurnState == "" {
			request.TurnState = s.affinity.TurnStateForResponse(request.PreviousResponseID)
		}
	}
	if request.PromptCacheKey == "" {
		if request.PreviousResponseID != "" {
			request.PromptCacheKey = s.affinity.ConversationForResponse(request.PreviousResponseID)
		}
		if request.PromptCacheKey == "" {
			request.PromptCacheKey = codex.StableConversationKey(request)
		}
	}
	strictAffinity := request.PreviousResponseID != "" && preferredAccountID != ""
	excludedAccountIDs := []string{}
	var lastDecision *proxyErrorDecision
	requestedModel := strings.TrimSpace(request.Model)
	modelFiltered := false

	for attempt := 0; ; attempt++ {
		lease, ok := s.accounts.Acquire(auth.AcquireOptions{
			PreferredID:     preferredAccountID,
			StrictPreferred: strictAffinity,
			ExcludeIDs:      excludedAccountIDs,
		})
		if !ok {
			if strictAffinity {
				writeProxyError(c, http.StatusServiceUnavailable, "Session-affine account unavailable for previous_response_id", false)
				return
			}
			if modelFiltered {
				writeModelNotFoundError(c, requestedModel)
				return
			}
			if lastDecision != nil {
				writeProxyError(c, lastDecision.Status, lastDecision.Message, lastDecision.UseRateLimitType)
				return
			}
			writeProxyError(c, http.StatusServiceUnavailable, "No available accounts", false)
			return
		}
		account := lease.Account
		if !sleepForLease(c.Request.Context(), lease.Wait) {
			s.accounts.Release(account.ID)
			return
		}
		attemptMetrics := newRequestAttemptMetrics()
		if shouldPassthroughCustomAccount(account) {
			if supported, known := s.models.AccountSupportsModel(account, requestedModel); known && !supported {
				s.accounts.Release(account.ID)
				excludedAccountIDs = append(excludedAccountIDs, account.ID)
				modelFiltered = true
				continue
			}
			rawHeaders := passthroughRequestHeaders(c.Request)
			resp, err := s.createCustomPassthroughResponse(c.Request.Context(), c, account, body, rawHeaders)
			if err != nil {
				s.accounts.Release(account.ID)
				attemptMetrics.statusCode = statusCodePtrFromError(err)
				s.recordRequestAttempt(account, c.Request.URL.Path, requestedModel, requestedModel, request.Stream, attempt, attemptMetrics, false, err.Error())
				decision := s.applyUpstreamError(account.ID, err)
				if decision.Retry && !strictAffinity {
					excludedAccountIDs = append(excludedAccountIDs, account.ID)
					lastDecision = &decision
					continue
				}
				writeProxyError(c, decision.Status, decision.Message, decision.UseRateLimitType)
				return
			}
			attemptMetrics.upstreamRequestID = upstreamRequestIDFromResponse(resp)
			attemptMetrics.statusCode = intPtr(resp.StatusCode)
			rawResponseBody, err := proxyHTTPResponse(c, resp)
			s.accounts.Release(account.ID)
			if err != nil {
				s.logUpstreamAttemptDiagnostics(account, c.Request.URL.Path, requestedModel, requestedModel, request.PreviousResponseID, strictAffinity, attempt, attemptMetrics, nil, 0, 0, 0, false, err.Error())
				s.recordRequestAttempt(account, c.Request.URL.Path, requestedModel, requestedModel, request.Stream, attempt, attemptMetrics, false, err.Error())
				return
			}
			attemptMetrics.usage, attemptMetrics.responseID = parsePassthroughUsage(account.CustomEndpointType, resp.Header, rawResponseBody)
			if attemptMetrics.usage.InputTokens > 0 || attemptMetrics.usage.OutputTokens > 0 {
				s.recordUsage(account.ID, requestedModel, attemptMetrics.usage)
			}
			s.recordRequestAttempt(account, c.Request.URL.Path, requestedModel, requestedModel, request.Stream, attempt, attemptMetrics, true, "")
			return
		}
		upstreamRequest := request
		resolvedMapping := s.models.ResolveMapping(request.Model, account.ID)
		upstreamRequest.Model = resolvedMapping.TargetModel
		applyMappedReasoningEffort(&upstreamRequest, resolvedMapping.ReasoningEffort, hasExplicitReasoning)
		if supported, known := s.models.AccountSupportsModel(account, upstreamRequest.Model); known && !supported {
			s.accounts.Release(account.ID)
			excludedAccountIDs = append(excludedAccountIDs, account.ID)
			modelFiltered = true
			continue
		}
		s.logUpstreamRouting(c, account, upstreamRequest)
		upstreamCtx := c.Request.Context()
		var cancelUpstream context.CancelFunc
		if request.Stream {
			upstreamCtx, cancelUpstream = detachUpstreamContext(c.Request.Context())
			defer cancelUpstream()
		}
		resp, err := s.createAccountResponse(upstreamCtx, c, account, upstreamRequest)
		if err != nil {
			s.accounts.Release(account.ID)
			attemptMetrics.statusCode = statusCodePtrFromError(err)
			s.recordRequestAttempt(account, c.Request.URL.Path, requestedModel, upstreamRequest.Model, request.Stream, attempt, attemptMetrics, false, err.Error())
			decision := s.applyUpstreamError(account.ID, err)
			if decision.Retry && !strictAffinity {
				excludedAccountIDs = append(excludedAccountIDs, account.ID)
				lastDecision = &decision
				continue
			}
			writeProxyError(c, decision.Status, decision.Message, decision.UseRateLimitType)
			return
		}

		s.applyRateLimitHeaders(account.ID, resp.Header)
		attemptMetrics.upstreamRequestID = upstreamRequestIDFromResponse(resp)
		if request.Stream {
			upstreamTurnState := resp.Header.Get("x-codex-turn-state")
			c.Status(http.StatusOK)
			c.Header("Content-Type", "text/event-stream")
			c.Header("Cache-Control", "no-cache, no-transform")
			reader := bufio.NewReader(resp.Body)
			stopReader := make(chan struct{})
			defer close(stopReader)
			eventCh := startUpstreamSSEReader(reader, stopReader)
			currentResponseID := ""
			streamUsage := codex.Usage{}
			heartbeatTicker := time.NewTicker(downstreamHeartbeatInterval)
			defer heartbeatTicker.Stop()
			c.Stream(func(w io.Writer) bool {
				select {
				case <-heartbeatTicker.C:
					writeSSEComment(w, "keepalive")
					return true
				case result, ok := <-eventCh:
					if !ok {
						attemptMetrics.streamEnded = true
						attemptMetrics.streamEndReason = "stream_reader_closed"
						return false
					}
					if result.err != nil {
						attemptMetrics.streamEnded = true
						attemptMetrics.streamEndReason = "read_error:" + result.err.Error()
						return false
					}
					if result.done {
						attemptMetrics.streamEnded = true
						attemptMetrics.streamEndReason = "done"
						return false
					}
					event := result.event
					attemptMetrics.eventCount++
					attemptMetrics.eventTypeCounts[event.Event]++
					if event.Event == "codex.rate_limits" {
						s.applyRateLimitEvent(account.ID, event.Data)
						return true
					}
					if event.Event == "keepalive" {
						writeSSEComment(w, "keepalive")
						return true
					}
					if usage, ok := extractUsageFromEvent(event); ok {
						streamUsage = usage
						attemptMetrics.usage = usage
					}
					if responseID := extractResponseIDFromEvent(event); responseID != "" {
						currentResponseID = responseID
						attemptMetrics.responseID = responseID
					}
					currentResponseID = recordResponseAffinity(s.affinity, event, currentResponseID, account.ID, upstreamRequest.PromptCacheKey, upstreamTurnState, upstreamRequest.Instructions)
					if isMeaningfulResponsesOutputEvent(event) {
						recordFirstTokenIfNil(&attemptMetrics.firstTokenMs, time.Since(attemptMetrics.startedAt))
					}
					if tupleSchema != nil {
						return streamTupleAwareResponsesEvent(w, event, tupleSchema)
					}
					writeRawSSEEvent(w, event)
					return true
				case <-c.Request.Context().Done():
					attemptMetrics.streamEnded = true
					attemptMetrics.streamEndReason = "request_context:" + c.Request.Context().Err().Error()
					return false
				}
			})
			if cancelUpstream != nil {
				cancelUpstream()
			}
			_ = resp.Body.Close()
			s.accounts.Release(account.ID)
			attemptMetrics.usage = streamUsage
			if attemptMetrics.responseID == "" {
				attemptMetrics.responseID = currentResponseID
			}
			if !attemptMetrics.streamEnded {
				attemptMetrics.streamEnded = true
				if err := c.Request.Context().Err(); err != nil {
					attemptMetrics.streamEndReason = "request_context:" + err.Error()
				} else {
					attemptMetrics.streamEndReason = "stream_returned_without_done"
				}
			}
			streamSuccess := streamAttemptSucceeded(attemptMetrics)
			errMessage := ""
			if streamSuccess {
				s.recordUsage(account.ID, upstreamRequest.Model, streamUsage)
			} else {
				errMessage = attemptMetrics.streamEndReason
			}
			s.logUpstreamAttemptDiagnostics(account, c.Request.URL.Path, requestedModel, upstreamRequest.Model, request.PreviousResponseID, strictAffinity, attempt, attemptMetrics, nil, 0, 0, 0, false, errMessage)
			s.recordRequestAttempt(account, c.Request.URL.Path, requestedModel, upstreamRequest.Model, request.Stream, attempt, attemptMetrics, streamSuccess, errMessage)
			return
		}

		events, err := readSSEWithObserver(resp, func(event codex.SSEEvent, elapsed time.Duration) {
			if event.Event == "codex.rate_limits" {
				return
			}
			if usage, ok := extractUsageFromEvent(event); ok {
				attemptMetrics.usage = usage
			}
			if responseID := extractResponseIDFromEvent(event); responseID != "" {
				attemptMetrics.responseID = responseID
			}
			if isMeaningfulResponsesOutputEvent(event) {
				recordFirstTokenIfNil(&attemptMetrics.firstTokenMs, elapsed)
			}
		}, nil)
		s.accounts.Release(account.ID)
		if err != nil {
			s.logUpstreamAttemptDiagnostics(account, c.Request.URL.Path, requestedModel, upstreamRequest.Model, request.PreviousResponseID, strictAffinity, attempt, attemptMetrics, nil, 0, 0, 0, false, err.Error())
			s.recordRequestAttempt(account, c.Request.URL.Path, requestedModel, upstreamRequest.Model, request.Stream, attempt, attemptMetrics, false, err.Error())
			writeProxyError(c, http.StatusBadGateway, err.Error(), false)
			return
		}
		s.applyRateLimitEvents(account.ID, events)
		payload, usage, responseID, err := buildResponsesPayload(events, request.Model, tupleSchema)
		if err != nil {
			attemptMetrics.usage = usage
			if attemptMetrics.responseID == "" {
				attemptMetrics.responseID = responseID
			}
			s.recordRequestAttempt(account, c.Request.URL.Path, requestedModel, upstreamRequest.Model, request.Stream, attempt, attemptMetrics, false, err.Error())
			writeProxyError(c, http.StatusBadGateway, err.Error(), false)
			return
		}
		attemptMetrics.usage = usage
		if attemptMetrics.responseID == "" {
			attemptMetrics.responseID = responseID
		}
		textLen, toolCallCount, outputItemCount := responseOutputStats(payload["output"])
		emptyResponse := isEmptyResponsesOutput(payload["output"], usage)
		s.logUpstreamAttemptDiagnostics(account, c.Request.URL.Path, requestedModel, upstreamRequest.Model, request.PreviousResponseID, strictAffinity, attempt, attemptMetrics, events, textLen, toolCallCount, outputItemCount, emptyResponse, "")
		s.recordUsage(account.ID, upstreamRequest.Model, usage)
		if emptyResponse && attempt < maxEmptyResponseRetries && !strictAffinity {
			s.recordRequestAttempt(account, c.Request.URL.Path, requestedModel, upstreamRequest.Model, request.Stream, attempt, attemptMetrics, true, "")
			_ = s.accounts.RecordEmptyResponse(account.ID)
			excludedAccountIDs = append(excludedAccountIDs, account.ID)
			lastDecision = &proxyErrorDecision{
				Status:  http.StatusBadGateway,
				Message: "Codex returned an empty response",
			}
			continue
		}
		functionCallIDs := collectFunctionCallIDs(events)
		s.affinity.Record(responseID, account.ID, upstreamRequest.PromptCacheKey, resp.Header.Get("x-codex-turn-state"), upstreamRequest.Instructions, usage.InputTokens, functionCallIDs)
		s.recordRequestAttempt(account, c.Request.URL.Path, requestedModel, upstreamRequest.Model, request.Stream, attempt, attemptMetrics, true, "")
		c.JSON(http.StatusOK, payload)
		return
	}
}

func (s *Server) handleChatCompletions(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	s.auditCursorRequest(c, body)
	chatRequest, err := parseChatProxyRequest(s.cfg, body, c.Request.UserAgent())
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	codexRequest := chatRequest.CodexRequest
	tupleSchema := chatRequest.TupleSchema
	hasExplicitReasoning := chatRequest.HasExplicitReasoning
	preferredAccountID := ""
	if codexRequest.PreviousResponseID != "" {
		preferredAccountID = s.affinity.AccountForResponse(codexRequest.PreviousResponseID)
		if codexRequest.TurnState == "" {
			codexRequest.TurnState = s.affinity.TurnStateForResponse(codexRequest.PreviousResponseID)
		}
	}
	if codexRequest.PromptCacheKey == "" {
		if codexRequest.PreviousResponseID != "" {
			codexRequest.PromptCacheKey = s.affinity.ConversationForResponse(codexRequest.PreviousResponseID)
		}
		if codexRequest.PromptCacheKey == "" {
			codexRequest.PromptCacheKey = codex.StableConversationKey(codexRequest)
		}
	}
	strictAffinity := codexRequest.PreviousResponseID != "" && preferredAccountID != ""
	excludedAccountIDs := []string{}
	var lastDecision *proxyErrorDecision
	requestedModel := strings.TrimSpace(codexRequest.Model)
	modelFiltered := false

	for attempt := 0; ; attempt++ {
		lease, ok := s.accounts.Acquire(auth.AcquireOptions{
			PreferredID:     preferredAccountID,
			StrictPreferred: strictAffinity,
			ExcludeIDs:      excludedAccountIDs,
		})
		if !ok {
			if strictAffinity {
				writeProxyError(c, http.StatusServiceUnavailable, "Session-affine account unavailable for previous_response_id", false)
				return
			}
			if modelFiltered {
				writeModelNotFoundError(c, requestedModel)
				return
			}
			if lastDecision != nil {
				writeProxyError(c, lastDecision.Status, lastDecision.Message, lastDecision.UseRateLimitType)
				return
			}
			writeProxyError(c, http.StatusServiceUnavailable, "No available accounts", false)
			return
		}
		account := lease.Account
		if !sleepForLease(c.Request.Context(), lease.Wait) {
			s.accounts.Release(account.ID)
			return
		}
		attemptMetrics := newRequestAttemptMetrics()
		if shouldPassthroughCustomAccount(account) {
			if supported, known := s.models.AccountSupportsModel(account, requestedModel); known && !supported {
				s.accounts.Release(account.ID)
				excludedAccountIDs = append(excludedAccountIDs, account.ID)
				modelFiltered = true
				continue
			}
			rawHeaders := passthroughRequestHeaders(c.Request)
			resp, err := s.createCustomPassthroughResponse(c.Request.Context(), c, account, body, rawHeaders)
			if err != nil {
				s.accounts.Release(account.ID)
				attemptMetrics.statusCode = statusCodePtrFromError(err)
				s.recordRequestAttempt(account, c.Request.URL.Path, requestedModel, requestedModel, chatRequest.Stream, attempt, attemptMetrics, false, err.Error())
				decision := s.applyUpstreamError(account.ID, err)
				if decision.Retry && !strictAffinity {
					excludedAccountIDs = append(excludedAccountIDs, account.ID)
					lastDecision = &decision
					continue
				}
				writeProxyError(c, decision.Status, decision.Message, decision.UseRateLimitType)
				return
			}
			attemptMetrics.upstreamRequestID = upstreamRequestIDFromResponse(resp)
			attemptMetrics.statusCode = intPtr(resp.StatusCode)
			rawResponseBody, err := proxyHTTPResponse(c, resp)
			s.accounts.Release(account.ID)
			if err != nil {
				s.logUpstreamAttemptDiagnostics(account, c.Request.URL.Path, requestedModel, requestedModel, "", strictAffinity, attempt, attemptMetrics, nil, 0, 0, 0, false, err.Error())
				s.recordRequestAttempt(account, c.Request.URL.Path, requestedModel, requestedModel, chatRequest.Stream, attempt, attemptMetrics, false, err.Error())
				return
			}
			attemptMetrics.usage, attemptMetrics.responseID = parsePassthroughUsage(account.CustomEndpointType, resp.Header, rawResponseBody)
			if attemptMetrics.usage.InputTokens > 0 || attemptMetrics.usage.OutputTokens > 0 {
				s.recordUsage(account.ID, requestedModel, attemptMetrics.usage)
			}
			s.recordRequestAttempt(account, c.Request.URL.Path, requestedModel, requestedModel, chatRequest.Stream, attempt, attemptMetrics, true, "")
			return
		}
		upstreamCodexRequest := codexRequest
		resolvedMapping := s.models.ResolveMapping(codexRequest.Model, account.ID)
		upstreamCodexRequest.Model = resolvedMapping.TargetModel
		applyMappedReasoningEffort(&upstreamCodexRequest, resolvedMapping.ReasoningEffort, hasExplicitReasoning)
		if supported, known := s.models.AccountSupportsModel(account, upstreamCodexRequest.Model); known && !supported {
			s.accounts.Release(account.ID)
			excludedAccountIDs = append(excludedAccountIDs, account.ID)
			modelFiltered = true
			continue
		}
		s.logUpstreamRouting(c, account, upstreamCodexRequest)
		upstreamCtx := c.Request.Context()
		var cancelUpstream context.CancelFunc
		if chatRequest.Stream {
			upstreamCtx, cancelUpstream = detachUpstreamContext(c.Request.Context())
			defer cancelUpstream()
		}
		resp, err := s.createAccountResponse(upstreamCtx, c, account, upstreamCodexRequest)
		if err != nil {
			s.accounts.Release(account.ID)
			attemptMetrics.statusCode = statusCodePtrFromError(err)
			s.recordRequestAttempt(account, c.Request.URL.Path, requestedModel, upstreamCodexRequest.Model, chatRequest.Stream, attempt, attemptMetrics, false, err.Error())
			decision := s.applyUpstreamError(account.ID, err)
			if decision.Retry && !strictAffinity {
				excludedAccountIDs = append(excludedAccountIDs, account.ID)
				lastDecision = &decision
				continue
			}
			writeProxyError(c, decision.Status, decision.Message, decision.UseRateLimitType)
			return
		}

		s.applyRateLimitHeaders(account.ID, resp.Header)
		attemptMetrics.upstreamRequestID = upstreamRequestIDFromResponse(resp)
		if chatRequest.Stream {
			upstreamTurnState := resp.Header.Get("x-codex-turn-state")
			c.Status(http.StatusOK)
			c.Header("Content-Type", "text/event-stream")
			c.Header("Cache-Control", "no-cache, no-transform")
			reader := bufio.NewReader(resp.Body)
			stopReader := make(chan struct{})
			defer close(stopReader)
			eventCh := startUpstreamSSEReader(reader, stopReader)
			currentResponseID := ""
			streamUsage := codex.Usage{}
			streamState := newChatStreamState(chatRequest.RequestedModel, tupleSchema)
			writeChatStreamChunk(wrapWriter{Writer: c.Writer}, streamState.initialRoleChunk())
			heartbeatTicker := time.NewTicker(downstreamHeartbeatInterval)
			defer heartbeatTicker.Stop()
			c.Stream(func(w io.Writer) bool {
				select {
				case <-heartbeatTicker.C:
					writeSSEComment(w, "keepalive")
					return true
				case result, ok := <-eventCh:
					if !ok {
						attemptMetrics.streamEnded = true
						attemptMetrics.streamEndReason = "stream_reader_closed"
						return false
					}
					if result.err != nil {
						attemptMetrics.streamEnded = true
						attemptMetrics.streamEndReason = "read_error:" + result.err.Error()
						return false
					}
					if result.done {
						attemptMetrics.streamEnded = true
						attemptMetrics.streamEndReason = "done"
						_, _ = io.WriteString(w, "data: [DONE]\n\n")
						return false
					}
					event := result.event
					attemptMetrics.eventCount++
					attemptMetrics.eventTypeCounts[event.Event]++
					if event.Event == "codex.rate_limits" {
						s.applyRateLimitEvent(account.ID, event.Data)
						return true
					}
					if event.Event == "keepalive" {
						writeSSEComment(w, "keepalive")
						return true
					}
					if usage, ok := extractUsageFromEvent(event); ok {
						streamUsage = usage
						attemptMetrics.usage = usage
					}
					if responseID := extractResponseIDFromEvent(event); responseID != "" {
						currentResponseID = responseID
						attemptMetrics.responseID = responseID
					}
					currentResponseID = recordResponseAffinity(s.affinity, event, currentResponseID, account.ID, upstreamCodexRequest.PromptCacheKey, upstreamTurnState, upstreamCodexRequest.Instructions)
					chunks := streamState.consume(event)
					if chatChunksCarryVisibleOutput(chunks) {
						recordFirstTokenIfNil(&attemptMetrics.firstTokenMs, time.Since(attemptMetrics.startedAt))
					}
					for _, chunk := range chunks {
						writeChatStreamChunk(w, chunk)
					}
					return true
				case <-c.Request.Context().Done():
					attemptMetrics.streamEnded = true
					attemptMetrics.streamEndReason = "request_context:" + c.Request.Context().Err().Error()
					return false
				}
			})
			if cancelUpstream != nil {
				cancelUpstream()
			}
			_ = resp.Body.Close()
			s.accounts.Release(account.ID)
			attemptMetrics.usage = streamUsage
			if attemptMetrics.responseID == "" {
				attemptMetrics.responseID = currentResponseID
			}
			if !attemptMetrics.streamEnded {
				attemptMetrics.streamEnded = true
				if err := c.Request.Context().Err(); err != nil {
					attemptMetrics.streamEndReason = "request_context:" + err.Error()
				} else {
					attemptMetrics.streamEndReason = "stream_returned_without_done"
				}
			}
			streamSuccess := streamAttemptSucceeded(attemptMetrics)
			errMessage := ""
			if streamSuccess {
				s.recordUsage(account.ID, upstreamCodexRequest.Model, streamUsage)
			} else {
				errMessage = attemptMetrics.streamEndReason
			}
			s.logUpstreamAttemptDiagnostics(account, c.Request.URL.Path, requestedModel, upstreamCodexRequest.Model, upstreamCodexRequest.PreviousResponseID, strictAffinity, attempt, attemptMetrics, nil, 0, 0, 0, false, errMessage)
			s.recordRequestAttempt(account, c.Request.URL.Path, requestedModel, upstreamCodexRequest.Model, chatRequest.Stream, attempt, attemptMetrics, streamSuccess, errMessage)
			return
		}

		events, err := readSSEWithObserver(resp, func(event codex.SSEEvent, elapsed time.Duration) {
			if event.Event == "codex.rate_limits" {
				return
			}
			if usage, ok := extractUsageFromEvent(event); ok {
				attemptMetrics.usage = usage
			}
			if responseID := extractResponseIDFromEvent(event); responseID != "" {
				attemptMetrics.responseID = responseID
			}
			if isMeaningfulResponsesOutputEvent(event) {
				recordFirstTokenIfNil(&attemptMetrics.firstTokenMs, elapsed)
			}
		}, nil)
		s.accounts.Release(account.ID)
		if err != nil {
			s.logUpstreamAttemptDiagnostics(account, c.Request.URL.Path, requestedModel, upstreamCodexRequest.Model, upstreamCodexRequest.PreviousResponseID, strictAffinity, attempt, attemptMetrics, nil, 0, 0, 0, false, err.Error())
			s.recordRequestAttempt(account, c.Request.URL.Path, requestedModel, upstreamCodexRequest.Model, chatRequest.Stream, attempt, attemptMetrics, false, err.Error())
			writeProxyError(c, http.StatusBadGateway, err.Error(), false)
			return
		}
		s.applyRateLimitEvents(account.ID, events)
		text, usage, responseID, err := codex.ReadResponseText(events)
		if err != nil {
			attemptMetrics.usage = usage
			if attemptMetrics.responseID == "" {
				attemptMetrics.responseID = responseID
			}
			s.recordRequestAttempt(account, c.Request.URL.Path, requestedModel, upstreamCodexRequest.Model, chatRequest.Stream, attempt, attemptMetrics, false, err.Error())
			writeProxyError(c, http.StatusBadGateway, err.Error(), false)
			return
		}
		attemptMetrics.usage = usage
		if attemptMetrics.responseID == "" {
			attemptMetrics.responseID = responseID
		}
		s.recordUsage(account.ID, upstreamCodexRequest.Model, usage)
		functionCallIDs := collectFunctionCallIDs(events)
		s.affinity.Record(responseID, account.ID, upstreamCodexRequest.PromptCacheKey, resp.Header.Get("x-codex-turn-state"), upstreamCodexRequest.Instructions, usage.InputTokens, functionCallIDs)
		if tupleSchema != nil {
			text = reconvertJSONText(text, tupleSchema)
		}
		toolCalls := collectToolCalls(events)
		emptyResponse := isEmptyCodexResponse(text, toolCalls, usage)
		s.logUpstreamAttemptDiagnostics(account, c.Request.URL.Path, requestedModel, upstreamCodexRequest.Model, upstreamCodexRequest.PreviousResponseID, strictAffinity, attempt, attemptMetrics, events, len(strings.TrimSpace(text)), len(toolCalls), 0, emptyResponse, "")
		if emptyResponse && attempt < maxEmptyResponseRetries && !strictAffinity {
			s.recordRequestAttempt(account, c.Request.URL.Path, requestedModel, upstreamCodexRequest.Model, chatRequest.Stream, attempt, attemptMetrics, true, "")
			_ = s.accounts.RecordEmptyResponse(account.ID)
			excludedAccountIDs = append(excludedAccountIDs, account.ID)
			lastDecision = &proxyErrorDecision{
				Status:  http.StatusBadGateway,
				Message: "Codex returned an empty response",
			}
			continue
		}
		reasoningContent := collectReasoningText(events)
		finishReason := "stop"
		messageBody := gin.H{
			"role":    "assistant",
			"content": text,
		}
		if reasoningContent != "" {
			messageBody["reasoning_content"] = reasoningContent
		}
		if len(toolCalls) > 0 {
			finishReason = "tool_calls"
			messageBody["tool_calls"] = toolCalls
			if text == "" {
				messageBody["content"] = nil
			}
		}
		c.JSON(http.StatusOK, gin.H{
			"id":      "chatcmpl-" + responseID,
			"object":  "chat.completion",
			"created": time.Now().Unix(),
			"model":   chatRequest.RequestedModel,
			"choices": []any{
				gin.H{
					"index":         0,
					"message":       messageBody,
					"finish_reason": finishReason,
				},
			},
			"usage": gin.H{
				"prompt_tokens":     usage.InputTokens,
				"completion_tokens": usage.OutputTokens,
				"total_tokens":      usage.InputTokens + usage.OutputTokens,
				"prompt_tokens_details": gin.H{
					"cached_tokens": usage.CachedTokens,
				},
				"completion_tokens_details": gin.H{
					"reasoning_tokens": usage.ReasoningTokens,
				},
			},
		})
		s.recordRequestAttempt(account, c.Request.URL.Path, requestedModel, upstreamCodexRequest.Model, chatRequest.Stream, attempt, attemptMetrics, true, "")
		return
	}
}

func (s *Server) createAccountResponse(ctx context.Context, c *gin.Context, account auth.Account, request codex.ResponsesRequest) (*http.Response, error) {
	targetURL, endpointStyle, requestBody, requestHeaders, marshalErr := s.describeUpstreamRequest(account, request)
	if marshalErr != nil {
		return nil, marshalErr
	}
	start := time.Now()
	if account.Provider == auth.ProviderCustom {
		resp, err := s.codex.CreateCustomResponse(ctx, account.CustomBaseURL, account.CustomAPIKey, account.CustomEndpointType, account.CustomUserAgent, request)
		s.auditUpstreamResult(c, account, targetURL, endpointStyle, requestBody, requestHeaders, resp, start, err)
		return resp, err
	}
	resp, err := s.codex.CreateResponse(ctx, account.AccessToken, account.AccountID, request)
	s.auditUpstreamResult(c, account, targetURL, endpointStyle, requestBody, requestHeaders, resp, start, err)
	return resp, err
}

func (s *Server) createCustomPassthroughResponse(ctx context.Context, c *gin.Context, account auth.Account, body []byte, requestHeaders map[string]string) (*http.Response, error) {
	targetURL, endpointStyle := describeCustomPassthroughRequest(account)
	start := time.Now()
	resp, err := s.codex.CreateCustomPassthrough(ctx, account.CustomBaseURL, account.CustomAPIKey, account.CustomEndpointType, account.CustomUserAgent, body, requestHeaders)
	s.auditUpstreamResult(c, account, targetURL, endpointStyle, body, requestHeaders, resp, start, err)
	return resp, err
}

func (s *Server) auditCursorRequest(c *gin.Context, body []byte) {
	if s == nil || s.audit == nil {
		return
	}
	s.audit.LogCursorRequest(c, body)
}

func (s *Server) logUpstreamRouting(c *gin.Context, account auth.Account, request codex.ResponsesRequest) {
	source := c.Request.URL.Path
	targetType := "openai"
	targetPath := "/codex/responses"
	if account.Provider == auth.ProviderCustom {
		targetType = "custom"
		targetPath = normalizeCustomEndpointPath(account.CustomEndpointType)
	}
	fmt.Fprintf(gin.DefaultWriter, "[proxy] source=%s target=%s%s account=%s model=%s\n", source, targetType, targetPath, account.ID, request.Model)
}

func (s *Server) describeUpstreamRequest(account auth.Account, request codex.ResponsesRequest) (string, string, []byte, map[string]string, error) {
	if account.Provider == auth.ProviderCustom {
		endpointStyle := normalizeCustomEndpointPath(account.CustomEndpointType)
		body, err := codex.MarshalAuditRequestBody(request, endpointStyle)
		if err != nil {
			return "", "", nil, nil, err
		}
		return codex.TargetURL(account.CustomBaseURL, endpointStyle), endpointStyle, body, map[string]string{
			"accept":       "text/event-stream",
			"content-type": "application/json",
		}, nil
	}
	body, err := codex.MarshalAuditRequestBody(request, "responses")
	if err != nil {
		return "", "", nil, nil, err
	}
	headers := map[string]string{
		"accept":       "text/event-stream",
		"content-type": "application/json",
		"openai-beta":  "responses_websockets=2026-02-06",
	}
	if request.TurnState != "" {
		headers["x-codex-turn-state"] = request.TurnState
	}
	return s.cfg.API.BaseURL + "/codex/responses", "/codex/responses", body, headers, nil
}

func describeCustomPassthroughRequest(account auth.Account) (string, string) {
	endpointStyle := normalizeCustomEndpointPath(account.CustomEndpointType)
	return codex.TargetURL(account.CustomBaseURL, endpointStyle), endpointStyle
}

func shouldPassthroughCustomAccount(account auth.Account) bool {
	return account.Provider == auth.ProviderCustom && !account.CustomProtocolTransformEnabled()
}

func passthroughRequestHeaders(req *http.Request) map[string]string {
	headers := map[string]string{}
	copyPassthroughHeader(headers, req.Header, "Accept")
	copyPassthroughHeader(headers, req.Header, "Content-Type")
	copyPassthroughHeader(headers, req.Header, "OpenAI-Beta")
	return headers
}

func copyPassthroughHeader(dst map[string]string, src http.Header, key string) {
	value := strings.TrimSpace(src.Get(key))
	if value == "" {
		return
	}
	dst[strings.ToLower(key)] = value
}

func proxyHTTPResponse(c *gin.Context, resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	copyForwardResponseHeaders(c.Writer.Header(), resp.Header)
	c.Status(resp.StatusCode)
	var captured bytes.Buffer
	buffer := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buffer)
		if n > 0 {
			chunk := buffer[:n]
			captured.Write(chunk)
			if _, err := c.Writer.Write(chunk); err != nil {
				return captured.Bytes(), err
			}
			if flusher, ok := c.Writer.(http.Flusher); ok {
				flusher.Flush()
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return captured.Bytes(), readErr
		}
	}
	return captured.Bytes(), nil
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
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailers", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func intPtr(value int) *int {
	return &value
}

func parsePassthroughUsage(endpointType string, headers http.Header, rawBody []byte) (codex.Usage, string) {
	contentType := strings.ToLower(strings.TrimSpace(headers.Get("Content-Type")))
	if strings.Contains(contentType, "text/event-stream") {
		return parsePassthroughSSEUsage(endpointType, rawBody)
	}
	return parsePassthroughJSONUsage(endpointType, rawBody)
}

func parsePassthroughSSEUsage(endpointType string, rawBody []byte) (codex.Usage, string) {
	reader := bufio.NewReader(bytes.NewReader(rawBody))
	usage := codex.Usage{}
	responseID := ""
	for {
		event, done, err := readOneSSEEvent(reader)
		if err != nil || done {
			return usage, responseID
		}
		parsedUsage, parsedResponseID, ok := parsePassthroughEventUsage(endpointType, event)
		if !ok {
			continue
		}
		usage = parsedUsage
		if parsedResponseID != "" {
			responseID = parsedResponseID
		}
	}
}

func parsePassthroughEventUsage(endpointType string, event codex.SSEEvent) (codex.Usage, string, bool) {
	var payload map[string]any
	if err := json.Unmarshal(event.Data, &payload); err != nil {
		return codex.Usage{}, "", false
	}
	if codexEndpointType(endpointType) == "chat_completions" {
		if id, _ := payload["id"].(string); id != "" {
			if rawUsage, ok := payload["usage"].(map[string]any); ok {
				return codex.ParseUsage(rawUsage), id, true
			}
			return codex.Usage{}, id, false
		}
		return codex.Usage{}, "", false
	}
	response, ok := payload["response"].(map[string]any)
	if !ok {
		return codex.Usage{}, "", false
	}
	responseID, _ := response["id"].(string)
	rawUsage, ok := response["usage"].(map[string]any)
	if !ok {
		return codex.Usage{}, responseID, false
	}
	return codex.ParseUsage(rawUsage), responseID, true
}

func parsePassthroughJSONUsage(endpointType string, rawBody []byte) (codex.Usage, string) {
	var payload map[string]any
	if err := json.Unmarshal(rawBody, &payload); err != nil {
		return codex.Usage{}, ""
	}
	if codexEndpointType(endpointType) == "chat_completions" {
		responseID, _ := payload["id"].(string)
		rawUsage, ok := payload["usage"].(map[string]any)
		if !ok {
			return codex.Usage{}, responseID
		}
		return codex.ParseUsage(rawUsage), responseID
	}
	responseID, _ := payload["id"].(string)
	rawUsage, ok := payload["usage"].(map[string]any)
	if !ok {
		return codex.Usage{}, responseID
	}
	return codex.ParseUsage(rawUsage), responseID
}

func (s *Server) auditUpstreamResult(c *gin.Context, account auth.Account, targetURL, endpointStyle string, requestBody []byte, requestHeaders map[string]string, resp *http.Response, start time.Time, err error) {
	if s == nil || s.audit == nil {
		return
	}
	status := 0
	responseHeaders := map[string]string{}
	responseBody := ""
	if resp != nil {
		status = resp.StatusCode
		for key, values := range resp.Header {
			if len(values) == 0 {
				continue
			}
			responseHeaders[key] = strings.Join(values, ", ")
		}
	}
	if upstreamErr, ok := err.(*codex.UpstreamError); ok {
		status = upstreamErr.StatusCode
		responseBody = upstreamErr.Body
		responseHeaders = map[string]string{}
		for key, values := range upstreamErr.Header {
			if len(values) == 0 {
				continue
			}
			responseHeaders[key] = strings.Join(values, ", ")
		}
	}
	duration := time.Since(start)
	if account.Provider == auth.ProviderCustom {
		s.audit.LogCustomEgress(c, account, targetURL, endpointStyle, requestBody, requestHeaders, status, responseHeaders, responseBody, duration, err)
		return
	}
	s.audit.LogOpenAIEgress(c, account, targetURL, endpointStyle, requestBody, requestHeaders, status, responseHeaders, responseBody, duration, err)
}

func codexEndpointType(value string) string {
	switch {
	case strings.HasSuffix(normalizeCustomEndpointPath(value), "/chat/completions"):
		return "chat_completions"
	default:
		return "responses"
	}
}

func normalizeCustomEndpointPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "/v1/responses"
	}
	parsed, err := url.Parse(value)
	if err == nil && parsed.Scheme != "" && parsed.Host != "" {
		value = parsed.Path
	}
	switch strings.Trim(strings.ToLower(strings.TrimSpace(value)), "/") {
	case "responses", "v1/responses", "response", "v1/response":
		return "/v1/responses"
	case "chat", "chat_completions", "chat/completions", "v1/chat", "v1/chat/completions":
		return "/v1/chat/completions"
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	return "/" + strings.Trim(strings.ToLower(strings.TrimSpace(value)), "/")
}

func (s *Server) handleAdminOverview(c *gin.Context) {
	if !s.requireDashboardSession(c) {
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"pool":          s.accounts.Summary(),
		"default_model": s.cfg.Model.DefaultModel,
		"models_total":  s.models.TotalModelCount(),
		"usage":         s.usage.Summary(),
	})
}

func (s *Server) handleAdminUsageSummary(c *gin.Context) {
	if !s.requireDashboardSession(c) {
		return
	}
	c.JSON(http.StatusOK, s.usage.Summary())
}

func (s *Server) handleAdminUsageHistory(c *gin.Context) {
	if !s.requireDashboardSession(c) {
		return
	}
	granularity := strings.TrimSpace(c.DefaultQuery("granularity", "day"))
	switch granularity {
	case "hour", "day", "month":
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "granularity must be hour, day, or month"})
		return
	}

	var fromPtr *time.Time
	var toPtr *time.Time
	if from := strings.TrimSpace(c.Query("from")); from != "" {
		parsed, err := time.Parse(time.RFC3339, from)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "from must be RFC3339"})
			return
		}
		fromPtr = &parsed
	}
	if to := strings.TrimSpace(c.Query("to")); to != "" {
		parsed, err := time.Parse(time.RFC3339, to)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "to must be RFC3339"})
			return
		}
		toPtr = &parsed
	}

	points, err := s.usage.History(fromPtr, toPtr, granularity)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"granularity": granularity,
		"from":        fromPtr,
		"to":          toPtr,
		"points":      points,
	})
}

func (s *Server) handleAdminUsageEvents(c *gin.Context) {
	if !s.requireDashboardSession(c) {
		return
	}

	var fromPtr *time.Time
	var toPtr *time.Time
	if from := strings.TrimSpace(c.Query("from")); from != "" {
		parsed, err := time.Parse(time.RFC3339, from)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "from must be RFC3339"})
			return
		}
		fromPtr = &parsed
	}
	if to := strings.TrimSpace(c.Query("to")); to != "" {
		parsed, err := time.Parse(time.RFC3339, to)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "to must be RFC3339"})
			return
		}
		toPtr = &parsed
	}

	page := 1
	if rawPage := strings.TrimSpace(c.DefaultQuery("page", "1")); rawPage != "" {
		parsed, err := strconv.Atoi(rawPage)
		if err != nil || parsed <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "page must be a positive integer"})
			return
		}
		page = parsed
	}

	pageSize := 20
	if rawPageSize := strings.TrimSpace(c.DefaultQuery("page_size", "20")); rawPageSize != "" {
		parsed, err := strconv.Atoi(rawPageSize)
		if err != nil || parsed <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "page_size must be a positive integer"})
			return
		}
		pageSize = parsed
	}

	result, err := s.usage.Events(usage.EventQuery{
		From:      fromPtr,
		To:        toPtr,
		ModelID:   strings.TrimSpace(c.Query("model")),
		AccountID: strings.TrimSpace(c.Query("account")),
		Page:      page,
		PageSize:  pageSize,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, result)
}

func (s *Server) handleAdminRequestEventSummary(c *gin.Context) {
	if !s.requireDashboardSession(c) {
		return
	}

	query, ok := parseRequestEventQuery(c)
	if !ok {
		return
	}
	summary, err := s.requestLog.Summary(query)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, summary)
}

func (s *Server) handleAdminRequestEvents(c *gin.Context) {
	if !s.requireDashboardSession(c) {
		return
	}

	query, ok := parseRequestEventQuery(c)
	if !ok {
		return
	}
	result, err := s.requestLog.Events(query)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, result)
}

func parseRequestEventQuery(c *gin.Context) (requestlog.EventQuery, bool) {
	var query requestlog.EventQuery

	if from := strings.TrimSpace(c.Query("from")); from != "" {
		parsed, err := time.Parse(time.RFC3339, from)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "from must be RFC3339"})
			return query, false
		}
		query.From = &parsed
	}
	if to := strings.TrimSpace(c.Query("to")); to != "" {
		parsed, err := time.Parse(time.RFC3339, to)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "to must be RFC3339"})
			return query, false
		}
		query.To = &parsed
	}

	page := 1
	if rawPage := strings.TrimSpace(c.DefaultQuery("page", "1")); rawPage != "" {
		parsed, err := strconv.Atoi(rawPage)
		if err != nil || parsed <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "page must be a positive integer"})
			return query, false
		}
		page = parsed
	}
	pageSize := 20
	if rawPageSize := strings.TrimSpace(c.DefaultQuery("page_size", "20")); rawPageSize != "" {
		parsed, err := strconv.Atoi(rawPageSize)
		if err != nil || parsed <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "page_size must be a positive integer"})
			return query, false
		}
		pageSize = parsed
	}

	query.ModelID = strings.TrimSpace(c.Query("model"))
	query.AccountID = strings.TrimSpace(c.Query("account"))
	query.SourcePath = strings.TrimSpace(c.Query("source_path"))
	query.Page = page
	query.PageSize = pageSize

	if rawSuccess := strings.TrimSpace(c.Query("success")); rawSuccess != "" {
		parsed, err := strconv.ParseBool(rawSuccess)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "success must be a boolean"})
			return query, false
		}
		query.Success = &parsed
	}

	return query, true
}

func (s *Server) handleAdminUsageBreakdownAccounts(c *gin.Context) {
	if !s.requireDashboardSession(c) {
		return
	}
	items, err := s.usage.BreakdownByAccount()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, items)
}

func (s *Server) handleAdminUsageBreakdownModels(c *gin.Context) {
	if !s.requireDashboardSession(c) {
		return
	}
	items, err := s.usage.BreakdownByModel()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, items)
}

func (s *Server) handleAdminUsageBreakdownAccountModels(c *gin.Context) {
	if !s.requireDashboardSession(c) {
		return
	}
	items, err := s.usage.BreakdownByAccountModel()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, items)
}

func (s *Server) handleSettings(c *gin.Context) {
	if !s.requireDashboardSession(c) {
		return
	}
	versionState := s.version.Snapshot()
	c.JSON(http.StatusOK, gin.H{
		"proxy_api_key":               s.cfg.Server.ProxyAPIKey,
		"default_model":               s.cfg.Model.DefaultModel,
		"default_reasoning_effort":    s.cfg.Model.DefaultReasoningEffort,
		"inject_desktop_context":      s.cfg.Model.InjectDesktopContext,
		"refresh_concurrency":         s.cfg.Auth.RefreshConcurrency,
		"request_interval_ms":         s.cfg.Auth.RequestIntervalMs,
		"max_concurrent_per_account":  s.cfg.Auth.MaxConcurrentPerAccount,
		"tier_priority":               s.cfg.Auth.TierPriority,
		"oauth_client_id":             s.cfg.Auth.OAuthClientID,
		"oauth_auth_endpoint":         s.cfg.Auth.OAuthAuthEndpoint,
		"oauth_token_endpoint":        s.cfg.Auth.OAuthTokenEndpoint,
		"openai_base_url":             s.cfg.API.BaseURL,
		"openai_originator":           s.cfg.API.Originator,
		"openai_user_agent":           s.cfg.API.UserAgent,
		"openai_client_version":       versionState.CurrentVersion,
		"client_version_last_checked": versionState.LastCheckedAt,
		"client_version_last_updated": versionState.LastUpdatedAt,
	})
}

func (s *Server) handleAdminAccountModels(c *gin.Context) {
	if !s.requireDashboardSession(c) {
		return
	}
	view, err := s.models.GetAccountCatalog(c.Request.Context(), c.Param("id"), false)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, view)
}

func (s *Server) handleAdminRefreshAccountModels(c *gin.Context) {
	if !s.requireDashboardSession(c) {
		return
	}
	view, err := s.models.GetAccountCatalog(c.Request.Context(), c.Param("id"), true)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, view)
}

func (s *Server) handleAdminListCustomAccountModels(c *gin.Context) {
	if !s.requireDashboardSession(c) {
		return
	}
	items, err := s.models.ListCustomAccountModels(
		strings.TrimSpace(c.Query("account_id")),
		strings.TrimSpace(c.Query("model")),
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, items)
}

func (s *Server) handleAdminManualModelUpsert(c *gin.Context) {
	if !s.requireDashboardSession(c) {
		return
	}
	var payload struct {
		ID                        string   `json:"id"`
		DisplayName               string   `json:"displayName"`
		Description               string   `json:"description"`
		DefaultReasoningEffort    string   `json:"defaultReasoningEffort"`
		SupportedReasoningEfforts []string `json:"supportedReasoningEfforts"`
		InputModalities           []string `json:"inputModalities"`
		OutputModalities          []string `json:"outputModalities"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if strings.TrimSpace(payload.ID) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model id is required"})
		return
	}
	record, err := s.models.UpsertManualModel(models.ManualModelInput{
		ID:                        payload.ID,
		DisplayName:               payload.DisplayName,
		Description:               payload.Description,
		DefaultReasoningEffort:    payload.DefaultReasoningEffort,
		SupportedReasoningEfforts: payload.SupportedReasoningEfforts,
		InputModalities:           payload.InputModalities,
		OutputModalities:          payload.OutputModalities,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "model": record})
}

func (s *Server) handleAdminListModelMappings(c *gin.Context) {
	if !s.requireDashboardSession(c) {
		return
	}
	accountID := strings.TrimSpace(c.Query("account_id"))
	c.JSON(http.StatusOK, s.models.ListModelMappings(accountID))
}

func (s *Server) handleAdminUpsertModelMapping(c *gin.Context) {
	if !s.requireDashboardSession(c) {
		return
	}
	var payload struct {
		RecordID        string `json:"recordId"`
		ModelName       string `json:"modelName"`
		TargetModel     string `json:"targetModel"`
		ReasoningEffort string `json:"reasoningEffort"`
		ApplyGlobal     bool   `json:"applyGlobal"`
		AccountID       string `json:"accountId"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	record, err := s.models.UpsertModelMapping(models.ModelMappingInput{
		RecordID:        payload.RecordID,
		ModelName:       payload.ModelName,
		TargetModel:     payload.TargetModel,
		ReasoningEffort: payload.ReasoningEffort,
		ApplyGlobal:     payload.ApplyGlobal,
		AccountID:       payload.AccountID,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "mapping": record})
}

func (s *Server) handleAdminUpdateModelMapping(c *gin.Context) {
	if !s.requireDashboardSession(c) {
		return
	}
	var payload struct {
		ModelName       string `json:"modelName"`
		TargetModel     string `json:"targetModel"`
		ReasoningEffort string `json:"reasoningEffort"`
		ApplyGlobal     bool   `json:"applyGlobal"`
		AccountID       string `json:"accountId"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	record, err := s.models.UpsertModelMapping(models.ModelMappingInput{
		RecordID:        c.Param("id"),
		ModelName:       payload.ModelName,
		TargetModel:     payload.TargetModel,
		ReasoningEffort: payload.ReasoningEffort,
		ApplyGlobal:     payload.ApplyGlobal,
		AccountID:       payload.AccountID,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "mapping": record})
}

func (s *Server) handleAdminDeleteModelMapping(c *gin.Context) {
	if !s.requireDashboardSession(c) {
		return
	}
	if err := s.models.DeleteModelMapping(c.Param("id")); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (s *Server) handleAdminIPSecurityOverview(c *gin.Context) {
	if !s.requireDashboardSession(c) {
		return
	}
	overview, err := s.security.Overview(50)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, overview)
}

func (s *Server) handleAdminIPSecuritySettings(c *gin.Context) {
	if !s.requireDashboardSession(c) {
		return
	}
	var payload struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := s.security.SetEnabled(payload.Enabled); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "enabled": payload.Enabled})
}

func (s *Server) handleAdminIPSecurityRuleUpsert(c *gin.Context) {
	if !s.requireDashboardSession(c) {
		return
	}
	var payload struct {
		ListType string `json:"listType"`
		Value    string `json:"value"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	record, err := s.security.UpsertRule(security.RuleInput{
		ListType: payload.ListType,
		Value:    payload.Value,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "rule": record})
}

func (s *Server) handleAdminIPSecurityRuleDelete(c *gin.Context) {
	if !s.requireDashboardSession(c) {
		return
	}
	if err := s.security.DeleteRule(c.Param("id")); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (s *Server) requireAPIKey() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		token := strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
		if token == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing bearer token"})
			return
		}
		if token != s.cfg.Server.ProxyAPIKey && !s.accounts.ValidateAPIKey(token) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid bearer token"})
			return
		}
		c.Next()
	}
}

func (s *Server) ipAccessGuard() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !tracksIPAccess(c.Request.URL.Path) {
			c.Next()
			return
		}

		info := security.InspectClientIP(c.Request.Header, c.Request.RemoteAddr)
		c.Set(contextKeyClientIP, info.ClientIP)
		c.Set(contextKeyClientIPSource, info.Source)
		c.Set(contextKeyRemoteIP, info.RemoteIP)
		c.Set(contextKeyTrustForwarded, info.TrustForwardedHeaders)

		decision := s.security.Evaluate(info.ClientIP)
		if !decision.Allowed {
			c.Set(contextKeyIPGuardAction, "deny")
			c.Set(contextKeyIPGuardReason, decision.Reason)
			_ = s.security.RecordAccess(info.ClientIP, c.Request.Method, c.Request.URL.Path, true)
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error":  "ip access denied",
				"ip":     info.ClientIP,
				"reason": decision.Reason,
			})
			return
		}

		c.Set(contextKeyIPGuardAction, "allow")
		c.Set(contextKeyIPGuardReason, decision.Reason)
		_ = s.security.RecordAccess(info.ClientIP, c.Request.Method, c.Request.URL.Path, false)
		c.Next()
	}
}

func (s *Server) requireDashboardSession(c *gin.Context) bool {
	remoteIP := security.ResolveClientIP(c.Request.Header, c.Request.RemoteAddr)
	if remoteIP == "127.0.0.1" || remoteIP == "::1" {
		return true
	}
	sessionID, _ := c.Cookie("prism_session")
	if !s.dashboard.Valid(sessionID) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "dashboard login required"})
		return false
	}
	return true
}

func (s *Server) requestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		fmt.Fprintln(gin.DefaultWriter, buildAccessLogLine(c, start))
	}
}

func buildAccessLogLine(c *gin.Context, start time.Time) string {
	clientIP, _ := c.Get(contextKeyClientIP)
	clientIPValue, _ := clientIP.(string)
	clientIPSource, _ := c.Get(contextKeyClientIPSource)
	clientIPSourceValue, _ := clientIPSource.(string)
	remoteIP, _ := c.Get(contextKeyRemoteIP)
	remoteIPValue, _ := remoteIP.(string)
	trustForwarded, _ := c.Get(contextKeyTrustForwarded)
	trustForwardedValue, ok := trustForwarded.(bool)
	if clientIPValue == "" || clientIPSourceValue == "" || remoteIPValue == "" || !ok {
		info := security.InspectClientIP(c.Request.Header, c.Request.RemoteAddr)
		clientIPValue = info.ClientIP
		clientIPSourceValue = info.Source
		remoteIPValue = info.RemoteIP
		trustForwardedValue = info.TrustForwardedHeaders
	}

	ipGuardAction, _ := c.Get(contextKeyIPGuardAction)
	ipGuardActionValue, _ := ipGuardAction.(string)
	if ipGuardActionValue == "" {
		if tracksIPAccess(c.Request.URL.Path) {
			ipGuardActionValue = "unknown"
		} else {
			ipGuardActionValue = "skip"
		}
	}
	ipGuardReason, _ := c.Get(contextKeyIPGuardReason)
	ipGuardReasonValue, _ := ipGuardReason.(string)

	bytesSent := c.Writer.Size()
	if bytesSent < 0 {
		bytesSent = 0
	}
	requestLength := c.Request.ContentLength
	if requestLength < 0 {
		requestLength = 0
	}

	return fmt.Sprintf(
		`time=%q remote_addr=%q remote_addr_raw=%q client_ip=%q client_ip_source=%q trust_forwarded=%t method=%q uri=%q status=%d latency=%q req_len=%d bytes_sent=%d host=%q referer=%q ua=%q x_real_ip=%q xff=%q forwarded=%q proto=%q ip_guard=%q ip_reason=%q`,
		start.Format(time.RFC3339),
		remoteIPValue,
		c.Request.RemoteAddr,
		clientIPValue,
		clientIPSourceValue,
		trustForwardedValue,
		c.Request.Method,
		c.Request.URL.RequestURI(),
		c.Writer.Status(),
		time.Since(start).Round(time.Millisecond),
		requestLength,
		bytesSent,
		c.Request.Host,
		c.Request.Referer(),
		c.Request.UserAgent(),
		c.Request.Header.Get("X-Real-IP"),
		strings.Join(c.Request.Header.Values("X-Forwarded-For"), ","),
		strings.Join(c.Request.Header.Values("Forwarded"), ","),
		c.Request.Proto,
		ipGuardActionValue,
		ipGuardReasonValue,
	)
}

func tracksIPAccess(path string) bool {
	return strings.HasPrefix(path, "/v1/") ||
		strings.HasPrefix(path, "/auth/") ||
		strings.HasPrefix(path, "/admin/") ||
		path == "/health"
}

func (s *Server) mountStatic() {
	if _, err := os.Stat(filepath.Join(s.cfg.Web.DistDir, "index.html")); err == nil {
		assetsDir := filepath.Join(s.cfg.Web.DistDir, "assets")
		if _, assetsErr := os.Stat(assetsDir); assetsErr == nil {
			s.engine.Static("/assets", assetsDir)
		}
		s.engine.NoRoute(func(c *gin.Context) {
			if strings.HasPrefix(c.Request.URL.Path, "/api/") || strings.HasPrefix(c.Request.URL.Path, "/v1/") || strings.HasPrefix(c.Request.URL.Path, "/auth/") || strings.HasPrefix(c.Request.URL.Path, "/admin/") {
				c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
				return
			}
			candidate := filepath.Join(s.cfg.Web.DistDir, strings.TrimPrefix(c.Request.URL.Path, "/"))
			if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
				c.File(candidate)
				return
			}
			c.File(filepath.Join(s.cfg.Web.DistDir, "index.html"))
		})
		return
	}

	sub, err := fs.Sub(embeddedStatic, "static")
	if err != nil {
		return
	}
	s.engine.NoRoute(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/v1/") || strings.HasPrefix(c.Request.URL.Path, "/auth/") || strings.HasPrefix(c.Request.URL.Path, "/admin/") {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		if data, readErr := fs.ReadFile(sub, ".keep"); readErr == nil {
			c.Data(http.StatusOK, "text/plain; charset=utf-8", data)
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
	})
}

func callbackHTML(success bool, message string) string {
	if success {
		return `<!doctype html><html><body style="font-family:sans-serif;padding:40px"><h2>Login successful</h2><p>You can close this window.</p><script>if(window.opener){window.opener.postMessage({type:'oauth-callback-success'},'*')}window.close()</script></body></html>`
	}
	return `<!doctype html><html><body style="font-family:sans-serif;padding:40px"><h2>Login failed</h2><p>` + message + `</p><script>if(window.opener){window.opener.postMessage({type:'oauth-callback-error',error:` + strconvQuote(message) + `},'*')}</script></body></html>`
}

func strconvQuote(input string) string {
	raw, _ := json.Marshal(input)
	return string(raw)
}

func readOneSSEEvent(reader *bufio.Reader) (codex.SSEEvent, bool, error) {
	var event codex.SSEEvent
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) && len(event.Data) == 0 {
				return codex.SSEEvent{}, true, nil
			}
			return codex.SSEEvent{}, false, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if len(event.Data) == 0 {
				continue
			}
			return event, false, nil
		}
		if strings.HasPrefix(line, "event: ") {
			event.Event = strings.TrimPrefix(line, "event: ")
			continue
		}
		if strings.HasPrefix(line, "data: ") {
			event.Data = append(event.Data, []byte(strings.TrimPrefix(line, "data: "))...)
		}
	}
}

func detachUpstreamContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(context.WithoutCancel(ctx))
}

func streamAttemptSucceeded(metrics requestAttemptMetrics) bool {
	return metrics.streamEnded && metrics.streamEndReason == "done"
}

type upstreamSSEReadResult struct {
	event codex.SSEEvent
	done  bool
	err   error
}

func startUpstreamSSEReader(reader *bufio.Reader, stop <-chan struct{}) <-chan upstreamSSEReadResult {
	results := make(chan upstreamSSEReadResult, 1)
	go func() {
		defer close(results)
		for {
			event, done, err := readOneSSEEvent(reader)
			result := upstreamSSEReadResult{event: event, done: done, err: err}
			select {
			case results <- result:
			case <-stop:
				return
			}
			if done || err != nil {
				return
			}
		}
	}()
	return results
}

func writeRawSSEEvent(w io.Writer, event codex.SSEEvent) {
	if event.Event != "" {
		_, _ = io.WriteString(w, "event: "+event.Event+"\n")
	}
	_, _ = io.WriteString(w, "data: "+string(event.Data)+"\n\n")
}

func recordResponseAffinity(store *affinity.Store, event codex.SSEEvent, currentResponseID, accountID, conversationID, upstreamTurnState, instructions string) string {
	var payload map[string]any
	if err := json.Unmarshal(event.Data, &payload); err != nil {
		return currentResponseID
	}
	responseID := currentResponseID
	var functionCallIDs []string
	inputTokens := 0
	switch event.Event {
	case "response.created", "response.in_progress", "response.completed":
		if response, ok := payload["response"].(map[string]any); ok {
			if id, ok := response["id"].(string); ok {
				responseID = id
			}
			if usage, ok := response["usage"].(map[string]any); ok {
				inputTokens = codex.ParseUsage(usage).InputTokens
			}
		}
	case "response.failed":
		if response, ok := payload["response"].(map[string]any); ok {
			if id, ok := response["id"].(string); ok {
				responseID = id
			}
		}
	case "response.output_item.added":
		if item, ok := payload["item"].(map[string]any); ok {
			if itemType, _ := item["type"].(string); itemType == "function_call" || itemType == "custom_tool_call" {
				if callID := toolCallIDFromItem(item); callID != "" {
					functionCallIDs = append(functionCallIDs, callID)
				}
			}
		}
	}
	if responseID == "" {
		return currentResponseID
	}
	turnState := upstreamTurnState
	if response, ok := payload["response"].(map[string]any); ok {
		if state, ok := response["turn_state"].(string); ok && state != "" {
			turnState = state
		}
	}
	store.Record(responseID, accountID, conversationID, turnState, instructions, inputTokens, functionCallIDs)
	return responseID
}

func extractUsageFromEvent(event codex.SSEEvent) (codex.Usage, bool) {
	switch event.Event {
	case "response.created", "response.in_progress", "response.completed":
	default:
		return codex.Usage{}, false
	}

	var payload map[string]any
	if err := json.Unmarshal(event.Data, &payload); err != nil {
		return codex.Usage{}, false
	}
	response, ok := payload["response"].(map[string]any)
	if !ok {
		return codex.Usage{}, false
	}
	rawUsage, ok := response["usage"].(map[string]any)
	if !ok {
		return codex.Usage{}, false
	}
	return codex.ParseUsage(rawUsage), true
}

func extractResponseIDFromEvent(event codex.SSEEvent) string {
	var payload map[string]any
	if err := json.Unmarshal(event.Data, &payload); err != nil {
		return ""
	}
	if response, ok := payload["response"].(map[string]any); ok {
		if id, ok := response["id"].(string); ok {
			return strings.TrimSpace(id)
		}
	}
	return ""
}

func usagePointersFromCodexUsage(usage codex.Usage) (*int, *int, *int, *int) {
	inputTokens := usage.InputTokens
	outputTokens := usage.OutputTokens
	cachedTokens := usage.CachedTokens
	reasoningTokens := usage.ReasoningTokens
	return &inputTokens, &outputTokens, &cachedTokens, &reasoningTokens
}

func isMeaningfulResponsesOutputEvent(event codex.SSEEvent) bool {
	switch event.Event {
	case "response.output_text.delta",
		"response.output_item.added",
		"response.content_part.added":
		return true
	default:
		return false
	}
}

func chatChunksCarryVisibleOutput(chunks []any) bool {
	for _, rawChunk := range chunks {
		chunk, ok := rawChunk.(gin.H)
		if !ok {
			continue
		}
		choices, ok := chunk["choices"].([]any)
		if !ok {
			continue
		}
		for _, rawChoice := range choices {
			choice, ok := rawChoice.(gin.H)
			if !ok {
				continue
			}
			delta, ok := choice["delta"].(gin.H)
			if !ok {
				continue
			}
			if content, ok := delta["content"].(string); ok && strings.TrimSpace(content) != "" {
				return true
			}
			if toolCalls, ok := delta["tool_calls"].([]any); ok && len(toolCalls) > 0 {
				return true
			}
		}
	}
	return false
}

func readSSEWithObserver(resp *http.Response, observer func(event codex.SSEEvent, elapsed time.Duration), onDone func(elapsed time.Duration)) ([]codex.SSEEvent, error) {
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	var events []codex.SSEEvent
	startedAt := time.Now()
	for {
		event, done, err := readOneSSEEvent(reader)
		if err != nil {
			return nil, err
		}
		if done {
			if onDone != nil {
				onDone(time.Since(startedAt))
			}
			return events, nil
		}
		if observer != nil {
			observer(event, time.Since(startedAt))
		}
		events = append(events, event)
	}
}

func recordFirstTokenIfNil(target **int, elapsed time.Duration) {
	if target == nil || *target != nil {
		return
	}
	value := int(elapsed.Milliseconds())
	*target = &value
}

func statusCodePtrFromError(err error) *int {
	upstreamErr, ok := err.(*codex.UpstreamError)
	if !ok {
		return nil
	}
	status := upstreamErr.StatusCode
	return &status
}

func upstreamRequestIDFromResponse(resp *http.Response) string {
	if resp == nil {
		return ""
	}
	return strings.TrimSpace(resp.Header.Get("x-request-id"))
}

func endpointStyleFromAccount(account auth.Account) string {
	if account.Provider == auth.ProviderCustom {
		return normalizeCustomEndpointPath(account.CustomEndpointType)
	}
	return "/codex/responses"
}

func upstreamTypeFromAccount(account auth.Account) string {
	if account.Provider == auth.ProviderCustom {
		return "custom"
	}
	return "openai"
}

func (s *Server) recordRequestAttempt(account auth.Account, sourcePath, requestedModel, routedModel string, requestStream bool, retryAttempt int, metrics requestAttemptMetrics, success bool, errMessage string) {
	if s == nil || s.requestLog == nil {
		return
	}
	completedAt := time.Now()
	durationMs := int(completedAt.Sub(metrics.startedAt).Milliseconds())
	if durationMs < 0 {
		durationMs = 0
	}
	inputTokens, outputTokens, cachedTokens, reasoningTokens := usagePointersFromCodexUsage(metrics.usage)
	snapshot := auth.BuildUsageSnapshot(account)
	record := requestlog.Record{
		StartedAt:          metrics.startedAt,
		CompletedAt:        completedAt,
		DurationMs:         durationMs,
		FirstTokenMs:       metrics.firstTokenMs,
		Success:            success,
		StatusCode:         metrics.statusCode,
		ErrorMessage:       errMessage,
		SourcePath:         sourcePath,
		EndpointStyle:      endpointStyleFromAccount(account),
		RequestStream:      requestStream,
		RetryAttempt:       retryAttempt,
		UpstreamType:       upstreamTypeFromAccount(account),
		AccountID:          account.ID,
		AccountProvider:    account.Provider,
		AccountIdentity:    snapshot.UsageIdentity,
		AccountDisplayName: snapshot.DisplayName,
		AccountLabel:       snapshot.Label,
		AccountEmail:       snapshot.Email,
		UpstreamAccountID:  snapshot.UpstreamID,
		AccountSnapshot:    &snapshot,
		RequestedModel:     requestedModel,
		RoutedModel:        routedModel,
		UpstreamRequestID:  metrics.upstreamRequestID,
		ResponseID:         metrics.responseID,
		InputTokens:        inputTokens,
		OutputTokens:       outputTokens,
		CachedTokens:       cachedTokens,
		ReasoningTokens:    reasoningTokens,
	}
	if err := s.requestLog.Record(record); err != nil {
		fmt.Fprintf(gin.DefaultWriter, "[requestlog] record failed: %v\n", err)
	}
}

func (s *Server) recordUsage(accountID, modelID string, resultUsage codex.Usage) {
	if accountID == "" {
		return
	}
	if err := s.accounts.RecordUsage(accountID, resultUsage.InputTokens, resultUsage.OutputTokens); err != nil {
		return
	}
	account, ok := s.accounts.Get(accountID)
	if !ok {
		return
	}
	_ = s.usage.Record(usage.RecordFromAccount(
		account,
		time.Now().UTC(),
		modelID,
		resultUsage.InputTokens,
		resultUsage.OutputTokens,
		1,
		resultUsage.CachedTokens,
		resultUsage.ReasoningTokens,
	))
}

func (s *Server) logUpstreamAttemptDiagnostics(account auth.Account, sourcePath, requestedModel, routedModel, previousResponseID string, strictAffinity bool, retryAttempt int, metrics requestAttemptMetrics, events []codex.SSEEvent, textLen, toolCallCount, outputItemCount int, emptyResponse bool, errMessage string) {
	if errMessage == "" && !emptyResponse && metrics.statusCode == nil {
		return
	}
	durationMs := int(time.Since(metrics.startedAt).Milliseconds())
	if durationMs < 0 {
		durationMs = 0
	}
	firstTokenMs := -1
	if metrics.firstTokenMs != nil {
		firstTokenMs = *metrics.firstTokenMs
	}
	statusCode := 0
	if metrics.statusCode != nil {
		statusCode = *metrics.statusCode
	}
	eventCount := len(events)
	if metrics.eventCount > eventCount {
		eventCount = metrics.eventCount
	}
	eventSummary := summarizeSSEEventTypes(events)
	if eventSummary == "" {
		eventSummary = summarizeEventTypeCounts(metrics.eventTypeCounts)
	}
	fmt.Fprintf(
		gin.DefaultErrorWriter,
		"[upstream-attempt] source=%s account=%s provider=%v requested_model=%s routed_model=%s retry_attempt=%d previous_response_id=%q strict_affinity=%t upstream_request_id=%q response_id=%q status_code=%d duration_ms=%d first_token_ms=%d usage_in=%d usage_out=%d usage_cached=%d usage_reasoning=%d event_count=%d output_items=%d tool_call_count=%d text_len=%d empty_response=%t stream_ended=%t stream_end_reason=%q events=%q error=%q\n",
		sourcePath,
		account.ID,
		account.Provider,
		requestedModel,
		routedModel,
		retryAttempt,
		previousResponseID,
		strictAffinity,
		metrics.upstreamRequestID,
		metrics.responseID,
		statusCode,
		durationMs,
		firstTokenMs,
		metrics.usage.InputTokens,
		metrics.usage.OutputTokens,
		metrics.usage.CachedTokens,
		metrics.usage.ReasoningTokens,
		eventCount,
		outputItemCount,
		toolCallCount,
		textLen,
		emptyResponse,
		metrics.streamEnded,
		metrics.streamEndReason,
		eventSummary,
		errMessage,
	)
}

func summarizeSSEEventTypes(events []codex.SSEEvent) string {
	if len(events) == 0 {
		return ""
	}
	counts := make(map[string]int, len(events))
	for _, event := range events {
		counts[event.Event]++
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, counts[key]))
	}
	return strings.Join(parts, ",")
}

func summarizeEventTypeCounts(counts map[string]int) string {
	if len(counts) == 0 {
		return ""
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, counts[key]))
	}
	return strings.Join(parts, ",")
}

func responseOutputStats(raw any) (textLen, toolCallCount, outputItemCount int) {
	output, ok := raw.([]any)
	if !ok {
		return 0, 0, 0
	}
	outputItemCount = len(output)
	for _, rawItem := range output {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		itemType, _ := item["type"].(string)
		if itemType == "function_call" || itemType == "custom_tool_call" {
			toolCallCount++
			continue
		}
		content, ok := item["content"].([]any)
		if !ok {
			continue
		}
		for _, rawContent := range content {
			part, ok := rawContent.(map[string]any)
			if !ok {
				continue
			}
			partType, _ := part["type"].(string)
			if partType != "output_text" && partType != "text" {
				continue
			}
			text, _ := part["text"].(string)
			textLen += len(strings.TrimSpace(text))
		}
	}
	return textLen, toolCallCount, outputItemCount
}

func collectFunctionCallIDs(events []codex.SSEEvent) []string {
	seen := map[string]struct{}{}
	var ids []string
	for _, event := range events {
		if event.Event != "response.output_item.added" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			continue
		}
		item, ok := payload["item"].(map[string]any)
		if !ok {
			continue
		}
		itemType, _ := item["type"].(string)
		if itemType != "function_call" && itemType != "custom_tool_call" {
			continue
		}
		callID := toolCallIDFromItem(item)
		if callID == "" {
			continue
		}
		if _, ok := seen[callID]; ok {
			continue
		}
		seen[callID] = struct{}{}
		ids = append(ids, callID)
	}
	return ids
}

func reconvertJSONText(text string, tupleSchema map[string]any) string {
	if tupleSchema == nil || strings.TrimSpace(text) == "" {
		return text
	}
	var parsed any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return text
	}
	reconverted := schema.ReconvertTupleValues(parsed, tupleSchema)
	raw, err := json.Marshal(reconverted)
	if err != nil {
		return text
	}
	return string(raw)
}

func firstOutputText(output []any) string {
	for _, item := range output {
		asMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		content, ok := asMap["content"].([]any)
		if !ok {
			continue
		}
		for _, raw := range content {
			part, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			typeName, _ := part["type"].(string)
			if (typeName == "output_text" || typeName == "text") && part["text"] != nil {
				if text, ok := part["text"].(string); ok {
					return text
				}
			}
		}
	}
	return ""
}

type wrapWriter struct {
	io.Writer
}

type proxyErrorDecision struct {
	Retry            bool
	Status           int
	Message          string
	UseRateLimitType bool
}

type requestAttemptMetrics struct {
	startedAt         time.Time
	firstTokenMs      *int
	responseID        string
	upstreamRequestID string
	statusCode        *int
	usage             codex.Usage
	eventCount        int
	eventTypeCounts   map[string]int
	streamEnded       bool
	streamEndReason   string
}

func newRequestAttemptMetrics() requestAttemptMetrics {
	return requestAttemptMetrics{
		startedAt:       time.Now(),
		eventTypeCounts: map[string]int{},
	}
}

type chatStreamState struct {
	chunkID            string
	model              string
	created            int64
	tupleSchema        map[string]any
	toolCallIndexByID  map[string]int
	itemIDToCallID     map[string]string
	itemIDToName       map[string]string
	nextToolCallIndex  int
	callIDsWithDeltas  map[string]struct{}
	tupleTextBuffer    strings.Builder
	reasoningBuffer    strings.Builder
	hasTupleTextBuffer bool
	hasToolCalls       bool
}

func newChatStreamState(model string, tupleSchema map[string]any) *chatStreamState {
	return &chatStreamState{
		chunkID:           "chatcmpl-stream",
		model:             model,
		created:           time.Now().Unix(),
		tupleSchema:       tupleSchema,
		toolCallIndexByID: map[string]int{},
		itemIDToCallID:    map[string]string{},
		itemIDToName:      map[string]string{},
		callIDsWithDeltas: map[string]struct{}{},
	}
}

func (s *chatStreamState) initialRoleChunk() any {
	return s.chunkWithDelta(gin.H{
		"role": "assistant",
	})
}

func (s *chatStreamState) chatChunk(choices []any, usage any) gin.H {
	return gin.H{
		"id":                 s.chunkID,
		"object":             "chat.completion.chunk",
		"created":            s.created,
		"model":              s.model,
		"choices":            choices,
		"system_fingerprint": nil,
		"usage":              usage,
	}
}

func chatChoice(delta gin.H, finishReason any) gin.H {
	return gin.H{
		"index":         0,
		"delta":         delta,
		"logprobs":      nil,
		"finish_reason": finishReason,
	}
}

func chatUsageChunk(usage codex.Usage) gin.H {
	return gin.H{
		"prompt_tokens":     usage.InputTokens,
		"completion_tokens": usage.OutputTokens,
		"total_tokens":      usage.InputTokens + usage.OutputTokens,
		"prompt_tokens_details": gin.H{
			"cached_tokens": usage.CachedTokens,
		},
		"completion_tokens_details": gin.H{
			"reasoning_tokens": usage.ReasoningTokens,
		},
	}
}

func usageFromCompletedPayload(payload map[string]any) (codex.Usage, bool) {
	response, ok := payload["response"].(map[string]any)
	if !ok {
		return codex.Usage{}, false
	}
	rawUsage, ok := response["usage"].(map[string]any)
	if !ok {
		return codex.Usage{}, false
	}
	return codex.ParseUsage(rawUsage), true
}

func (s *chatStreamState) chunkWithDelta(delta gin.H) gin.H {
	return s.chatChunk([]any{chatChoice(delta, nil)}, nil)
}

func (s *chatStreamState) finishChunk(finishReason string) gin.H {
	return s.chatChunk([]any{chatChoice(gin.H{}, finishReason)}, nil)
}

func (s *chatStreamState) usageChunk(usage codex.Usage) gin.H {
	return s.chatChunk([]any{}, chatUsageChunk(usage))
}

func (s *chatStreamState) consume(event codex.SSEEvent) []any {
	var payload map[string]any
	if err := json.Unmarshal(event.Data, &payload); err != nil {
		return nil
	}
	switch event.Event {
	case "response.output_item.added":
		item, ok := payload["item"].(map[string]any)
		if !ok {
			return nil
		}
		itemType, _ := item["type"].(string)
		if itemType != "function_call" && itemType != "custom_tool_call" {
			return nil
		}
		callID := toolCallIDFromItem(item)
		if callID == "" {
			return nil
		}
		itemID, _ := item["id"].(string)
		name, _ := item["name"].(string)
		if itemID != "" {
			s.itemIDToCallID[itemID] = callID
			if name != "" {
				s.itemIDToName[itemID] = name
			}
		}
		index := s.nextToolCallIndex
		s.nextToolCallIndex++
		s.toolCallIndexByID[callID] = index
		s.hasToolCalls = true
		return []any{
			s.chunkWithDelta(gin.H{
				"tool_calls": []any{
					gin.H{
						"index": index,
						"id":    callID,
						"type":  "function",
						"function": gin.H{
							"name":      name,
							"arguments": "",
						},
					},
				},
			}),
		}
	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		callID, _ := payload["call_id"].(string)
		itemID, _ := payload["item_id"].(string)
		callID = s.resolveCallID(callID, itemID)
		if callID == "" {
			return nil
		}
		index, ok := s.toolCallIndexByID[callID]
		if !ok {
			index = 0
		}
		delta, _ := payload["delta"].(string)
		s.callIDsWithDeltas[callID] = struct{}{}
		return []any{
			s.chunkWithDelta(gin.H{
				"tool_calls": []any{
					gin.H{
						"index": index,
						"function": gin.H{
							"arguments": delta,
						},
					},
				},
			}),
		}
	case "response.function_call_arguments.done", "response.custom_tool_call_input.done":
		callID, _ := payload["call_id"].(string)
		itemID, _ := payload["item_id"].(string)
		callID = s.resolveCallID(callID, itemID)
		if callID == "" {
			return nil
		}
		if _, ok := s.callIDsWithDeltas[callID]; ok {
			return nil
		}
		index, ok := s.toolCallIndexByID[callID]
		if !ok {
			index = 0
		}
		arguments, _ := payload["arguments"].(string)
		if arguments == "" {
			arguments, _ = payload["input"].(string)
		}
		return []any{
			s.chunkWithDelta(gin.H{
				"tool_calls": []any{
					gin.H{
						"index": index,
						"function": gin.H{
							"arguments": arguments,
						},
					},
				},
			}),
		}
	case "response.output_text.delta":
		delta, _ := payload["delta"].(string)
		if s.tupleSchema != nil {
			s.hasTupleTextBuffer = true
			s.tupleTextBuffer.WriteString(delta)
			return nil
		}
		return []any{
			s.chunkWithDelta(gin.H{"content": delta}),
		}
	case "response.reasoning_summary_text.delta":
		delta, _ := payload["delta"].(string)
		if delta == "" {
			return nil
		}
		s.reasoningBuffer.WriteString(delta)
		return []any{
			s.chunkWithDelta(gin.H{"reasoning_content": delta}),
		}
	case "response.reasoning_summary_text.done":
		text, _ := payload["text"].(string)
		if text == "" || s.reasoningBuffer.Len() > 0 {
			return nil
		}
		s.reasoningBuffer.WriteString(text)
		return []any{
			s.chunkWithDelta(gin.H{"reasoning_content": text}),
		}
	case "response.completed":
		var chunks []any
		if s.tupleSchema != nil {
			text := s.tupleTextBuffer.String()
			if text == "" {
				if response, ok := payload["response"].(map[string]any); ok {
					if output, ok := response["output"].([]any); ok {
						text = firstOutputText(output)
					}
				}
			}
			if text != "" {
				chunks = append(chunks, s.chunkWithDelta(gin.H{
					"content": reconvertJSONText(text, s.tupleSchema),
				}))
			}
		}
		finishReason := "stop"
		if s.hasToolCalls {
			finishReason = "tool_calls"
		}
		chunks = append(chunks, s.finishChunk(finishReason))
		if usage, ok := usageFromCompletedPayload(payload); ok {
			chunks = append(chunks, s.usageChunk(usage))
		}
		return chunks
	default:
		return nil
	}
}

func (s *chatStreamState) resolveCallID(callID, itemID string) string {
	return resolveToolCallID(callID, itemID, s.itemIDToCallID)
}

func writeChatStreamChunk(w io.Writer, chunk any) {
	raw, _ := json.Marshal(chunk)
	_, _ = io.WriteString(w, "data: "+string(raw)+"\n\n")
}

func writeSSEComment(w io.Writer, comment string) {
	comment = strings.TrimSpace(comment)
	if comment == "" {
		comment = "keepalive"
	}
	_, _ = io.WriteString(w, ": "+comment+"\n\n")
}

func collectReasoningText(events []codex.SSEEvent) string {
	var builder strings.Builder
	var fallback string
	for _, event := range events {
		var payload map[string]any
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			continue
		}
		switch event.Event {
		case "response.reasoning_summary_text.delta":
			if delta, _ := payload["delta"].(string); delta != "" {
				builder.WriteString(delta)
			}
		case "response.reasoning_summary_text.done":
			if text, _ := payload["text"].(string); text != "" && fallback == "" {
				fallback = text
			}
		}
	}
	if builder.Len() > 0 {
		return builder.String()
	}
	return fallback
}

func applyMappedReasoningEffort(request *codex.ResponsesRequest, mappedEffort string, hasExplicitReasoning bool) {
	effort := strings.TrimSpace(mappedEffort)
	if effort == "" || hasExplicitReasoning {
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

func collectToolCalls(events []codex.SSEEvent) []gin.H {
	callByID := map[string]gin.H{}
	itemIDToCallID := map[string]string{}
	itemIDToName := map[string]string{}
	order := make([]string, 0)
	for _, event := range events {
		var payload map[string]any
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			continue
		}
		switch event.Event {
		case "response.output_item.added":
			item, ok := payload["item"].(map[string]any)
			if !ok {
				continue
			}
			itemType, _ := item["type"].(string)
			if itemType != "function_call" && itemType != "custom_tool_call" {
				continue
			}
			callID := toolCallIDFromItem(item)
			if callID == "" {
				continue
			}
			itemID, _ := item["id"].(string)
			name, _ := item["name"].(string)
			if itemID != "" {
				itemIDToCallID[itemID] = callID
				if name != "" {
					itemIDToName[itemID] = name
				}
			}
			if _, ok := callByID[callID]; !ok {
				callByID[callID] = gin.H{
					"id":   callID,
					"type": "function",
					"function": gin.H{
						"name":      name,
						"arguments": "",
					},
				}
				order = append(order, callID)
			}
		case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
			callID, _ := payload["call_id"].(string)
			itemID, _ := payload["item_id"].(string)
			callID = resolveToolCallID(callID, itemID, itemIDToCallID)
			if callID == "" {
				continue
			}
			entry, ok := callByID[callID]
			if !ok {
				entry = gin.H{
					"id":   callID,
					"type": "function",
					"function": gin.H{
						"arguments": "",
					},
				}
				order = append(order, callID)
			}
			function := entry["function"].(gin.H)
			delta, _ := payload["delta"].(string)
			function["arguments"] = function["arguments"].(string) + delta
			entry["function"] = function
			callByID[callID] = entry
		case "response.function_call_arguments.done", "response.custom_tool_call_input.done":
			callID, _ := payload["call_id"].(string)
			itemID, _ := payload["item_id"].(string)
			callID = resolveToolCallID(callID, itemID, itemIDToCallID)
			if callID == "" {
				continue
			}
			entry, ok := callByID[callID]
			if !ok {
				entry = gin.H{
					"id":       callID,
					"type":     "function",
					"function": gin.H{},
				}
				order = append(order, callID)
			}
			function := entry["function"].(gin.H)
			if name, _ := payload["name"].(string); name != "" {
				function["name"] = name
			} else if itemID != "" {
				if mappedName, ok := itemIDToName[itemID]; ok && mappedName != "" {
					function["name"] = mappedName
				}
			}
			if arguments, _ := payload["arguments"].(string); arguments != "" {
				function["arguments"] = arguments
			} else if input, _ := payload["input"].(string); input != "" {
				function["arguments"] = input
			}
			entry["function"] = function
			callByID[callID] = entry
		}
	}
	result := make([]gin.H, 0, len(order))
	for _, callID := range order {
		result = append(result, callByID[callID])
	}
	return result
}

func toolCallIDFromItem(item map[string]any) string {
	callID, _ := item["call_id"].(string)
	if callID != "" {
		return callID
	}
	id, _ := item["id"].(string)
	return id
}

func resolveToolCallID(callID, itemID string, itemIDToCallID map[string]string) string {
	if callID != "" {
		if resolved, ok := itemIDToCallID[callID]; ok {
			return resolved
		}
		return callID
	}
	if itemID == "" {
		return ""
	}
	if resolved, ok := itemIDToCallID[itemID]; ok {
		return resolved
	}
	return itemID
}

func buildResponsesPayload(events []codex.SSEEvent, model string, tupleSchema map[string]any) (gin.H, codex.Usage, string, error) {
	var usage codex.Usage
	responseID := ""
	resolvedModel := model
	var output []any
	for _, event := range events {
		var payload map[string]any
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			continue
		}
		switch event.Event {
		case "response.created", "response.in_progress", "response.completed":
			response, ok := payload["response"].(map[string]any)
			if !ok {
				continue
			}
			if id, ok := response["id"].(string); ok && id != "" {
				responseID = id
			}
			if responseModel, ok := response["model"].(string); ok && responseModel != "" {
				resolvedModel = responseModel
			}
			if rawUsage, ok := response["usage"].(map[string]any); ok {
				usage = codex.ParseUsage(rawUsage)
			}
			if event.Event == "response.completed" {
				if rawOutput, ok := response["output"].([]any); ok {
					output = rewriteResponseOutput(rawOutput, tupleSchema)
				}
			}
		case "error", "response.failed":
			return nil, usage, responseID, errors.New(string(event.Data))
		}
	}
	if len(output) == 0 {
		text, _, _, err := codex.ReadResponseText(events)
		if err != nil {
			return nil, usage, responseID, err
		}
		if tupleSchema != nil {
			text = reconvertJSONText(text, tupleSchema)
		}
		output = []any{
			gin.H{
				"type": "message",
				"content": []any{
					gin.H{
						"type": "output_text",
						"text": text,
					},
				},
			},
		}
	}
	return gin.H{
		"id":     responseID,
		"object": "response",
		"model":  resolvedModel,
		"output": output,
		"usage": gin.H{
			"input_tokens": usage.InputTokens,
			"input_tokens_details": gin.H{
				"cached_tokens": usage.CachedTokens,
			},
			"output_tokens": usage.OutputTokens,
			"output_tokens_details": gin.H{
				"reasoning_tokens": usage.ReasoningTokens,
			},
			"total_tokens": usage.TotalTokens,
		},
	}, usage, responseID, nil
}

func rewriteResponseOutput(output []any, tupleSchema map[string]any) []any {
	if tupleSchema == nil {
		return output
	}
	rewritten := make([]any, 0, len(output))
	for _, rawItem := range output {
		item, ok := rawItem.(map[string]any)
		if !ok {
			rewritten = append(rewritten, rawItem)
			continue
		}
		itemCopy := mapsClone(item)
		content, ok := item["content"].([]any)
		if !ok {
			rewritten = append(rewritten, itemCopy)
			continue
		}
		contentCopy := make([]any, 0, len(content))
		for _, rawContent := range content {
			part, ok := rawContent.(map[string]any)
			if !ok {
				contentCopy = append(contentCopy, rawContent)
				continue
			}
			partCopy := mapsClone(part)
			typeName, _ := partCopy["type"].(string)
			if (typeName == "output_text" || typeName == "text") && partCopy["text"] != nil {
				if text, ok := partCopy["text"].(string); ok {
					partCopy["text"] = reconvertJSONText(text, tupleSchema)
				}
			}
			contentCopy = append(contentCopy, partCopy)
		}
		itemCopy["content"] = contentCopy
		rewritten = append(rewritten, itemCopy)
	}
	return rewritten
}

func mapsClone(input map[string]any) map[string]any {
	cloned := make(map[string]any, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

func isEmptyCodexResponse(text string, toolCalls []gin.H, usage codex.Usage) bool {
	return strings.TrimSpace(text) == "" && len(toolCalls) == 0 && usage.OutputTokens == 0
}

func isEmptyResponsesOutput(raw any, usage codex.Usage) bool {
	output, ok := raw.([]any)
	if !ok {
		return usage.OutputTokens == 0
	}
	for _, rawItem := range output {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		itemType, _ := item["type"].(string)
		if itemType == "function_call" || itemType == "custom_tool_call" {
			return false
		}
		content, ok := item["content"].([]any)
		if !ok {
			continue
		}
		for _, rawContent := range content {
			part, ok := rawContent.(map[string]any)
			if !ok {
				continue
			}
			if text, _ := part["text"].(string); strings.TrimSpace(text) != "" {
				return false
			}
		}
	}
	return usage.OutputTokens == 0
}

func sleepForLease(ctx context.Context, wait time.Duration) bool {
	if wait <= 0 {
		return true
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func writeProxyError(c *gin.Context, status int, message string, useRateLimitType bool) {
	errType := "server_error"
	if useRateLimitType || status == http.StatusTooManyRequests {
		errType = "rate_limit_error"
	}
	c.JSON(status, gin.H{
		"error": gin.H{
			"message": message,
			"type":    errType,
		},
	})
}

func writeModelNotFoundError(c *gin.Context, modelID string) {
	modelID = strings.TrimSpace(modelID)
	message := "Model not found"
	if modelID != "" {
		message = "The model '" + modelID + "' does not exist or is not available for any synced account."
	}
	c.JSON(http.StatusBadRequest, gin.H{
		"error": gin.H{
			"message": message,
			"type":    "invalid_request_error",
			"param":   "model",
			"code":    "model_not_found",
		},
	})
}

func (s *Server) applyUpstreamError(accountID string, err error) proxyErrorDecision {
	upstreamErr, ok := err.(*codex.UpstreamError)
	if !ok {
		return proxyErrorDecision{
			Status:  http.StatusBadGateway,
			Message: err.Error(),
		}
	}
	s.applyRateLimitHeaders(accountID, upstreamErr.Header)
	bodyLower := strings.ToLower(upstreamErr.Body)
	if isModelUnsupported(upstreamErr.Body) {
		return proxyErrorDecision{
			Retry:   true,
			Status:  upstreamErr.StatusCode,
			Message: upstreamErr.Error(),
		}
	}
	switch upstreamErr.StatusCode {
	case http.StatusTooManyRequests:
		if retryUntil := retryUntilFromUpstream(upstreamErr); !retryUntil.IsZero() {
			_ = s.accounts.MarkRateLimited(accountID, retryUntil)
		}
		return proxyErrorDecision{
			Retry:            true,
			Status:           http.StatusTooManyRequests,
			Message:          upstreamErr.Error(),
			UseRateLimitType: true,
		}
	case http.StatusPaymentRequired:
		_ = s.accounts.UpdateStatus(accountID, auth.StatusQuotaExhausted)
		return proxyErrorDecision{
			Retry:   true,
			Status:  http.StatusPaymentRequired,
			Message: upstreamErr.Error(),
		}
	case http.StatusForbidden:
		if !strings.Contains(bodyLower, "<html") && !strings.Contains(bodyLower, "cf_chl") {
			_ = s.accounts.UpdateStatus(accountID, auth.StatusBanned)
			return proxyErrorDecision{
				Retry:   true,
				Status:  http.StatusForbidden,
				Message: upstreamErr.Error(),
			}
		}
	case http.StatusUnauthorized:
		newStatus := auth.StatusExpired
		if strings.Contains(bodyLower, "deactivated") {
			newStatus = auth.StatusBanned
		}
		_ = s.accounts.UpdateStatus(accountID, newStatus)
		return proxyErrorDecision{
			Retry:   true,
			Status:  http.StatusUnauthorized,
			Message: upstreamErr.Error(),
		}
	}
	return proxyErrorDecision{
		Status:  upstreamErr.StatusCode,
		Message: upstreamErr.Error(),
	}
}

func isModelUnsupported(body string) bool {
	lower := strings.ToLower(body)
	if !strings.Contains(lower, "model") {
		return false
	}
	return strings.Contains(lower, "not supported") ||
		strings.Contains(lower, "not_supported") ||
		strings.Contains(lower, "not available") ||
		strings.Contains(lower, "not_available")
}

func retryUntilFromUpstream(err *codex.UpstreamError) time.Time {
	if err == nil {
		return time.Time{}
	}
	if retryAfter := strings.TrimSpace(err.Header.Get("Retry-After")); retryAfter != "" {
		if seconds, convErr := time.ParseDuration(retryAfter + "s"); convErr == nil {
			return time.Now().Add(seconds)
		}
	}
	var payload map[string]any
	if jsonErr := json.Unmarshal([]byte(err.Body), &payload); jsonErr == nil {
		if errorBody, ok := payload["error"].(map[string]any); ok {
			if seconds := intFromAny(errorBody["resets_in_seconds"]); seconds > 0 {
				return time.Now().Add(time.Duration(seconds) * time.Second)
			}
			if resetAt := intFromAny(errorBody["resets_at"]); resetAt > 0 {
				return time.Unix(int64(resetAt), 0)
			}
		}
	}
	if resetAt := parsePrimaryResetAt(err.Header); !resetAt.IsZero() {
		return resetAt
	}
	return time.Now().Add(60 * time.Second)
}

func (s *Server) applyRateLimitHeaders(accountID string, header http.Header) {
	if accountID == "" || header == nil {
		return
	}
	primaryResetAt := parsePrimaryResetAt(header)
	primaryUsedPercent := parseHeaderFloat(header, "x-codex-primary-used-percent")
	primaryWindowMinutes := parseHeaderInt(header, "x-codex-primary-window-minutes")
	secondaryUsedPercent := parseHeaderFloat(header, "x-codex-secondary-used-percent")
	secondaryWindowMinutes := parseHeaderInt(header, "x-codex-secondary-window-minutes")
	secondaryResetAt := parseHeaderTime(header, "x-codex-secondary-reset-at")
	if !primaryResetAt.IsZero() || primaryUsedPercent > 0 || primaryWindowMinutes > 0 || !secondaryResetAt.IsZero() || secondaryUsedPercent > 0 || secondaryWindowMinutes > 0 {
		account, ok := s.accounts.Get(accountID)
		if ok {
			quota := accountQuotaFromAccount(account)
			quota.PrimaryRateLimit.Window.UsedPercent = floatPtr(primaryUsedPercent)
			if !primaryResetAt.IsZero() {
				quota.PrimaryRateLimit.Window.ResetAt = int64Ptr(primaryResetAt.Unix())
			}
			if primaryWindowMinutes > 0 {
				seconds := primaryWindowMinutes * 60
				quota.PrimaryRateLimit.Window.LimitWindowSeconds = intPtr(seconds)
			}
			quota.PrimaryRateLimit.LimitReached = primaryUsedPercent >= 100
			if !secondaryResetAt.IsZero() || secondaryUsedPercent > 0 || secondaryWindowMinutes > 0 {
				secondary := quota.SecondaryRateLimit
				if secondary == nil {
					secondary = &auth.AccountQuotaRateLimit{
						Allowed: quota.PrimaryRateLimit.Allowed,
					}
				}
				secondary.Window.UsedPercent = floatPtr(secondaryUsedPercent)
				if !secondaryResetAt.IsZero() {
					secondary.Window.ResetAt = int64Ptr(secondaryResetAt.Unix())
				}
				if secondaryWindowMinutes > 0 {
					seconds := secondaryWindowMinutes * 60
					secondary.Window.LimitWindowSeconds = intPtr(seconds)
				}
				secondary.LimitReached = secondaryUsedPercent >= 100
				quota.SecondaryRateLimit = secondary
			}
			_ = s.accounts.UpdateQuota(accountID, quota)
		}
	}
	if primaryUsedPercent >= 100 && !primaryResetAt.IsZero() {
		_ = s.accounts.MarkRateLimited(accountID, primaryResetAt)
	}
}

func (s *Server) applyRateLimitEvents(accountID string, events []codex.SSEEvent) {
	for _, event := range events {
		if event.Event == "codex.rate_limits" {
			s.applyRateLimitEvent(accountID, event.Data)
		}
	}
}

func (s *Server) applyRateLimitEvent(accountID string, data []byte) {
	if accountID == "" {
		return
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return
	}
	rateLimits, ok := payload["rate_limits"].(map[string]any)
	if !ok {
		return
	}
	primary, ok := rateLimits["primary"].(map[string]any)
	if !ok {
		return
	}
	var secondary map[string]any
	if typed, ok := rateLimits["secondary"].(map[string]any); ok {
		secondary = typed
	}
	usedPercent := 0.0
	if value, ok := primary["used_percent"].(float64); ok {
		usedPercent = value
	}
	resetAt := int64(intFromAny(primary["reset_at"]))
	windowSeconds := intFromAny(primary["window_minutes"]) * 60
	account, ok := s.accounts.Get(accountID)
	if ok {
		quota := accountQuotaFromAccount(account)
		quota.PrimaryRateLimit.Window.UsedPercent = floatPtr(usedPercent)
		if resetAt > 0 {
			quota.PrimaryRateLimit.Window.ResetAt = int64Ptr(resetAt)
		}
		if windowSeconds > 0 {
			quota.PrimaryRateLimit.Window.LimitWindowSeconds = intPtr(windowSeconds)
		}
		quota.PrimaryRateLimit.LimitReached = usedPercent >= 100
		if secondary != nil {
			secondaryUsedPercent := 0.0
			if value, ok := secondary["used_percent"].(float64); ok {
				secondaryUsedPercent = value
			}
			secondaryResetAt := int64(intFromAny(secondary["reset_at"]))
			secondaryWindowSeconds := intFromAny(secondary["window_minutes"]) * 60
			quota.SecondaryRateLimit = &auth.AccountQuotaRateLimit{
				Allowed:      quota.PrimaryRateLimit.Allowed,
				LimitReached: secondaryUsedPercent >= 100,
				Window: auth.AccountQuotaWindow{
					UsedPercent:        floatPtr(secondaryUsedPercent),
					ResetAt:            int64PtrIfPositive(secondaryResetAt),
					LimitWindowSeconds: intPtrIfPositive(secondaryWindowSeconds),
				},
			}
		}
		_ = s.accounts.UpdateQuota(accountID, quota)
	}
	if usedPercent >= 100 && resetAt > 0 {
		_ = s.accounts.MarkRateLimited(accountID, time.Unix(resetAt, 0))
	}
}

func accountQuotaFromUsage(payload codex.UsageResponse) auth.AccountQuota {
	quota := auth.AccountQuota{
		PlanType: payload.PlanType,
		PrimaryRateLimit: auth.AccountQuotaRateLimit{
			Allowed:      payload.RateLimit.Allowed,
			LimitReached: payload.RateLimit.LimitReached,
			Window:       quotaWindowFromUsage(payload.RateLimit.PrimaryWindow),
		},
	}
	if payload.RateLimit.SecondaryWindow != nil {
		quota.SecondaryRateLimit = &auth.AccountQuotaRateLimit{
			Allowed:      payload.RateLimit.Allowed,
			LimitReached: payload.RateLimit.SecondaryWindow.UsedPercent >= 100,
			Window:       quotaWindowFromUsage(payload.RateLimit.SecondaryWindow),
		}
	}
	return quota
}

func accountQuotaFromAccount(account auth.Account) auth.AccountQuota {
	if account.Quota != nil {
		return *auth.CloneQuota(account.Quota)
	}
	return auth.AccountQuota{
		PlanType: account.PlanType,
		PrimaryRateLimit: auth.AccountQuotaRateLimit{
			Allowed: true,
		},
	}
}

func quotaWindowFromUsage(window *codex.UsageRateWindow) auth.AccountQuotaWindow {
	if window == nil {
		return auth.AccountQuotaWindow{}
	}
	return auth.AccountQuotaWindow{
		UsedPercent:        floatPtr(window.UsedPercent),
		ResetAt:            int64Ptr(window.ResetAt),
		LimitWindowSeconds: intPtr(window.LimitWindowSeconds),
	}
}

func floatPtr(value float64) *float64 {
	return &value
}

func int64Ptr(value int64) *int64 {
	return &value
}

func int64PtrIfPositive(value int64) *int64 {
	if value <= 0 {
		return nil
	}
	return &value
}

func intPtrIfPositive(value int) *int {
	if value <= 0 {
		return nil
	}
	return &value
}

func parsePrimaryResetAt(header http.Header) time.Time {
	resetAt := int64(parseHeaderInt(header, "x-codex-primary-reset-at"))
	if resetAt <= 0 {
		return time.Time{}
	}
	return time.Unix(resetAt, 0)
}

func parseHeaderTime(header http.Header, key string) time.Time {
	value := int64(parseHeaderInt(header, key))
	if value <= 0 {
		return time.Time{}
	}
	return time.Unix(value, 0)
}

func parseHeaderInt(header http.Header, key string) int {
	value := strings.TrimSpace(header.Get(key))
	if value == "" {
		return 0
	}
	if parsed, err := strconv.Atoi(value); err == nil {
		return parsed
	}
	return 0
}

func parseHeaderFloat(header http.Header, key string) float64 {
	value := strings.TrimSpace(header.Get(key))
	if value == "" {
		return 0
	}
	if parsed, err := strconv.ParseFloat(value, 64); err == nil {
		return parsed
	}
	return 0
}

func intFromAny(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	default:
		return 0
	}
}

func streamTupleAwareResponsesEvent(w io.Writer, event codex.SSEEvent, tupleSchema map[string]any) bool {
	if tupleSchema == nil {
		writeRawSSEEvent(w, event)
		return true
	}
	if event.Event == "response.output_text.delta" {
		return true
	}
	if event.Event == "response.completed" {
		var payload map[string]any
		if err := json.Unmarshal(event.Data, &payload); err == nil {
			if response, ok := payload["response"].(map[string]any); ok {
				if output, ok := response["output"].([]any); ok {
					if text := firstOutputText(output); text != "" {
						reconverted := reconvertJSONText(text, tupleSchema)
						_, _ = io.WriteString(w, "event: response.output_text.delta\n")
						_, _ = io.WriteString(w, "data: "+string(mustJSON(map[string]any{
							"type":  "response.output_text.delta",
							"delta": reconverted,
						}))+"\n\n")
					}
				}
			}
		}
	}
	writeRawSSEEvent(w, event)
	return true
}

func mustJSON(value any) []byte {
	raw, _ := json.Marshal(value)
	return raw
}
