package models

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jushjay/prism/internal/auth"
	"github.com/jushjay/prism/internal/codex"
	"github.com/jushjay/prism/internal/config"
	"github.com/jushjay/prism/internal/store"
	clientversion "github.com/jushjay/prism/internal/version"
)

type ModelSource string

const (
	SourceDynamic ModelSource = "dynamic"
	SourceManual  ModelSource = "manual"
	SourceStatic  ModelSource = "static"
)

type ManualModelRecord struct {
	Info
	RecordID  string    `json:"record_id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ManualModelInput struct {
	ID                        string
	DisplayName               string
	Description               string
	DefaultReasoningEffort    string
	SupportedReasoningEfforts []string
	InputModalities           []string
	OutputModalities          []string
}

type AccountCatalogCache struct {
	AccountID     string    `json:"account_id"`
	AccountEmail  string    `json:"account_email"`
	ClientVersion string    `json:"client_version"`
	FetchedAt     time.Time `json:"fetched_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	Models        []Info    `json:"models"`
}

type cacheState struct {
	Accounts map[string]AccountCatalogCache `json:"accounts"`
}

type manualState struct {
	Models []ManualModelRecord `json:"models"`
}

type ModelMappingRecord struct {
	RecordID     string    `json:"record_id"`
	ModelName    string    `json:"model_name"`
	TargetModel  string    `json:"target_model"`
	ApplyGlobal  bool      `json:"apply_global"`
	AccountID    string    `json:"account_id,omitempty"`
	AccountEmail string    `json:"account_email,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type ModelMappingInput struct {
	RecordID    string
	ModelName   string
	TargetModel string
	ApplyGlobal bool
	AccountID   string
}

type mappingsState struct {
	Items []ModelMappingRecord `json:"items"`
}

type Entry struct {
	Info
	Source    ModelSource `json:"source"`
	RecordID  string      `json:"record_id,omitempty"`
	UpdatedAt *time.Time  `json:"updated_at,omitempty"`
}

type AccountCatalogView struct {
	AccountID      string    `json:"account_id"`
	AccountEmail   string    `json:"account_email"`
	ClientVersion  string    `json:"client_version"`
	FetchedAt      time.Time `json:"fetched_at,omitempty"`
	ExpiresAt      time.Time `json:"expires_at,omitempty"`
	RefreshError   string    `json:"refresh_error,omitempty"`
	UsedStaleCache bool      `json:"used_stale_cache"`
	DynamicModels  []Entry   `json:"dynamic_models"`
	ManualModels   []Entry   `json:"manual_models"`
	Models         []Entry   `json:"models"`
}

type Service struct {
	mu       sync.RWMutex
	static   Catalog
	caches   map[string]AccountCatalogCache
	manual   []ManualModelRecord
	mappings []ModelMappingRecord
	cacheTTL time.Duration

	accounts *auth.AccountPool
	client   *codex.Client
	version  *clientversion.Manager
	db       *sql.DB
	store    store.StateStore
	cfg      config.Config
	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

func NewService(
	cfg config.Config,
	static Catalog,
	accounts *auth.AccountPool,
	client *codex.Client,
	version *clientversion.Manager,
	db *sql.DB,
	stateStore store.StateStore,
) (*Service, error) {
	svc := &Service{
		static:   static,
		caches:   map[string]AccountCatalogCache{},
		manual:   nil,
		mappings: nil,
		cacheTTL: 24 * time.Hour,
		accounts: accounts,
		client:   client,
		version:  version,
		db:       db,
		store:    stateStore,
		cfg:      cfg,
		stopCh:   make(chan struct{}),
	}
	if err := svc.load(); err != nil {
		return nil, err
	}
	return svc, nil
}

func (s *Service) load() error {
	var cached cacheState
	if err := s.store.Load(s.cfg.Storage.ModelCacheFile, &cached); err != nil {
		return err
	}
	var manual manualState
	if err := s.store.Load(s.cfg.Storage.ManualModelsFile, &manual); err != nil {
		return err
	}
	var mappings mappingsState
	if err := s.store.Load(s.cfg.Storage.ModelMappingsFile, &mappings); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if cached.Accounts != nil {
		s.caches = cached.Accounts
	}
	s.manual = manual.Models
	s.mappings = mappings.Items
	return nil
}

func (s *Service) GetAccountCatalog(ctx context.Context, accountID string, forceRefresh bool) (AccountCatalogView, error) {
	account, ok := s.accounts.Get(accountID)
	if !ok {
		return AccountCatalogView{}, errors.New("account not found")
	}
	clientVersion := ""
	if s.version != nil {
		clientVersion = s.version.CurrentVersion()
	}

	view := AccountCatalogView{
		AccountID:     account.ID,
		AccountEmail:  account.Email,
		ClientVersion: clientVersion,
	}
	if account.Provider == auth.ProviderCustom {
		return s.getCustomAccountCatalog(ctx, account, forceRefresh)
	}

	manualModels := s.listManualEntries()
	if s.db != nil {
		state, dynamicModels, err := s.loadPersistedAccountModels(account, accountModelTable(auth.ProviderOpenAI), accountModelSyncStateTable(auth.ProviderOpenAI))
		if err == nil {
			if !forceRefresh && state.ExpiresAt != nil && time.Now().UTC().Before(*state.ExpiresAt) {
				view.DynamicModels = toEntries(dynamicModels, SourceDynamic)
				view.ManualModels = manualModels
				view.Models = append(slices.Clone(view.DynamicModels), view.ManualModels...)
				if state.FetchedAt != nil {
					view.FetchedAt = *state.FetchedAt
				}
				if state.ExpiresAt != nil {
					view.ExpiresAt = *state.ExpiresAt
				}
				return view, nil
			}

			refreshed, fetchedAt, expiresAt, refreshErr := s.refreshOpenAIAccountModels(ctx, account)
			if refreshErr != nil {
				view.ManualModels = manualModels
				view.RefreshError = refreshErr.Error()
				if len(dynamicModels) > 0 {
					view.DynamicModels = toEntries(dynamicModels, SourceDynamic)
					view.Models = append(slices.Clone(view.DynamicModels), view.ManualModels...)
					if state.FetchedAt != nil {
						view.FetchedAt = *state.FetchedAt
					}
					if state.ExpiresAt != nil {
						view.ExpiresAt = *state.ExpiresAt
					}
					view.UsedStaleCache = true
					return view, nil
				}
				view.Models = slices.Clone(view.ManualModels)
				return view, nil
			}

			view.DynamicModels = toEntries(refreshed, SourceDynamic)
			view.ManualModels = manualModels
			view.Models = append(slices.Clone(view.DynamicModels), view.ManualModels...)
			view.FetchedAt = fetchedAt
			view.ExpiresAt = expiresAt

			nextCache := AccountCatalogCache{
				AccountID:     account.ID,
				AccountEmail:  account.Email,
				ClientVersion: clientVersion,
				FetchedAt:     fetchedAt,
				ExpiresAt:     expiresAt,
				Models:        refreshed,
			}
			if err := s.saveCache(nextCache); err != nil && view.RefreshError == "" {
				view.RefreshError = err.Error()
			}
			return view, nil
		}
	}

	cache, hasCache := s.getCache(account.ID)
	if !forceRefresh && hasCache && time.Now().Before(cache.ExpiresAt) {
		view.DynamicModels = toEntries(cache.Models, SourceDynamic)
		view.ManualModels = manualModels
		view.Models = append(slices.Clone(view.DynamicModels), view.ManualModels...)
		view.FetchedAt = cache.FetchedAt
		view.ExpiresAt = cache.ExpiresAt
		return view, nil
	}

	raw, err := s.client.GetModels(ctx, account.AccessToken, account.AccountID, clientVersion)
	if err != nil {
		view.ManualModels = manualModels
		view.Models = append([]Entry{}, view.ManualModels...)
		if hasCache {
			view.DynamicModels = toEntries(cache.Models, SourceDynamic)
			view.Models = append(view.DynamicModels, view.ManualModels...)
			view.FetchedAt = cache.FetchedAt
			view.ExpiresAt = cache.ExpiresAt
			view.UsedStaleCache = true
		}
		view.RefreshError = err.Error()
		return view, nil
	}

	entries := DecodeBackendEntries(raw)
	normalized := NormalizeBackendEntries(entries)
	now := time.Now().UTC()
	nextCache := AccountCatalogCache{
		AccountID:     account.ID,
		AccountEmail:  account.Email,
		ClientVersion: clientVersion,
		FetchedAt:     now,
		ExpiresAt:     now.Add(s.cacheTTL),
		Models:        normalized,
	}
	if err := s.saveCache(nextCache); err != nil {
		view.RefreshError = err.Error()
	}

	view.DynamicModels = toEntries(normalized, SourceDynamic)
	view.ManualModels = manualModels
	view.Models = append(slices.Clone(view.DynamicModels), view.ManualModels...)
	view.FetchedAt = nextCache.FetchedAt
	view.ExpiresAt = nextCache.ExpiresAt
	return view, nil
}

func (s *Service) UpsertManualModel(input ManualModelInput) (ManualModelRecord, error) {
	now := time.Now().UTC()
	record := ManualModelRecord{
		RecordID:  uuid.NewString(),
		CreatedAt: now,
		UpdatedAt: now,
		Info: Info{
			ID:                        strings.TrimSpace(input.ID),
			DisplayName:               defaultString(input.DisplayName, input.ID),
			Description:               input.Description,
			DefaultReasoningEffort:    defaultString(input.DefaultReasoningEffort, "medium"),
			SupportedReasoningEfforts: buildManualReasoningEfforts(input.SupportedReasoningEfforts, input.DefaultReasoningEffort),
			InputModalities:           ensureModalities(input.InputModalities, []string{"text"}),
			OutputModalities:          ensureModalities(input.OutputModalities, []string{"text"}),
		},
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for idx, existing := range s.manual {
		if existing.ID != input.ID {
			continue
		}
		record.RecordID = existing.RecordID
		record.CreatedAt = existing.CreatedAt
		s.manual[idx] = record
		return record, s.persistManualLocked()
	}

	s.manual = append(s.manual, record)
	return record, s.persistManualLocked()
}

func (s *Service) UpsertModelMapping(input ModelMappingInput) (ModelMappingRecord, error) {
	now := time.Now().UTC()
	recordID := strings.TrimSpace(input.RecordID)
	modelName := strings.TrimSpace(input.ModelName)
	targetModel := strings.TrimSpace(input.TargetModel)
	accountID := strings.TrimSpace(input.AccountID)
	if modelName == "" {
		return ModelMappingRecord{}, errors.New("model name is required")
	}
	if targetModel == "" {
		return ModelMappingRecord{}, errors.New("target model is required")
	}
	if !input.ApplyGlobal && accountID == "" {
		return ModelMappingRecord{}, errors.New("account id is required for account-scoped mapping")
	}

	record := ModelMappingRecord{
		RecordID:    recordID,
		ModelName:   modelName,
		TargetModel: targetModel,
		ApplyGlobal: input.ApplyGlobal,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if !input.ApplyGlobal {
		account, ok := s.accounts.Get(accountID)
		if !ok {
			return ModelMappingRecord{}, errors.New("account not found")
		}
		record.AccountID = account.ID
		record.AccountEmail = account.Email
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if record.RecordID != "" {
		targetIndex := -1
		for idx, existing := range s.mappings {
			if existing.RecordID == record.RecordID {
				targetIndex = idx
				record.CreatedAt = existing.CreatedAt
				break
			}
		}
		if targetIndex < 0 {
			return ModelMappingRecord{}, errors.New("mapping not found")
		}
		for _, existing := range s.mappings {
			if existing.RecordID == record.RecordID {
				continue
			}
			if existing.ModelName != record.ModelName {
				continue
			}
			if existing.ApplyGlobal != record.ApplyGlobal {
				continue
			}
			if !record.ApplyGlobal && existing.AccountID != record.AccountID {
				continue
			}
			return ModelMappingRecord{}, errors.New("mapping already exists for the same model and scope")
		}
		s.mappings[targetIndex] = record
		return record, s.persistMappingsLocked()
	}

	record.RecordID = uuid.NewString()
	for idx, existing := range s.mappings {
		if existing.ModelName != record.ModelName {
			continue
		}
		if existing.ApplyGlobal != record.ApplyGlobal {
			continue
		}
		if !record.ApplyGlobal && existing.AccountID != record.AccountID {
			continue
		}
		record.RecordID = existing.RecordID
		record.CreatedAt = existing.CreatedAt
		s.mappings[idx] = record
		return record, s.persistMappingsLocked()
	}

	s.mappings = append(s.mappings, record)
	return record, s.persistMappingsLocked()
}

func (s *Service) DeleteModelMapping(recordID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	filtered := s.mappings[:0]
	found := false
	for _, item := range s.mappings {
		if item.RecordID == recordID {
			found = true
			continue
		}
		filtered = append(filtered, item)
	}
	if !found {
		return errors.New("mapping not found")
	}
	s.mappings = filtered
	return s.persistMappingsLocked()
}

func (s *Service) ListModelMappings(accountID string) []ModelMappingRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	requestedAccountID := strings.TrimSpace(accountID)
	out := make([]ModelMappingRecord, 0, len(s.mappings))
	for _, item := range s.mappings {
		if item.ApplyGlobal || requestedAccountID == "" || item.AccountID == requestedAccountID {
			out = append(out, item)
		}
	}
	slices.SortFunc(out, func(a, b ModelMappingRecord) int {
		if a.ApplyGlobal != b.ApplyGlobal {
			if a.ApplyGlobal {
				return -1
			}
			return 1
		}
		if cmp := strings.Compare(a.ModelName, b.ModelName); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.AccountID, b.AccountID)
	})
	return out
}

func (s *Service) TotalModelCount() int {
	return len(s.GlobalCatalog().Models)
}

func (s *Service) GlobalCatalog() Catalog {
	s.mu.RLock()
	staticModels := slices.Clone(s.static.Models)
	staticAliases := mapsClone(s.static.Aliases)
	manualModels := slices.Clone(s.manual)
	cacheModels := make([]AccountCatalogCache, 0, len(s.caches))
	for _, cache := range s.caches {
		cacheModels = append(cacheModels, cache)
	}
	s.mu.RUnlock()

	customModels := s.listAllDynamicModelInfo()

	merged := make(map[string]Info, len(staticModels)+len(manualModels)+len(customModels))
	order := make([]string, 0, len(staticModels)+len(manualModels)+len(customModels))

	for _, item := range staticModels {
		merged[item.ID] = item
		order = append(order, item.ID)
	}

	for _, cache := range cacheModels {
		for _, item := range cache.Models {
			if _, exists := merged[item.ID]; !exists {
				order = append(order, item.ID)
			}
			merged[item.ID] = item
		}
	}

	for _, item := range customModels {
		if _, exists := merged[item.ID]; !exists {
			order = append(order, item.ID)
		}
		merged[item.ID] = item
	}

	for _, item := range manualModels {
		if _, exists := merged[item.ID]; !exists {
			order = append(order, item.ID)
		}
		merged[item.ID] = item.Info
	}

	models := make([]Info, 0, len(order))
	seen := map[string]struct{}{}
	for _, id := range order {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		models = append(models, merged[id])
	}

	return Catalog{
		Models:  models,
		Aliases: staticAliases,
	}
}

func (s *Service) ResolveMappedModelID(modelID, accountID string) string {
	requested := strings.TrimSpace(modelID)
	if requested == "" {
		return ""
	}
	scopedAccountID := strings.TrimSpace(accountID)

	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.mappings {
		if item.ApplyGlobal || item.AccountID != scopedAccountID {
			continue
		}
		if item.ModelName == requested {
			return item.TargetModel
		}
	}
	for _, item := range s.mappings {
		if !item.ApplyGlobal {
			continue
		}
		if item.ModelName == requested {
			return item.TargetModel
		}
	}
	return s.static.ResolveUpstreamID(requested)
}

func (s *Service) AccountSupportsModel(account auth.Account, modelID string) (bool, bool) {
	targetModel := strings.TrimSpace(modelID)
	if targetModel == "" {
		return false, false
	}

	if s.db == nil {
		return false, false
	}

	provider := account.Provider
	if provider == "" {
		provider = auth.ProviderOpenAI
	}
	modelTable := accountModelTable(provider)
	syncTable := accountModelSyncStateTable(provider)

	var syncedAccountID string
	err := s.db.QueryRow(`SELECT account_id FROM `+syncTable+` WHERE account_id = ? LIMIT 1`, account.ID).Scan(&syncedAccountID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, false
		}
		return false, false
	}

	var matchedAccountID string
	err = s.db.QueryRow(`SELECT account_id FROM `+modelTable+` WHERE account_id = ? AND model_id = ? LIMIT 1`, account.ID, targetModel).Scan(&matchedAccountID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, true
		}
		return false, false
	}
	return true, true
}

func (s *Service) listManualEntries() []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Entry, 0, len(s.manual))
	for _, item := range s.manual {
		updatedAt := item.UpdatedAt
		out = append(out, Entry{
			Info:      item.Info,
			Source:    SourceManual,
			RecordID:  item.RecordID,
			UpdatedAt: &updatedAt,
		})
	}
	return out
}

func (s *Service) getCache(accountID string) (AccountCatalogCache, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cache, ok := s.caches[accountID]
	return cache, ok
}

func (s *Service) saveCache(cache AccountCatalogCache) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.caches[cache.AccountID] = cache
	return s.persistCacheLocked()
}

func (s *Service) persistCacheLocked() error {
	return s.store.Save(s.cfg.Storage.ModelCacheFile, cacheState{Accounts: s.caches})
}

func (s *Service) persistManualLocked() error {
	return s.store.Save(s.cfg.Storage.ManualModelsFile, manualState{Models: s.manual})
}

func (s *Service) persistMappingsLocked() error {
	return s.store.Save(s.cfg.Storage.ModelMappingsFile, mappingsState{Items: s.mappings})
}

func toEntries(models []Info, source ModelSource) []Entry {
	out := make([]Entry, 0, len(models))
	for _, item := range models {
		out = append(out, Entry{Info: item, Source: source})
	}
	return out
}

func buildManualReasoningEfforts(efforts []string, fallback string) []ReasoningEffort {
	cleaned := ensureModalities(efforts, nil)
	if len(cleaned) == 0 {
		cleaned = []string{defaultString(fallback, "medium")}
	}
	out := make([]ReasoningEffort, 0, len(cleaned))
	for _, effort := range cleaned {
		out = append(out, ReasoningEffort{
			ReasoningEffort: effort,
			Description:     "Manual model entry",
		})
	}
	return out
}

func ensureModalities(values []string, fallback []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		out = append(out, value)
	}
	if len(out) == 0 {
		return slices.Clone(fallback)
	}
	return out
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}
