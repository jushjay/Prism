package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Server  ServerConfig
	Auth    AuthConfig
	API     APIConfig
	Model   ModelConfig
	Audit   AuditConfig
	Storage StorageConfig
	Web     WebConfig
	Update  UpdateConfig
}

type ServerConfig struct {
	Host        string
	Port        int
	ProxyAPIKey string
}

type AuthConfig struct {
	OAuthClientID           string
	OAuthAuthEndpoint       string
	OAuthTokenEndpoint      string
	RefreshMargin           time.Duration
	RefreshConcurrency      int
	RequestIntervalMs       int
	MaxConcurrentPerAccount int
	TierPriority            []string
	SessionTTL              time.Duration
}

type APIConfig struct {
	BaseURL       string
	Originator    string
	UserAgent     string
	ClientVersion string
}

type ModelConfig struct {
	DefaultModel           string
	DefaultReasoningEffort string
	InjectDesktopContext   bool
}

type AuditConfig struct {
	CursorRequestLogEnabled bool
	CursorRequestLogFile    string
	OpenAIEgressLogEnabled  bool
	OpenAIEgressLogFile     string
	CustomEgressLogEnabled  bool
	CustomEgressLogFile     string
}

type StorageConfig struct {
	BaseDir           string
	DBFile            string
	JSONArchiveDir    string
	AccountsFile      string
	SettingsFile      string
	ProxiesFile       string
	ModelCatalogFile  string
	ModelCacheFile    string
	ManualModelsFile  string
	ModelMappingsFile string
	UsageStatsFile    string
	VersionStateFile  string
}

type WebConfig struct {
	DistDir string
}

type UpdateConfig struct {
	ClientVersionAppcastURL string
	ClientVersionAutoUpdate bool
	ClientVersionPoll       time.Duration
}

func Load() Config {
	root := detectProjectRoot()
	dataDir := filepath.Join(root, "data")
	dbDir := getEnv("STORAGE_DB_DIR", dataDir)
	versionStateFile := filepath.Join(dataDir, "client-version-state.json")
	clientVersion := getEnv("OPENAI_CLIENT_VERSION", loadClientVersionState(versionStateFile, "26.318.11754"))
	return Config{
		Server: ServerConfig{
			Host:        getEnv("HOST", "0.0.0.0"),
			Port:        getEnvInt("PORT", 8080),
			ProxyAPIKey: getEnv("PROXY_API_KEY", "dev-change-me"),
		},
		Auth: AuthConfig{
			OAuthClientID:           getEnv("OPENAI_OAUTH_CLIENT_ID", ""),
			OAuthAuthEndpoint:       getEnv("OPENAI_OAUTH_AUTH_ENDPOINT", "https://auth.openai.com/oauth/authorize"),
			OAuthTokenEndpoint:      getEnv("OPENAI_OAUTH_TOKEN_ENDPOINT", "https://auth.openai.com/oauth/token"),
			RefreshMargin:           getEnvDuration("REFRESH_MARGIN", 5*time.Minute),
			RefreshConcurrency:      getEnvInt("REFRESH_CONCURRENCY", 2),
			RequestIntervalMs:       getEnvInt("REQUEST_INTERVAL_MS", 50),
			MaxConcurrentPerAccount: getEnvInt("MAX_CONCURRENT_PER_ACCOUNT", 3),
			TierPriority:            getEnvCSV("TIER_PRIORITY", nil),
			SessionTTL:              getEnvDuration("SESSION_TTL", 12*time.Hour),
		},
		API: APIConfig{
			BaseURL:       getEnv("OPENAI_BASE_URL", "https://chatgpt.com/backend-api"),
			Originator:    getEnv("OPENAI_ORIGINATOR", "codex_cli_rs"),
			UserAgent:     getEnv("OPENAI_USER_AGENT", "Codex/1.0 (OpenAI; Linux x86_64)"),
			ClientVersion: clientVersion,
		},
		Model: ModelConfig{
			DefaultModel:           getEnv("DEFAULT_MODEL", "gpt-5.4"),
			DefaultReasoningEffort: getEnv("DEFAULT_REASONING_EFFORT", "medium"),
			InjectDesktopContext:   getEnvBool("INJECT_DESKTOP_CONTEXT", false),
		},
		Audit: AuditConfig{
			CursorRequestLogEnabled: getEnvBool("CURSOR_AUDIT_LOG_ENABLED", false),
			CursorRequestLogFile:    getEnv("CURSOR_AUDIT_LOG_FILE", filepath.Join(dataDir, "logs", "cursor-audit.jsonl")),
			OpenAIEgressLogEnabled:  getEnvBool("OPENAI_EGRESS_AUDIT_LOG_ENABLED", false),
			OpenAIEgressLogFile:     getEnv("OPENAI_EGRESS_AUDIT_LOG_FILE", filepath.Join(dataDir, "logs", "openai-egress.jsonl")),
			CustomEgressLogEnabled:  getEnvBool("CUSTOM_EGRESS_AUDIT_LOG_ENABLED", false),
			CustomEgressLogFile:     getEnv("CUSTOM_EGRESS_AUDIT_LOG_FILE", filepath.Join(dataDir, "logs", "custom-egress.jsonl")),
		},
		Storage: StorageConfig{
			BaseDir:           dataDir,
			DBFile:            getEnv("STORAGE_DB_FILE", filepath.Join(dbDir, "prism.db")),
			JSONArchiveDir:    filepath.Join(dataDir, "json-archive"),
			AccountsFile:      filepath.Join(dataDir, "accounts.json"),
			SettingsFile:      filepath.Join(dataDir, "settings.json"),
			ProxiesFile:       filepath.Join(dataDir, "proxies.json"),
			ModelCatalogFile:  filepath.Join(root, "server", "configs", "models.yaml"),
			ModelCacheFile:    filepath.Join(dataDir, "model-cache.json"),
			ManualModelsFile:  filepath.Join(dataDir, "manual-models.json"),
			ModelMappingsFile: filepath.Join(dataDir, "model-mappings.json"),
			UsageStatsFile:    filepath.Join(dataDir, "usage-stats.json"),
			VersionStateFile:  versionStateFile,
		},
		Web: WebConfig{
			DistDir: filepath.Join(root, "web", "dist"),
		},
		Update: UpdateConfig{
			ClientVersionAppcastURL: getEnv("CLIENT_VERSION_APPCAST_URL", "https://persistent.oaistatic.com/codex-app-prod/appcast.xml"),
			ClientVersionAutoUpdate: getEnvBool("CLIENT_VERSION_AUTO_UPDATE", true),
			ClientVersionPoll:       getEnvDuration("CLIENT_VERSION_POLL_INTERVAL", 72*time.Hour),
		},
	}
}

func detectProjectRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	if strings.HasSuffix(wd, string(filepath.Separator)+"server") {
		return filepath.Dir(wd)
	}
	return wd
}

func getEnv(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func getEnvBool(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func getEnvCSV(key string, fallback []string) []string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return fallback
	}
	return out
}

func loadClientVersionState(path, fallback string) string {
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 {
		return fallback
	}

	var payload struct {
		CurrentVersion string `json:"current_version"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fallback
	}
	if strings.TrimSpace(payload.CurrentVersion) == "" {
		return fallback
	}
	return strings.TrimSpace(payload.CurrentVersion)
}
