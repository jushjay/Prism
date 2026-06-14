package models

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const createdAt = int64(1700000000)

type Catalog struct {
	Models  []Info            `yaml:"models" json:"models"`
	Aliases map[string]string `yaml:"aliases" json:"aliases"`
}

type Info struct {
	ID                        string            `yaml:"id" json:"id"`
	DisplayName               string            `yaml:"displayName" json:"displayName"`
	Description               string            `yaml:"description" json:"description"`
	Object                    string            `yaml:"object" json:"object,omitempty"`
	Created                   int64             `yaml:"created" json:"created,omitempty"`
	OwnedBy                   string            `yaml:"ownedBy" json:"owned_by,omitempty"`
	IsDefault                 bool              `yaml:"isDefault" json:"isDefault"`
	SupportedReasoningEfforts []ReasoningEffort `yaml:"supportedReasoningEfforts" json:"supportedReasoningEfforts"`
	DefaultReasoningEffort    string            `yaml:"defaultReasoningEffort" json:"defaultReasoningEffort"`
	InputModalities           []string          `yaml:"inputModalities" json:"inputModalities"`
	OutputModalities          []string          `yaml:"outputModalities" json:"outputModalities"`
	Upgrade                   any               `yaml:"upgrade" json:"upgrade"`
}

type ReasoningEffort struct {
	ReasoningEffort string `yaml:"reasoningEffort" json:"reasoningEffort"`
	Description     string `yaml:"description" json:"description"`
}

type OpenAIModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type BackendModelEntry struct {
	Slug                      string                   `json:"slug"`
	ID                        string                   `json:"id"`
	Object                    string                   `json:"object"`
	Created                   int64                    `json:"created"`
	OwnedBy                   string                   `json:"owned_by"`
	Name                      string                   `json:"name"`
	DisplayName               string                   `json:"display_name"`
	Description               string                   `json:"description"`
	IsDefault                 bool                     `json:"is_default"`
	DefaultReasoningEffort    string                   `json:"default_reasoning_effort"`
	DefaultReasoningLevel     string                   `json:"default_reasoning_level"`
	SupportedReasoningEfforts []BackendReasoningEffort `json:"supported_reasoning_efforts"`
	SupportedReasoningLevels  []BackendReasoningLevel  `json:"supported_reasoning_levels"`
	InputModalities           []string                 `json:"input_modalities"`
	OutputModalities          []string                 `json:"output_modalities"`
	Upgrade                   any                      `json:"upgrade"`
}

type BackendReasoningEffort struct {
	ReasoningEffortCamel string `json:"reasoningEffort"`
	ReasoningEffortSnake string `json:"reasoning_effort"`
	Effort               string `json:"effort"`
	Description          string `json:"description"`
}

type BackendReasoningLevel struct {
	Effort      string `json:"effort"`
	Description string `json:"description"`
}

func Load(path string) (Catalog, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Catalog{}, err
	}
	var catalog Catalog
	if err := yaml.Unmarshal(raw, &catalog); err != nil {
		return Catalog{}, err
	}
	return catalog, nil
}

func DecodeBackendEntries(raw []map[string]any) []BackendModelEntry {
	entries := make([]BackendModelEntry, 0, len(raw))
	for _, item := range raw {
		payload, err := json.Marshal(item)
		if err != nil {
			continue
		}
		var entry BackendModelEntry
		if err := json.Unmarshal(payload, &entry); err != nil {
			continue
		}
		if backendModelID(entry) == "" {
			continue
		}
		entries = append(entries, entry)
	}
	return entries
}

func NormalizeBackendEntries(entries []BackendModelEntry) []Info {
	out := make([]Info, 0, len(entries))
	for _, entry := range entries {
		info, _ := normalizeBackendModel(entry)
		out = append(out, info)
	}
	return out
}

func (c Catalog) OpenAIModels() []OpenAIModel {
	data := make([]OpenAIModel, 0, len(c.Models)+len(c.Aliases))
	seen := map[string]struct{}{}
	for _, model := range c.Models {
		seen[model.ID] = struct{}{}
		data = append(data, OpenAIModel{
			ID:      model.ID,
			Object:  "model",
			Created: createdAt,
			OwnedBy: "openai",
		})
	}
	for alias := range c.Aliases {
		if _, ok := seen[alias]; ok {
			continue
		}
		data = append(data, OpenAIModel{
			ID:      alias,
			Object:  "model",
			Created: createdAt,
			OwnedBy: "openai",
		})
	}
	return data
}

func (c Catalog) MergeBackendEntries(entries []BackendModelEntry) Catalog {
	staticMap := make(map[string]Info, len(c.Models))
	merged := make([]Info, 0, len(entries)+len(c.Models))
	seen := make(map[string]struct{}, len(entries))

	for _, model := range c.Models {
		staticMap[model.ID] = model
	}

	for _, raw := range entries {
		id := backendModelID(raw)
		if id == "" {
			continue
		}
		seen[id] = struct{}{}
		normalized, hasExplicitEfforts := normalizeBackendModel(raw)

		if existing, ok := staticMap[id]; ok {
			out := existing
			out.ID = normalized.ID
			if normalized.DisplayName != "" {
				out.DisplayName = normalized.DisplayName
			}
			if normalized.Description != "" {
				out.Description = normalized.Description
			}
			if raw.IsDefault {
				out.IsDefault = true
			}
			if hasExplicitEfforts {
				out.SupportedReasoningEfforts = normalized.SupportedReasoningEfforts
			}
			if normalized.DefaultReasoningEffort != "" {
				out.DefaultReasoningEffort = normalized.DefaultReasoningEffort
			}
			if len(normalized.InputModalities) > 0 {
				out.InputModalities = normalized.InputModalities
			}
			if len(normalized.OutputModalities) > 0 {
				out.OutputModalities = normalized.OutputModalities
			}
			out.Upgrade = normalized.Upgrade
			merged = append(merged, out)
			continue
		}

		merged = append(merged, normalized)
	}

	for _, model := range c.Models {
		if _, ok := seen[model.ID]; ok {
			continue
		}
		merged = append(merged, model)
	}

	return Catalog{
		Models:  merged,
		Aliases: mapsClone(c.Aliases),
	}
}

func (c Catalog) Resolve(id string) (Info, bool) {
	for _, model := range c.Models {
		if model.ID == id {
			return model, true
		}
	}
	if resolved, ok := c.Aliases[id]; ok {
		for _, model := range c.Models {
			if model.ID == resolved {
				return model, true
			}
		}
	}
	return Info{}, false
}

func (c Catalog) ResolveUpstreamID(id string) string {
	requested := strings.TrimSpace(id)
	if requested == "" {
		return ""
	}
	if resolved, ok := c.Aliases[requested]; ok {
		return strings.TrimSpace(resolved)
	}
	return requested
}

func (c Catalog) DefaultModel() string {
	for _, model := range c.Models {
		if model.IsDefault {
			return model.ID
		}
	}
	if len(c.Models) > 0 {
		return c.Models[0].ID
	}
	return "gpt-5.4"
}

func Timestamp() int64 {
	return time.Unix(createdAt, 0).Unix()
}

func normalizeBackendModel(raw BackendModelEntry) (Info, bool) {
	id := backendModelID(raw)
	if id == "" {
		return Info{}, false
	}

	efforts, hasExplicitEfforts := normalizeReasoningEfforts(raw)
	if len(efforts) == 0 {
		efforts = []ReasoningEffort{{ReasoningEffort: "medium", Description: "Default"}}
	}

	defaultReasoning := raw.DefaultReasoningEffort
	if defaultReasoning == "" {
		defaultReasoning = raw.DefaultReasoningLevel
	}
	if defaultReasoning == "" {
		defaultReasoning = "medium"
	}

	inputModalities := raw.InputModalities
	if len(inputModalities) == 0 {
		inputModalities = []string{"text"}
	}

	return Info{
		ID:                        id,
		DisplayName:               firstNonEmpty(raw.DisplayName, raw.Name, id),
		Description:               raw.Description,
		Object:                    firstNonEmpty(raw.Object, "model"),
		Created:                   raw.Created,
		OwnedBy:                   raw.OwnedBy,
		IsDefault:                 raw.IsDefault,
		SupportedReasoningEfforts: efforts,
		DefaultReasoningEffort:    defaultReasoning,
		InputModalities:           slices.Clone(inputModalities),
		OutputModalities:          slices.Clone(raw.OutputModalities),
		Upgrade:                   raw.Upgrade,
	}, hasExplicitEfforts
}

func normalizeReasoningEfforts(raw BackendModelEntry) ([]ReasoningEffort, bool) {
	efforts := make([]ReasoningEffort, 0, len(raw.SupportedReasoningEfforts)+len(raw.SupportedReasoningLevels))

	for _, effort := range raw.SupportedReasoningEfforts {
		value := firstNonEmpty(effort.ReasoningEffortCamel, effort.ReasoningEffortSnake, effort.Effort)
		if value == "" {
			continue
		}
		efforts = append(efforts, ReasoningEffort{
			ReasoningEffort: value,
			Description:     effort.Description,
		})
	}
	if len(efforts) > 0 {
		return efforts, true
	}

	for _, effort := range raw.SupportedReasoningLevels {
		if effort.Effort == "" {
			continue
		}
		efforts = append(efforts, ReasoningEffort{
			ReasoningEffort: effort.Effort,
			Description:     effort.Description,
		})
	}
	return efforts, len(efforts) > 0
}

func backendModelID(raw BackendModelEntry) string {
	return firstNonEmpty(raw.Slug, raw.ID, raw.Name)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func mapsClone(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}
