package version

import (
	"context"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jushjay/prism/internal/config"
	"github.com/jushjay/prism/internal/store"
)

var (
	shortVersionPattern = regexp.MustCompile(`sparkle:shortVersionString="([^"]+)"|<sparkle:shortVersionString>([^<]+)</sparkle:shortVersionString>`)
	buildPattern        = regexp.MustCompile(`sparkle:version="([^"]+)"|<sparkle:version>([^<]+)</sparkle:version>`)
)

type State struct {
	CurrentVersion string    `json:"current_version"`
	CurrentBuild   string    `json:"current_build,omitempty"`
	LastCheckedAt  time.Time `json:"last_checked_at,omitempty"`
	LastUpdatedAt  time.Time `json:"last_updated_at,omitempty"`
	AppcastURL     string    `json:"appcast_url,omitempty"`
}

type Manager struct {
	mu           sync.RWMutex
	state        State
	autoUpdate   bool
	pollInterval time.Duration
	stateFile    string
	stateStore   store.StateStore
	httpClient   *http.Client
	stopCh       chan struct{}
}

func NewManager(cfg config.Config, stateStore store.StateStore) *Manager {
	manager := &Manager{
		state: State{
			CurrentVersion: cfg.API.ClientVersion,
			AppcastURL:     cfg.Update.ClientVersionAppcastURL,
		},
		autoUpdate:   cfg.Update.ClientVersionAutoUpdate,
		pollInterval: cfg.Update.ClientVersionPoll,
		stateFile:    cfg.Storage.VersionStateFile,
		stateStore:   stateStore,
		httpClient: &http.Client{
			Timeout: 20 * time.Second,
		},
		stopCh: make(chan struct{}),
	}
	manager.loadState()
	return manager
}

func (m *Manager) CurrentVersion() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.state.CurrentVersion == "" {
		return "26.318.11754"
	}
	return m.state.CurrentVersion
}

func (m *Manager) Snapshot() State {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state
}

func (m *Manager) Start() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		_ = m.Refresh(ctx)
		cancel()

		ticker := time.NewTicker(m.pollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				_ = m.Refresh(ctx)
				cancel()
			case <-m.stopCh:
				return
			}
		}
	}()
}

func (m *Manager) Stop() {
	select {
	case <-m.stopCh:
		return
	default:
		close(m.stopCh)
	}
}

func (m *Manager) Refresh(ctx context.Context) error {
	m.mu.RLock()
	state := m.state
	m.mu.RUnlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, state.AppcastURL, nil)
	if err != nil {
		return err
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var next State
	next = state
	next.LastCheckedAt = time.Now().UTC()

	if resp.StatusCode >= 300 {
		m.storeState(next)
		return nil
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	version, build := parseAppcast(string(raw))
	if m.autoUpdate && version != "" && (version != next.CurrentVersion || build != next.CurrentBuild) {
		next.CurrentVersion = version
		next.CurrentBuild = build
		next.LastUpdatedAt = time.Now().UTC()
	}

	m.storeState(next)
	return nil
}

func (m *Manager) loadState() {
	if m.stateStore == nil {
		return
	}
	var state State
	if err := m.stateStore.Load(m.stateFile, &state); err != nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if strings.TrimSpace(state.CurrentVersion) != "" {
		m.state.CurrentVersion = strings.TrimSpace(state.CurrentVersion)
	}
	if strings.TrimSpace(state.CurrentBuild) != "" {
		m.state.CurrentBuild = strings.TrimSpace(state.CurrentBuild)
	}
	if !state.LastCheckedAt.IsZero() {
		m.state.LastCheckedAt = state.LastCheckedAt
	}
	if !state.LastUpdatedAt.IsZero() {
		m.state.LastUpdatedAt = state.LastUpdatedAt
	}
}

func (m *Manager) storeState(state State) {
	m.mu.Lock()
	m.state = state
	m.mu.Unlock()

	if m.stateStore == nil {
		return
	}
	_ = m.stateStore.Save(m.stateFile, state)
}

func parseAppcast(xml string) (string, string) {
	versionMatch := shortVersionPattern.FindStringSubmatch(xml)
	buildMatch := buildPattern.FindStringSubmatch(xml)
	return firstNonEmpty(versionMatch[1:]...), firstNonEmpty(buildMatch[1:]...)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
