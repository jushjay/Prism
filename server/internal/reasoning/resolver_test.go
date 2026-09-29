package reasoning

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveUsesPerModelEffort(t *testing.T) {
	resolver, err := New(FileConfig{Models: map[string]Model{
		"gpt-5.4":      {ReasoningEffort: "medium"},
		"gpt-5.4-high": {Target: "gpt-5.4", ReasoningEffort: "HIGH"},
	}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got := resolver.Resolve("gpt-5.4-high")
	if got.TargetModel != "gpt-5.4" || got.ReasoningEffort != "high" {
		t.Fatalf("unexpected virtual model resolution: %+v", got)
	}

	got = resolver.Resolve("gpt-5.4")
	if got.TargetModel != "gpt-5.4" || got.ReasoningEffort != "medium" {
		t.Fatalf("unexpected base model resolution: %+v", got)
	}

	got = resolver.Resolve("unknown-model")
	if got.TargetModel != "unknown-model" || got.ReasoningEffort != "" {
		t.Fatalf("unexpected passthrough resolution: %+v", got)
	}
}

func TestNewRejectsInvalidOrMissingEffort(t *testing.T) {
	if _, err := New(FileConfig{Models: map[string]Model{"a": {ReasoningEffort: "ultra"}}}); err == nil {
		t.Fatal("expected invalid effort error")
	}
	if _, err := New(FileConfig{Models: map[string]Model{"a": {Target: "b"}}}); err == nil {
		t.Fatal("expected missing effort error")
	}
	if _, err := New(FileConfig{Models: map[string]Model{"a": {ReasoningEffort: "low"}, " a ": {ReasoningEffort: "high"}}}); err == nil {
		t.Fatal("expected duplicate model error")
	}
}

func TestLoadRejectsDuplicateYAMLKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sidecar.yaml")
	content := "models:\n  gpt-5.4: { reasoning_effort: low }\n  gpt-5.4: { reasoning_effort: high }\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, true); err == nil {
		t.Fatal("expected duplicate key error")
	}
}

func TestLoadMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	resolver, err := Load(missing, false)
	if err != nil {
		t.Fatalf("optional missing file should not fail: %v", err)
	}
	if resolver.ModelCount() != 0 {
		t.Fatalf("expected empty resolver")
	}
	if _, err := Load(missing, true); err == nil {
		t.Fatal("required missing file should fail")
	}
}

func TestLoadBundledConfig(t *testing.T) {
	path := filepath.Join("..", "..", "configs", "sidecar.yaml")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("bundled config not found: %v", err)
	}
	resolver, err := Load(path, true)
	if err != nil {
		t.Fatalf("Load bundled config: %v", err)
	}
	for model, want := range map[string]Resolution{
		"gpt-5.4-xhigh": {TargetModel: "gpt-5.4", ReasoningEffort: "xhigh"},
		"gpt-5.4":       {TargetModel: "gpt-5.4", ReasoningEffort: "medium"},
	} {
		if got := resolver.Resolve(model); got != want {
			t.Fatalf("Resolve(%q) = %+v, want %+v", model, got, want)
		}
	}
}
