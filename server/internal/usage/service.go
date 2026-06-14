package usage

import (
	"database/sql"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/jushjay/prism/internal/auth"
	"github.com/jushjay/prism/internal/config"
	"github.com/jushjay/prism/internal/store"
)

const legacyUnattributedModelID = "legacy-unattributed"

type Summary struct {
	TotalInputTokens  int        `json:"total_input_tokens"`
	TotalOutputTokens int        `json:"total_output_tokens"`
	TotalCachedTokens int        `json:"total_cached_tokens"`
	TotalRequestCount int        `json:"total_request_count"`
	LastRecordedAt    *time.Time `json:"last_recorded_at,omitempty"`
}

type Record struct {
	OccurredAt         time.Time
	AccountID          string
	AccountProvider    auth.AccountProvider
	AccountIdentity    string
	AccountDisplayName string
	AccountLabel       string
	AccountEmail       string
	UpstreamAccountID  string
	AccountSnapshot    *auth.UsageAccountSnapshot
	ModelID            string
	InputTokens        int
	OutputTokens       int
	RequestCount       int
	CachedTokens       int
	ReasoningTokens    int
}

type HistoryPoint struct {
	Timestamp    time.Time `json:"timestamp"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	CachedTokens int       `json:"cached_tokens"`
	RequestCount int       `json:"request_count"`
	TotalTokens  int       `json:"total_tokens"`
}

type AccountBreakdown struct {
	AccountIdentity    string     `json:"account_identity"`
	AccountID          string     `json:"account_id"`
	AccountProvider    string     `json:"account_provider"`
	AccountDisplayName string     `json:"account_display_name"`
	AccountLabel       string     `json:"account_label"`
	AccountEmail       string     `json:"account_email"`
	UpstreamAccountID  string     `json:"upstream_account_id"`
	TotalInputTokens   int        `json:"total_input_tokens"`
	TotalOutputTokens  int        `json:"total_output_tokens"`
	TotalCachedTokens  int        `json:"total_cached_tokens"`
	TotalRequestCount  int        `json:"total_request_count"`
	LastRecordedAt     *time.Time `json:"last_recorded_at,omitempty"`
}

type ModelBreakdown struct {
	ModelID           string     `json:"model_id"`
	TotalInputTokens  int        `json:"total_input_tokens"`
	TotalOutputTokens int        `json:"total_output_tokens"`
	TotalCachedTokens int        `json:"total_cached_tokens"`
	TotalRequestCount int        `json:"total_request_count"`
	LastRecordedAt    *time.Time `json:"last_recorded_at,omitempty"`
}

type AccountModelBreakdown struct {
	AccountIdentity    string     `json:"account_identity"`
	AccountID          string     `json:"account_id"`
	AccountProvider    string     `json:"account_provider"`
	AccountDisplayName string     `json:"account_display_name"`
	AccountLabel       string     `json:"account_label"`
	AccountEmail       string     `json:"account_email"`
	UpstreamAccountID  string     `json:"upstream_account_id"`
	ModelID            string     `json:"model_id"`
	TotalInputTokens   int        `json:"total_input_tokens"`
	TotalOutputTokens  int        `json:"total_output_tokens"`
	TotalCachedTokens  int        `json:"total_cached_tokens"`
	TotalRequestCount  int        `json:"total_request_count"`
	LastRecordedAt     *time.Time `json:"last_recorded_at,omitempty"`
}

type EventQuery struct {
	From      *time.Time
	To        *time.Time
	ModelID   string
	AccountID string
	Page      int
	PageSize  int
}

type EventItem struct {
	ID                 int       `json:"id"`
	OccurredAt         time.Time `json:"occurred_at"`
	AccountIdentity    string    `json:"account_identity"`
	AccountID          string    `json:"account_id"`
	AccountProvider    string    `json:"account_provider"`
	AccountDisplayName string    `json:"account_display_name"`
	AccountLabel       string    `json:"account_label"`
	AccountEmail       string    `json:"account_email"`
	UpstreamAccountID  string    `json:"upstream_account_id"`
	ModelID            string    `json:"model_id"`
	InputTokens        int       `json:"input_tokens"`
	OutputTokens       int       `json:"output_tokens"`
	CachedTokens       int       `json:"cached_tokens"`
	TotalTokens        int       `json:"total_tokens"`
	RequestCount       int       `json:"request_count"`
}

type EventListResult struct {
	Items      []EventItem `json:"items"`
	Page       int         `json:"page"`
	PageSize   int         `json:"page_size"`
	Total      int         `json:"total"`
	TotalPages int         `json:"total_pages"`
	Summary    Summary     `json:"summary"`
}

type Service struct {
	mu         sync.RWMutex
	db         *sql.DB
	stateStore store.StateStore
	cfg        config.Config
}

func normalizeUsageDisplayName(record *Record) {
	if strings.TrimSpace(record.AccountDisplayName) != "" {
		record.AccountDisplayName = strings.TrimSpace(record.AccountDisplayName)
		return
	}
	if strings.TrimSpace(record.AccountLabel) != "" {
		record.AccountDisplayName = strings.TrimSpace(record.AccountLabel)
		return
	}
	if strings.TrimSpace(record.AccountEmail) != "" {
		record.AccountDisplayName = strings.TrimSpace(record.AccountEmail)
		return
	}
	if strings.TrimSpace(record.UpstreamAccountID) != "" {
		record.AccountDisplayName = strings.TrimSpace(record.UpstreamAccountID)
		return
	}
	record.AccountDisplayName = strings.TrimSpace(record.AccountID)
}

func looksLikeEmail(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	at := strings.IndexByte(value, '@')
	if at <= 0 || at >= len(value)-1 {
		return false
	}
	domain := value[at+1:]
	return strings.Contains(domain, ".")
}

func normalizeUsageIdentityRecord(record *Record) {
	if strings.TrimSpace(record.AccountIdentity) != "" {
		record.AccountIdentity = strings.TrimSpace(record.AccountIdentity)
		return
	}
	if record.AccountProvider == auth.ProviderCustom {
		if strings.TrimSpace(record.AccountID) != "" {
			record.AccountIdentity = "custom:" + strings.TrimSpace(record.AccountID)
			return
		}
		if strings.TrimSpace(record.AccountDisplayName) != "" {
			record.AccountIdentity = "custom_legacy_name:" + strings.ToLower(strings.TrimSpace(record.AccountDisplayName))
			return
		}
	}
	if value := strings.ToLower(strings.TrimSpace(record.UpstreamAccountID)); value != "" {
		record.AccountIdentity = "openai:" + value
		return
	}
	if value := strings.ToLower(strings.TrimSpace(record.AccountEmail)); value != "" {
		record.AccountIdentity = "openai_email:" + value
		return
	}
	if strings.TrimSpace(record.AccountID) != "" {
		record.AccountIdentity = "legacy_internal:" + strings.TrimSpace(record.AccountID)
		return
	}
	record.AccountIdentity = "legacy:unknown"
}

func ensureUsageRecordDerivedFields(record *Record) {
	record.AccountID = strings.TrimSpace(record.AccountID)
	record.AccountLabel = strings.TrimSpace(record.AccountLabel)
	record.AccountEmail = strings.TrimSpace(record.AccountEmail)
	record.UpstreamAccountID = strings.TrimSpace(record.UpstreamAccountID)
	normalizeUsageDisplayName(record)
	normalizeUsageIdentityRecord(record)
}

func snapshotJSON(snapshot *auth.UsageAccountSnapshot) string {
	if snapshot == nil {
		return ""
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return ""
	}
	return string(raw)
}

func RecordFromAccount(account auth.Account, occurredAt time.Time, modelID string, resultUsageInput, resultUsageOutput, requestCount, cachedTokens, reasoningTokens int) Record {
	snapshot := auth.BuildUsageSnapshot(account)
	return Record{
		OccurredAt:         occurredAt,
		AccountID:          account.ID,
		AccountProvider:    account.Provider,
		AccountIdentity:    snapshot.UsageIdentity,
		AccountDisplayName: snapshot.DisplayName,
		AccountLabel:       snapshot.Label,
		AccountEmail:       snapshot.Email,
		UpstreamAccountID:  snapshot.UpstreamID,
		AccountSnapshot:    &snapshot,
		ModelID:            modelID,
		InputTokens:        resultUsageInput,
		OutputTokens:       resultUsageOutput,
		RequestCount:       requestCount,
		CachedTokens:       cachedTokens,
		ReasoningTokens:    reasoningTokens,
	}
}

func NewService(cfg config.Config, db *sql.DB, stateStore store.StateStore) (*Service, error) {
	service := &Service{
		db:         db,
		stateStore: stateStore,
		cfg:        cfg,
	}
	return service, nil
}

func (s *Service) MigrateAccountUsageData(accounts []auth.Account) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := ensureUsageSchema(s.db); err != nil {
		return err
	}

	accountByID := map[string]auth.Account{}
	accountByEmail := map[string]auth.Account{}
	accountByLabel := map[string]auth.Account{}
	for _, account := range accounts {
		accountByID[strings.TrimSpace(account.ID)] = account
		if value := strings.ToLower(strings.TrimSpace(account.Email)); value != "" {
			accountByEmail[value] = account
		}
		if value := strings.ToLower(strings.TrimSpace(account.Label)); value != "" {
			accountByLabel[value] = account
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}

	rows, err := tx.Query(`
		SELECT id, occurred_at, account_id, COALESCE(account_provider, ''), COALESCE(account_label, ''),
			COALESCE(account_email, ''), COALESCE(upstream_account_id, ''), COALESCE(account_display_name, '')
		FROM usage_events
		ORDER BY id ASC
	`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}

	type migratedRow struct {
		ID     int
		Record Record
	}
	var migrated []migratedRow
	for rows.Next() {
		var (
			id                int
			occurredAtRaw     string
			accountID         string
			accountProvider   string
			accountLabel      string
			accountEmail      string
			upstreamAccountID string
			accountDisplay    string
		)
		if err := rows.Scan(&id, &occurredAtRaw, &accountID, &accountProvider, &accountLabel, &accountEmail, &upstreamAccountID, &accountDisplay); err != nil {
			rows.Close()
			_ = tx.Rollback()
			return err
		}
		occurredAt, err := time.Parse(time.RFC3339Nano, occurredAtRaw)
		if err != nil {
			occurredAt, err = time.Parse(time.RFC3339, occurredAtRaw)
			if err != nil {
				rows.Close()
				_ = tx.Rollback()
				return err
			}
		}

		record := Record{
			OccurredAt:         occurredAt,
			AccountID:          strings.TrimSpace(accountID),
			AccountProvider:    auth.AccountProvider(strings.TrimSpace(accountProvider)),
			AccountDisplayName: strings.TrimSpace(accountDisplay),
			AccountLabel:       strings.TrimSpace(accountLabel),
			AccountEmail:       strings.TrimSpace(accountEmail),
			UpstreamAccountID:  strings.TrimSpace(upstreamAccountID),
		}

		if record.AccountLabel == "" && !looksLikeEmail(record.AccountEmail) && record.AccountEmail != "" {
			record.AccountLabel = record.AccountEmail
			record.AccountEmail = ""
		}

		if account, ok := accountByID[record.AccountID]; ok {
			snapshot := auth.BuildUsageSnapshot(account)
			record.AccountProvider = account.Provider
			if record.AccountLabel == "" {
				record.AccountLabel = snapshot.Label
			}
			if record.AccountEmail == "" {
				record.AccountEmail = snapshot.Email
			}
			if record.UpstreamAccountID == "" {
				record.UpstreamAccountID = snapshot.UpstreamID
			}
			if record.AccountDisplayName == "" {
				record.AccountDisplayName = snapshot.DisplayName
			}
			record.AccountIdentity = snapshot.UsageIdentity
			record.AccountSnapshot = &snapshot
		} else if account, ok := accountByLabel[strings.ToLower(strings.TrimSpace(record.AccountLabel))]; ok {
			snapshot := auth.BuildUsageSnapshot(account)
			record.AccountProvider = account.Provider
			record.AccountLabel = snapshot.Label
			if record.AccountEmail == "" {
				record.AccountEmail = snapshot.Email
			}
			if record.AccountDisplayName == "" {
				record.AccountDisplayName = snapshot.DisplayName
			}
			record.AccountIdentity = snapshot.UsageIdentity
			record.AccountSnapshot = &snapshot
		} else if looksLikeEmail(record.AccountEmail) {
			if account, ok := accountByEmail[strings.ToLower(record.AccountEmail)]; ok {
				snapshot := auth.BuildUsageSnapshot(account)
				if record.AccountDisplayName == "" {
					record.AccountDisplayName = snapshot.DisplayName
				}
				if record.UpstreamAccountID == "" {
					record.UpstreamAccountID = snapshot.UpstreamID
				}
				record.AccountProvider = account.Provider
				record.AccountIdentity = snapshot.UsageIdentity
				record.AccountSnapshot = &snapshot
			}
		} else if record.AccountProvider == auth.ProviderCustom {
			snapshot := auth.BuildUsageSnapshot(account)
			if record.AccountDisplayName == "" {
				record.AccountDisplayName = snapshot.DisplayName
			}
			record.AccountIdentity = snapshot.UsageIdentity
			record.AccountSnapshot = &snapshot
		}

		if record.AccountProvider == "" {
			if record.AccountLabel != "" && record.AccountEmail == "" {
				record.AccountProvider = auth.ProviderCustom
			} else {
				record.AccountProvider = auth.ProviderOpenAI
			}
		}
		ensureUsageRecordDerivedFields(&record)
		if record.AccountSnapshot == nil {
			snapshot := auth.UsageAccountSnapshot{
				AccountID:     record.AccountID,
				Provider:      record.AccountProvider,
				UsageIdentity: record.AccountIdentity,
				DisplayName:   record.AccountDisplayName,
				Label:         record.AccountLabel,
				Email:         record.AccountEmail,
				UpstreamID:    record.UpstreamAccountID,
			}
			record.AccountSnapshot = &snapshot
		}
		migrated = append(migrated, migratedRow{ID: id, Record: record})
	}
	if err := rows.Close(); err != nil {
		_ = tx.Rollback()
		return err
	}

	for _, item := range migrated {
		if _, err := tx.Exec(`
			UPDATE usage_events
			SET account_identity = ?, account_provider = ?, account_display_name = ?,
				account_label = ?, account_email = ?, upstream_account_id = ?, account_snapshot = ?
			WHERE id = ?
		`, item.Record.AccountIdentity, string(item.Record.AccountProvider), item.Record.AccountDisplayName,
			item.Record.AccountLabel, item.Record.AccountEmail, item.Record.UpstreamAccountID, snapshotJSON(item.Record.AccountSnapshot), item.ID); err != nil {
			_ = tx.Rollback()
			return err
		}
	}

	if _, err := tx.Exec(`DELETE FROM usage_totals_by_account_model`); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`DELETE FROM usage_totals_by_account`); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`DELETE FROM usage_totals_by_model`); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`DELETE FROM usage_totals_global`); err != nil {
		_ = tx.Rollback()
		return err
	}

	rows, err = tx.Query(`
		SELECT occurred_at, account_identity, account_id, account_provider, account_display_name,
			account_label, account_email, upstream_account_id, model_id,
			input_tokens, output_tokens, request_count, cached_tokens, reasoning_tokens
		FROM usage_events
		ORDER BY id ASC
	`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	for rows.Next() {
		var (
			record          Record
			occurredAtRaw   string
			accountProvider string
		)
		if err := rows.Scan(
			&occurredAtRaw,
			&record.AccountIdentity,
			&record.AccountID,
			&accountProvider,
			&record.AccountDisplayName,
			&record.AccountLabel,
			&record.AccountEmail,
			&record.UpstreamAccountID,
			&record.ModelID,
			&record.InputTokens,
			&record.OutputTokens,
			&record.RequestCount,
			&record.CachedTokens,
			&record.ReasoningTokens,
		); err != nil {
			rows.Close()
			_ = tx.Rollback()
			return err
		}
		record.AccountProvider = auth.AccountProvider(accountProvider)
		occurredAt, err := time.Parse(time.RFC3339Nano, occurredAtRaw)
		if err != nil {
			occurredAt, err = time.Parse(time.RFC3339, occurredAtRaw)
			if err != nil {
				rows.Close()
				_ = tx.Rollback()
				return err
			}
		}
		record.OccurredAt = occurredAt
		if err := accumulateTotalsTx(tx, record); err != nil {
			rows.Close()
			_ = tx.Rollback()
			return err
		}
	}
	if err := rows.Close(); err != nil {
		_ = tx.Rollback()
		return err
	}

	return tx.Commit()
}

func ensureUsageSchema(db *sql.DB) error {
	statements := []string{
		`ALTER TABLE usage_events ADD COLUMN account_identity TEXT`,
		`ALTER TABLE usage_events ADD COLUMN account_provider TEXT`,
		`ALTER TABLE usage_events ADD COLUMN account_display_name TEXT`,
		`ALTER TABLE usage_events ADD COLUMN account_label TEXT`,
		`ALTER TABLE usage_events ADD COLUMN upstream_account_id TEXT`,
		`ALTER TABLE usage_events ADD COLUMN account_snapshot TEXT`,
		`ALTER TABLE usage_totals_global ADD COLUMN total_cached_tokens INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE usage_totals_by_model ADD COLUMN total_cached_tokens INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE usage_totals_by_account ADD COLUMN account_identity TEXT`,
		`ALTER TABLE usage_totals_by_account ADD COLUMN account_provider TEXT`,
		`ALTER TABLE usage_totals_by_account ADD COLUMN account_display_name TEXT`,
		`ALTER TABLE usage_totals_by_account ADD COLUMN account_label TEXT`,
		`ALTER TABLE usage_totals_by_account ADD COLUMN upstream_account_id TEXT`,
		`ALTER TABLE usage_totals_by_account ADD COLUMN total_cached_tokens INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE usage_totals_by_account_model ADD COLUMN account_identity TEXT`,
		`ALTER TABLE usage_totals_by_account_model ADD COLUMN account_provider TEXT`,
		`ALTER TABLE usage_totals_by_account_model ADD COLUMN account_display_name TEXT`,
		`ALTER TABLE usage_totals_by_account_model ADD COLUMN account_label TEXT`,
		`ALTER TABLE usage_totals_by_account_model ADD COLUMN upstream_account_id TEXT`,
		`ALTER TABLE usage_totals_by_account_model ADD COLUMN total_cached_tokens INTEGER NOT NULL DEFAULT 0`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column name") {
			return err
		}
	}
	ddl := []string{
		`DROP TABLE IF EXISTS usage_totals_by_account_model`,
		`DROP TABLE IF EXISTS usage_totals_by_account`,
		`CREATE TABLE IF NOT EXISTS usage_totals_by_account (
			account_identity TEXT PRIMARY KEY,
			account_id TEXT NOT NULL,
			account_provider TEXT,
			account_display_name TEXT,
			account_label TEXT,
			account_email TEXT,
			upstream_account_id TEXT,
			total_input_tokens INTEGER NOT NULL,
			total_output_tokens INTEGER NOT NULL,
			total_cached_tokens INTEGER NOT NULL,
			total_request_count INTEGER NOT NULL,
			last_recorded_at TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS usage_totals_by_account_model (
			account_identity TEXT NOT NULL,
			account_id TEXT NOT NULL,
			account_provider TEXT,
			account_display_name TEXT,
			account_label TEXT,
			account_email TEXT,
			upstream_account_id TEXT,
			model_id TEXT NOT NULL,
			total_input_tokens INTEGER NOT NULL,
			total_output_tokens INTEGER NOT NULL,
			total_cached_tokens INTEGER NOT NULL,
			total_request_count INTEGER NOT NULL,
			last_recorded_at TEXT,
			PRIMARY KEY (account_identity, model_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_events_account_identity ON usage_events(account_identity)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_events_account_display_name ON usage_events(account_display_name)`,
	}
	for _, statement := range ddl {
		if _, err := db.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) Summary() Summary {
	s.mu.RLock()
	defer s.mu.RUnlock()

	row := s.db.QueryRow(`
		SELECT total_input_tokens, total_output_tokens, total_cached_tokens, total_request_count, last_recorded_at
		FROM usage_totals_global
		WHERE bucket = 'global'
	`)
	var summary Summary
	var rawLastRecorded sql.NullString
	err := row.Scan(
		&summary.TotalInputTokens,
		&summary.TotalOutputTokens,
		&summary.TotalCachedTokens,
		&summary.TotalRequestCount,
		&rawLastRecorded,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return Summary{}
		}
		return Summary{}
	}
	summary.LastRecordedAt = parseNullableTime(rawLastRecorded)
	return summary
}

func (s *Service) Record(record Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	ensureUsageRecordDerivedFields(&record)
	if record.AccountIdentity == "" || record.ModelID == "" {
		return nil
	}
	if record.OccurredAt.IsZero() {
		record.OccurredAt = time.Now().UTC()
	}
	if record.RequestCount <= 0 {
		record.RequestCount = 1
	}
	recordedAt := record.OccurredAt.UTC().Format(time.RFC3339Nano)

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO usage_events (
			occurred_at, account_identity, account_id, account_provider, account_display_name,
			account_label, account_email, upstream_account_id, account_snapshot, model_id,
			input_tokens, output_tokens, request_count, cached_tokens, reasoning_tokens
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, recordedAt, record.AccountIdentity, record.AccountID, string(record.AccountProvider), record.AccountDisplayName,
		record.AccountLabel, record.AccountEmail, record.UpstreamAccountID, snapshotJSON(record.AccountSnapshot), record.ModelID,
		record.InputTokens, record.OutputTokens, record.RequestCount, record.CachedTokens, record.ReasoningTokens); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO usage_totals_global (bucket, total_input_tokens, total_output_tokens, total_cached_tokens, total_request_count, last_recorded_at)
		VALUES ('global', ?, ?, ?, ?, ?)
		ON CONFLICT(bucket) DO UPDATE SET
			total_input_tokens = total_input_tokens + excluded.total_input_tokens,
			total_output_tokens = total_output_tokens + excluded.total_output_tokens,
			total_cached_tokens = total_cached_tokens + excluded.total_cached_tokens,
			total_request_count = total_request_count + excluded.total_request_count,
			last_recorded_at = excluded.last_recorded_at
	`, record.InputTokens, record.OutputTokens, record.CachedTokens, record.RequestCount, recordedAt); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO usage_totals_by_account (
			account_identity, account_id, account_provider, account_display_name,
			account_label, account_email, upstream_account_id,
			total_input_tokens, total_output_tokens, total_cached_tokens, total_request_count, last_recorded_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(account_identity) DO UPDATE SET
			account_id = excluded.account_id,
			account_provider = excluded.account_provider,
			account_display_name = excluded.account_display_name,
			account_label = excluded.account_label,
			account_email = excluded.account_email,
			upstream_account_id = excluded.upstream_account_id,
			total_input_tokens = total_input_tokens + excluded.total_input_tokens,
			total_output_tokens = total_output_tokens + excluded.total_output_tokens,
			total_cached_tokens = total_cached_tokens + excluded.total_cached_tokens,
			total_request_count = total_request_count + excluded.total_request_count,
			last_recorded_at = excluded.last_recorded_at
	`, record.AccountIdentity, record.AccountID, string(record.AccountProvider), record.AccountDisplayName,
		record.AccountLabel, record.AccountEmail, record.UpstreamAccountID,
		record.InputTokens, record.OutputTokens, record.CachedTokens, record.RequestCount, recordedAt); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO usage_totals_by_model (model_id, total_input_tokens, total_output_tokens, total_cached_tokens, total_request_count, last_recorded_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(model_id) DO UPDATE SET
			total_input_tokens = total_input_tokens + excluded.total_input_tokens,
			total_output_tokens = total_output_tokens + excluded.total_output_tokens,
			total_cached_tokens = total_cached_tokens + excluded.total_cached_tokens,
			total_request_count = total_request_count + excluded.total_request_count,
			last_recorded_at = excluded.last_recorded_at
	`, record.ModelID, record.InputTokens, record.OutputTokens, record.CachedTokens, record.RequestCount, recordedAt); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO usage_totals_by_account_model (
			account_identity, account_id, account_provider, account_display_name,
			account_label, account_email, upstream_account_id, model_id,
			total_input_tokens, total_output_tokens, total_cached_tokens, total_request_count, last_recorded_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(account_identity, model_id) DO UPDATE SET
			account_id = excluded.account_id,
			account_provider = excluded.account_provider,
			account_display_name = excluded.account_display_name,
			account_label = excluded.account_label,
			account_email = excluded.account_email,
			upstream_account_id = excluded.upstream_account_id,
			total_input_tokens = total_input_tokens + excluded.total_input_tokens,
			total_output_tokens = total_output_tokens + excluded.total_output_tokens,
			total_cached_tokens = total_cached_tokens + excluded.total_cached_tokens,
			total_request_count = total_request_count + excluded.total_request_count,
			last_recorded_at = excluded.last_recorded_at
	`, record.AccountIdentity, record.AccountID, string(record.AccountProvider), record.AccountDisplayName,
		record.AccountLabel, record.AccountEmail, record.UpstreamAccountID, record.ModelID,
		record.InputTokens, record.OutputTokens, record.CachedTokens, record.RequestCount, recordedAt); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Service) History(from, to *time.Time, granularity string) ([]HistoryPoint, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	bucketExpr := usageBucketExpr(granularity)
	query := `
		SELECT ` + bucketExpr + ` AS bucket,
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cached_tokens), 0),
			COALESCE(SUM(request_count), 0)
		FROM usage_events
		WHERE (? IS NULL OR occurred_at >= ?)
			AND (? IS NULL OR occurred_at <= ?)
		GROUP BY bucket
		ORDER BY bucket
	`
	var fromValue any
	var toValue any
	if from != nil {
		fromValue = from.UTC().Format(time.RFC3339Nano)
	}
	if to != nil {
		toValue = to.UTC().Format(time.RFC3339Nano)
	}
	rows, err := s.db.Query(query, fromValue, fromValue, toValue, toValue)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var points []HistoryPoint
	for rows.Next() {
		var bucket string
		var point HistoryPoint
		if err := rows.Scan(
			&bucket,
			&point.InputTokens,
			&point.OutputTokens,
			&point.CachedTokens,
			&point.RequestCount,
		); err != nil {
			return nil, err
		}
		parsed, err := time.Parse(time.RFC3339, bucket)
		if err != nil {
			return nil, err
		}
		point.Timestamp = parsed
		point.TotalTokens = point.InputTokens + point.OutputTokens + point.CachedTokens
		points = append(points, point)
	}
	return points, rows.Err()
}

func (s *Service) BreakdownByAccount() ([]AccountBreakdown, error) {
	rows, err := s.db.Query(`
		SELECT
			account_identity,
			account_id,
			account_provider,
			account_display_name,
			account_label,
			account_email,
			upstream_account_id,
			total_input_tokens,
			total_output_tokens,
			total_cached_tokens,
			total_request_count,
			last_recorded_at
		FROM usage_totals_by_account
		ORDER BY last_recorded_at DESC, (total_input_tokens + total_output_tokens) DESC, account_display_name ASC, account_identity ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []AccountBreakdown
	for rows.Next() {
		var item AccountBreakdown
		var rawLastRecorded sql.NullString
		if err := rows.Scan(
			&item.AccountIdentity,
			&item.AccountID,
			&item.AccountProvider,
			&item.AccountDisplayName,
			&item.AccountLabel,
			&item.AccountEmail,
			&item.UpstreamAccountID,
			&item.TotalInputTokens,
			&item.TotalOutputTokens,
			&item.TotalCachedTokens,
			&item.TotalRequestCount,
			&rawLastRecorded,
		); err != nil {
			return nil, err
		}
		item.LastRecordedAt = parseNullableTime(rawLastRecorded)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Service) BreakdownByModel() ([]ModelBreakdown, error) {
	rows, err := s.db.Query(`
		SELECT model_id, total_input_tokens, total_output_tokens, total_cached_tokens, total_request_count, last_recorded_at
		FROM usage_totals_by_model
		WHERE model_id <> ?
		ORDER BY (total_input_tokens + total_output_tokens) DESC, model_id ASC
	`, legacyUnattributedModelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []ModelBreakdown
	for rows.Next() {
		var item ModelBreakdown
		var rawLastRecorded sql.NullString
		if err := rows.Scan(
			&item.ModelID,
			&item.TotalInputTokens,
			&item.TotalOutputTokens,
			&item.TotalCachedTokens,
			&item.TotalRequestCount,
			&rawLastRecorded,
		); err != nil {
			return nil, err
		}
		item.LastRecordedAt = parseNullableTime(rawLastRecorded)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Service) BreakdownByAccountModel() ([]AccountModelBreakdown, error) {
	rows, err := s.db.Query(`
		SELECT
			account_identity,
			account_id,
			account_provider,
			account_display_name,
			account_label,
			account_email,
			upstream_account_id,
			model_id,
			total_input_tokens,
			total_output_tokens,
			total_cached_tokens,
			total_request_count,
			last_recorded_at
		FROM usage_totals_by_account_model
		WHERE model_id <> ?
		ORDER BY last_recorded_at DESC, (total_input_tokens + total_output_tokens) DESC, account_display_name ASC, account_identity ASC, model_id ASC
	`, legacyUnattributedModelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []AccountModelBreakdown
	for rows.Next() {
		var item AccountModelBreakdown
		var rawLastRecorded sql.NullString
		if err := rows.Scan(
			&item.AccountIdentity,
			&item.AccountID,
			&item.AccountProvider,
			&item.AccountDisplayName,
			&item.AccountLabel,
			&item.AccountEmail,
			&item.UpstreamAccountID,
			&item.ModelID,
			&item.TotalInputTokens,
			&item.TotalOutputTokens,
			&item.TotalCachedTokens,
			&item.TotalRequestCount,
			&rawLastRecorded,
		); err != nil {
			return nil, err
		}
		item.LastRecordedAt = parseNullableTime(rawLastRecorded)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Service) Events(query EventQuery) (EventListResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	page := query.Page
	if page <= 0 {
		page = 1
	}
	pageSize := query.PageSize
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 200 {
		pageSize = 200
	}

	filters := []string{
		"model_id <> ?",
		"(? IS NULL OR occurred_at >= ?)",
		"(? IS NULL OR occurred_at <= ?)",
		"(? = '' OR model_id = ?)",
		"(? = '' OR account_identity = ? OR account_id = ? OR lower(trim(COALESCE(account_display_name, ''))) = lower(trim(?)) OR lower(trim(COALESCE(account_email, ''))) = lower(trim(?)))",
	}
	args := []any{
		legacyUnattributedModelID,
		nil, nil,
		nil, nil,
		query.ModelID, query.ModelID,
		query.AccountID, query.AccountID, query.AccountID, query.AccountID, query.AccountID,
	}
	if query.From != nil {
		fromValue := query.From.UTC().Format(time.RFC3339Nano)
		args[1] = fromValue
		args[2] = fromValue
	}
	if query.To != nil {
		toValue := query.To.UTC().Format(time.RFC3339Nano)
		args[3] = toValue
		args[4] = toValue
	}

	whereClause := strings.Join(filters, " AND ")

	var result EventListResult
	result.Page = page
	result.PageSize = pageSize

	summaryRow := s.db.QueryRow(`
		SELECT
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cached_tokens), 0),
			COALESCE(SUM(request_count), 0),
			MAX(occurred_at)
		FROM usage_events
		WHERE `+whereClause, args...)
	var summaryLastRecorded sql.NullString
	if err := summaryRow.Scan(
		&result.Summary.TotalInputTokens,
		&result.Summary.TotalOutputTokens,
		&result.Summary.TotalCachedTokens,
		&result.Summary.TotalRequestCount,
		&summaryLastRecorded,
	); err != nil {
		return result, err
	}
	result.Summary.LastRecordedAt = parseNullableTime(summaryLastRecorded)

	countRow := s.db.QueryRow(`SELECT COUNT(1) FROM usage_events WHERE `+whereClause, args...)
	if err := countRow.Scan(&result.Total); err != nil {
		return result, err
	}
	if result.Total == 0 {
		return result, nil
	}
	result.TotalPages = (result.Total + pageSize - 1) / pageSize

	offset := (page - 1) * pageSize
	rows, err := s.db.Query(`
		SELECT
			id, occurred_at, account_identity, account_id, account_provider, account_display_name, account_label, account_email, upstream_account_id, model_id,
			input_tokens, output_tokens, cached_tokens, request_count
		FROM usage_events
		WHERE `+whereClause+`
		ORDER BY occurred_at DESC, id DESC
		LIMIT ? OFFSET ?
	`, append(args, pageSize, offset)...)
	if err != nil {
		return result, err
	}
	defer rows.Close()

	for rows.Next() {
		var item EventItem
		var occurredAt string
		if err := rows.Scan(
			&item.ID,
			&occurredAt,
			&item.AccountIdentity,
			&item.AccountID,
			&item.AccountProvider,
			&item.AccountDisplayName,
			&item.AccountLabel,
			&item.AccountEmail,
			&item.UpstreamAccountID,
			&item.ModelID,
			&item.InputTokens,
			&item.OutputTokens,
			&item.CachedTokens,
			&item.RequestCount,
		); err != nil {
			return result, err
		}
		parsed, err := time.Parse(time.RFC3339Nano, occurredAt)
		if err != nil {
			return result, err
		}
		item.OccurredAt = parsed
		item.TotalTokens = item.InputTokens + item.OutputTokens + item.CachedTokens
		result.Items = append(result.Items, item)
	}
	return result, rows.Err()
}

func (s *Service) BootstrapFromAccounts(accounts []auth.Account) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var existing int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM usage_events`).Scan(&existing); err != nil {
		return err
	}
	if existing > 0 {
		return nil
	}

	var records []Record
	for _, account := range accounts {
		if account.Usage.RequestCount == 0 && account.Usage.InputTokens == 0 && account.Usage.OutputTokens == 0 {
			continue
		}
		recordedAt := account.Usage.LastUsedAt
		if recordedAt == nil || recordedAt.IsZero() {
			recordedAt = &account.UpdatedAt
		}
		if recordedAt == nil || recordedAt.IsZero() {
			now := time.Now().UTC()
			recordedAt = &now
		}
		snapshot := auth.BuildUsageSnapshot(account)
		records = append(records, Record{
			OccurredAt:         *recordedAt,
			AccountID:          account.ID,
			AccountProvider:    account.Provider,
			AccountIdentity:    snapshot.UsageIdentity,
			AccountDisplayName: snapshot.DisplayName,
			AccountLabel:       snapshot.Label,
			AccountEmail:       snapshot.Email,
			UpstreamAccountID:  snapshot.UpstreamID,
			AccountSnapshot:    &snapshot,
			ModelID:            legacyUnattributedModelID,
			InputTokens:        account.Usage.InputTokens,
			OutputTokens:       account.Usage.OutputTokens,
			RequestCount:       account.Usage.RequestCount,
		})
	}

	if len(records) == 0 {
		var legacy struct {
			Summary Summary `json:"summary"`
		}
		if err := s.stateStore.Load(s.cfg.Storage.UsageStatsFile, &legacy); err == nil {
			if legacy.Summary.TotalRequestCount > 0 || legacy.Summary.TotalInputTokens > 0 || legacy.Summary.TotalOutputTokens > 0 {
				recordedAt := time.Now().UTC()
				if legacy.Summary.LastRecordedAt != nil {
					recordedAt = legacy.Summary.LastRecordedAt.UTC()
				}
				records = append(records, Record{
					OccurredAt:         recordedAt,
					AccountID:          legacyUnattributedModelID,
					AccountProvider:    auth.ProviderCustom,
					AccountIdentity:    "legacy:" + legacyUnattributedModelID,
					AccountDisplayName: legacyUnattributedModelID,
					AccountLabel:       legacyUnattributedModelID,
					AccountEmail:       legacyUnattributedModelID,
					ModelID:            legacyUnattributedModelID,
					InputTokens:        legacy.Summary.TotalInputTokens,
					OutputTokens:       legacy.Summary.TotalOutputTokens,
					RequestCount:       legacy.Summary.TotalRequestCount,
				})
			}
		}
	}

	if len(records) == 0 {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	for _, record := range records {
		ensureUsageRecordDerivedFields(&record)
		recordedAtValue := record.OccurredAt.UTC().Format(time.RFC3339Nano)
		if _, err := tx.Exec(`
			INSERT INTO usage_events (
				occurred_at, account_identity, account_id, account_provider, account_display_name,
				account_label, account_email, upstream_account_id, account_snapshot, model_id,
				input_tokens, output_tokens, request_count, cached_tokens, reasoning_tokens
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0)
		`, recordedAtValue, record.AccountIdentity, record.AccountID, string(record.AccountProvider), record.AccountDisplayName,
			record.AccountLabel, record.AccountEmail, record.UpstreamAccountID, snapshotJSON(record.AccountSnapshot), record.ModelID,
			record.InputTokens, record.OutputTokens, record.RequestCount); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := accumulateTotalsTx(tx, record); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func usageBucketExpr(granularity string) string {
	switch granularity {
	case "hour":
		return "strftime('%Y-%m-%dT%H:00:00Z', occurred_at)"
	case "month":
		return "strftime('%Y-%m-01T00:00:00Z', occurred_at)"
	default:
		return "strftime('%Y-%m-%dT00:00:00Z', occurred_at)"
	}
}

func parseNullableTime(value sql.NullString) *time.Time {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		parsed, err = time.Parse(time.RFC3339, value.String)
		if err != nil {
			return nil
		}
	}
	return &parsed
}

func accumulateTotalsTx(tx *sql.Tx, record Record) error {
	ensureUsageRecordDerivedFields(&record)
	recordedAt := record.OccurredAt.UTC().Format(time.RFC3339Nano)
	if _, err := tx.Exec(`
		INSERT INTO usage_totals_global (bucket, total_input_tokens, total_output_tokens, total_cached_tokens, total_request_count, last_recorded_at)
		VALUES ('global', ?, ?, ?, ?, ?)
		ON CONFLICT(bucket) DO UPDATE SET
			total_input_tokens = total_input_tokens + excluded.total_input_tokens,
			total_output_tokens = total_output_tokens + excluded.total_output_tokens,
			total_cached_tokens = total_cached_tokens + excluded.total_cached_tokens,
			total_request_count = total_request_count + excluded.total_request_count,
			last_recorded_at = excluded.last_recorded_at
	`, record.InputTokens, record.OutputTokens, record.CachedTokens, record.RequestCount, recordedAt); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO usage_totals_by_account (
			account_identity, account_id, account_provider, account_display_name,
			account_label, account_email, upstream_account_id,
			total_input_tokens, total_output_tokens, total_cached_tokens, total_request_count, last_recorded_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(account_identity) DO UPDATE SET
			account_id = excluded.account_id,
			account_provider = excluded.account_provider,
			account_display_name = excluded.account_display_name,
			account_label = excluded.account_label,
			account_email = excluded.account_email,
			upstream_account_id = excluded.upstream_account_id,
			total_input_tokens = total_input_tokens + excluded.total_input_tokens,
			total_output_tokens = total_output_tokens + excluded.total_output_tokens,
			total_cached_tokens = total_cached_tokens + excluded.total_cached_tokens,
			total_request_count = total_request_count + excluded.total_request_count,
			last_recorded_at = excluded.last_recorded_at
	`, record.AccountIdentity, record.AccountID, string(record.AccountProvider), record.AccountDisplayName,
		record.AccountLabel, record.AccountEmail, record.UpstreamAccountID,
		record.InputTokens, record.OutputTokens, record.CachedTokens, record.RequestCount, recordedAt); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO usage_totals_by_model (model_id, total_input_tokens, total_output_tokens, total_cached_tokens, total_request_count, last_recorded_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(model_id) DO UPDATE SET
			total_input_tokens = total_input_tokens + excluded.total_input_tokens,
			total_output_tokens = total_output_tokens + excluded.total_output_tokens,
			total_cached_tokens = total_cached_tokens + excluded.total_cached_tokens,
			total_request_count = total_request_count + excluded.total_request_count,
			last_recorded_at = excluded.last_recorded_at
	`, record.ModelID, record.InputTokens, record.OutputTokens, record.CachedTokens, record.RequestCount, recordedAt); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO usage_totals_by_account_model (
			account_identity, account_id, account_provider, account_display_name,
			account_label, account_email, upstream_account_id, model_id,
			total_input_tokens, total_output_tokens, total_cached_tokens, total_request_count, last_recorded_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(account_identity, model_id) DO UPDATE SET
			account_id = excluded.account_id,
			account_provider = excluded.account_provider,
			account_display_name = excluded.account_display_name,
			account_label = excluded.account_label,
			account_email = excluded.account_email,
			upstream_account_id = excluded.upstream_account_id,
			total_input_tokens = total_input_tokens + excluded.total_input_tokens,
			total_output_tokens = total_output_tokens + excluded.total_output_tokens,
			total_cached_tokens = total_cached_tokens + excluded.total_cached_tokens,
			total_request_count = total_request_count + excluded.total_request_count,
			last_recorded_at = excluded.last_recorded_at
	`, record.AccountIdentity, record.AccountID, string(record.AccountProvider), record.AccountDisplayName,
		record.AccountLabel, record.AccountEmail, record.UpstreamAccountID, record.ModelID,
		record.InputTokens, record.OutputTokens, record.CachedTokens, record.RequestCount, recordedAt); err != nil {
		return err
	}
	return nil
}
