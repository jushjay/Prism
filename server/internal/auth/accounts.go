package auth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/rand"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jushjay/prism/internal/config"
	"github.com/jushjay/prism/internal/store"
)

type AccountStatus string
type AccountProvider string

const (
	StatusActive         AccountStatus = "active"
	StatusExpired        AccountStatus = "expired"
	StatusQuotaExhausted AccountStatus = "quota_exhausted"
	StatusRateLimited    AccountStatus = "rate_limited"
	StatusRefreshing     AccountStatus = "refreshing"
	StatusDisabled       AccountStatus = "disabled"
	StatusBanned         AccountStatus = "banned"

	ProviderOpenAI AccountProvider = "openai"
	ProviderCustom AccountProvider = "custom"
)

type Account struct {
	ID                 string            `json:"id"`
	Provider           AccountProvider   `json:"provider,omitempty"`
	UserID             string            `json:"user_id,omitempty"`
	AccountID          string            `json:"account_id,omitempty"`
	Email              string            `json:"email,omitempty"`
	UsageIdentity      string            `json:"usage_identity,omitempty"`
	DisabledByUser     bool              `json:"disabled_by_user,omitempty"`
	AccessToken        string            `json:"access_token"`
	RefreshToken       string            `json:"refresh_token,omitempty"`
	PlanType           string            `json:"plan_type,omitempty"`
	Status             AccountStatus     `json:"status"`
	ProxyAPIKey        string            `json:"proxy_api_key"`
	Label              string            `json:"label,omitempty"`
	CustomBaseURL      string            `json:"custom_base_url,omitempty"`
	CustomAPIKey       string            `json:"custom_api_key,omitempty"`
	CustomEndpointType string            `json:"custom_endpoint_type,omitempty"`
	CustomUserAgent    string            `json:"custom_user_agent,omitempty"`
	CustomModel        string            `json:"custom_model,omitempty"`
	CustomHeaders      map[string]string `json:"custom_headers,omitempty"`
	Usage              AccountUsage      `json:"usage"`
	Quota              *AccountQuota     `json:"quota,omitempty"`
	QuotaFetchedAt     *time.Time        `json:"quota_fetched_at,omitempty"`
	CreatedAt          time.Time         `json:"created_at"`
	UpdatedAt          time.Time         `json:"updated_at"`
	ExpiresAt          time.Time         `json:"expires_at"`
	LastRefreshAt      *time.Time        `json:"last_refresh_at,omitempty"`
	RateLimitedUntil   *time.Time        `json:"rate_limited_until,omitempty"`
}

type AccountUsage struct {
	RequestCount       int        `json:"request_count"`
	InputTokens        int        `json:"input_tokens"`
	OutputTokens       int        `json:"output_tokens"`
	EmptyResponseCount int        `json:"empty_response_count"`
	LastUsedAt         *time.Time `json:"last_used_at,omitempty"`
}

type AccountQuotaWindow struct {
	UsedPercent        *float64 `json:"used_percent,omitempty"`
	ResetAt            *int64   `json:"reset_at,omitempty"`
	LimitWindowSeconds *int     `json:"limit_window_seconds,omitempty"`
}

type AccountQuotaRateLimit struct {
	Allowed      bool               `json:"allowed"`
	LimitReached bool               `json:"limit_reached"`
	Window       AccountQuotaWindow `json:"window"`
}

type AccountQuota struct {
	PlanType           string                 `json:"plan_type"`
	PrimaryRateLimit   AccountQuotaRateLimit  `json:"primary_rate_limit"`
	SecondaryRateLimit *AccountQuotaRateLimit `json:"secondary_rate_limit,omitempty"`
}

type UsageAccountSnapshot struct {
	AccountID     string          `json:"account_id"`
	Provider      AccountProvider `json:"provider"`
	UsageIdentity string          `json:"usage_identity"`
	DisplayName   string          `json:"display_name"`
	Label         string          `json:"label,omitempty"`
	Email         string          `json:"email,omitempty"`
	UpstreamID    string          `json:"upstream_account_id,omitempty"`
	CustomBaseURL string          `json:"custom_base_url,omitempty"`
	ProxyAPIKey   string          `json:"proxy_api_key,omitempty"`
}

type PoolSummary struct {
	Active         int `json:"active"`
	Expired        int `json:"expired"`
	QuotaExhausted int `json:"quota_exhausted"`
	RateLimited    int `json:"rate_limited"`
	Refreshing     int `json:"refreshing"`
	Disabled       int `json:"disabled"`
	Banned         int `json:"banned"`
	Total          int `json:"total"`
}

type AccountPool struct {
	mu         sync.RWMutex
	accounts   []Account
	index      int
	file       string
	store      store.StateStore
	cfg        config.AuthConfig
	inFlight   map[string]int
	lastIssued map[string]time.Time
}

type AccountProfileUpdate struct {
	Provider           *string
	UserID             *string
	AccountID          *string
	Email              *string
	PlanType           *string
	ProxyAPIKey        *string
	Label              *string
	Enabled            *bool
	CustomBaseURL      *string
	CustomAPIKey       *string
	CustomEndpointType *string
	CustomUserAgent    *string
}

type CustomAccountInput struct {
	Label              string
	PlanType           string
	CustomBaseURL      string
	CustomAPIKey       string
	CustomEndpointType string
	CustomUserAgent    string
	Enabled            bool
}

type AcquireOptions struct {
	PreferredID     string
	StrictPreferred bool
	ExcludeIDs      []string
}

type Lease struct {
	Account
	Wait time.Duration
}

func NewAccountPool(file string, stateStore store.StateStore, cfg config.AuthConfig) (*AccountPool, error) {
	pool := &AccountPool{
		file:       file,
		store:      stateStore,
		cfg:        cfg,
		inFlight:   map[string]int{},
		lastIssued: map[string]time.Time{},
	}
	if err := pool.store.Load(file, &pool.accounts); err != nil {
		return nil, err
	}
	if err := pool.backfillAccountMetadata(); err != nil {
		return nil, err
	}
	return pool, nil
}

func (p *AccountPool) AddAccount(accessToken, refreshToken string) (Account, error) {
	claims, _ := parseJWTClaims(accessToken)
	now := time.Now()
	account := Account{
		ID:           uuid.NewString(),
		Provider:     ProviderOpenAI,
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		Status:       StatusActive,
		ProxyAPIKey:  randomProxyAPIKey(),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	applyAccountClaims(&account, claims, now.Add(55*time.Minute))
	p.mu.Lock()
	defer p.mu.Unlock()

	inheritUsageLocked(&account, p.accounts)

	for i, existing := range p.accounts {
		if existing.AccountID != "" && existing.AccountID == account.AccountID && existing.UserID == account.UserID {
			account.ID = existing.ID
			if refreshToken == "" {
				account.RefreshToken = existing.RefreshToken
			}
			account.DisabledByUser = existing.DisabledByUser
			account.ProxyAPIKey = existing.ProxyAPIKey
			account.Usage = existing.Usage
			account.CreatedAt = existing.CreatedAt
			p.accounts[i] = account
			return account, p.persistLocked()
		}
	}
	p.accounts = append(p.accounts, account)
	return account, p.persistLocked()
}

func (p *AccountPool) AddCustomAccount(input CustomAccountInput) (Account, error) {
	now := time.Now()
	customBaseURL, customEndpointType := normalizeCustomEndpointConfig(input.CustomBaseURL, input.CustomEndpointType)
	account := Account{
		ID:                 uuid.NewString(),
		Provider:           ProviderCustom,
		UsageIdentity:      "custom:" + uuid.NewString(),
		PlanType:           strings.TrimSpace(input.PlanType),
		Status:             StatusActive,
		ProxyAPIKey:        randomProxyAPIKey(),
		Label:              strings.TrimSpace(input.Label),
		CustomBaseURL:      customBaseURL,
		CustomAPIKey:       strings.TrimSpace(input.CustomAPIKey),
		CustomEndpointType: customEndpointType,
		CustomUserAgent:    strings.TrimSpace(input.CustomUserAgent),
		DisabledByUser:     !input.Enabled,
		CreatedAt:          now,
		UpdatedAt:          now,
		ExpiresAt:          now.AddDate(100, 0, 0),
	}
	if account.PlanType == "" {
		account.PlanType = "custom"
	}
	if err := validateCustomAccount(account); err != nil {
		return Account{}, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.accounts = append(p.accounts, account)
	return account, p.persistLocked()
}

func (p *AccountPool) ReplaceAccount(account Account) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, candidate := range p.accounts {
		if candidate.ID == account.ID {
			account.UpdatedAt = time.Now()
			p.accounts[i] = account
			return p.persistLocked()
		}
	}
	return errors.New("account not found")
}

func (p *AccountPool) List() []Account {
	p.mu.RLock()
	defer p.mu.RUnlock()
	cloned := make([]Account, len(p.accounts))
	copy(cloned, p.accounts)
	return cloned
}

func (p *AccountPool) Get(id string) (Account, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, account := range p.accounts {
		if account.ID == id {
			return account, true
		}
	}
	return Account{}, false
}

func (p *AccountPool) Delete(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	filtered := p.accounts[:0]
	found := false
	for _, account := range p.accounts {
		if account.ID == id {
			found = true
			continue
		}
		filtered = append(filtered, account)
	}
	if !found {
		return errors.New("account not found")
	}
	p.accounts = filtered
	return p.persistLocked()
}

func (p *AccountPool) Clear() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.accounts = nil
	return p.persistLocked()
}

func (p *AccountPool) IsAuthenticated() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.accounts) > 0
}

func (p *AccountPool) Summary() PoolSummary {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var summary PoolSummary
	for _, account := range p.accounts {
		summary.Total++
		switch account.Status {
		case StatusActive:
			summary.Active++
		case StatusExpired:
			summary.Expired++
		case StatusQuotaExhausted:
			summary.QuotaExhausted++
		case StatusRateLimited:
			summary.RateLimited++
		case StatusRefreshing:
			summary.Refreshing++
		case StatusDisabled:
			summary.Disabled++
		case StatusBanned:
			summary.Banned++
		}
	}
	return summary
}

func (p *AccountPool) Acquire(options AcquireOptions) (Lease, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	excludeSet := map[string]struct{}{}
	for _, id := range options.ExcludeIDs {
		excludeSet[id] = struct{}{}
	}
	for i := range p.accounts {
		p.refreshAccountStatusLocked(&p.accounts[i], now)
	}

	if options.PreferredID != "" {
		for _, account := range p.accounts {
			if account.ID != options.PreferredID {
				continue
			}
			if _, excluded := excludeSet[account.ID]; excluded {
				return Lease{}, false
			}
			if isAccountUsable(account) && p.inFlight[account.ID] < p.maxConcurrentPerAccount() {
				return p.issueLeaseLocked(account, now), true
			}
			if options.StrictPreferred {
				return Lease{}, false
			}
		}
		if options.StrictPreferred {
			return Lease{}, false
		}
	}
	if len(p.accounts) == 0 {
		return Lease{}, false
	}
	candidates := make([]Account, 0, len(p.accounts))
	for _, account := range p.accounts {
		if !isAccountUsable(account) {
			continue
		}
		if p.inFlight[account.ID] >= p.maxConcurrentPerAccount() {
			continue
		}
		if _, excluded := excludeSet[account.ID]; excluded {
			continue
		}
		candidates = append(candidates, account)
	}
	candidates = p.filterByTierPriority(candidates)
	if len(candidates) == 0 {
		return Lease{}, false
	}
	for i := 0; i < len(p.accounts); i++ {
		idx := (p.index + i) % len(candidates)
		account := candidates[idx]
		if options.PreferredID != "" && account.ID == options.PreferredID {
			p.index = idx + 1
			return p.issueLeaseLocked(account, now), true
		}
	}
	account := candidates[p.index%len(candidates)]
	p.index = (p.index + 1) % len(candidates)
	return p.issueLeaseLocked(account, now), true
}

func (p *AccountPool) Release(id string) {
	if id == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.inFlight[id] <= 1 {
		delete(p.inFlight, id)
		return
	}
	p.inFlight[id]--
}

func (p *AccountPool) ValidateAPIKey(token string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, account := range p.accounts {
		if account.ProxyAPIKey == token {
			return true
		}
	}
	return false
}

func (p *AccountPool) UpdateStatus(id string, status AccountStatus) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.accounts {
		if p.accounts[i].ID == id {
			p.accounts[i].Status = status
			if status != StatusRateLimited {
				p.accounts[i].RateLimitedUntil = nil
			}
			p.accounts[i].UpdatedAt = time.Now()
			return p.persistLocked()
		}
	}
	return errors.New("account not found")
}

func (p *AccountPool) SetEnabled(id string, enabled bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.accounts {
		if p.accounts[i].ID != id {
			continue
		}
		p.accounts[i].DisabledByUser = !enabled
		p.accounts[i].UpdatedAt = time.Now()
		return p.persistLocked()
	}
	return errors.New("account not found")
}

func (p *AccountPool) UpdateProfile(id string, update AccountProfileUpdate) (Account, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.accounts {
		if p.accounts[i].ID != id {
			continue
		}
		next := p.accounts[i]
		if update.UserID != nil {
			next.UserID = strings.TrimSpace(*update.UserID)
		}
		if update.Provider != nil {
			next.Provider = AccountProvider(strings.TrimSpace(*update.Provider))
		}
		if update.AccountID != nil {
			next.AccountID = strings.TrimSpace(*update.AccountID)
		}
		if update.Email != nil {
			next.Email = strings.TrimSpace(*update.Email)
		}
		if update.PlanType != nil {
			next.PlanType = strings.TrimSpace(*update.PlanType)
		}
		if update.ProxyAPIKey != nil {
			value := strings.TrimSpace(*update.ProxyAPIKey)
			if value == "" {
				return Account{}, errors.New("proxy_api_key is required")
			}
			next.ProxyAPIKey = value
		}
		if update.Label != nil {
			next.Label = strings.TrimSpace(*update.Label)
		}
		if update.Enabled != nil {
			next.DisabledByUser = !*update.Enabled
		}
		if update.CustomBaseURL != nil {
			next.CustomBaseURL, next.CustomEndpointType = normalizeCustomEndpointConfig(*update.CustomBaseURL, next.CustomEndpointType)
		}
		if update.CustomAPIKey != nil {
			next.CustomAPIKey = strings.TrimSpace(*update.CustomAPIKey)
		}
		if update.CustomEndpointType != nil {
			next.CustomBaseURL, next.CustomEndpointType = normalizeCustomEndpointConfig(next.CustomBaseURL, *update.CustomEndpointType)
		}
		if update.CustomUserAgent != nil {
			next.CustomUserAgent = strings.TrimSpace(*update.CustomUserAgent)
		}
		if next.Provider == "" {
			next.Provider = ProviderOpenAI
		}
		if next.Provider == ProviderCustom {
			if err := validateCustomAccount(next); err != nil {
				return Account{}, err
			}
		}
		next.UpdatedAt = time.Now()
		p.accounts[i] = next
		if err := p.persistLocked(); err != nil {
			return Account{}, err
		}
		return next, nil
	}
	return Account{}, errors.New("account not found")
}

func (p *AccountPool) RecordUsage(id string, inputTokens, outputTokens int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.accounts {
		if p.accounts[i].ID != id {
			continue
		}
		now := time.Now()
		p.accounts[i].Usage.RequestCount++
		p.accounts[i].Usage.InputTokens += inputTokens
		p.accounts[i].Usage.OutputTokens += outputTokens
		p.accounts[i].Usage.LastUsedAt = &now
		p.accounts[i].UpdatedAt = now
		return p.persistLocked()
	}
	return errors.New("account not found")
}

func (p *AccountPool) RecordEmptyResponse(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.accounts {
		if p.accounts[i].ID != id {
			continue
		}
		p.accounts[i].Usage.EmptyResponseCount++
		p.accounts[i].UpdatedAt = time.Now()
		return p.persistLocked()
	}
	return errors.New("account not found")
}

func (p *AccountPool) UpdateQuota(id string, quota AccountQuota) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.accounts {
		if p.accounts[i].ID != id {
			continue
		}
		now := time.Now()
		p.accounts[i].Quota = cloneQuota(quota)
		p.accounts[i].QuotaFetchedAt = &now
		if strings.TrimSpace(quota.PlanType) != "" {
			p.accounts[i].PlanType = quota.PlanType
		}
		p.accounts[i].UpdatedAt = now
		return p.persistLocked()
	}
	return errors.New("account not found")
}

func (p *AccountPool) MarkRateLimited(id string, until time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.accounts {
		if p.accounts[i].ID == id {
			p.accounts[i].Status = StatusRateLimited
			p.accounts[i].RateLimitedUntil = &until
			p.accounts[i].UpdatedAt = time.Now()
			return p.persistLocked()
		}
	}
	return errors.New("account not found")
}

func (p *AccountPool) BeginRefresh(id string) (Account, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.accounts {
		if p.accounts[i].ID != id {
			continue
		}
		if p.accounts[i].RefreshToken == "" || p.accounts[i].Status == StatusRefreshing {
			return Account{}, false
		}
		p.accounts[i].Status = StatusRefreshing
		p.accounts[i].UpdatedAt = time.Now()
		_ = p.persistLocked()
		return p.accounts[i], true
	}
	return Account{}, false
}

func (p *AccountPool) UpdateTokens(id, accessToken, refreshToken string, expiresAt time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.accounts {
		if p.accounts[i].ID == id {
			now := time.Now()
			claims, _ := parseJWTClaims(accessToken)
			p.accounts[i].AccessToken = accessToken
			if refreshToken != "" {
				p.accounts[i].RefreshToken = refreshToken
			}
			p.accounts[i].Status = StatusActive
			p.accounts[i].LastRefreshAt = &now
			p.accounts[i].UpdatedAt = now
			applyAccountClaims(&p.accounts[i], claims, expiresAt)
			inheritUsageLocked(&p.accounts[i], p.accounts)
			return p.persistLocked()
		}
	}
	return errors.New("account not found")
}

func (p *AccountPool) ReauthenticateAccount(id, accessToken, refreshToken string) (Account, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	index := -1
	for i := range p.accounts {
		if p.accounts[i].ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return Account{}, errors.New("account not found")
	}

	existing := p.accounts[index]
	if existing.Provider == ProviderCustom {
		return Account{}, errors.New("custom account does not support oauth reauthentication")
	}

	claims, _ := parseJWTClaims(accessToken)
	now := time.Now()
	reauthenticated := existing
	reauthenticated.Provider = ProviderOpenAI
	reauthenticated.AccessToken = accessToken
	if strings.TrimSpace(refreshToken) != "" {
		reauthenticated.RefreshToken = refreshToken
	}
	reauthenticated.Status = StatusActive
	reauthenticated.LastRefreshAt = &now
	reauthenticated.UpdatedAt = now
	reauthenticated.RateLimitedUntil = nil
	applyAccountClaims(&reauthenticated, claims, now.Add(55*time.Minute))
	inheritUsageLocked(&reauthenticated, p.accounts)

	p.accounts[index] = reauthenticated
	if err := p.persistLocked(); err != nil {
		return Account{}, err
	}
	return reauthenticated, nil
}

func (p *AccountPool) ActiveForRefresh() []Account {
	p.mu.RLock()
	defer p.mu.RUnlock()
	list := make([]Account, 0, len(p.accounts))
	for _, account := range p.accounts {
		if account.RefreshToken != "" {
			list = append(list, account)
		}
	}
	return list
}

func (p *AccountPool) persistLocked() error {
	return p.store.Save(p.file, p.accounts)
}

func isAccountUsable(account Account) bool {
	if account.DisabledByUser {
		return false
	}
	if account.Status != StatusActive && account.Status != StatusRateLimited {
		return false
	}
	if account.Status == StatusRateLimited && account.RateLimitedUntil != nil && time.Now().Before(*account.RateLimitedUntil) {
		return false
	}
	return true
}

func parseJWTClaims(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil, errors.New("invalid JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, err
	}
	return claims, nil
}

func stringValue(claims map[string]any, key string) string {
	return claimString(claims, key)
}

func claimString(claims map[string]any, path ...string) string {
	var current any = claims
	for _, key := range path {
		asMap, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current, ok = asMap[key]
		if !ok {
			return ""
		}
	}
	asString, ok := current.(string)
	if !ok {
		return ""
	}
	return asString
}

func deriveExpiresAt(claims map[string]any, fallback time.Time) time.Time {
	if raw, ok := claims["exp"].(float64); ok {
		return time.Unix(int64(raw), 0)
	}
	return fallback
}

func derivePlanType(claims map[string]any) string {
	if plan := stringValue(claims, "plan_type"); plan != "" {
		return plan
	}
	if plan := claimString(claims, "https://api.openai.com/auth", "chatgpt_plan_type"); plan != "" {
		return plan
	}
	if plan := claimString(claims, "https://api.openai.com/profile", "chatgpt_plan_type"); plan != "" {
		return plan
	}
	return "unknown"
}

func deriveAccountID(claims map[string]any) string {
	if accountID := stringValue(claims, "chatgpt_account_id"); accountID != "" {
		return accountID
	}
	return claimString(claims, "https://api.openai.com/auth", "chatgpt_account_id")
}

func deriveUserID(claims map[string]any) string {
	candidates := [][]string{
		{"https://api.openai.com/auth", "chatgpt_user_id"},
		{"https://api.openai.com/profile", "chatgpt_user_id"},
		{"https://api.openai.com/auth", "user_id"},
		{"sub"},
	}
	for _, path := range candidates {
		if userID := claimString(claims, path...); userID != "" {
			return userID
		}
	}
	return ""
}

func deriveEmail(claims map[string]any) string {
	candidates := [][]string{
		{"email"},
		{"https://api.openai.com/profile", "email"},
	}
	for _, path := range candidates {
		if email := claimString(claims, path...); email != "" {
			return email
		}
	}
	return ""
}

func applyAccountClaims(account *Account, claims map[string]any, expiresFallback time.Time) {
	if claims != nil {
		if userID := deriveUserID(claims); userID != "" {
			account.UserID = userID
		}
		if accountID := deriveAccountID(claims); accountID != "" {
			account.AccountID = accountID
		}
		if email := deriveEmail(claims); email != "" {
			account.Email = email
		}
		account.PlanType = derivePlanType(claims)
		account.ExpiresAt = deriveExpiresAt(claims, expiresFallback)
	} else {
		account.ExpiresAt = expiresFallback
	}
	if account.Email == "" {
		account.Email = "unknown@openai.local"
	}
	if account.PlanType == "" {
		account.PlanType = "unknown"
	}
}

func (p *AccountPool) backfillAccountMetadata() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	changed := false
	for i := range p.accounts {
		if normalizeAccountMetadata(&p.accounts[i]) {
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return p.persistLocked()
}

func normalizeAccountMetadata(account *Account) bool {
	original := *account
	if account.Provider == "" {
		account.Provider = ProviderOpenAI
	}
	if account.Provider == ProviderCustom {
		if strings.TrimSpace(account.UsageIdentity) == "" {
			account.UsageIdentity = "custom:" + account.ID
		}
		account.CustomBaseURL, account.CustomEndpointType = normalizeCustomEndpointConfig(account.CustomBaseURL, account.CustomEndpointType)
		account.CustomAPIKey = strings.TrimSpace(account.CustomAPIKey)
		account.CustomUserAgent = strings.TrimSpace(account.CustomUserAgent)
		if account.PlanType == "" {
			account.PlanType = "custom"
		}
		if account.ExpiresAt.IsZero() {
			account.ExpiresAt = time.Now().AddDate(100, 0, 0)
		}
		if account.Status == "" {
			account.Status = StatusActive
		}
		return !accountsEqualForMetadata(original, *account)
	}
	account.UsageIdentity = ""
	claims, err := parseJWTClaims(account.AccessToken)
	if err == nil {
		applyAccountClaims(account, claims, account.ExpiresAt)
	} else {
		if account.Email == "" {
			account.Email = "unknown@openai.local"
		}
		if account.PlanType == "" {
			account.PlanType = "unknown"
		}
	}
	if account.Usage.LastUsedAt != nil && account.Usage.LastUsedAt.IsZero() {
		account.Usage.LastUsedAt = nil
	}
	return !accountsEqualForMetadata(original, *account)
}

func cloneQuota(quota AccountQuota) *AccountQuota {
	copied := quota
	if quota.SecondaryRateLimit != nil {
		secondary := *quota.SecondaryRateLimit
		copied.SecondaryRateLimit = &secondary
	}
	return &copied
}

func cloneUsage(usage AccountUsage) AccountUsage {
	cloned := usage
	if usage.LastUsedAt != nil {
		lastUsedAt := *usage.LastUsedAt
		cloned.LastUsedAt = &lastUsedAt
	}
	return cloned
}

func accountUsageEqual(before, after AccountUsage) bool {
	if before.RequestCount != after.RequestCount ||
		before.InputTokens != after.InputTokens ||
		before.OutputTokens != after.OutputTokens ||
		before.EmptyResponseCount != after.EmptyResponseCount {
		return false
	}
	if before.LastUsedAt == nil || after.LastUsedAt == nil {
		return before.LastUsedAt == nil && after.LastUsedAt == nil
	}
	return before.LastUsedAt.Equal(*after.LastUsedAt)
}

func normalizeUsageIdentity(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func buildUsageIdentity(account Account) string {
	switch account.Provider {
	case ProviderCustom:
		if value := strings.TrimSpace(account.UsageIdentity); value != "" {
			return value
		}
		return "custom:" + account.ID
	default:
		if value := normalizeUsageIdentity(account.AccountID); value != "" {
			return "openai:" + value
		}
		if value := normalizeUsageIdentity(account.Email); value != "" {
			return "openai_email:" + value
		}
		if value := strings.TrimSpace(account.ID); value != "" {
			return "openai_internal:" + value
		}
		return "openai:unknown"
	}
}

func buildUsageDisplayName(account Account) string {
	if value := strings.TrimSpace(account.Label); value != "" {
		return value
	}
	if value := strings.TrimSpace(account.Email); value != "" {
		return value
	}
	if value := strings.TrimSpace(account.AccountID); value != "" {
		return value
	}
	if value := strings.TrimSpace(account.CustomBaseURL); value != "" {
		return value
	}
	if value := strings.TrimSpace(account.ID); value != "" {
		return value
	}
	return "-"
}

func BuildUsageSnapshot(account Account) UsageAccountSnapshot {
	return UsageAccountSnapshot{
		AccountID:     account.ID,
		Provider:      account.Provider,
		UsageIdentity: buildUsageIdentity(account),
		DisplayName:   buildUsageDisplayName(account),
		Label:         strings.TrimSpace(account.Label),
		Email:         strings.TrimSpace(account.Email),
		UpstreamID:    strings.TrimSpace(account.AccountID),
		CustomBaseURL: strings.TrimSpace(account.CustomBaseURL),
		ProxyAPIKey:   strings.TrimSpace(account.ProxyAPIKey),
	}
}

func inheritUsageLocked(target *Account, accounts []Account) {
	email := normalizeUsageIdentity(target.Email)
	if email == "" {
		return
	}
	for _, existing := range accounts {
		if existing.ID == target.ID {
			continue
		}
		if normalizeUsageIdentity(existing.Email) != email {
			continue
		}
		if target.Usage.RequestCount == 0 && target.Usage.InputTokens == 0 && target.Usage.OutputTokens == 0 && target.Usage.EmptyResponseCount == 0 {
			target.Usage = cloneUsage(existing.Usage)
		}
		return
	}
}

func CloneQuota(quota *AccountQuota) *AccountQuota {
	if quota == nil {
		return nil
	}
	return cloneQuota(*quota)
}

func accountsEqualForMetadata(before, after Account) bool {
	return before.Provider == after.Provider &&
		before.UserID == after.UserID &&
		before.AccountID == after.AccountID &&
		before.Email == after.Email &&
		before.PlanType == after.PlanType &&
		before.CustomBaseURL == after.CustomBaseURL &&
		before.CustomAPIKey == after.CustomAPIKey &&
		before.CustomEndpointType == after.CustomEndpointType &&
		before.CustomUserAgent == after.CustomUserAgent &&
		before.ExpiresAt.Equal(after.ExpiresAt)
}

func validateCustomAccount(account Account) error {
	if account.Provider != ProviderCustom {
		return nil
	}
	if strings.TrimSpace(account.CustomBaseURL) == "" {
		return errors.New("custom_base_url is required")
	}
	parsed, err := url.Parse(strings.TrimSpace(account.CustomBaseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return errors.New("custom_base_url must be a valid URL")
	}
	if strings.TrimSpace(account.CustomAPIKey) == "" {
		return errors.New("custom_api_key is required")
	}
	if normalizeCustomEndpointType(account.CustomEndpointType) == "" {
		return errors.New("custom_endpoint_type is required")
	}
	return nil
}

func normalizeCustomEndpointType(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "/v1/responses"
	}
	if parsed, err := url.Parse(value); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		value = parsed.Path
	}
	switch strings.Trim(strings.ToLower(strings.TrimSpace(value)), "/") {
	case "", "responses", "v1/responses", "response", "v1/response":
		return "/v1/responses"
	case "chat", "chat_completions", "chat/completions", "v1/chat", "v1/chat/completions":
		return "/v1/chat/completions"
	default:
		cleaned := strings.TrimSpace(value)
		if !strings.HasPrefix(cleaned, "/") {
			cleaned = "/" + cleaned
		}
		return "/" + strings.Trim(strings.ToLower(strings.TrimSpace(cleaned)), "/")
	}
}

func normalizeCustomEndpointConfig(baseURL, endpointType string) (string, string) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	endpointType = normalizeCustomEndpointType(endpointType)
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return baseURL, endpointType
	}

	path := strings.TrimSpace(parsed.Path)
	if path == "" || path == "/" {
		return baseURL, endpointType
	}

	normalizedPath := normalizeCustomEndpointType(path)
	switch normalizedPath {
	case "/v1/responses", "/v1/chat/completions":
		parsed.Path = strings.TrimSuffix(strings.TrimSuffix(path, normalizedPath), "/")
		if parsed.Path == "" {
			parsed.Path = "/"
		}
		return strings.TrimRight(parsed.String(), "/"), normalizedPath
	default:
		return baseURL, endpointType
	}
}

func randomProxyAPIKey() string {
	return "prism_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

func init() {
	rand.Seed(time.Now().UnixNano())
}

func (p *AccountPool) issueLeaseLocked(account Account, now time.Time) Lease {
	wait := time.Duration(0)
	if last := p.lastIssued[account.ID]; !last.IsZero() {
		interval := time.Duration(p.cfg.RequestIntervalMs) * time.Millisecond
		if interval > 0 {
			wait = interval - now.Sub(last)
			if wait < 0 {
				wait = 0
			}
		}
	}
	p.lastIssued[account.ID] = now
	p.inFlight[account.ID]++
	return Lease{Account: account, Wait: wait}
}

func (p *AccountPool) maxConcurrentPerAccount() int {
	if p.cfg.MaxConcurrentPerAccount > 0 {
		return p.cfg.MaxConcurrentPerAccount
	}
	return 3
}

func (p *AccountPool) filterByTierPriority(candidates []Account) []Account {
	if len(candidates) == 0 || len(p.cfg.TierPriority) == 0 {
		return candidates
	}
	bestIndex := len(p.cfg.TierPriority) + 1
	for _, candidate := range candidates {
		idx := slices.Index(p.cfg.TierPriority, candidate.PlanType)
		if idx >= 0 && idx < bestIndex {
			bestIndex = idx
		}
	}
	if bestIndex > len(p.cfg.TierPriority) {
		return candidates
	}
	bestTier := p.cfg.TierPriority[bestIndex]
	filtered := make([]Account, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.PlanType == bestTier {
			filtered = append(filtered, candidate)
		}
	}
	if len(filtered) == 0 {
		return candidates
	}
	return filtered
}

func (p *AccountPool) refreshAccountStatusLocked(account *Account, now time.Time) {
	if account.Status == StatusRateLimited && account.RateLimitedUntil != nil && !now.Before(*account.RateLimitedUntil) {
		account.Status = StatusActive
		account.RateLimitedUntil = nil
		account.UpdatedAt = now
		_ = p.persistLocked()
	}
}
