package prompts_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFileEngineLoadTemplate(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	got, err := engine.Render(context.Background(), "agents/coordinator-core.md", map[string]any{
		"execution_mode": "orchestrate",
		"has_file_tools": true,
	})
	testutil.FailErr(t, "render prompt template", err)
	if !strings.Contains(got, "Orchestrate work") {
		t.Fatalf("coordinator prompt = %q want Orchestrate work fragment", got)
	}
}

func TestFileEngineMissingTemplateFails(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	_, err := engine.Render(context.Background(), "agents/does-not-exist.md", nil)
	if err == nil {
		t.Fatal("expected error for missing template")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %v want not found", err)
	}
}

func TestFileEngineRegisterOverride(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	if err := engine.Register("custom.md", "hello {{ role_title }}"); err != nil {
		testutil.FailErr(t, "engine.Register failed", err)
	}
	got, err := engine.Render(context.Background(), "custom.md", map[string]any{"role_title": "world"})
	testutil.FailErr(t, "render prompt template", err)
	if got != "hello world" {
		t.Fatalf("got %q", got)
	}
}

func TestFileEngineRejectsPathEscape(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "safe.md"), []byte("ok"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{Site: dir})
	_, err := engine.Render(context.Background(), "../outside.md", nil)
	if err == nil {
		t.Fatal("expected path escape error")
	}
}
