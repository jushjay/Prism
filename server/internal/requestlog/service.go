package requestlog

import (
	"database/sql"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/jushjay/prism/internal/auth"
)

const maxErrorMessageLength = 4096

type Record struct {
	StartedAt    time.Time
	CompletedAt  time.Time
	DurationMs   int
	FirstTokenMs *int

	Success      bool
	StatusCode   *int
	ErrorMessage string

	SourcePath    string
	EndpointStyle string
	RequestStream bool
	RetryAttempt  int
	UpstreamType  string

	AccountID          string
	AccountProvider    auth.AccountProvider
	AccountIdentity    string
	AccountDisplayName string
	AccountLabel       string
	AccountEmail       string
	UpstreamAccountID  string
	AccountSnapshot    *auth.UsageAccountSnapshot

	RequestedModel string
	RoutedModel    string

	UpstreamRequestID string
	ResponseID        string

	InputTokens     *int
	OutputTokens    *int
	CachedTokens    *int
	ReasoningTokens *int
}

type Service struct {
	mu sync.RWMutex
	db *sql.DB
}

type Summary struct {
	TotalRequestCount   int        `json:"total_request_count"`
	SuccessRequestCount int        `json:"success_request_count"`
	FailedRequestCount  int        `json:"failed_request_count"`
	AvgDurationMs       float64    `json:"avg_duration_ms"`
	AvgFirstTokenMs     *float64   `json:"avg_first_token_ms"`
	LastCompletedAt     *time.Time `json:"last_completed_at,omitempty"`
}

type EventQuery struct {
	From       *time.Time
	To         *time.Time
	ModelID    string
	AccountID  string
	SourcePath string
	Success    *bool
	Page       int
	PageSize   int
}

type EventItem struct {
	ID                 int       `json:"id"`
	StartedAt          time.Time `json:"started_at"`
	CompletedAt        time.Time `json:"completed_at"`
	DurationMs         int       `json:"duration_ms"`
	FirstTokenMs       *int      `json:"first_token_ms"`
	Success            bool      `json:"success"`
	StatusCode         *int      `json:"status_code"`
	ErrorMessage       string    `json:"error_message"`
	SourcePath         string    `json:"source_path"`
	EndpointStyle      string    `json:"endpoint_style"`
	RequestStream      bool      `json:"request_stream"`
	RetryAttempt       int       `json:"retry_attempt"`
	UpstreamType       string    `json:"upstream_type"`
	AccountIdentity    string    `json:"account_identity"`
	AccountID          string    `json:"account_id"`
	AccountProvider    string    `json:"account_provider"`
	AccountDisplayName string    `json:"account_display_name"`
	AccountLabel       string    `json:"account_label"`
	AccountEmail       string    `json:"account_email"`
	UpstreamAccountID  string    `json:"upstream_account_id"`
	RequestedModel     string    `json:"requested_model"`
	RoutedModel        string    `json:"routed_model"`
	UpstreamRequestID  string    `json:"upstream_request_id"`
	ResponseID         string    `json:"response_id"`
	InputTokens        *int      `json:"input_tokens"`
	OutputTokens       *int      `json:"output_tokens"`
	CachedTokens       *int      `json:"cached_tokens"`
	ReasoningTokens    *int      `json:"reasoning_tokens"`
}

type EventListResult struct {
	Items      []EventItem `json:"items"`
	Page       int         `json:"page"`
	PageSize   int         `json:"page_size"`
	Total      int         `json:"total"`
	TotalPages int         `json:"total_pages"`
	Summary    Summary     `json:"summary"`
}

func NewService(db *sql.DB) (*Service, error) {
	return &Service{db: db}, nil
}

func (s *Service) Record(record Record) error {
	if s == nil || s.db == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	record = normalizeRecord(record)

	_, err := s.db.Exec(`
		INSERT INTO request_events (
			started_at,
			completed_at,
			duration_ms,
			first_token_ms,
			success,
			status_code,
			error_message,
			source_path,
			endpoint_style,
			request_stream,
			retry_attempt,
			upstream_type,
			account_id,
			account_provider,
			account_identity,
			account_display_name,
			account_label,
			account_email,
			upstream_account_id,
			account_snapshot,
			requested_model,
			routed_model,
			upstream_request_id,
			response_id,
			input_tokens,
			output_tokens,
			cached_tokens,
			reasoning_tokens
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		record.StartedAt.UTC().Format(time.RFC3339Nano),
		record.CompletedAt.UTC().Format(time.RFC3339Nano),
		record.DurationMs,
		record.FirstTokenMs,
		boolToInt(record.Success),
		record.StatusCode,
		record.ErrorMessage,
		record.SourcePath,
		record.EndpointStyle,
		boolToInt(record.RequestStream),
		record.RetryAttempt,
		record.UpstreamType,
		record.AccountID,
		string(record.AccountProvider),
		record.AccountIdentity,
		record.AccountDisplayName,
		record.AccountLabel,
		record.AccountEmail,
		record.UpstreamAccountID,
		snapshotJSON(record.AccountSnapshot),
		record.RequestedModel,
		record.RoutedModel,
		record.UpstreamRequestID,
		record.ResponseID,
		record.InputTokens,
		record.OutputTokens,
		record.CachedTokens,
		record.ReasoningTokens,
	)
	return err
}

func (s *Service) Summary(query EventQuery) (Summary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.summaryLocked(query)
}

func (s *Service) summaryLocked(query EventQuery) (Summary, error) {
	whereClause, args := buildEventWhereClause(query)
	row := s.db.QueryRow(`
		SELECT
			COUNT(1),
			COALESCE(SUM(CASE WHEN success = 1 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN success = 0 THEN 1 ELSE 0 END), 0),
			COALESCE(AVG(duration_ms), 0),
			AVG(first_token_ms),
			MAX(completed_at)
		FROM request_events
		WHERE `+whereClause, args...)

	var summary Summary
	var avgFirstTokenMs sql.NullFloat64
	var rawLastCompleted sql.NullString
	if err := row.Scan(
		&summary.TotalRequestCount,
		&summary.SuccessRequestCount,
		&summary.FailedRequestCount,
		&summary.AvgDurationMs,
		&avgFirstTokenMs,
		&rawLastCompleted,
	); err != nil {
		return summary, err
	}
	if avgFirstTokenMs.Valid {
		value := avgFirstTokenMs.Float64
		summary.AvgFirstTokenMs = &value
	}
	summary.LastCompletedAt = parseNullableTime(rawLastCompleted)
	return summary, nil
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

	whereClause, args := buildEventWhereClause(query)

	var result EventListResult
	result.Page = page
	result.PageSize = pageSize

	summary, err := s.summaryLocked(query)
	if err != nil {
		return result, err
	}
	result.Summary = summary

	countRow := s.db.QueryRow(`SELECT COUNT(1) FROM request_events WHERE `+whereClause, args...)
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
			id,
			started_at,
			completed_at,
			duration_ms,
			first_token_ms,
			success,
			status_code,
			error_message,
			source_path,
			endpoint_style,
			request_stream,
			retry_attempt,
			upstream_type,
			account_identity,
			account_id,
			account_provider,
			account_display_name,
			account_label,
			account_email,
			upstream_account_id,
			requested_model,
			routed_model,
			upstream_request_id,
			response_id,
			input_tokens,
			output_tokens,
			cached_tokens,
			reasoning_tokens
		FROM request_events
		WHERE `+whereClause+`
		ORDER BY completed_at DESC, id DESC
		LIMIT ? OFFSET ?
	`, append(args, pageSize, offset)...)
	if err != nil {
		return result, err
	}
	defer rows.Close()

	for rows.Next() {
		var item EventItem
		var startedAt string
		var completedAt string
		var firstTokenMs sql.NullInt64
		var successInt int
		var statusCode sql.NullInt64
		var requestStreamInt int
		var inputTokens sql.NullInt64
		var outputTokens sql.NullInt64
		var cachedTokens sql.NullInt64
		var reasoningTokens sql.NullInt64
		if err := rows.Scan(
			&item.ID,
			&startedAt,
			&completedAt,
			&item.DurationMs,
			&firstTokenMs,
			&successInt,
			&statusCode,
			&item.ErrorMessage,
			&item.SourcePath,
			&item.EndpointStyle,
			&requestStreamInt,
			&item.RetryAttempt,
			&item.UpstreamType,
			&item.AccountIdentity,
			&item.AccountID,
			&item.AccountProvider,
			&item.AccountDisplayName,
			&item.AccountLabel,
			&item.AccountEmail,
			&item.UpstreamAccountID,
			&item.RequestedModel,
			&item.RoutedModel,
			&item.UpstreamRequestID,
			&item.ResponseID,
			&inputTokens,
			&outputTokens,
			&cachedTokens,
			&reasoningTokens,
		); err != nil {
			return result, err
		}
		parsedStartedAt, err := time.Parse(time.RFC3339Nano, startedAt)
		if err != nil {
			return result, err
		}
		parsedCompletedAt, err := time.Parse(time.RFC3339Nano, completedAt)
		if err != nil {
			return result, err
		}
		item.StartedAt = parsedStartedAt
		item.CompletedAt = parsedCompletedAt
		item.FirstTokenMs = nullableIntPointer(firstTokenMs)
		item.Success = successInt == 1
		item.StatusCode = nullableIntPointer(statusCode)
		item.RequestStream = requestStreamInt == 1
		item.InputTokens = nullableIntPointer(inputTokens)
		item.OutputTokens = nullableIntPointer(outputTokens)
		item.CachedTokens = nullableIntPointer(cachedTokens)
		item.ReasoningTokens = nullableIntPointer(reasoningTokens)
		result.Items = append(result.Items, item)
	}
	return result, rows.Err()
}

func normalizeRecord(record Record) Record {
	record.SourcePath = strings.TrimSpace(record.SourcePath)
	record.EndpointStyle = strings.TrimSpace(record.EndpointStyle)
	record.UpstreamType = strings.TrimSpace(record.UpstreamType)
	record.AccountID = strings.TrimSpace(record.AccountID)
	record.AccountIdentity = strings.TrimSpace(record.AccountIdentity)
	record.AccountDisplayName = strings.TrimSpace(record.AccountDisplayName)
	record.AccountLabel = strings.TrimSpace(record.AccountLabel)
	record.AccountEmail = strings.TrimSpace(record.AccountEmail)
	record.UpstreamAccountID = strings.TrimSpace(record.UpstreamAccountID)
	record.RequestedModel = strings.TrimSpace(record.RequestedModel)
	record.RoutedModel = strings.TrimSpace(record.RoutedModel)
	record.UpstreamRequestID = strings.TrimSpace(record.UpstreamRequestID)
	record.ResponseID = strings.TrimSpace(record.ResponseID)
	record.ErrorMessage = truncateString(strings.TrimSpace(record.ErrorMessage), maxErrorMessageLength)
	if record.CompletedAt.IsZero() {
		record.CompletedAt = record.StartedAt
	}
	if record.DurationMs < 0 {
		record.DurationMs = 0
	}
	if record.AccountSnapshot != nil {
		if record.AccountIdentity == "" {
			record.AccountIdentity = strings.TrimSpace(record.AccountSnapshot.UsageIdentity)
		}
		if record.AccountDisplayName == "" {
			record.AccountDisplayName = strings.TrimSpace(record.AccountSnapshot.DisplayName)
		}
		if record.AccountLabel == "" {
			record.AccountLabel = strings.TrimSpace(record.AccountSnapshot.Label)
		}
		if record.AccountEmail == "" {
			record.AccountEmail = strings.TrimSpace(record.AccountSnapshot.Email)
		}
		if record.UpstreamAccountID == "" {
			record.UpstreamAccountID = strings.TrimSpace(record.AccountSnapshot.UpstreamID)
		}
	}
	return record
}

func buildEventWhereClause(query EventQuery) (string, []any) {
	filters := []string{
		"(? IS NULL OR started_at >= ?)",
		"(? IS NULL OR started_at <= ?)",
		"(? = '' OR routed_model = ? OR requested_model = ?)",
		"(? = '' OR account_identity = ? OR account_id = ? OR lower(trim(COALESCE(account_display_name, ''))) = lower(trim(?)) OR lower(trim(COALESCE(account_email, ''))) = lower(trim(?)))",
		"(? = '' OR source_path = ?)",
	}
	args := []any{
		nil, nil,
		nil, nil,
		query.ModelID, query.ModelID, query.ModelID,
		query.AccountID, query.AccountID, query.AccountID, query.AccountID, query.AccountID,
		query.SourcePath, query.SourcePath,
	}
	if query.From != nil {
		fromValue := query.From.UTC().Format(time.RFC3339Nano)
		args[0] = fromValue
		args[1] = fromValue
	}
	if query.To != nil {
		toValue := query.To.UTC().Format(time.RFC3339Nano)
		args[2] = toValue
		args[3] = toValue
	}
	if query.Success != nil {
		filters = append(filters, "success = ?")
		args = append(args, boolToInt(*query.Success))
	}
	return strings.Join(filters, " AND "), args
}

func parseNullableTime(value sql.NullString) *time.Time {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil
	}
	return &parsed
}

func nullableIntPointer(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	parsed := int(value.Int64)
	return &parsed
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

func truncateString(value string, maxLen int) string {
	if maxLen <= 0 || len(value) <= maxLen {
		return value
	}
	return value[:maxLen]
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
