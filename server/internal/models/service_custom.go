package models

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/jushjay/prism/internal/auth"
)

type accountModelSyncState struct {
	AccountID string
	FetchedAt *time.Time
	ExpiresAt *time.Time
	LastError string
	UpdatedAt *time.Time
}

type CustomAccountModelRecord struct {
	AccountID    string     `json:"account_id"`
	AccountEmail string     `json:"account_email,omitempty"`
	AccountLabel string     `json:"account_label,omitempty"`
	Provider     string     `json:"provider,omitempty"`
	ModelID      string     `json:"model_id"`
	DisplayName  string     `json:"display_name,omitempty"`
	Object       string     `json:"object,omitempty"`
	OwnedBy      string     `json:"owned_by,omitempty"`
	Created      int64      `json:"created,omitempty"`
	FetchedAt    *time.Time `json:"fetched_at,omitempty"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	UpdatedAt    *time.Time `json:"updated_at,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
}

func (s *Service) Start() {
	if s == nil || s.db == nil {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.refreshStaleAccountModels(context.Background())
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.refreshStaleAccountModels(context.Background())
			case <-s.stopCh:
				return
			}
		}
	}()
}

func (s *Service) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
	s.wg.Wait()
}

func (s *Service) getCustomAccountCatalog(ctx context.Context, account auth.Account, forceRefresh bool) (AccountCatalogView, error) {
	view := AccountCatalogView{
		AccountID:     account.ID,
		AccountEmail:  firstNonEmpty(strings.TrimSpace(account.Email), strings.TrimSpace(account.Label), account.ID),
		ClientVersion: "custom",
	}
	if s.version != nil && strings.TrimSpace(s.version.CurrentVersion()) != "" {
		view.ClientVersion = s.version.CurrentVersion()
	}
	manualModels := s.listManualEntries()
	state, dynamicModels, err := s.loadPersistedAccountModels(account, accountModelTable(account.Provider), accountModelSyncStateTable(account.Provider))
	if err != nil {
		view.ManualModels = manualModels
		view.Models = slicesCloneEntries(manualModels)
		view.RefreshError = err.Error()
		return view, nil
	}

	if !forceRefresh && state.ExpiresAt != nil && time.Now().UTC().Before(*state.ExpiresAt) {
		view.DynamicModels = toEntries(dynamicModels, SourceDynamic)
		view.ManualModels = manualModels
		view.Models = append(slicesCloneEntries(view.DynamicModels), manualModels...)
		if state.FetchedAt != nil {
			view.FetchedAt = *state.FetchedAt
		}
		if state.ExpiresAt != nil {
			view.ExpiresAt = *state.ExpiresAt
		}
		return view, nil
	}

	refreshed, fetchedAt, expiresAt, refreshErr := s.refreshCustomAccountModels(ctx, account)
	if refreshErr != nil {
		view.ManualModels = manualModels
		view.RefreshError = refreshErr.Error()
		if len(dynamicModels) > 0 {
			view.DynamicModels = toEntries(dynamicModels, SourceDynamic)
			view.Models = append(slicesCloneEntries(view.DynamicModels), manualModels...)
			if state.FetchedAt != nil {
				view.FetchedAt = *state.FetchedAt
			}
			if state.ExpiresAt != nil {
				view.ExpiresAt = *state.ExpiresAt
			}
			view.UsedStaleCache = true
			return view, nil
		}
		view.Models = slicesCloneEntries(manualModels)
		return view, nil
	}

	view.DynamicModels = toEntries(refreshed, SourceDynamic)
	view.ManualModels = manualModels
	view.Models = append(slicesCloneEntries(view.DynamicModels), manualModels...)
	view.FetchedAt = fetchedAt
	view.ExpiresAt = expiresAt
	return view, nil
}

func (s *Service) refreshStaleAccountModels(ctx context.Context) {
	now := time.Now().UTC()
	for _, account := range s.accounts.List() {
		if account.DisabledByUser {
			continue
		}
		if !supportsPersistedAccountModels(account) {
			continue
		}
		state, _, err := s.loadPersistedAccountModels(account, accountModelTable(account.Provider), accountModelSyncStateTable(account.Provider))
		if err != nil {
			continue
		}
		if state.ExpiresAt != nil && now.Before(*state.ExpiresAt) {
			continue
		}
		refreshCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if account.Provider == auth.ProviderCustom {
			_, _, _, _ = s.refreshCustomAccountModels(refreshCtx, account)
		} else {
			_, _, _, _ = s.refreshOpenAIAccountModels(refreshCtx, account)
		}
		cancel()
	}
}

func supportsPersistedAccountModels(account auth.Account) bool {
	switch account.Provider {
	case auth.ProviderCustom:
		return strings.TrimSpace(account.CustomAPIKey) != ""
	case auth.ProviderOpenAI, "":
		return strings.TrimSpace(account.AccessToken) != ""
	default:
		return false
	}
}

func (s *Service) refreshCustomAccountModels(ctx context.Context, account auth.Account) ([]Info, time.Time, time.Time, error) {
	if s.db == nil {
		return nil, time.Time{}, time.Time{}, sql.ErrConnDone
	}
	rawModels, err := s.client.GetCustomModels(ctx, account.CustomBaseURL, account.CustomAPIKey, account.CustomEndpointType, account.CustomUserAgent)
	if err != nil {
		_ = s.updateAccountModelSyncState(account.Provider, account.ID, nil, nil, err.Error())
		return nil, time.Time{}, time.Time{}, err
	}
	return s.persistAccountModels(account.Provider, account.ID, rawModels)
}

func (s *Service) refreshOpenAIAccountModels(ctx context.Context, account auth.Account) ([]Info, time.Time, time.Time, error) {
	if s.db == nil {
		return nil, time.Time{}, time.Time{}, sql.ErrConnDone
	}
	clientVersion := ""
	if s.version != nil {
		clientVersion = s.version.CurrentVersion()
	}
	rawModels, err := s.client.GetModels(ctx, account.AccessToken, account.AccountID, clientVersion)
	if err != nil {
		_ = s.updateAccountModelSyncState(auth.ProviderOpenAI, account.ID, nil, nil, err.Error())
		return nil, time.Time{}, time.Time{}, err
	}
	return s.persistAccountModels(auth.ProviderOpenAI, account.ID, rawModels)
}

func (s *Service) persistAccountModels(provider auth.AccountProvider, accountID string, rawModels []map[string]any) ([]Info, time.Time, time.Time, error) {
	entries := DecodeBackendEntries(rawModels)
	normalized := NormalizeBackendEntries(entries)
	fetchedAt := time.Now().UTC()
	expiresAt := fetchedAt.Add(s.cacheTTL)
	if err := s.replaceAccountModels(provider, accountID, rawModels, normalized, fetchedAt, expiresAt); err != nil {
		return nil, time.Time{}, time.Time{}, err
	}
	return normalized, fetchedAt, expiresAt, nil
}

func (s *Service) loadPersistedAccountModels(account auth.Account, modelTable, syncTable string) (accountModelSyncState, []Info, error) {
	if s.db == nil {
		return accountModelSyncState{}, nil, sql.ErrConnDone
	}
	state, err := s.accountModelSyncState(account.ID, syncTable)
	if err != nil {
		return accountModelSyncState{}, nil, err
	}
	rows, err := s.db.Query(`
		SELECT model_id, display_name, model_object, owned_by, created_unix
		FROM `+modelTable+`
		WHERE account_id = ?
		ORDER BY model_id ASC
	`, account.ID)
	if err != nil {
		return accountModelSyncState{}, nil, err
	}
	defer rows.Close()

	items := make([]Info, 0)
	for rows.Next() {
		var info Info
		if err := rows.Scan(&info.ID, &info.DisplayName, &info.Object, &info.OwnedBy, &info.Created); err != nil {
			return accountModelSyncState{}, nil, err
		}
		if strings.TrimSpace(info.DisplayName) == "" {
			info.DisplayName = info.ID
		}
		items = append(items, info)
	}
	return state, items, rows.Err()
}

func (s *Service) accountModelSyncState(accountID, table string) (accountModelSyncState, error) {
	if s.db == nil {
		return accountModelSyncState{}, sql.ErrConnDone
	}
	row := s.db.QueryRow(`
		SELECT account_id, fetched_at, expires_at, last_error, updated_at
		FROM `+table+`
		WHERE account_id = ?
	`, accountID)
	var (
		state     accountModelSyncState
		fetchedAt sql.NullString
		expiresAt sql.NullString
		lastError sql.NullString
		updatedAt sql.NullString
	)
	if err := row.Scan(&state.AccountID, &fetchedAt, &expiresAt, &lastError, &updatedAt); err != nil {
		if err == sql.ErrNoRows {
			return accountModelSyncState{AccountID: accountID}, nil
		}
		return accountModelSyncState{}, err
	}
	state.FetchedAt = parseNullableRFC3339Nano(fetchedAt)
	state.ExpiresAt = parseNullableRFC3339Nano(expiresAt)
	state.UpdatedAt = parseNullableRFC3339Nano(updatedAt)
	state.LastError = strings.TrimSpace(lastError.String)
	return state, nil
}

func (s *Service) replaceAccountModels(provider auth.AccountProvider, accountID string, rawModels []map[string]any, normalized []Info, fetchedAt, expiresAt time.Time) error {
	modelTable := accountModelTable(provider)
	syncTable := accountModelSyncStateTable(provider)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM `+modelTable+` WHERE account_id = ?`, accountID); err != nil {
		_ = tx.Rollback()
		return err
	}
	rawByID := make(map[string]map[string]any, len(rawModels))
	for _, item := range rawModels {
		id, _ := item["id"].(string)
		if strings.TrimSpace(id) == "" {
			continue
		}
		rawByID[id] = item
	}
	for _, info := range normalized {
		sourcePayload := ""
		if raw, ok := rawByID[info.ID]; ok {
			if payload, err := json.Marshal(raw); err == nil {
				sourcePayload = string(payload)
			}
		}
		if _, err := tx.Exec(`
			INSERT INTO `+modelTable+` (
				account_id, model_id, display_name, model_object, owned_by, created_unix, source_payload, fetched_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, accountID, info.ID, info.DisplayName, info.Object, info.OwnedBy, info.Created, sourcePayload, fetchedAt.Format(time.RFC3339Nano), fetchedAt.Format(time.RFC3339Nano)); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if _, err := tx.Exec(`
		INSERT INTO `+syncTable+` (account_id, fetched_at, expires_at, last_error, updated_at)
		VALUES (?, ?, ?, '', ?)
		ON CONFLICT(account_id) DO UPDATE SET
			fetched_at = excluded.fetched_at,
			expires_at = excluded.expires_at,
			last_error = excluded.last_error,
			updated_at = excluded.updated_at
	`, accountID, fetchedAt.Format(time.RFC3339Nano), expiresAt.Format(time.RFC3339Nano), fetchedAt.Format(time.RFC3339Nano)); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Service) updateAccountModelSyncState(provider auth.AccountProvider, accountID string, fetchedAt, expiresAt *time.Time, lastError string) error {
	if s.db == nil {
		return sql.ErrConnDone
	}
	var fetchedValue any
	var expiresValue any
	if fetchedAt != nil {
		fetchedValue = fetchedAt.UTC().Format(time.RFC3339Nano)
	}
	if expiresAt != nil {
		expiresValue = expiresAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.Exec(`
		INSERT INTO `+accountModelSyncStateTable(provider)+` (account_id, fetched_at, expires_at, last_error, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(account_id) DO UPDATE SET
			fetched_at = COALESCE(excluded.fetched_at, `+accountModelSyncStateTable(provider)+`.fetched_at),
			expires_at = COALESCE(excluded.expires_at, `+accountModelSyncStateTable(provider)+`.expires_at),
			last_error = excluded.last_error,
			updated_at = excluded.updated_at
	`, accountID, fetchedValue, expiresValue, strings.TrimSpace(lastError), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Service) listAllDynamicModelInfo() []Info {
	if s.db == nil {
		return nil
	}
	queries := []string{
		`SELECT model_id, display_name, model_object, owned_by, created_unix, updated_at FROM custom_account_models`,
		`SELECT model_id, display_name, model_object, owned_by, created_unix, updated_at FROM openai_account_models`,
	}
	seen := map[string]struct{}{}
	items := make([]Info, 0)
	for _, query := range queries {
		rows, err := s.db.Query(query + ` ORDER BY updated_at DESC, account_id ASC, model_id ASC`)
		if err != nil {
			continue
		}
		for rows.Next() {
			var (
				info      Info
				updatedAt string
			)
			if err := rows.Scan(&info.ID, &info.DisplayName, &info.Object, &info.OwnedBy, &info.Created, &updatedAt); err != nil {
				_ = rows.Close()
				return items
			}
			if _, exists := seen[info.ID]; exists {
				continue
			}
			seen[info.ID] = struct{}{}
			if strings.TrimSpace(info.DisplayName) == "" {
				info.DisplayName = info.ID
			}
			items = append(items, info)
		}
		_ = rows.Close()
	}
	return items
}

func (s *Service) ListCustomAccountModels(accountID, modelQuery string) ([]CustomAccountModelRecord, error) {
	if s.db == nil {
		return nil, sql.ErrConnDone
	}
	filters := []string{"1 = 1"}
	args := make([]any, 0, 2)
	accountID = strings.TrimSpace(accountID)
	modelQuery = strings.TrimSpace(modelQuery)
	if accountID != "" {
		filters = append(filters, "m.account_id = ?")
		args = append(args, accountID)
	}
	if modelQuery != "" {
		filters = append(filters, "(lower(m.model_id) LIKE ? OR lower(coalesce(m.display_name, '')) LIKE ? OR lower(coalesce(m.owned_by, '')) LIKE ?)")
		like := "%" + strings.ToLower(modelQuery) + "%"
		args = append(args, like, like, like)
	}

	accountMeta := map[string]auth.Account{}
	for _, account := range s.accounts.List() {
		accountMeta[account.ID] = account
	}

	collect := func(provider auth.AccountProvider, modelTable, syncTable string, items []CustomAccountModelRecord) ([]CustomAccountModelRecord, error) {
		rows, err := s.db.Query(`
			SELECT
				m.account_id,
				m.model_id,
				m.display_name,
				m.model_object,
				m.owned_by,
				m.created_unix,
				s.fetched_at,
				s.expires_at,
				s.updated_at,
				s.last_error
			FROM `+modelTable+` m
			LEFT JOIN `+syncTable+` s ON s.account_id = m.account_id
			WHERE `+strings.Join(filters, " AND ")+`
			ORDER BY m.account_id ASC, m.model_id ASC
		`, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		for rows.Next() {
			var (
				item      CustomAccountModelRecord
				fetchedAt sql.NullString
				expiresAt sql.NullString
				updatedAt sql.NullString
				lastError sql.NullString
			)
			if err := rows.Scan(
				&item.AccountID,
				&item.ModelID,
				&item.DisplayName,
				&item.Object,
				&item.OwnedBy,
				&item.Created,
				&fetchedAt,
				&expiresAt,
				&updatedAt,
				&lastError,
			); err != nil {
				return nil, err
			}
			item.Provider = string(provider)
			item.FetchedAt = parseNullableRFC3339Nano(fetchedAt)
			item.ExpiresAt = parseNullableRFC3339Nano(expiresAt)
			item.UpdatedAt = parseNullableRFC3339Nano(updatedAt)
			item.LastError = strings.TrimSpace(lastError.String)
			if item.DisplayName == "" {
				item.DisplayName = item.ModelID
			}
			if account, ok := accountMeta[item.AccountID]; ok {
				item.AccountEmail = strings.TrimSpace(account.Email)
				item.AccountLabel = strings.TrimSpace(account.Label)
				if item.Provider == "" {
					item.Provider = string(account.Provider)
				}
			}
			items = append(items, item)
		}
		return items, rows.Err()
	}

	items := make([]CustomAccountModelRecord, 0)
	var err error
	items, err = collect(auth.ProviderOpenAI, accountModelTable(auth.ProviderOpenAI), accountModelSyncStateTable(auth.ProviderOpenAI), items)
	if err != nil {
		return nil, err
	}
	items, err = collect(auth.ProviderCustom, accountModelTable(auth.ProviderCustom), accountModelSyncStateTable(auth.ProviderCustom), items)
	if err != nil {
		return nil, err
	}
	return items, nil
}

func accountModelTable(provider auth.AccountProvider) string {
	switch provider {
	case auth.ProviderCustom:
		return "custom_account_models"
	default:
		return "openai_account_models"
	}
}

func accountModelSyncStateTable(provider auth.AccountProvider) string {
	switch provider {
	case auth.ProviderCustom:
		return "custom_account_model_sync_state"
	default:
		return "openai_account_model_sync_state"
	}
}

func parseNullableRFC3339Nano(value sql.NullString) *time.Time {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil
	}
	return &parsed
}

func slicesCloneEntries[T any](items []T) []T {
	if len(items) == 0 {
		return nil
	}
	return append([]T{}, items...)
}
