package models

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/jushjay/prism/internal/auth"
	"github.com/jushjay/prism/internal/codex"
	"github.com/jushjay/prism/internal/config"
	"github.com/jushjay/prism/internal/store"
)

func TestServiceUpsertManualModelAndGlobalCatalog(t *testing.T) {
	dir := t.TempDir()
	svc := &Service{
		static: Catalog{
			Models: []Info{
				{ID: "gpt-5.4", DisplayName: "GPT-5.4", DefaultReasoningEffort: "medium", InputModalities: []string{"text"}},
			},
			Aliases: map[string]string{"default": "gpt-5.4"},
		},
		caches: map[string]AccountCatalogCache{
			"account-a": {
				AccountID: "account-a",
				Models: []Info{
					{ID: "gpt-5.5", DisplayName: "GPT-5.5", DefaultReasoningEffort: "high", InputModalities: []string{"text", "image"}},
				},
			},
		},
		store: store.NewJSONStore(),
		cfg: config.Config{
			Storage: config.StorageConfig{
				ManualModelsFile: filepath.Join(dir, "manual-models.json"),
				ModelCacheFile:   filepath.Join(dir, "model-cache.json"),
			},
		},
	}

	record, err := svc.UpsertManualModel(ManualModelInput{
		ID:                     "custom-enterprise-model",
		DisplayName:            "Custom Enterprise Model",
		DefaultReasoningEffort: "medium",
		InputModalities:        []string{"text"},
		OutputModalities:       []string{"text"},
	})
	if err != nil {
		t.Fatalf("expected manual model upsert to succeed: %v", err)
	}
	if record.RecordID == "" {
		t.Fatalf("expected manual model to get a record id")
	}

	catalog := svc.GlobalCatalog()
	if len(catalog.Models) != 3 {
		t.Fatalf("expected 3 merged models, got %d", len(catalog.Models))
	}

	if resolved, ok := catalog.Resolve("custom-enterprise-model"); !ok || resolved.DisplayName != "Custom Enterprise Model" {
		t.Fatalf("expected manual model to appear in global catalog")
	}
	if catalog.Aliases["default"] != "gpt-5.4" {
		t.Fatalf("expected aliases to be preserved in global catalog")
	}
}

func TestServiceResolveMappedModelID(t *testing.T) {
	dir := t.TempDir()
	accountPool := &auth.AccountPool{}
	svc := &Service{
		static: Catalog{
			Models: []Info{
				{ID: "gpt-5.4", DisplayName: "GPT-5.4"},
			},
		},
		mappings: []ModelMappingRecord{
			{
				RecordID:    "global-1",
				ModelName:   "got-5.4",
				TargetModel: "gpt-5.4",
				ApplyGlobal: true,
			},
			{
				RecordID:     "acct-1",
				ModelName:    "got-5.4",
				TargetModel:  "gpt-5.4-mini",
				ApplyGlobal:  false,
				AccountID:    "account-a",
				AccountEmail: "a@example.com",
			},
		},
		accounts: accountPool,
		store:    store.NewJSONStore(),
		cfg: config.Config{
			Storage: config.StorageConfig{
				ManualModelsFile:  filepath.Join(dir, "manual-models.json"),
				ModelCacheFile:    filepath.Join(dir, "model-cache.json"),
				ModelMappingsFile: filepath.Join(dir, "model-mappings.json"),
			},
		},
	}

	if resolved := svc.ResolveMappedModelID("got-5.4", "account-a"); resolved != "gpt-5.4-mini" {
		t.Fatalf("expected account mapping to override global mapping, got %q", resolved)
	}
	if resolved := svc.ResolveMappedModelID("got-5.4", "account-b"); resolved != "gpt-5.4" {
		t.Fatalf("expected global mapping for other accounts, got %q", resolved)
	}
	if resolved := svc.ResolveMappedModelID("gpt-5.4", "account-a"); resolved != "gpt-5.4" {
		t.Fatalf("expected unmapped model to pass through, got %q", resolved)
	}
}

func TestAccountSupportsModelUsesPersistedCatalog(t *testing.T) {
	dir := t.TempDir()
	sqliteStore, err := store.NewSQLiteStore(filepath.Join(dir, "state.db"), dir)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer sqliteStore.Close()

	accountPool, err := auth.NewAccountPool(
		filepath.Join(dir, "accounts.json"),
		sqliteStore,
		config.AuthConfig{},
	)
	if err != nil {
		t.Fatalf("NewAccountPool() error = %v", err)
	}
	openAIAccount, err := accountPool.AddAccount(testJWT(map[string]any{
		"sub":                "user-1",
		"chatgpt_account_id": "acct-openai",
		"email":              "openai@example.com",
		"exp":                float64(4102444800),
	}), "refresh-token")
	if err != nil {
		t.Fatalf("AddAccount() error = %v", err)
	}
	customAccount, err := accountPool.AddCustomAccount(auth.CustomAccountInput{
		Label:              "fflycode",
		CustomBaseURL:      "https://example.com",
		CustomAPIKey:       "secret",
		CustomEndpointType: "responses",
		Enabled:            true,
	})
	if err != nil {
		t.Fatalf("AddCustomAccount() error = %v", err)
	}

	client, err := codex.NewClient(config.Config{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	svc, err := NewService(
		config.Config{
			Storage: config.StorageConfig{
				ModelCacheFile: filepath.Join(dir, "model-cache.json"),
			},
		},
		Catalog{},
		accountPool,
		client,
		nil,
		sqliteStore.DB(),
		sqliteStore,
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
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
	`, customAccount.ID, "glm-5.1", "GLM-5.1", "model", "z-ai", now.Unix(), `{}`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert custom_account_models: %v", err)
	}
	if _, err := sqliteStore.DB().Exec(`
		INSERT INTO custom_account_model_sync_state (account_id, fetched_at, expires_at, last_error, updated_at)
		VALUES (?, ?, ?, '', ?)
	`, customAccount.ID, now.Format(time.RFC3339Nano), expires.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert custom_account_model_sync_state: %v", err)
	}

	if supported, known := svc.AccountSupportsModel(openAIAccount, "gpt-5.4"); !known || !supported {
		t.Fatalf("expected openai account to support gpt-5.4, got supported=%v known=%v", supported, known)
	}
	if supported, known := svc.AccountSupportsModel(openAIAccount, "glm-5.1"); !known || supported {
		t.Fatalf("expected openai account to reject glm-5.1, got supported=%v known=%v", supported, known)
	}
	if supported, known := svc.AccountSupportsModel(customAccount, "glm-5.1"); !known || !supported {
		t.Fatalf("expected custom account to support glm-5.1, got supported=%v known=%v", supported, known)
	}
}

func TestServiceUpsertModelMappingByRecordID(t *testing.T) {
	dir := t.TempDir()
	accountPool, err := auth.NewAccountPool(
		filepath.Join(dir, "accounts.json"),
		store.NewJSONStore(),
		config.AuthConfig{},
	)
	if err != nil {
		t.Fatalf("NewAccountPool() error = %v", err)
	}
	account, err := accountPool.AddAccount(testJWT(map[string]any{
		"sub":                "user-1",
		"chatgpt_account_id": "acct-1",
		"email":              "acct-1@example.com",
		"exp":                float64(4102444800),
	}), "refresh-token")
	if err != nil {
		t.Fatalf("AddAccount() error = %v", err)
	}

	svc := &Service{
		accounts: accountPool,
		store:    store.NewJSONStore(),
		cfg: config.Config{
			Storage: config.StorageConfig{
				ModelMappingsFile: filepath.Join(dir, "model-mappings.json"),
			},
		},
	}

	record, err := svc.UpsertModelMapping(ModelMappingInput{
		ModelName:   "got-5.4",
		TargetModel: "gpt-5.4",
		ApplyGlobal: true,
	})
	if err != nil {
		t.Fatalf("initial UpsertModelMapping() error = %v", err)
	}

	updated, err := svc.UpsertModelMapping(ModelMappingInput{
		RecordID:    record.RecordID,
		ModelName:   "team-gpt-5.4",
		TargetModel: "gpt-5.4-mini",
		ApplyGlobal: false,
		AccountID:   account.ID,
	})
	if err != nil {
		t.Fatalf("update UpsertModelMapping() error = %v", err)
	}
	if updated.RecordID != record.RecordID {
		t.Fatalf("updated record id = %q, want %q", updated.RecordID, record.RecordID)
	}
	if updated.ModelName != "team-gpt-5.4" || updated.TargetModel != "gpt-5.4-mini" {
		t.Fatalf("unexpected updated mapping %+v", updated)
	}
	if updated.ApplyGlobal {
		t.Fatalf("expected updated mapping to become account scoped")
	}
	if updated.AccountID != account.ID {
		t.Fatalf("updated account id = %q, want %q", updated.AccountID, account.ID)
	}
	if len(svc.ListModelMappings(account.ID)) != 1 {
		t.Fatalf("expected one mapping after record update, got %d", len(svc.ListModelMappings(account.ID)))
	}
	if resolved := svc.ResolveMappedModelID("team-gpt-5.4", account.ID); resolved != "gpt-5.4-mini" {
		t.Fatalf("expected updated mapping to resolve, got %q", resolved)
	}
}

func TestGetCustomAccountCatalogRefreshesFromUpstreamAndPersists(t *testing.T) {
	dir := t.TempDir()
	sqliteStore, err := store.NewSQLiteStore(filepath.Join(dir, "state.db"), dir)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer sqliteStore.Close()

	accountPool, err := auth.NewAccountPool(
		filepath.Join(dir, "accounts.json"),
		sqliteStore,
		config.AuthConfig{},
	)
	if err != nil {
		t.Fatalf("NewAccountPool() error = %v", err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/compat/api/paas/v4/models" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"glm-4.5","object":"model","created":1753632000,"owned_by":"z-ai"}]}`))
	}))
	defer upstream.Close()

	account, err := accountPool.AddCustomAccount(auth.CustomAccountInput{
		Label:              "UModel",
		CustomBaseURL:      upstream.URL + "/compat",
		CustomAPIKey:       "secret",
		CustomEndpointType: "https://open.bigmodel.cn/api/paas/v4/chat/completions",
		Enabled:            true,
	})
	if err != nil {
		t.Fatalf("AddCustomAccount() error = %v", err)
	}
	client, err := codex.NewClient(config.Config{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	svc, err := NewService(
		config.Config{
			Storage: config.StorageConfig{
				ManualModelsFile:  filepath.Join(dir, "manual-models.json"),
				ModelCacheFile:    filepath.Join(dir, "model-cache.json"),
				ModelMappingsFile: filepath.Join(dir, "model-mappings.json"),
			},
		},
		Catalog{},
		accountPool,
		client,
		nil,
		sqliteStore.DB(),
		sqliteStore,
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	view, err := svc.GetAccountCatalog(t.Context(), account.ID, true)
	if err != nil {
		t.Fatalf("GetAccountCatalog() error = %v", err)
	}
	if len(view.DynamicModels) != 1 {
		t.Fatalf("expected one dynamic model, got %#v", view.DynamicModels)
	}
	model := view.DynamicModels[0]
	if model.ID != "glm-4.5" || model.Object != "model" || model.OwnedBy != "z-ai" || model.Created != 1753632000 {
		t.Fatalf("unexpected model %+v", model)
	}
	var count int
	if err := sqliteStore.DB().QueryRow(`SELECT COUNT(1) FROM custom_account_models WHERE account_id = ?`, account.ID).Scan(&count); err != nil {
		t.Fatalf("count persisted models: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one persisted model, got %d", count)
	}
}

func TestGetOpenAIAccountCatalogRefreshesFromUpstreamAndPersists(t *testing.T) {
	dir := t.TempDir()
	sqliteStore, err := store.NewSQLiteStore(filepath.Join(dir, "state.db"), dir)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer sqliteStore.Close()

	accountPool, err := auth.NewAccountPool(
		filepath.Join(dir, "accounts.json"),
		sqliteStore,
		config.AuthConfig{},
	)
	if err != nil {
		t.Fatalf("NewAccountPool() error = %v", err)
	}
	account, err := accountPool.AddAccount(testJWT(map[string]any{
		"sub":                "user-openai",
		"chatgpt_account_id": "acct-openai",
		"email":              "openai@example.com",
		"exp":                float64(4102444800),
	}), "refresh-token")
	if err != nil {
		t.Fatalf("AddAccount() error = %v", err)
	}
	account.AccessToken = "token-openai"
	if err := accountPool.ReplaceAccount(account); err != nil {
		t.Fatalf("ReplaceAccount() error = %v", err)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/codex/models" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-5.4","object":"model","created":1779417304,"owned_by":"openai"}]}`))
	}))
	defer upstream.Close()

	client, err := codex.NewClient(config.Config{
		API: config.APIConfig{
			BaseURL: upstream.URL,
		},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	svc, err := NewService(
		config.Config{
			Storage: config.StorageConfig{
				ManualModelsFile:  filepath.Join(dir, "manual-models.json"),
				ModelCacheFile:    filepath.Join(dir, "model-cache.json"),
				ModelMappingsFile: filepath.Join(dir, "model-mappings.json"),
			},
		},
		Catalog{},
		accountPool,
		client,
		nil,
		sqliteStore.DB(),
		sqliteStore,
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	view, err := svc.GetAccountCatalog(t.Context(), account.ID, true)
	if err != nil {
		t.Fatalf("GetAccountCatalog() error = %v", err)
	}
	if len(view.DynamicModels) != 1 {
		t.Fatalf("expected one dynamic model, got %#v", view.DynamicModels)
	}
	model := view.DynamicModels[0]
	if model.ID != "gpt-5.4" || model.Object != "model" || model.OwnedBy != "openai" || model.Created != 1779417304 {
		t.Fatalf("unexpected model %+v", model)
	}

	var count int
	if err := sqliteStore.DB().QueryRow(`SELECT COUNT(1) FROM openai_account_models WHERE account_id = ?`, account.ID).Scan(&count); err != nil {
		t.Fatalf("count persisted openai models: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one persisted openai model, got %d", count)
	}
}

func TestListCustomAccountModelsReturnsOpenAIAndCustomRows(t *testing.T) {
	dir := t.TempDir()
	sqliteStore, err := store.NewSQLiteStore(filepath.Join(dir, "state.db"), dir)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer sqliteStore.Close()

	accountPool, err := auth.NewAccountPool(
		filepath.Join(dir, "accounts.json"),
		sqliteStore,
		config.AuthConfig{},
	)
	if err != nil {
		t.Fatalf("NewAccountPool() error = %v", err)
	}
	openAIAccount, err := accountPool.AddAccount(testJWT(map[string]any{
		"sub":                "user-openai",
		"chatgpt_account_id": "acct-openai",
		"email":              "openai@example.com",
		"exp":                float64(4102444800),
	}), "refresh-token")
	if err != nil {
		t.Fatalf("AddAccount() error = %v", err)
	}
	customAccount, err := accountPool.AddCustomAccount(auth.CustomAccountInput{
		Label:              "UC",
		CustomBaseURL:      "https://example.com",
		CustomAPIKey:       "secret",
		CustomEndpointType: "/v1/chat/completions",
		Enabled:            true,
	})
	if err != nil {
		t.Fatalf("AddCustomAccount() error = %v", err)
	}

	now := time.Now().UTC()
	expires := now.Add(24 * time.Hour)
	if _, err := sqliteStore.DB().Exec(`
		INSERT INTO openai_account_models (account_id, model_id, display_name, model_object, owned_by, created_unix, source_payload, fetched_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, openAIAccount.ID, "gpt-5.4", "GPT-5.4", "model", "openai", 1779417304, `{}`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert openai model: %v", err)
	}
	if _, err := sqliteStore.DB().Exec(`
		INSERT INTO openai_account_model_sync_state (account_id, fetched_at, expires_at, last_error, updated_at)
		VALUES (?, ?, ?, '', ?)
	`, openAIAccount.ID, now.Format(time.RFC3339Nano), expires.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert openai sync state: %v", err)
	}
	if _, err := sqliteStore.DB().Exec(`
		INSERT INTO custom_account_models (account_id, model_id, display_name, model_object, owned_by, created_unix, source_payload, fetched_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, customAccount.ID, "glm-4.5", "GLM-4.5", "model", "z-ai", 1753632000, `{}`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert custom model: %v", err)
	}
	if _, err := sqliteStore.DB().Exec(`
		INSERT INTO custom_account_model_sync_state (account_id, fetched_at, expires_at, last_error, updated_at)
		VALUES (?, ?, ?, '', ?)
	`, customAccount.ID, now.Format(time.RFC3339Nano), expires.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert custom sync state: %v", err)
	}

	svc, err := NewService(
		config.Config{
			Storage: config.StorageConfig{
				ManualModelsFile:  filepath.Join(dir, "manual-models.json"),
				ModelCacheFile:    filepath.Join(dir, "model-cache.json"),
				ModelMappingsFile: filepath.Join(dir, "model-mappings.json"),
			},
		},
		Catalog{},
		accountPool,
		nil,
		nil,
		sqliteStore.DB(),
		sqliteStore,
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	rows, err := svc.ListCustomAccountModels("", "gpt")
	if err != nil {
		t.Fatalf("ListCustomAccountModels() error = %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected one fuzzy matched row, got %d", len(rows))
	}
	if rows[0].Provider != string(auth.ProviderOpenAI) || rows[0].ModelID != "gpt-5.4" {
		t.Fatalf("unexpected openai row %+v", rows[0])
	}

	rows, err = svc.ListCustomAccountModels("", "")
	if err != nil {
		t.Fatalf("ListCustomAccountModels() all error = %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected two rows, got %d", len(rows))
	}
}

func testJWT(claims map[string]any) string {
	return "test-token"
}
