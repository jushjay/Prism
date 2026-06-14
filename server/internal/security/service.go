package security

import (
	"database/sql"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	ListTypeWhitelist = "whitelist"
	ListTypeBlacklist = "blacklist"
	MatchTypeIP       = "ip"
	MatchTypeCIDR     = "cidr"
)

type Rule struct {
	ID        string    `json:"id"`
	ListType  string    `json:"list_type"`
	Value     string    `json:"value"`
	MatchType string    `json:"match_type"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type RuleInput struct {
	ListType string
	Value    string
}

type AccessStat struct {
	IP            string     `json:"ip"`
	RequestCount  int        `json:"request_count"`
	DeniedCount   int        `json:"denied_count"`
	LastSeenAt    *time.Time `json:"last_seen_at,omitempty"`
	LastAllowedAt *time.Time `json:"last_allowed_at,omitempty"`
	LastDeniedAt  *time.Time `json:"last_denied_at,omitempty"`
	LastPath      string     `json:"last_path,omitempty"`
	LastMethod    string     `json:"last_method,omitempty"`
}

type SourceSummary struct {
	UniqueIPs     int        `json:"unique_ips"`
	TotalRequests int        `json:"total_requests"`
	LastSeenAt    *time.Time `json:"last_seen_at,omitempty"`
}

type DeniedSummary struct {
	UniqueIPs        int        `json:"unique_ips"`
	TotalDeniedCount int        `json:"total_denied_count"`
	LastDeniedAt     *time.Time `json:"last_denied_at,omitempty"`
}

type Overview struct {
	Enabled        bool          `json:"enabled"`
	UpdatedAt      *time.Time    `json:"updated_at,omitempty"`
	WhitelistRules []Rule        `json:"whitelist_rules"`
	BlacklistRules []Rule        `json:"blacklist_rules"`
	TopSources     []AccessStat  `json:"top_sources"`
	TopDenied      []AccessStat  `json:"top_denied"`
	SourceSummary  SourceSummary `json:"source_summary"`
	DeniedSummary  DeniedSummary `json:"denied_summary"`
}

type Decision struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty"`
}

type ClientIPInfo struct {
	RemoteIP              string `json:"remote_ip"`
	ClientIP              string `json:"client_ip"`
	Source                string `json:"source"`
	TrustForwardedHeaders bool   `json:"trust_forwarded_headers"`
}

type parsedRule struct {
	Rule
	addr   netip.Addr
	prefix netip.Prefix
}

type Service struct {
	mu        sync.RWMutex
	db        *sql.DB
	enabled   bool
	updatedAt *time.Time
	whitelist []parsedRule
	blacklist []parsedRule
}

func NewService(db *sql.DB) (*Service, error) {
	service := &Service{db: db}
	if err := service.reload(); err != nil {
		return nil, err
	}
	return service, nil
}

func NormalizeClientIP(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "unknown"
	}
	if host, _, err := net.SplitHostPort(trimmed); err == nil {
		trimmed = host
	}
	addr, err := netip.ParseAddr(trimmed)
	if err != nil {
		return trimmed
	}
	return addr.Unmap().String()
}

func shouldTrackIP(ip string) bool {
	normalized := NormalizeClientIP(ip)
	switch normalized {
	case "", "unknown", "127.0.0.1", "::1":
		return false
	}
	addr, err := netip.ParseAddr(normalized)
	if err != nil {
		return true
	}
	return !addr.IsLoopback()
}

func ResolveClientIP(headers http.Header, remoteAddr string) string {
	return InspectClientIP(headers, remoteAddr).ClientIP
}

func InspectClientIP(headers http.Header, remoteAddr string) ClientIPInfo {
	remoteIP := NormalizeClientIP(remoteAddr)
	info := ClientIPInfo{
		RemoteIP:              remoteIP,
		ClientIP:              remoteIP,
		Source:                "remote_addr",
		TrustForwardedHeaders: shouldTrustForwardedHeaders(remoteIP),
	}
	if !info.TrustForwardedHeaders {
		return info
	}

	if candidate := resolveFromHeaderValue(headers.Get("X-Real-IP")); candidate != "" {
		info.ClientIP = candidate
		info.Source = "x_real_ip"
		return info
	}
	if candidate := resolveFromXForwardedFor(headers.Values("X-Forwarded-For")); candidate != "" {
		info.ClientIP = candidate
		info.Source = "x_forwarded_for"
		return info
	}
	if candidate := resolveFromForwarded(headers.Values("Forwarded")); candidate != "" {
		info.ClientIP = candidate
		info.Source = "forwarded"
		return info
	}
	return info
}

func (s *Service) SetEnabled(enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	_, err := s.db.Exec(`
		INSERT INTO security_ip_filter_settings (bucket, enabled, updated_at)
		VALUES ('global', ?, ?)
		ON CONFLICT(bucket) DO UPDATE SET
			enabled = excluded.enabled,
			updated_at = excluded.updated_at
	`, boolToInt(enabled), now.Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	s.enabled = enabled
	s.updatedAt = cloneTime(&now)
	return nil
}

func (s *Service) UpsertRule(input RuleInput) (Rule, error) {
	listType, err := normalizeListType(input.ListType)
	if err != nil {
		return Rule{}, err
	}
	normalizedValue, matchType, err := normalizeRuleValue(input.Value)
	if err != nil {
		return Rule{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	var existing Rule
	var rawCreatedAt string
	var rawUpdatedAt string
	err = s.db.QueryRow(`
		SELECT id, list_type, value, match_type, created_at, updated_at
		FROM security_ip_rules
		WHERE list_type = ? AND normalized_value = ?
	`, listType, normalizedValue).Scan(
		&existing.ID,
		&existing.ListType,
		&existing.Value,
		&existing.MatchType,
		&rawCreatedAt,
		&rawUpdatedAt,
	)
	switch {
	case err == nil:
		if _, err := s.db.Exec(`
			UPDATE security_ip_rules
			SET value = ?, match_type = ?, updated_at = ?
			WHERE id = ?
		`, normalizedValue, matchType, now.Format(time.RFC3339Nano), existing.ID); err != nil {
			return Rule{}, err
		}
		existing.CreatedAt = parseStoredTime(rawCreatedAt)
		existing.UpdatedAt = now
		existing.Value = normalizedValue
		existing.MatchType = matchType
		if err := s.reloadLocked(); err != nil {
			return Rule{}, err
		}
		return existing, nil
	case !errors.Is(err, sql.ErrNoRows):
		return Rule{}, err
	}

	record := Rule{
		ID:        uuid.NewString(),
		ListType:  listType,
		Value:     normalizedValue,
		MatchType: matchType,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if _, err := s.db.Exec(`
		INSERT INTO security_ip_rules (
			id, list_type, value, normalized_value, match_type, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`, record.ID, record.ListType, record.Value, record.Value, record.MatchType,
		record.CreatedAt.Format(time.RFC3339Nano), record.UpdatedAt.Format(time.RFC3339Nano)); err != nil {
		return Rule{}, err
	}
	if err := s.reloadLocked(); err != nil {
		return Rule{}, err
	}
	return record, nil
}

func (s *Service) DeleteRule(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	result, err := s.db.Exec(`DELETE FROM security_ip_rules WHERE id = ?`, strings.TrimSpace(id))
	if err != nil {
		return err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return errors.New("rule not found")
	}
	return s.reloadLocked()
}

func (s *Service) Evaluate(ip string) Decision {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.enabled {
		return Decision{Allowed: true}
	}
	addr, err := parseClientAddr(ip)
	if err != nil {
		return Decision{Allowed: false, Reason: "invalid_source_ip"}
	}
	for _, rule := range s.blacklist {
		if ruleMatches(rule, addr) {
			return Decision{Allowed: false, Reason: "blacklist"}
		}
	}
	if len(s.whitelist) == 0 {
		return Decision{Allowed: true}
	}
	for _, rule := range s.whitelist {
		if ruleMatches(rule, addr) {
			return Decision{Allowed: true}
		}
	}
	return Decision{Allowed: false, Reason: "whitelist"}
}

func (s *Service) RecordAccess(ip, method, path string, denied bool) error {
	recordedIP := NormalizeClientIP(ip)
	if !shouldTrackIP(recordedIP) {
		return nil
	}
	now := time.Now().UTC()
	recordedAt := now.Format(time.RFC3339Nano)
	lastAllowedAt := sql.NullString{}
	lastDeniedAt := sql.NullString{}
	deniedCount := 0
	if denied {
		deniedCount = 1
		lastDeniedAt = sql.NullString{String: recordedAt, Valid: true}
	} else {
		lastAllowedAt = sql.NullString{String: recordedAt, Valid: true}
	}

	_, err := s.db.Exec(`
		INSERT INTO security_ip_stats (
			ip, request_count, denied_count, last_seen_at,
			last_allowed_at, last_denied_at, last_path, last_method
		) VALUES (?, 1, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(ip) DO UPDATE SET
			request_count = security_ip_stats.request_count + 1,
			denied_count = security_ip_stats.denied_count + excluded.denied_count,
			last_seen_at = excluded.last_seen_at,
			last_allowed_at = CASE
				WHEN excluded.last_allowed_at IS NOT NULL AND excluded.last_allowed_at <> '' THEN excluded.last_allowed_at
				ELSE security_ip_stats.last_allowed_at
			END,
			last_denied_at = CASE
				WHEN excluded.last_denied_at IS NOT NULL AND excluded.last_denied_at <> '' THEN excluded.last_denied_at
				ELSE security_ip_stats.last_denied_at
			END,
			last_path = excluded.last_path,
			last_method = excluded.last_method
	`, recordedIP, deniedCount, recordedAt, nullableStringValue(lastAllowedAt), nullableStringValue(lastDeniedAt),
		strings.TrimSpace(path), strings.TrimSpace(method))
	return err
}

func (s *Service) CleanupLocalhostStats() error {
	_, err := s.db.Exec(`DELETE FROM security_ip_stats WHERE ip IN ('127.0.0.1', '::1')`)
	return err
}

func (s *Service) Overview(limit int) (Overview, error) {
	if limit <= 0 {
		limit = 20
	}

	s.mu.RLock()
	enabled := s.enabled
	updatedAt := cloneTime(s.updatedAt)
	whitelist := make([]Rule, 0, len(s.whitelist))
	for _, item := range s.whitelist {
		whitelist = append(whitelist, item.Rule)
	}
	blacklist := make([]Rule, 0, len(s.blacklist))
	for _, item := range s.blacklist {
		blacklist = append(blacklist, item.Rule)
	}
	s.mu.RUnlock()

	overview := Overview{
		Enabled:        enabled,
		UpdatedAt:      updatedAt,
		WhitelistRules: whitelist,
		BlacklistRules: blacklist,
	}

	row := s.db.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(request_count), 0), MAX(last_seen_at)
		FROM security_ip_stats
	`)
	var sourceLastSeen sql.NullString
	if err := row.Scan(
		&overview.SourceSummary.UniqueIPs,
		&overview.SourceSummary.TotalRequests,
		&sourceLastSeen,
	); err != nil {
		return Overview{}, err
	}
	overview.SourceSummary.LastSeenAt = parseNullableTime(sourceLastSeen)

	row = s.db.QueryRow(`
		SELECT
			COUNT(*) FILTER (WHERE denied_count > 0),
			COALESCE(SUM(denied_count), 0),
			MAX(last_denied_at)
		FROM security_ip_stats
	`)
	var deniedLastSeen sql.NullString
	if err := row.Scan(
		&overview.DeniedSummary.UniqueIPs,
		&overview.DeniedSummary.TotalDeniedCount,
		&deniedLastSeen,
	); err != nil {
		return Overview{}, err
	}
	overview.DeniedSummary.LastDeniedAt = parseNullableTime(deniedLastSeen)

	var err error
	overview.TopSources, err = s.queryStats(`
		SELECT ip, request_count, denied_count, last_seen_at, last_allowed_at, last_denied_at, last_path, last_method
		FROM security_ip_stats
		ORDER BY request_count DESC, COALESCE(last_seen_at, '') DESC, ip ASC
		LIMIT ?
	`, limit)
	if err != nil {
		return Overview{}, err
	}
	overview.TopDenied, err = s.queryStats(`
		SELECT ip, request_count, denied_count, last_seen_at, last_allowed_at, last_denied_at, last_path, last_method
		FROM security_ip_stats
		WHERE denied_count > 0
		ORDER BY denied_count DESC, COALESCE(last_denied_at, '') DESC, ip ASC
		LIMIT ?
	`, limit)
	if err != nil {
		return Overview{}, err
	}
	return overview, nil
}

func (s *Service) queryStats(query string, limit int) ([]AccessStat, error) {
	rows, err := s.db.Query(query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []AccessStat
	for rows.Next() {
		var item AccessStat
		var lastSeen sql.NullString
		var lastAllowed sql.NullString
		var lastDenied sql.NullString
		if err := rows.Scan(
			&item.IP,
			&item.RequestCount,
			&item.DeniedCount,
			&lastSeen,
			&lastAllowed,
			&lastDenied,
			&item.LastPath,
			&item.LastMethod,
		); err != nil {
			return nil, err
		}
		item.LastSeenAt = parseNullableTime(lastSeen)
		item.LastAllowedAt = parseNullableTime(lastAllowed)
		item.LastDeniedAt = parseNullableTime(lastDenied)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Service) reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reloadLocked()
}

func (s *Service) reloadLocked() error {
	row := s.db.QueryRow(`
		SELECT enabled, updated_at
		FROM security_ip_filter_settings
		WHERE bucket = 'global'
	`)
	var enabled int
	var rawUpdatedAt sql.NullString
	err := row.Scan(&enabled, &rawUpdatedAt)
	switch {
	case err == nil:
		s.enabled = enabled != 0
		s.updatedAt = parseNullableTime(rawUpdatedAt)
	case errors.Is(err, sql.ErrNoRows):
		s.enabled = false
		s.updatedAt = nil
	default:
		return err
	}

	rows, err := s.db.Query(`
		SELECT id, list_type, value, match_type, created_at, updated_at
		FROM security_ip_rules
		ORDER BY updated_at DESC, id ASC
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var whitelist []parsedRule
	var blacklist []parsedRule
	for rows.Next() {
		var item Rule
		var rawCreatedAt string
		var rawUpdatedAt string
		if err := rows.Scan(
			&item.ID,
			&item.ListType,
			&item.Value,
			&item.MatchType,
			&rawCreatedAt,
			&rawUpdatedAt,
		); err != nil {
			return err
		}
		item.CreatedAt = parseStoredTime(rawCreatedAt)
		item.UpdatedAt = parseStoredTime(rawUpdatedAt)
		parsed, err := toParsedRule(item)
		if err != nil {
			continue
		}
		if item.ListType == ListTypeWhitelist {
			whitelist = append(whitelist, parsed)
		} else {
			blacklist = append(blacklist, parsed)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	s.whitelist = whitelist
	s.blacklist = blacklist
	return nil
}

func normalizeListType(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case ListTypeWhitelist:
		return ListTypeWhitelist, nil
	case ListTypeBlacklist:
		return ListTypeBlacklist, nil
	default:
		return "", errors.New("listType must be whitelist or blacklist")
	}
}

func shouldTrustForwardedHeaders(remoteIP string) bool {
	addr, err := netip.ParseAddr(remoteIP)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast()
}

func resolveFromHeaderValue(raw string) string {
	normalized := NormalizeClientIP(raw)
	if normalized == "" || normalized == "unknown" {
		return ""
	}
	if _, err := netip.ParseAddr(normalized); err != nil {
		return ""
	}
	return normalized
}

func resolveFromXForwardedFor(values []string) string {
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			if candidate := resolveFromHeaderValue(part); candidate != "" {
				return candidate
			}
		}
	}
	return ""
}

func resolveFromForwarded(values []string) string {
	for _, value := range values {
		for _, segment := range strings.Split(value, ",") {
			for _, token := range strings.Split(segment, ";") {
				token = strings.TrimSpace(token)
				if !strings.HasPrefix(strings.ToLower(token), "for=") {
					continue
				}
				candidate := strings.Trim(strings.TrimSpace(token[4:]), `"[]`)
				if resolved := resolveFromHeaderValue(candidate); resolved != "" {
					return resolved
				}
			}
		}
	}
	return ""
}

func normalizeRuleValue(raw string) (string, string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", "", errors.New("ip rule value is required")
	}
	if prefix, err := netip.ParsePrefix(trimmed); err == nil {
		masked := prefix.Masked()
		return masked.String(), MatchTypeCIDR, nil
	}
	addr, err := netip.ParseAddr(trimmed)
	if err != nil {
		return "", "", errors.New("ip rule must be a valid IP or CIDR")
	}
	return addr.Unmap().String(), MatchTypeIP, nil
}

func parseClientAddr(raw string) (netip.Addr, error) {
	return netip.ParseAddr(NormalizeClientIP(raw))
}

func toParsedRule(rule Rule) (parsedRule, error) {
	result := parsedRule{Rule: rule}
	switch rule.MatchType {
	case MatchTypeCIDR:
		prefix, err := netip.ParsePrefix(rule.Value)
		if err != nil {
			return parsedRule{}, err
		}
		result.prefix = prefix.Masked()
	case MatchTypeIP:
		addr, err := netip.ParseAddr(rule.Value)
		if err != nil {
			return parsedRule{}, err
		}
		result.addr = addr.Unmap()
	default:
		return parsedRule{}, errors.New("unknown match type")
	}
	return result, nil
}

func ruleMatches(rule parsedRule, addr netip.Addr) bool {
	switch rule.MatchType {
	case MatchTypeCIDR:
		return rule.prefix.Contains(addr)
	case MatchTypeIP:
		return rule.addr == addr.Unmap()
	default:
		return false
	}
}

func parseNullableTime(value sql.NullString) *time.Time {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil
	}
	parsed := parseStoredTime(value.String)
	return &parsed
}

func parseStoredTime(raw string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err == nil {
		return parsed
	}
	parsed, err = time.Parse(time.RFC3339, raw)
	if err == nil {
		return parsed
	}
	return time.Time{}
}

func nullableStringValue(value sql.NullString) any {
	if value.Valid {
		return value.String
	}
	return nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
