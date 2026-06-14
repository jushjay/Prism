package security

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/jushjay/prism/internal/store"
)

func TestServiceEvaluateAndOverview(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	sqliteStore, err := store.NewSQLiteStore(filepath.Join(tempDir, "security.db"), tempDir)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() {
		_ = sqliteStore.Close()
	})

	service, err := NewService(sqliteStore.DB())
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	if err := service.SetEnabled(true); err != nil {
		t.Fatalf("SetEnabled() error = %v", err)
	}
	if _, err := service.UpsertRule(RuleInput{
		ListType: ListTypeWhitelist,
		Value:    "203.0.113.0/24",
	}); err != nil {
		t.Fatalf("UpsertRule(whitelist) error = %v", err)
	}
	if _, err := service.UpsertRule(RuleInput{
		ListType: ListTypeBlacklist,
		Value:    "203.0.113.12",
	}); err != nil {
		t.Fatalf("UpsertRule(blacklist) error = %v", err)
	}

	if decision := service.Evaluate("203.0.113.8"); !decision.Allowed {
		t.Fatalf("Evaluate(allowed) = %#v, want allowed", decision)
	}
	if decision := service.Evaluate("203.0.113.12"); decision.Allowed || decision.Reason != "blacklist" {
		t.Fatalf("Evaluate(blacklisted) = %#v, want blacklist denial", decision)
	}
	if decision := service.Evaluate("198.51.100.10"); decision.Allowed || decision.Reason != "whitelist" {
		t.Fatalf("Evaluate(non-whitelisted) = %#v, want whitelist denial", decision)
	}

	if err := service.RecordAccess("203.0.113.8", "POST", "/v1/responses", false); err != nil {
		t.Fatalf("RecordAccess(allowed) error = %v", err)
	}
	if err := service.RecordAccess("203.0.113.12", "POST", "/v1/responses", true); err != nil {
		t.Fatalf("RecordAccess(denied) error = %v", err)
	}
	if err := service.RecordAccess("198.51.100.10", "GET", "/admin/overview", true); err != nil {
		t.Fatalf("RecordAccess(denied admin) error = %v", err)
	}
	if err := service.RecordAccess("127.0.0.1", "GET", "/health", false); err != nil {
		t.Fatalf("RecordAccess(loopback ipv4) error = %v", err)
	}
	if err := service.RecordAccess("::1", "GET", "/health", true); err != nil {
		t.Fatalf("RecordAccess(loopback ipv6) error = %v", err)
	}

	overview, err := service.Overview(10)
	if err != nil {
		t.Fatalf("Overview() error = %v", err)
	}
	if !overview.Enabled {
		t.Fatalf("overview.Enabled = false, want true")
	}
	if len(overview.WhitelistRules) != 1 {
		t.Fatalf("len(overview.WhitelistRules) = %d, want 1", len(overview.WhitelistRules))
	}
	if len(overview.BlacklistRules) != 1 {
		t.Fatalf("len(overview.BlacklistRules) = %d, want 1", len(overview.BlacklistRules))
	}
	if overview.SourceSummary.TotalRequests != 3 {
		t.Fatalf("overview.SourceSummary.TotalRequests = %d, want 3", overview.SourceSummary.TotalRequests)
	}
	if overview.DeniedSummary.TotalDeniedCount != 2 {
		t.Fatalf("overview.DeniedSummary.TotalDeniedCount = %d, want 2", overview.DeniedSummary.TotalDeniedCount)
	}
	if len(overview.TopDenied) != 2 {
		t.Fatalf("len(overview.TopDenied) = %d, want 2", len(overview.TopDenied))
	}
}

func TestCleanupLocalhostStats(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	sqliteStore, err := store.NewSQLiteStore(filepath.Join(tempDir, "security.db"), tempDir)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() {
		_ = sqliteStore.Close()
	})

	service, err := NewService(sqliteStore.DB())
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	if _, err := sqliteStore.DB().Exec(`
		INSERT INTO security_ip_stats (ip, request_count, denied_count, last_seen_at, last_path, last_method)
		VALUES ('127.0.0.1', 3, 0, '2026-05-25T00:00:00Z', '/a', 'GET'),
		       ('::1', 4, 1, '2026-05-25T00:00:00Z', '/b', 'POST'),
		       ('203.0.113.8', 5, 0, '2026-05-25T00:00:00Z', '/c', 'GET')
	`); err != nil {
		t.Fatalf("seed localhost stats: %v", err)
	}

	if err := service.CleanupLocalhostStats(); err != nil {
		t.Fatalf("CleanupLocalhostStats() error = %v", err)
	}

	var count int
	if err := sqliteStore.DB().QueryRow(`SELECT COUNT(1) FROM security_ip_stats`).Scan(&count); err != nil {
		t.Fatalf("count stats: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 remaining stat row, got %d", count)
	}
}

func TestNormalizeClientIP(t *testing.T) {
	t.Parallel()

	if got := NormalizeClientIP("::ffff:203.0.113.20"); got != "203.0.113.20" {
		t.Fatalf("NormalizeClientIP(mapped) = %q, want %q", got, "203.0.113.20")
	}
	if got := NormalizeClientIP("[2001:db8::1]:8080"); got != "2001:db8::1" {
		t.Fatalf("NormalizeClientIP(hostport) = %q, want %q", got, "2001:db8::1")
	}
}

func TestResolveClientIP(t *testing.T) {
	t.Parallel()

	headers := http.Header{}
	headers.Set("X-Real-IP", "183.192.84.113")
	if got := ResolveClientIP(headers, "[::1]:48000"); got != "183.192.84.113" {
		t.Fatalf("ResolveClientIP(X-Real-IP) = %q, want %q", got, "183.192.84.113")
	}

	headers = http.Header{}
	headers.Set("X-Forwarded-For", "183.192.84.113, 127.0.0.1")
	if got := ResolveClientIP(headers, "127.0.0.1:48000"); got != "183.192.84.113" {
		t.Fatalf("ResolveClientIP(X-Forwarded-For) = %q, want %q", got, "183.192.84.113")
	}

	headers = http.Header{}
	headers.Set("Forwarded", `for=183.192.84.113;proto=https`)
	if got := ResolveClientIP(headers, "172.17.0.2:8080"); got != "183.192.84.113" {
		t.Fatalf("ResolveClientIP(Forwarded) = %q, want %q", got, "183.192.84.113")
	}

	headers = http.Header{}
	headers.Set("X-Real-IP", "198.51.100.22")
	if got := ResolveClientIP(headers, "198.51.100.44:8080"); got != "198.51.100.44" {
		t.Fatalf("ResolveClientIP(untrusted remote) = %q, want %q", got, "198.51.100.44")
	}
}

func TestInspectClientIP(t *testing.T) {
	t.Parallel()

	headers := http.Header{}
	headers.Set("X-Forwarded-For", "183.192.84.113, 127.0.0.1")

	info := InspectClientIP(headers, "127.0.0.1:48000")
	if info.ClientIP != "183.192.84.113" {
		t.Fatalf("InspectClientIP().ClientIP = %q, want %q", info.ClientIP, "183.192.84.113")
	}
	if info.Source != "x_forwarded_for" {
		t.Fatalf("InspectClientIP().Source = %q, want %q", info.Source, "x_forwarded_for")
	}
	if !info.TrustForwardedHeaders {
		t.Fatalf("InspectClientIP().TrustForwardedHeaders = false, want true")
	}

	headers = http.Header{}
	headers.Set("X-Forwarded-For", "183.192.84.113")
	info = InspectClientIP(headers, "198.51.100.44:8080")
	if info.ClientIP != "198.51.100.44" {
		t.Fatalf("InspectClientIP(untrusted).ClientIP = %q, want %q", info.ClientIP, "198.51.100.44")
	}
	if info.Source != "remote_addr" {
		t.Fatalf("InspectClientIP(untrusted).Source = %q, want %q", info.Source, "remote_addr")
	}
	if info.TrustForwardedHeaders {
		t.Fatalf("InspectClientIP(untrusted).TrustForwardedHeaders = true, want false")
	}
}
