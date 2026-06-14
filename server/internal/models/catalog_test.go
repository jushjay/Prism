package models

import "testing"

func TestMergeBackendEntriesOverlaysStaticCatalog(t *testing.T) {
	static := Catalog{
		Models: []Info{
			{
				ID:          "gpt-5.4",
				DisplayName: "GPT-5.4",
				Description: "Static description",
				IsDefault:   true,
				SupportedReasoningEfforts: []ReasoningEffort{
					{ReasoningEffort: "medium", Description: "Static default"},
				},
				DefaultReasoningEffort: "medium",
				InputModalities:        []string{"text", "image"},
				OutputModalities:       []string{"text"},
			},
		},
		Aliases: map[string]string{"latest": "gpt-5.4"},
	}

	merged := static.MergeBackendEntries([]BackendModelEntry{
		{
			ID:                     "gpt-5.4",
			DisplayName:            "GPT-5.4 Live",
			Description:            "Dynamic description",
			DefaultReasoningEffort: "high",
			InputModalities:        []string{"text"},
		},
		{
			ID:                        "gpt-5.5",
			DisplayName:               "GPT-5.5",
			Description:               "Dynamic-only model",
			SupportedReasoningEfforts: []BackendReasoningEffort{{Effort: "high", Description: "Deep"}},
			DefaultReasoningEffort:    "high",
			InputModalities:           []string{"text", "image"},
			OutputModalities:          []string{"text"},
		},
	})

	if len(merged.Models) != 2 {
		t.Fatalf("expected 2 models after merge, got %d", len(merged.Models))
	}

	primary, ok := merged.Resolve("gpt-5.4")
	if !ok {
		t.Fatalf("expected merged catalog to resolve gpt-5.4")
	}
	if primary.DisplayName != "GPT-5.4 Live" {
		t.Fatalf("expected display name to be overlaid from backend, got %q", primary.DisplayName)
	}
	if primary.Description != "Dynamic description" {
		t.Fatalf("expected description to be overlaid from backend, got %q", primary.Description)
	}
	if primary.IsDefault != true {
		t.Fatalf("expected static default flag to be preserved")
	}
	if len(primary.SupportedReasoningEfforts) != 1 || primary.SupportedReasoningEfforts[0].ReasoningEffort != "medium" {
		t.Fatalf("expected static reasoning efforts to be preserved when backend omits them")
	}

	secondary, ok := merged.Resolve("gpt-5.5")
	if !ok {
		t.Fatalf("expected merged catalog to resolve gpt-5.5")
	}
	if secondary.DefaultReasoningEffort != "high" {
		t.Fatalf("expected dynamic-only model default reasoning effort to be retained, got %q", secondary.DefaultReasoningEffort)
	}
	if len(secondary.SupportedReasoningEfforts) != 1 || secondary.SupportedReasoningEfforts[0].ReasoningEffort != "high" {
		t.Fatalf("expected dynamic-only reasoning efforts to be retained")
	}

	if merged.Aliases["latest"] != "gpt-5.4" {
		t.Fatalf("expected aliases to be preserved")
	}
}
