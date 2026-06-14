package usage

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/jushjay/prism/internal/auth"
	"github.com/jushjay/prism/internal/config"
	"github.com/jushjay/prism/internal/store"
)

func TestServiceRecordPersistsSummaryAndBreakdowns(t *testing.T) {
	service, sqliteStore, cleanup := newTestService(t)
	defer cleanup()

	firstAt := time.Date(2026, 4, 26, 3, 0, 0, 0, time.UTC)
	secondAt := firstAt.Add(2 * time.Hour)
	if err := service.Record(Record{
		OccurredAt:   firstAt,
		AccountID:    "acct-1",
		AccountEmail: "one@example.com",
		ModelID:      "gpt-5.4",
		InputTokens:  120,
		OutputTokens: 34,
		CachedTokens: 11,
		RequestCount: 1,
	}); err != nil {
		t.Fatalf("record first usage: %v", err)
	}
	if err := service.Record(Record{
		OccurredAt:   secondAt,
		AccountID:    "acct-2",
		AccountEmail: "two@example.com",
		ModelID:      "gpt-5.4-mini",
		InputTokens:  80,
		OutputTokens: 21,
		CachedTokens: 7,
		RequestCount: 2,
	}); err != nil {
		t.Fatalf("record second usage: %v", err)
	}

	summary := service.Summary()
	if summary.TotalRequestCount != 3 {
		t.Fatalf("expected 3 requests, got %d", summary.TotalRequestCount)
	}
	if summary.TotalInputTokens != 200 || summary.TotalOutputTokens != 55 || summary.TotalCachedTokens != 18 {
		t.Fatalf("unexpected token totals %+v", summary)
	}
	if summary.LastRecordedAt == nil || !summary.LastRecordedAt.Equal(secondAt) {
		t.Fatalf("expected last recorded timestamp to match second event, got %+v", summary.LastRecordedAt)
	}

	reloaded, err := NewService(config.Config{
		Storage: config.StorageConfig{
			BaseDir:        sqliteStore.BaseDir(),
			DBFile:         sqliteStore.DBFile(),
			UsageStatsFile: filepath.Join(sqliteStore.BaseDir(), "usage-stats.json"),
		},
	}, sqliteStore.DB(), sqliteStore)
	if err != nil {
		t.Fatalf("reload service: %v", err)
	}
	reloadedSummary := reloaded.Summary()
	if reloadedSummary.TotalRequestCount != 3 {
		t.Fatalf("expected persisted request count, got %d", reloadedSummary.TotalRequestCount)
	}

	accountItems, err := reloaded.BreakdownByAccount()
	if err != nil {
		t.Fatalf("breakdown by account: %v", err)
	}
	if len(accountItems) != 2 {
		t.Fatalf("expected 2 account breakdown items, got %d", len(accountItems))
	}
	if accountItems[0].AccountDisplayName == "" || accountItems[1].AccountDisplayName == "" {
		t.Fatalf("expected account display names to be populated, got %+v", accountItems)
	}

	modelItems, err := reloaded.BreakdownByModel()
	if err != nil {
		t.Fatalf("breakdown by model: %v", err)
	}
	if len(modelItems) != 2 {
		t.Fatalf("expected 2 model breakdown items, got %d", len(modelItems))
	}
	if modelItems[0].TotalCachedTokens+modelItems[1].TotalCachedTokens != 18 {
		t.Fatalf("expected cached token totals in model breakdown, got %+v", modelItems)
	}

	points, err := reloaded.History(nil, nil, "hour")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(points) != 2 {
		t.Fatalf("expected 2 history points, got %d", len(points))
	}
	if points[0].CachedTokens != 11 || points[1].CachedTokens != 7 {
		t.Fatalf("expected cached token history points, got %+v", points)
	}
}

func TestBreakdownByAccountSeparatesByStableIdentity(t *testing.T) {
	service, _, cleanup := newTestService(t)
	defer cleanup()

	firstAt := time.Date(2026, 5, 9, 10, 32, 59, 0, time.UTC)
	secondAt := time.Date(2026, 5, 15, 7, 57, 1, 0, time.UTC)
	if err := service.Record(Record{
		OccurredAt:         firstAt,
		AccountID:          "old-internal-id",
		AccountProvider:    auth.ProviderOpenAI,
		AccountIdentity:    "openai:acct-openai",
		AccountDisplayName: "zhu0530sj@gmail.com",
		AccountEmail:       "zhu0530sj@gmail.com",
		UpstreamAccountID:  "acct-openai",
		ModelID:            "gpt-5.5",
		InputTokens:        23198792,
		OutputTokens:       104391,
		RequestCount:       339,
	}); err != nil {
		t.Fatalf("record old account usage: %v", err)
	}
	if err := service.Record(Record{
		OccurredAt:         secondAt,
		AccountID:          "new-internal-id",
		AccountProvider:    auth.ProviderOpenAI,
		AccountIdentity:    "openai:acct-openai",
		AccountDisplayName: "zhu0530sj@gmail.com",
		AccountEmail:       "zhu0530sj@gmail.com",
		UpstreamAccountID:  "acct-openai",
		ModelID:            "gpt-5.4",
		InputTokens:        20350172,
		OutputTokens:       43049,
		RequestCount:       140,
	}); err != nil {
		t.Fatalf("record new account usage: %v", err)
	}
	if err := service.Record(Record{
		OccurredAt:         secondAt.Add(-time.Hour),
		AccountID:          "other-account",
		AccountProvider:    auth.ProviderOpenAI,
		AccountIdentity:    "openai_email:other@example.com",
		AccountDisplayName: "other@example.com",
		AccountEmail:       "other@example.com",
		ModelID:            "gpt-5.4-mini",
		InputTokens:        100,
		OutputTokens:       20,
		RequestCount:       1,
	}); err != nil {
		t.Fatalf("record other account usage: %v", err)
	}

	accountItems, err := service.BreakdownByAccount()
	if err != nil {
		t.Fatalf("breakdown by account: %v", err)
	}
	if len(accountItems) != 2 {
		t.Fatalf("expected 2 merged account breakdown items, got %d", len(accountItems))
	}
	if accountItems[0].AccountIdentity != "openai:acct-openai" {
		t.Fatalf("expected merged item identity to use stable key, got %q", accountItems[0].AccountIdentity)
	}
	if accountItems[0].AccountDisplayName != "zhu0530sj@gmail.com" {
		t.Fatalf("expected merged display name, got %q", accountItems[0].AccountDisplayName)
	}
	if accountItems[0].TotalRequestCount != 479 {
		t.Fatalf("expected merged request count 479, got %d", accountItems[0].TotalRequestCount)
	}
	if accountItems[0].TotalInputTokens != 43548964 || accountItems[0].TotalOutputTokens != 147440 {
		t.Fatalf("unexpected merged token totals %+v", accountItems[0])
	}
	if accountItems[0].LastRecordedAt == nil || !accountItems[0].LastRecordedAt.Equal(secondAt) {
		t.Fatalf("expected latest timestamp on merged account, got %+v", accountItems[0].LastRecordedAt)
	}

	accountModelItems, err := service.BreakdownByAccountModel()
	if err != nil {
		t.Fatalf("breakdown by account model: %v", err)
	}
	if len(accountModelItems) != 3 {
		t.Fatalf("expected 3 account-model breakdown items, got %d", len(accountModelItems))
	}
	if accountModelItems[0].AccountIdentity != "openai:acct-openai" {
		t.Fatalf("expected stable account identity in account-model breakdown, got %q", accountModelItems[0].AccountIdentity)
	}
	if accountModelItems[0].AccountDisplayName != "zhu0530sj@gmail.com" {
		t.Fatalf("expected merged display in account-model breakdown, got %q", accountModelItems[0].AccountDisplayName)
	}
}

func TestBootstrapFromAccountsKeepsLegacyUsageOutOfModelBreakdowns(t *testing.T) {
	service, _, cleanup := newTestService(t)
	defer cleanup()

	lastUsedAt := time.Date(2026, 4, 26, 8, 0, 0, 0, time.UTC)
	accounts := []auth.Account{
		{
			ID:        "acct-1",
			Email:     "one@example.com",
			UpdatedAt: lastUsedAt,
			Usage: auth.AccountUsage{
				RequestCount: 3,
				InputTokens:  120,
				OutputTokens: 45,
				LastUsedAt:   &lastUsedAt,
			},
		},
	}

	if err := service.BootstrapFromAccounts(accounts); err != nil {
		t.Fatalf("bootstrap from accounts: %v", err)
	}

	summary := service.Summary()
	if summary.TotalRequestCount != 3 || summary.TotalInputTokens != 120 || summary.TotalOutputTokens != 45 {
		t.Fatalf("unexpected summary after bootstrap: %+v", summary)
	}

	accountItems, err := service.BreakdownByAccount()
	if err != nil {
		t.Fatalf("breakdown by account: %v", err)
	}
	if len(accountItems) != 1 {
		t.Fatalf("expected 1 account breakdown item, got %d", len(accountItems))
	}

	modelItems, err := service.BreakdownByModel()
	if err != nil {
		t.Fatalf("breakdown by model: %v", err)
	}
	if len(modelItems) != 0 {
		t.Fatalf("expected legacy bootstrap data to be excluded from model breakdown, got %d items", len(modelItems))
	}

	accountModelItems, err := service.BreakdownByAccountModel()
	if err != nil {
		t.Fatalf("breakdown by account-model: %v", err)
	}
	if len(accountModelItems) != 0 {
		t.Fatalf("expected legacy bootstrap data to be excluded from account-model breakdown, got %d items", len(accountModelItems))
	}
}

func TestBootstrapFromLegacySummaryKeepsLegacyUsageOutOfModelBreakdowns(t *testing.T) {
	service, sqliteStore, cleanup := newTestService(t)
	defer cleanup()

	recordedAt := time.Date(2026, 4, 26, 10, 0, 0, 0, time.UTC)
	legacyState := struct {
		Summary Summary `json:"summary"`
	}{
		Summary: Summary{
			TotalInputTokens:  250,
			TotalOutputTokens: 80,
			TotalRequestCount: 7,
			LastRecordedAt:    &recordedAt,
		},
	}

	if err := sqliteStore.Save(service.cfg.Storage.UsageStatsFile, legacyState); err != nil {
		t.Fatalf("save legacy usage stats: %v", err)
	}

	if err := service.BootstrapFromAccounts(nil); err != nil {
		t.Fatalf("bootstrap from legacy summary: %v", err)
	}

	summary := service.Summary()
	if summary.TotalRequestCount != 7 || summary.TotalInputTokens != 250 || summary.TotalOutputTokens != 80 {
		t.Fatalf("unexpected summary after legacy bootstrap: %+v", summary)
	}

	modelItems, err := service.BreakdownByModel()
	if err != nil {
		t.Fatalf("breakdown by model: %v", err)
	}
	if len(modelItems) != 0 {
		t.Fatalf("expected legacy summary data to be excluded from model breakdown, got %d items", len(modelItems))
	}

	accountModelItems, err := service.BreakdownByAccountModel()
	if err != nil {
		t.Fatalf("breakdown by account-model: %v", err)
	}
	if len(accountModelItems) != 0 {
		t.Fatalf("expected legacy summary data to be excluded from account-model breakdown, got %d items", len(accountModelItems))
	}
}

func newTestService(t *testing.T) (*Service, *store.SQLiteStore, func()) {
	t.Helper()

	baseDir := t.TempDir()
	dbFile := filepath.Join(baseDir, "test.db")
	sqliteStore, err := store.NewSQLiteStore(dbFile, baseDir)
	if err != nil {
		t.Fatalf("new sqlite store: %v", err)
	}

	service, err := NewService(config.Config{
		Storage: config.StorageConfig{
			BaseDir:        baseDir,
			DBFile:         dbFile,
			UsageStatsFile: filepath.Join(baseDir, "usage-stats.json"),
		},
	}, sqliteStore.DB(), sqliteStore)
	if err != nil {
		_ = sqliteStore.Close()
		t.Fatalf("new service: %v", err)
	}
	cleanup := func() {
		_ = sqliteStore.Close()
	}
	return service, sqliteStore, cleanup
}
