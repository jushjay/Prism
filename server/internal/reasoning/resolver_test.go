package reasoning

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveAppliesMappingAliasAndDefaults(t *testing.T) {
	resolver, err := New(FileConfig{
		Models: []ModelMapping{
			{Name: "gpt-5.4-high", Target: "gpt-5.4", ReasoningEffort: "HIGH"},
			{Name: "fast", Target: "codex-alias"},
		},
		ReasoningDefaults: map[string]string{"gpt-5.4": "low", "gpt-5.3-codex": "medium"},
		Aliases:           map[string]string{"codex-alias": "gpt-5.3-codex"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got := resolver.Resolve("gpt-5.4-high")
	if got.TargetModel != "gpt-5.4" || got.ReasoningEffort != "high" || got.DefaultReasoningEffort != "low" {
		t.Fatalf("unexpected mapping resolution: %+v", got)
	}

	got = resolver.Resolve("fast")
	if got.TargetModel != "gpt-5.3-codex" || got.ReasoningEffort != "" || got.DefaultReasoningEffort != "medium" {
		t.Fatalf("unexpected alias resolution: %+v", got)
	}

	got = resolver.Resolve("unknown-model")
	if got.TargetModel != "unknown-model" || got.ReasoningEffort != "" || got.DefaultReasoningEffort != "" {
		t.Fatalf("unexpected passthrough resolution: %+v", got)
	}
}

func TestNewRejectsInvalidEffortAndDuplicates(t *testing.T) {
	if _, err := New(FileConfig{Models: []ModelMapping{{Name: "a", ReasoningEffort: "ultra"}}}); err == nil {
		t.Fatal("expected invalid effort error")
	}
	if _, err := New(FileConfig{Models: []ModelMapping{{Name: "a"}, {Name: "a"}}}); err == nil {
		t.Fatal("expected duplicate mapping error")
	}
}

func TestLoadMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	resolver, err := Load(missing, false)
	if err != nil {
		t.Fatalf("optional missing file should not fail: %v", err)
	}
	if resolver.MappingCount() != 0 {
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
	got := resolver.Resolve("gpt-5.4-xhigh")
	if got.TargetModel != "gpt-5.4" || got.ReasoningEffort != "xhigh" {
		t.Fatalf("unexpected bundled resolution: %+v", got)
	}
}
