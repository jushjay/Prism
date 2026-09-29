package reasoning

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type ModelMapping struct {
	Name            string `yaml:"name"`
	Target          string `yaml:"target"`
	ReasoningEffort string `yaml:"reasoning_effort"`
}

type FileConfig struct {
	Models            []ModelMapping    `yaml:"models"`
	ReasoningDefaults map[string]string `yaml:"reasoning_defaults"`
	Aliases           map[string]string `yaml:"aliases"`
}

// Resolution describes how a requested model is sent upstream.
// ReasoningEffort comes from an explicit model mapping and wins over
// DefaultReasoningEffort, which is the per-target-model default.
type Resolution struct {
	TargetModel            string
	ReasoningEffort        string
	DefaultReasoningEffort string
}

type Resolver struct {
	mappings map[string]ModelMapping
	defaults map[string]string
	aliases  map[string]string
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
	r := &Resolver{
		mappings: map[string]ModelMapping{},
		defaults: map[string]string{},
		aliases:  map[string]string{},
	}
	for _, item := range cfg.Models {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			return nil, errors.New("models[].name is required")
		}
		if _, exists := r.mappings[name]; exists {
			return nil, fmt.Errorf("duplicate model mapping %q", name)
		}
		effort, err := normalizeEffort(item.ReasoningEffort)
		if err != nil {
			return nil, fmt.Errorf("models[%s]: %w", name, err)
		}
		target := strings.TrimSpace(item.Target)
		if target == "" {
			target = name
		}
		r.mappings[name] = ModelMapping{Name: name, Target: target, ReasoningEffort: effort}
	}
	for model, value := range cfg.ReasoningDefaults {
		model = strings.TrimSpace(model)
		effort, err := normalizeEffort(value)
		if err != nil {
			return nil, fmt.Errorf("reasoning_defaults[%s]: %w", model, err)
		}
		if model != "" && effort != "" {
			r.defaults[model] = effort
		}
	}
	for alias, target := range cfg.Aliases {
		alias = strings.TrimSpace(alias)
		target = strings.TrimSpace(target)
		if alias != "" && target != "" {
			r.aliases[alias] = target
		}
	}
	return r, nil
}

func (r *Resolver) Resolve(model string) Resolution {
	requested := strings.TrimSpace(model)
	if requested == "" {
		return Resolution{}
	}
	resolution := Resolution{TargetModel: requested}
	if r == nil {
		return resolution
	}
	if mapping, ok := r.mappings[requested]; ok {
		resolution.TargetModel = mapping.Target
		resolution.ReasoningEffort = mapping.ReasoningEffort
	}
	if target, ok := r.aliases[resolution.TargetModel]; ok {
		resolution.TargetModel = target
	}
	resolution.DefaultReasoningEffort = r.defaults[resolution.TargetModel]
	return resolution
}

func (r *Resolver) MappingCount() int {
	if r == nil {
		return 0
	}
	return len(r.mappings)
}

func normalizeEffort(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "", nil
	}
	if _, ok := validEfforts[value]; !ok {
		return "", fmt.Errorf("unsupported reasoning effort %q", value)
	}
	return value, nil
}
