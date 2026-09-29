package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	Server   ServerConfig
	Upstream UpstreamConfig
	Audit    AuditConfig
	Sidecar  SidecarConfig
}

type ServerConfig struct {
	Host string
	Port int
}

type UpstreamConfig struct {
	BaseURL      string
	PreserveHost bool
}

type AuditConfig struct {
	CursorRequestLogEnabled bool
	CursorRequestLogFile    string
	UpstreamLogEnabled      bool
	UpstreamLogFile         string
}

type SidecarConfig struct {
	ConfigFile string
	// ConfigFileExplicit is true when SIDECAR_CONFIG was set, in which case a
	// missing file is a startup error instead of an empty configuration.
	ConfigFileExplicit bool
}

func Load() Config {
	root := detectProjectRoot()
	dataDir := getEnv("DATA_DIR", filepath.Join(root, "data"))
	sidecarConfig := strings.TrimSpace(os.Getenv("SIDECAR_CONFIG"))
	return Config{
		Server: ServerConfig{
			Host: getEnv("HOST", "127.0.0.1"),
			Port: getEnvInt("PORT", 8090),
		},
		Upstream: UpstreamConfig{
			BaseURL:      strings.TrimRight(getEnv("SUB2API_BASE_URL", "http://127.0.0.1:8080"), "/"),
			PreserveHost: getEnvBool("UPSTREAM_PRESERVE_HOST", true),
		},
		Audit: AuditConfig{
			CursorRequestLogEnabled: getEnvBool("CURSOR_AUDIT_LOG_ENABLED", false),
			CursorRequestLogFile:    getEnv("CURSOR_AUDIT_LOG_FILE", filepath.Join(dataDir, "logs", "cursor-audit.jsonl")),
			UpstreamLogEnabled:      getEnvBool("UPSTREAM_AUDIT_LOG_ENABLED", false),
			UpstreamLogFile:         getEnv("UPSTREAM_AUDIT_LOG_FILE", filepath.Join(dataDir, "logs", "upstream-audit.jsonl")),
		},
		Sidecar: SidecarConfig{
			ConfigFile:         firstNonEmpty(sidecarConfig, filepath.Join(root, "server", "configs", "sidecar.yaml")),
			ConfigFileExplicit: sidecarConfig != "",
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
