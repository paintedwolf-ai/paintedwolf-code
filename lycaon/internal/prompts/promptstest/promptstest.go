// Package promptstest builds bundled test renderers.
package promptstest

import (
	"github.com/lycaon/lycaon/internal/configlayout"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
)

// GuidanceRenderer returns a renderer over the bundled guidance templates.
func GuidanceRenderer(t testing.TB) *prompts.GuidanceRenderer {
	t.Helper()
	return BundledRenderer()
}

// BundledRenderer returns a bundled guidance renderer.
func BundledRenderer() *prompts.GuidanceRenderer {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("promptstest: runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	return prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root}))
}

// InjectRenderer returns a bundled inject renderer.
func InjectRenderer(t testing.TB) *prompts.InjectRenderer {
	t.Helper()
	mod := configlayout.FindModuleRoot()
	return prompts.NewInjectRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: mod}))
}
