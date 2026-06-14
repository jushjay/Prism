package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

type StateStore interface {
	Load(path string, target any) error
	Save(path string, value any) error
}

type JSONStore struct {
	mu sync.Mutex
}

func NewJSONStore() *JSONStore {
	return &JSONStore{}
}

func (s *JSONStore) Load(path string, target any) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, target)
}

func (s *JSONStore) Save(path string, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}
