package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type SQLiteStore struct {
	db      *sql.DB
	baseDir string
	dbPath  string
}

func NewSQLiteStore(dbPath, baseDir string) (*SQLiteStore, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)

	store := &SQLiteStore{
		db:      db,
		baseDir: baseDir,
		dbPath:  dbPath,
	}
	if err := store.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *SQLiteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *SQLiteStore) DB() *sql.DB {
	return s.db
}

func (s *SQLiteStore) BaseDir() string {
	return s.baseDir
}

func (s *SQLiteStore) DBFile() string {
	return s.dbPath
}

func (s *SQLiteStore) Load(path string, target any) error {
	key := s.normalizeKey(path)
	var payload string
	err := s.db.QueryRow(`SELECT payload FROM documents WHERE doc_key = ?`, key).Scan(&payload)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if strings.TrimSpace(payload) == "" {
		return nil
	}
	return json.Unmarshal([]byte(payload), target)
}

func (s *SQLiteStore) Save(path string, value any) error {
	key := s.normalizeKey(path)
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`
		INSERT INTO documents (doc_key, payload, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(doc_key) DO UPDATE SET
			payload = excluded.payload,
			updated_at = excluded.updated_at
	`, key, string(raw), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *SQLiteStore) ImportAndArchiveJSONFiles(paths []string, archiveDir string) error {
	type filePayload struct {
		path string
		raw  []byte
	}
	var payloads []filePayload
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		if info.IsDir() {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		payloads = append(payloads, filePayload{path: path, raw: raw})
	}
	if len(payloads) == 0 {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	for _, item := range payloads {
		if _, err := tx.Exec(`
			INSERT INTO documents (doc_key, payload, updated_at)
			VALUES (?, ?, ?)
			ON CONFLICT(doc_key) DO NOTHING
		`, s.normalizeKey(item.path), string(item.raw), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	stamp := time.Now().UTC().Format("20060102-150405")
	archiveRoot := filepath.Join(archiveDir, stamp)
	if err := os.MkdirAll(archiveRoot, 0o755); err != nil {
		return err
	}
	for _, item := range payloads {
		target := filepath.Join(archiveRoot, filepath.Base(item.path))
		if err := os.Rename(item.path, target); err != nil {
			return fmt.Errorf("archive %s: %w", filepath.Base(item.path), err)
		}
	}
	return nil
}

func (s *SQLiteStore) migrate() error {
	statements := []string{
		`PRAGMA journal_mode = WAL;`,
		`PRAGMA foreign_keys = ON;`,
		`CREATE TABLE IF NOT EXISTS documents (
			doc_key TEXT PRIMARY KEY,
			payload TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS usage_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			occurred_at TEXT NOT NULL,
			account_identity TEXT,
			account_id TEXT NOT NULL,
			account_provider TEXT,
			account_display_name TEXT,
			account_label TEXT,
			account_email TEXT,
			upstream_account_id TEXT,
			account_snapshot TEXT,
			model_id TEXT NOT NULL,
			input_tokens INTEGER NOT NULL,
			output_tokens INTEGER NOT NULL,
			request_count INTEGER NOT NULL DEFAULT 1,
			cached_tokens INTEGER NOT NULL DEFAULT 0,
			reasoning_tokens INTEGER NOT NULL DEFAULT 0
		);`,
		`CREATE INDEX IF NOT EXISTS idx_usage_events_occurred_at ON usage_events(occurred_at);`,
		`CREATE INDEX IF NOT EXISTS idx_usage_events_account_id ON usage_events(account_id);`,
		`CREATE INDEX IF NOT EXISTS idx_usage_events_model_id ON usage_events(model_id);`,
		`CREATE TABLE IF NOT EXISTS request_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			started_at TEXT NOT NULL,
			completed_at TEXT NOT NULL,
			duration_ms INTEGER NOT NULL,
			first_token_ms INTEGER,
			success INTEGER NOT NULL,
			status_code INTEGER,
			error_message TEXT,
			source_path TEXT,
			endpoint_style TEXT NOT NULL,
			request_stream INTEGER NOT NULL,
			retry_attempt INTEGER NOT NULL DEFAULT 0,
			upstream_type TEXT NOT NULL,
			account_id TEXT NOT NULL,
			account_provider TEXT,
			account_identity TEXT,
			account_display_name TEXT,
			account_label TEXT,
			account_email TEXT,
			upstream_account_id TEXT,
			account_snapshot TEXT,
			requested_model TEXT NOT NULL,
			routed_model TEXT NOT NULL,
			upstream_request_id TEXT,
			response_id TEXT,
			input_tokens INTEGER,
			output_tokens INTEGER,
			cached_tokens INTEGER,
			reasoning_tokens INTEGER
		);`,
		`CREATE INDEX IF NOT EXISTS idx_request_events_started_at ON request_events(started_at);`,
		`CREATE INDEX IF NOT EXISTS idx_request_events_account_id ON request_events(account_id);`,
		`CREATE INDEX IF NOT EXISTS idx_request_events_account_identity ON request_events(account_identity);`,
		`CREATE INDEX IF NOT EXISTS idx_request_events_requested_model ON request_events(requested_model);`,
		`CREATE INDEX IF NOT EXISTS idx_request_events_routed_model ON request_events(routed_model);`,
		`CREATE INDEX IF NOT EXISTS idx_request_events_success ON request_events(success);`,
		`CREATE TABLE IF NOT EXISTS usage_totals_global (
			bucket TEXT PRIMARY KEY,
			total_input_tokens INTEGER NOT NULL,
			total_output_tokens INTEGER NOT NULL,
			total_cached_tokens INTEGER NOT NULL DEFAULT 0,
			total_request_count INTEGER NOT NULL,
			last_recorded_at TEXT
		);`,
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
			total_cached_tokens INTEGER NOT NULL DEFAULT 0,
			total_request_count INTEGER NOT NULL,
			last_recorded_at TEXT
		);`,
		`CREATE TABLE IF NOT EXISTS usage_totals_by_model (
			model_id TEXT PRIMARY KEY,
			total_input_tokens INTEGER NOT NULL,
			total_output_tokens INTEGER NOT NULL,
			total_cached_tokens INTEGER NOT NULL DEFAULT 0,
			total_request_count INTEGER NOT NULL,
			last_recorded_at TEXT
		);`,
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
			total_cached_tokens INTEGER NOT NULL DEFAULT 0,
			total_request_count INTEGER NOT NULL,
			last_recorded_at TEXT,
			PRIMARY KEY (account_identity, model_id)
		);`,
		`CREATE TABLE IF NOT EXISTS custom_account_models (
			account_id TEXT NOT NULL,
			model_id TEXT NOT NULL,
			display_name TEXT,
			model_object TEXT,
			owned_by TEXT,
			created_unix INTEGER NOT NULL DEFAULT 0,
			source_payload TEXT,
			fetched_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (account_id, model_id)
		);`,
		`CREATE INDEX IF NOT EXISTS idx_custom_account_models_account_id ON custom_account_models(account_id);`,
		`CREATE TABLE IF NOT EXISTS custom_account_model_sync_state (
			account_id TEXT PRIMARY KEY,
			fetched_at TEXT,
			expires_at TEXT,
			last_error TEXT,
			updated_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS openai_account_models (
			account_id TEXT NOT NULL,
			model_id TEXT NOT NULL,
			display_name TEXT,
			model_object TEXT,
			owned_by TEXT,
			created_unix INTEGER NOT NULL DEFAULT 0,
			source_payload TEXT,
			fetched_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (account_id, model_id)
		);`,
		`CREATE INDEX IF NOT EXISTS idx_openai_account_models_account_id ON openai_account_models(account_id);`,
		`CREATE TABLE IF NOT EXISTS openai_account_model_sync_state (
			account_id TEXT PRIMARY KEY,
			fetched_at TEXT,
			expires_at TEXT,
			last_error TEXT,
			updated_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS security_ip_filter_settings (
			bucket TEXT PRIMARY KEY,
			enabled INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS security_ip_rules (
			id TEXT PRIMARY KEY,
			list_type TEXT NOT NULL,
			value TEXT NOT NULL,
			normalized_value TEXT NOT NULL,
			match_type TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			UNIQUE(list_type, normalized_value)
		);`,
		`CREATE INDEX IF NOT EXISTS idx_security_ip_rules_list_type ON security_ip_rules(list_type);`,
		`CREATE TABLE IF NOT EXISTS security_ip_stats (
			ip TEXT PRIMARY KEY,
			request_count INTEGER NOT NULL DEFAULT 0,
			denied_count INTEGER NOT NULL DEFAULT 0,
			last_seen_at TEXT,
			last_allowed_at TEXT,
			last_denied_at TEXT,
			last_path TEXT,
			last_method TEXT
		);`,
		`CREATE INDEX IF NOT EXISTS idx_security_ip_stats_request_count ON security_ip_stats(request_count DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_security_ip_stats_denied_count ON security_ip_stats(denied_count DESC);`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			return err
		}
	}
	alterStatements := []string{
		`ALTER TABLE usage_events ADD COLUMN account_identity TEXT`,
		`ALTER TABLE usage_events ADD COLUMN account_provider TEXT`,
		`ALTER TABLE usage_events ADD COLUMN account_display_name TEXT`,
		`ALTER TABLE usage_events ADD COLUMN account_label TEXT`,
		`ALTER TABLE usage_events ADD COLUMN upstream_account_id TEXT`,
		`ALTER TABLE usage_events ADD COLUMN account_snapshot TEXT`,
		`ALTER TABLE usage_totals_global ADD COLUMN total_cached_tokens INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE usage_totals_by_account ADD COLUMN total_cached_tokens INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE usage_totals_by_model ADD COLUMN total_cached_tokens INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE usage_totals_by_account_model ADD COLUMN total_cached_tokens INTEGER NOT NULL DEFAULT 0`,
	}
	for _, statement := range alterStatements {
		if _, err := s.db.Exec(statement); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column name") {
			return err
		}
	}
	postAlterStatements := []string{
		`CREATE INDEX IF NOT EXISTS idx_usage_events_account_identity ON usage_events(account_identity);`,
		`CREATE INDEX IF NOT EXISTS idx_usage_events_account_display_name ON usage_events(account_display_name);`,
	}
	for _, statement := range postAlterStatements {
		if _, err := s.db.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteStore) normalizeKey(path string) string {
	if s.baseDir != "" {
		if rel, err := filepath.Rel(s.baseDir, path); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.Base(path)
}
