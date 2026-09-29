package reasoning

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Model is one entry of sidecar.yaml `models`, keyed by the model name Cursor
// sends. Every model carries exactly one reasoning effort.
type Model struct {
	Target          string `yaml:"target"`
	ReasoningEffort string `yaml:"reasoning_effort"`
}

type FileConfig struct {
	Models map[string]Model `yaml:"models"`
}

// Resolution describes how a requested model is sent upstream. ReasoningEffort
// is empty for models that are not configured.
type Resolution struct {
	TargetModel     string
	ReasoningEffort string
}

type Resolver struct {
	models map[string]Model
}

var validEfforts = map[string]struct{}{
	"none":    {},
	"minimal": {},
	"low":     {},
	"medium":  {},
	"high":    {},
	"xhigh":   {},
}

func Load(path string, required bool) (*Resolver, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && !required {
			return New(FileConfig{})
		}
		return nil, err
	}
	var cfg FileConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return New(cfg)
}

func New(cfg FileConfig) (*Resolver, error) {
	r := &Resolver{models: map[string]Model{}}
	for rawName, item := range cfg.Models {
		name := strings.TrimSpace(rawName)
		if name == "" {
			return nil, errors.New("models: empty model name")
		}
		if _, exists := r.models[name]; exists {
			return nil, fmt.Errorf("models: duplicate model %q", name)
		}
		effort, err := normalizeEffort(item.ReasoningEffort)
		if err != nil {
			return nil, fmt.Errorf("models[%s]: %w", name, err)
		}
		target := strings.TrimSpace(item.Target)
		if target == "" {
			target = name
		}
		r.models[name] = Model{Target: target, ReasoningEffort: effort}
	}
	return r, nil
}

func (r *Resolver) Resolve(model string) Resolution {
	requested := strings.TrimSpace(model)
	if requested == "" {
		return Resolution{}
	}
	if r != nil {
		if item, ok := r.models[requested]; ok {
			return Resolution{TargetModel: item.Target, ReasoningEffort: item.ReasoningEffort}
		}
	}
	return Resolution{TargetModel: requested}
}

func (r *Resolver) ModelCount() int {
	if r == nil {
		return 0
	}
	return len(r.models)
}

func normalizeEffort(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "", errors.New("reasoning_effort is required")
	}
	if _, ok := validEfforts[value]; !ok {
		return "", fmt.Errorf("unsupported reasoning effort %q", value)
	}
	return value, nil
}
