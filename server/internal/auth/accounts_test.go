package auth

import (
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/jushjay/prism/internal/config"
	"github.com/jushjay/prism/internal/store"
)

func TestAcquireStrictPreferred(t *testing.T) {
	future := time.Now().Add(2 * time.Minute)
	pool := &AccountPool{
		cfg:        config.AuthConfig{MaxConcurrentPerAccount: 1},
		inFlight:   map[string]int{},
		lastIssued: map[string]time.Time{},
		accounts: []Account{
			{ID: "preferred", Status: StatusRateLimited, RateLimitedUntil: &future},
			{ID: "fallback", Status: StatusActive},
		},
	}

	if _, ok := pool.Acquire(AcquireOptions{PreferredID: "preferred", StrictPreferred: true}); ok {
		t.Fatalf("expected strict preferred acquire to fail when preferred account is unavailable")
	}

	lease, ok := pool.Acquire(AcquireOptions{PreferredID: "preferred"})
	if !ok {
		t.Fatalf("expected non-strict acquire to fall back")
	}
	if lease.ID != "fallback" {
		t.Fatalf("expected fallback account, got %s", lease.ID)
	}
}

func TestAcquireRespectsConcurrencyAndInterval(t *testing.T) {
	pool := &AccountPool{
		cfg:        config.AuthConfig{MaxConcurrentPerAccount: 1, RequestIntervalMs: 100},
		inFlight:   map[string]int{},
		lastIssued: map[string]time.Time{},
		accounts: []Account{
			{ID: "solo", Status: StatusActive},
		},
	}

	lease, ok := pool.Acquire(AcquireOptions{})
	if !ok {
		t.Fatalf("expected first acquire to succeed")
	}
	if lease.Wait != 0 {
		t.Fatalf("expected first acquire wait to be zero, got %s", lease.Wait)
	}

	if _, ok := pool.Acquire(AcquireOptions{}); ok {
		t.Fatalf("expected acquire to fail while max concurrency is exhausted")
	}

	pool.Release("solo")
	lease, ok = pool.Acquire(AcquireOptions{})
	if !ok {
		t.Fatalf("expected acquire to succeed after release")
	}
	if lease.Wait <= 0 {
		t.Fatalf("expected second acquire to return request interval wait, got %s", lease.Wait)
	}
}

func TestAcquireUsesTierPriority(t *testing.T) {
	pool := &AccountPool{
		cfg:        config.AuthConfig{MaxConcurrentPerAccount: 1, TierPriority: []string{"plus", "free"}},
		inFlight:   map[string]int{},
		lastIssued: map[string]time.Time{},
		accounts: []Account{
			{ID: "free-1", Status: StatusActive, PlanType: "free"},
			{ID: "plus-1", Status: StatusActive, PlanType: "plus"},
		},
	}

	lease, ok := pool.Acquire(AcquireOptions{})
	if !ok {
		t.Fatalf("expected acquire to succeed")
	}
	if lease.ID != "plus-1" {
		t.Fatalf("expected tier-priority account, got %s", lease.ID)
	}
}

func TestAcquireSkipsUserDisabledAccounts(t *testing.T) {
	pool := &AccountPool{
		cfg:        config.AuthConfig{MaxConcurrentPerAccount: 1},
		inFlight:   map[string]int{},
		lastIssued: map[string]time.Time{},
		accounts: []Account{
			{ID: "disabled-1", Status: StatusActive, DisabledByUser: true},
			{ID: "active-1", Status: StatusActive},
		},
	}

	lease, ok := pool.Acquire(AcquireOptions{})
	if !ok {
		t.Fatalf("expected acquire to succeed")
	}
	if lease.ID != "active-1" {
		t.Fatalf("expected enabled account, got %s", lease.ID)
	}
}

func TestUpdateProfileUpdatesEditableFieldsAndPreservesRuntimeState(t *testing.T) {
	pool, err := NewAccountPool(
		filepath.Join(t.TempDir(), "accounts.json"),
		store.NewJSONStore(),
		config.AuthConfig{},
	)
	if err != nil {
		t.Fatalf("new account pool: %v", err)
	}
	account, err := pool.AddAccount(testJWT(map[string]any{
		"sub":                "user-original",
		"chatgpt_account_id": "acct-original",
		"email":              "original@example.com",
		"exp":                float64(time.Now().Add(time.Hour).Unix()),
	}), "refresh-token")
	if err != nil {
		t.Fatalf("add account: %v", err)
	}
	if err := pool.RecordUsage(account.ID, 12, 7); err != nil {
		t.Fatalf("record usage: %v", err)
	}

	label := "Main Plus"
	email := "renamed@example.com"
	planType := "plus"
	proxyKey := "proxy-renamed"
	enabled := false
	updated, err := pool.UpdateProfile(account.ID, AccountProfileUpdate{
		Label:       &label,
		Email:       &email,
		PlanType:    &planType,
		ProxyAPIKey: &proxyKey,
		Enabled:     &enabled,
	})
	if err != nil {
		t.Fatalf("update profile: %v", err)
	}

	if updated.Label != label || updated.Email != email || updated.PlanType != planType {
		t.Fatalf("editable fields not updated: %+v", updated)
	}
	if updated.ProxyAPIKey != proxyKey {
		t.Fatalf("expected proxy key %q, got %q", proxyKey, updated.ProxyAPIKey)
	}
	if !updated.DisabledByUser {
		t.Fatalf("expected account to be disabled")
	}
	if updated.AccessToken != account.AccessToken || updated.RefreshToken != account.RefreshToken {
		t.Fatalf("expected tokens to be preserved")
	}
	if updated.Usage.InputTokens != 12 || updated.Usage.OutputTokens != 7 {
		t.Fatalf("expected usage to be preserved, got %+v", updated.Usage)
	}
}

func TestAddAndUpdateCustomAccount(t *testing.T) {
	pool, err := NewAccountPool(
		filepath.Join(t.TempDir(), "accounts.json"),
		store.NewJSONStore(),
		config.AuthConfig{},
	)
	if err != nil {
		t.Fatalf("new account pool: %v", err)
	}

	account, err := pool.AddCustomAccount(CustomAccountInput{
		Label:              "OpenRouter",
		CustomBaseURL:      "https://openrouter.ai/api/v1/",
		CustomAPIKey:       "upstream-key",
		CustomEndpointType: "v1/chat",
		CustomUserAgent:    "OpenRouter-Agent/1.0",
		Enabled:            true,
	})
	if err != nil {
		t.Fatalf("add custom account: %v", err)
	}
	if account.Provider != ProviderCustom {
		t.Fatalf("expected custom provider, got %q", account.Provider)
	}
	if account.CustomBaseURL != "https://openrouter.ai/api/v1" {
		t.Fatalf("expected normalized base url, got %q", account.CustomBaseURL)
	}
	if account.CustomEndpointType != "/v1/chat/completions" {
		t.Fatalf("expected normalized endpoint path, got %q", account.CustomEndpointType)
	}
	if account.CustomUserAgent != "OpenRouter-Agent/1.0" {
		t.Fatalf("expected custom user agent to persist, got %q", account.CustomUserAgent)
	}
	if account.DisabledByUser {
		t.Fatalf("expected account to be enabled")
	}

	nextType := "responses"
	nextUserAgent := "OpenRouter-Agent/2.0"
	updated, err := pool.UpdateProfile(account.ID, AccountProfileUpdate{
		CustomEndpointType: &nextType,
		CustomUserAgent:    &nextUserAgent,
	})
	if err != nil {
		t.Fatalf("update custom account: %v", err)
	}
	if updated.CustomAPIKey != "upstream-key" {
		t.Fatalf("expected custom api key to be preserved")
	}
	if updated.CustomEndpointType != "/v1/responses" {
		t.Fatalf("expected custom endpoint path to update, got %q", updated.CustomEndpointType)
	}
	if updated.CustomUserAgent != nextUserAgent {
		t.Fatalf("expected custom user agent to update, got %q", updated.CustomUserAgent)
	}
}

func TestBeginRefreshMarksRefreshingAndPreventsReuse(t *testing.T) {
	pool, err := NewAccountPool(
		filepath.Join(t.TempDir(), "accounts.json"),
		store.NewJSONStore(),
		config.AuthConfig{},
	)
	if err != nil {
		t.Fatalf("new account pool: %v", err)
	}
	pool.accounts = []Account{{ID: "acct-1", Status: StatusActive, RefreshToken: "rtok"}}

	account, ok := pool.BeginRefresh("acct-1")
	if !ok {
		t.Fatalf("expected begin refresh to succeed")
	}
	if account.ID != "acct-1" {
		t.Fatalf("unexpected account id %s", account.ID)
	}
	updated, _ := pool.Get("acct-1")
	if updated.Status != StatusRefreshing {
		t.Fatalf("expected status refreshing, got %s", updated.Status)
	}
	if _, ok := pool.BeginRefresh("acct-1"); ok {
		t.Fatalf("expected second begin refresh to be rejected")
	}
}

func TestAddAccountExtractsOpenAIProfileClaims(t *testing.T) {
	pool, err := NewAccountPool(
		filepath.Join(t.TempDir(), "accounts.json"),
		store.NewJSONStore(),
		config.AuthConfig{},
	)
	if err != nil {
		t.Fatalf("new account pool: %v", err)
	}

	account, err := pool.AddAccount(testJWT(map[string]any{
		"exp": float64(time.Now().Add(2 * time.Hour).Unix()),
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acct-123",
			"chatgpt_plan_type":  "plus",
			"chatgpt_user_id":    "user-123",
		},
		"https://api.openai.com/profile": map[string]any{
			"email": "person@example.com",
		},
	}), "refresh-token")
	if err != nil {
		t.Fatalf("add account: %v", err)
	}

	if account.Email != "person@example.com" {
		t.Fatalf("expected extracted email, got %q", account.Email)
	}
	if account.AccountID != "acct-123" {
		t.Fatalf("expected extracted account id, got %q", account.AccountID)
	}
	if account.UserID != "user-123" {
		t.Fatalf("expected extracted user id, got %q", account.UserID)
	}
	if account.PlanType != "plus" {
		t.Fatalf("expected extracted plan type, got %q", account.PlanType)
	}
}

func TestNewAccountPoolBackfillsLegacyMetadata(t *testing.T) {
	file := filepath.Join(t.TempDir(), "accounts.json")
	legacyToken := testJWT(map[string]any{
		"exp": float64(time.Now().Add(90 * time.Minute).Unix()),
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acct-legacy",
			"chatgpt_plan_type":  "pro",
			"chatgpt_user_id":    "user-legacy",
		},
		"https://api.openai.com/profile": map[string]any{
			"email": "legacy@example.com",
		},
	})
	jsonStore := store.NewJSONStore()
	if err := jsonStore.Save(file, []Account{
		{
			ID:          "legacy",
			UserID:      "google-oauth2|old",
			Email:       "unknown@openai.local",
			AccessToken: legacyToken,
			Status:      StatusActive,
			PlanType:    "unknown",
		},
	}); err != nil {
		t.Fatalf("seed legacy account: %v", err)
	}

	pool, err := NewAccountPool(file, jsonStore, config.AuthConfig{})
	if err != nil {
		t.Fatalf("new account pool: %v", err)
	}
	account, ok := pool.Get("legacy")
	if !ok {
		t.Fatalf("expected migrated account to exist")
	}
	if account.Email != "legacy@example.com" {
		t.Fatalf("expected backfilled email, got %q", account.Email)
	}
	if account.AccountID != "acct-legacy" {
		t.Fatalf("expected backfilled account id, got %q", account.AccountID)
	}
	if account.UserID != "user-legacy" {
		t.Fatalf("expected backfilled user id, got %q", account.UserID)
	}
	if account.PlanType != "pro" {
		t.Fatalf("expected backfilled plan type, got %q", account.PlanType)
	}
}

func TestNewAccountPoolNormalizesLegacyCustomEndpointInBaseURL(t *testing.T) {
	file := filepath.Join(t.TempDir(), "accounts.json")
	jsonStore := store.NewJSONStore()
	if err := jsonStore.Save(file, []Account{
		{
			ID:                 "legacy-custom",
			Provider:           ProviderCustom,
			Status:             StatusActive,
			PlanType:           "custom",
			UsageIdentity:      "custom:legacy-custom",
			CustomBaseURL:      "https://fflycode.ai-tols.ggff.net/v1/responses",
			CustomAPIKey:       "secret",
			CustomEndpointType: "/v1/responses",
		},
	}); err != nil {
		t.Fatalf("seed legacy custom account: %v", err)
	}

	pool, err := NewAccountPool(file, jsonStore, config.AuthConfig{})
	if err != nil {
		t.Fatalf("new account pool: %v", err)
	}
	account, ok := pool.Get("legacy-custom")
	if !ok {
		t.Fatalf("expected migrated custom account to exist")
	}
	if account.CustomBaseURL != "https://fflycode.ai-tols.ggff.net" {
		t.Fatalf("expected normalized custom base url, got %q", account.CustomBaseURL)
	}
	if account.CustomEndpointType != "/v1/responses" {
		t.Fatalf("expected normalized custom endpoint path, got %q", account.CustomEndpointType)
	}
}

func TestAddAccountPreservesUsageForExistingAccount(t *testing.T) {
	pool, err := NewAccountPool(
		filepath.Join(t.TempDir(), "accounts.json"),
		store.NewJSONStore(),
		config.AuthConfig{},
	)
	if err != nil {
		t.Fatalf("new account pool: %v", err)
	}

	token := testJWT(map[string]any{
		"exp": float64(time.Now().Add(2 * time.Hour).Unix()),
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acct-preserve",
			"chatgpt_user_id":    "user-preserve",
		},
	})
	account, err := pool.AddAccount(token, "refresh-1")
	if err != nil {
		t.Fatalf("add account: %v", err)
	}
	if err := pool.RecordUsage(account.ID, 123, 45); err != nil {
		t.Fatalf("record usage: %v", err)
	}

	replaced, err := pool.AddAccount(token, "refresh-2")
	if err != nil {
		t.Fatalf("replace account: %v", err)
	}
	if replaced.Usage.RequestCount != 1 {
		t.Fatalf("expected request count to be preserved, got %d", replaced.Usage.RequestCount)
	}
	if replaced.Usage.InputTokens != 123 || replaced.Usage.OutputTokens != 45 {
		t.Fatalf("expected usage totals to be preserved, got %+v", replaced.Usage)
	}
}

func TestAddAccountInheritsUsageFromSameEmailAfterReAdd(t *testing.T) {
	pool, err := NewAccountPool(
		filepath.Join(t.TempDir(), "accounts.json"),
		store.NewJSONStore(),
		config.AuthConfig{},
	)
	if err != nil {
		t.Fatalf("new account pool: %v", err)
	}

	now := time.Now()
	pool.accounts = []Account{
		{
			ID:     "old-id",
			Email:  "zhu0530sj@gmail.com",
			Status: StatusActive,
			Usage: AccountUsage{
				RequestCount: 100,
				InputTokens:  8398654,
				OutputTokens: 46283,
				LastUsedAt:   &now,
			},
		},
	}

	token := testJWT(map[string]any{
		"exp": float64(time.Now().Add(2 * time.Hour).Unix()),
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acct-new",
			"chatgpt_user_id":    "user-new",
		},
		"https://api.openai.com/profile": map[string]any{
			"email": "zhu0530sj@gmail.com",
		},
	})

	account, err := pool.AddAccount(token, "refresh-new")
	if err != nil {
		t.Fatalf("add account: %v", err)
	}
	if account.ID == "old-id" {
		t.Fatalf("expected re-added account to use a new internal id")
	}
	if account.Usage.RequestCount != 100 || account.Usage.InputTokens != 8398654 || account.Usage.OutputTokens != 46283 {
		t.Fatalf("expected inherited usage totals, got %+v", account.Usage)
	}
	if account.Usage.LastUsedAt == nil || !account.Usage.LastUsedAt.Equal(now) {
		t.Fatalf("expected inherited last used timestamp, got %+v", account.Usage.LastUsedAt)
	}
}

func TestReauthenticateAccountUpdatesExistingOpenAIAccount(t *testing.T) {
	pool, err := NewAccountPool(
		filepath.Join(t.TempDir(), "accounts.json"),
		store.NewJSONStore(),
		config.AuthConfig{},
	)
	if err != nil {
		t.Fatalf("new account pool: %v", err)
	}

	original, err := pool.AddAccount(testJWT(map[string]any{
		"exp": float64(time.Now().Add(30 * time.Minute).Unix()),
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acct-old",
			"chatgpt_user_id":    "user-old",
		},
		"https://api.openai.com/profile": map[string]any{
			"email": "old@example.com",
		},
	}), "refresh-old")
	if err != nil {
		t.Fatalf("add account: %v", err)
	}
	if err := pool.RecordUsage(original.ID, 9, 4); err != nil {
		t.Fatalf("record usage: %v", err)
	}
	if err := pool.UpdateStatus(original.ID, StatusExpired); err != nil {
		t.Fatalf("mark expired: %v", err)
	}

	updated, err := pool.ReauthenticateAccount(original.ID, testJWT(map[string]any{
		"exp": float64(time.Now().Add(2 * time.Hour).Unix()),
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acct-new",
			"chatgpt_user_id":    "user-new",
		},
		"https://api.openai.com/profile": map[string]any{
			"email": "new@example.com",
		},
	}), "refresh-new")
	if err != nil {
		t.Fatalf("reauthenticate account: %v", err)
	}

	if updated.ID != original.ID {
		t.Fatalf("expected same internal id, got %q want %q", updated.ID, original.ID)
	}
	if updated.Status != StatusActive {
		t.Fatalf("expected active status, got %q", updated.Status)
	}
	if updated.Email != "new@example.com" || updated.AccountID != "acct-new" || updated.UserID != "user-new" {
		t.Fatalf("expected refreshed claims, got %+v", updated)
	}
	if updated.RefreshToken != "refresh-new" {
		t.Fatalf("expected refresh token to update, got %q", updated.RefreshToken)
	}
	if updated.Usage.RequestCount != 1 || updated.Usage.InputTokens != 9 || updated.Usage.OutputTokens != 4 {
		t.Fatalf("expected usage preserved, got %+v", updated.Usage)
	}
	if len(pool.List()) != 1 {
		t.Fatalf("expected no extra accounts after reauthentication")
	}
}

func TestReauthenticateAccountRejectsCustomProvider(t *testing.T) {
	pool, err := NewAccountPool(
		filepath.Join(t.TempDir(), "accounts.json"),
		store.NewJSONStore(),
		config.AuthConfig{},
	)
	if err != nil {
		t.Fatalf("new account pool: %v", err)
	}
	account, err := pool.AddCustomAccount(CustomAccountInput{
		Label:              "Custom",
		CustomBaseURL:      "https://api.example.com",
		CustomAPIKey:       "secret",
		CustomEndpointType: "responses",
		Enabled:            true,
	})
	if err != nil {
		t.Fatalf("add custom account: %v", err)
	}

	if _, err := pool.ReauthenticateAccount(account.ID, testJWT(map[string]any{}), "refresh"); err == nil {
		t.Fatalf("expected custom account reauthentication to fail")
	}
}

func testJWT(claims map[string]any) string {
	raw, _ := json.Marshal(claims)
	return "test." + base64.RawURLEncoding.EncodeToString(raw) + ".sig"
}
