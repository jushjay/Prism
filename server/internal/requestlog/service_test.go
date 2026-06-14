package requestlog

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/jushjay/prism/internal/auth"
	"github.com/jushjay/prism/internal/store"
)

func TestServiceRecordPersistsRequestEvent(t *testing.T) {
	dir := t.TempDir()
	sqliteStore, err := store.NewSQLiteStore(filepath.Join(dir, "state.db"), dir)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer sqliteStore.Close()

	service, err := NewService(sqliteStore.DB())
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	startedAt := time.Now().Add(-2 * time.Second).UTC()
	completedAt := startedAt.Add(1500 * time.Millisecond)
	firstTokenMs := 220
	statusCode := 200
	inputTokens := 11
	outputTokens := 7
	cachedTokens := 3
	reasoningTokens := 2

	record := Record{
		StartedAt:       startedAt,
		CompletedAt:     completedAt,
		DurationMs:      1500,
		FirstTokenMs:    &firstTokenMs,
		Success:         true,
		StatusCode:      &statusCode,
		SourcePath:      "/v1/chat/completions",
		EndpointStyle:   "/codex/responses",
		RequestStream:   false,
		RetryAttempt:    1,
		UpstreamType:    "openai",
		AccountID:       "acct-1",
		AccountProvider: auth.ProviderOpenAI,
		AccountSnapshot: &auth.UsageAccountSnapshot{
			AccountID:     "acct-1",
			Provider:      auth.ProviderOpenAI,
			UsageIdentity: "openai:acct-1",
			DisplayName:   "acct@example.com",
			Email:         "acct@example.com",
			UpstreamID:    "upstream-acct-1",
		},
		RequestedModel:    "OpenAI-gpt5",
		RoutedModel:       "gpt-5.4",
		UpstreamRequestID: "req-123",
		ResponseID:        "resp-123",
		InputTokens:       &inputTokens,
		OutputTokens:      &outputTokens,
		CachedTokens:      &cachedTokens,
		ReasoningTokens:   &reasoningTokens,
	}

	if err := service.Record(record); err != nil {
		t.Fatalf("Record() error = %v", err)
	}

	var count int
	if err := sqliteStore.DB().QueryRow(`SELECT COUNT(1) FROM request_events`).Scan(&count); err != nil {
		t.Fatalf("count request_events: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 request event, got %d", count)
	}

	var gotDuration, gotSuccess, gotStream, gotRetry int
	var gotStartedAt, gotCompletedAt, gotSourcePath, gotRequestedModel, gotRoutedModel, gotRequestID, gotResponseID string
	if err := sqliteStore.DB().QueryRow(`
		SELECT started_at, completed_at, duration_ms, success, source_path, request_stream, retry_attempt, requested_model, routed_model, upstream_request_id, response_id
		FROM request_events
		LIMIT 1
	`).Scan(&gotStartedAt, &gotCompletedAt, &gotDuration, &gotSuccess, &gotSourcePath, &gotStream, &gotRetry, &gotRequestedModel, &gotRoutedModel, &gotRequestID, &gotResponseID); err != nil {
		t.Fatalf("select request_events: %v", err)
	}
	if gotStartedAt != startedAt.Format(time.RFC3339Nano) {
		t.Fatalf("unexpected started_at %q", gotStartedAt)
	}
	if gotCompletedAt != completedAt.Format(time.RFC3339Nano) {
		t.Fatalf("unexpected completed_at %q", gotCompletedAt)
	}
	if gotDuration != 1500 || gotSuccess != 1 || gotStream != 0 || gotRetry != 1 {
		t.Fatalf("unexpected scalar fields duration=%d success=%d stream=%d retry=%d", gotDuration, gotSuccess, gotStream, gotRetry)
	}
	if gotSourcePath != "/v1/chat/completions" || gotRequestedModel != "OpenAI-gpt5" || gotRoutedModel != "gpt-5.4" || gotRequestID != "req-123" || gotResponseID != "resp-123" {
		t.Fatalf("unexpected text fields source=%q requested=%q routed=%q reqID=%q respID=%q", gotSourcePath, gotRequestedModel, gotRoutedModel, gotRequestID, gotResponseID)
	}
}

func TestServiceEventsAndSummary(t *testing.T) {
	dir := t.TempDir()
	sqliteStore, err := store.NewSQLiteStore(filepath.Join(dir, "state.db"), dir)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer sqliteStore.Close()

	service, err := NewService(sqliteStore.DB())
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	startedAt := time.Now().Add(-5 * time.Minute).UTC()
	for idx, item := range []struct {
		success      bool
		durationMs   int
		firstTokenMs *int
		statusCode   *int
		sourcePath   string
	}{
		{success: true, durationMs: 1000, firstTokenMs: intPtr(120), statusCode: intPtr(200), sourcePath: "/v1/responses"},
		{success: false, durationMs: 2500, firstTokenMs: nil, statusCode: intPtr(502), sourcePath: "/v1/chat/completions"},
		{success: true, durationMs: 1500, firstTokenMs: intPtr(200), statusCode: intPtr(200), sourcePath: "/v1/responses"},
	} {
		record := Record{
			StartedAt:       startedAt.Add(time.Duration(idx) * time.Minute),
			CompletedAt:     startedAt.Add(time.Duration(idx)*time.Minute + time.Duration(item.durationMs)*time.Millisecond),
			DurationMs:      item.durationMs,
			FirstTokenMs:    item.firstTokenMs,
			Success:         item.success,
			StatusCode:      item.statusCode,
			SourcePath:      item.sourcePath,
			EndpointStyle:   "/codex/responses",
			RequestStream:   false,
			RetryAttempt:    idx,
			UpstreamType:    "openai",
			AccountID:       "acct-1",
			AccountProvider: auth.ProviderOpenAI,
			AccountSnapshot: &auth.UsageAccountSnapshot{
				AccountID:     "acct-1",
				Provider:      auth.ProviderOpenAI,
				UsageIdentity: "openai:acct-1",
				DisplayName:   "acct@example.com",
			},
			RequestedModel: "gpt-5",
			RoutedModel:    "gpt-5.4",
			ResponseID:     "resp-" + string(rune('a'+idx)),
		}
		if err := service.Record(record); err != nil {
			t.Fatalf("Record() error = %v", err)
		}
	}

	summary, err := service.Summary(EventQuery{})
	if err != nil {
		t.Fatalf("Summary() error = %v", err)
	}
	if summary.TotalRequestCount != 3 || summary.SuccessRequestCount != 2 || summary.FailedRequestCount != 1 {
		t.Fatalf("unexpected summary counts %+v", summary)
	}
	if summary.AvgDurationMs <= 0 {
		t.Fatalf("expected positive avg duration, got %f", summary.AvgDurationMs)
	}
	if summary.AvgFirstTokenMs == nil || *summary.AvgFirstTokenMs <= 0 {
		t.Fatalf("expected avg first token ms, got %#v", summary.AvgFirstTokenMs)
	}

	success := true
	result, err := service.Events(EventQuery{Success: &success, Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if result.Total != 2 || len(result.Items) != 2 {
		t.Fatalf("unexpected event result total=%d len=%d", result.Total, len(result.Items))
	}
	for _, item := range result.Items {
		if !item.Success {
			t.Fatalf("expected only success items, got %+v", item)
		}
	}
}

func intPtr(value int) *int {
	return &value
}
