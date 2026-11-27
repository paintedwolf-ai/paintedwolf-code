// Package promptstest builds bundled test renderers.
package promptstest

import (
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/toolschema"
)

var stockProjectedSchemasOnce = sync.OnceValue(func() map[string]map[string]any {
	eff, err := extpacks.CatalogForConsumers()
	if err != nil || eff == nil {
		return nil
	}
	cfg, _, err := extpacks.LoadEffectiveToolSchemas(eff)
	if err != nil || cfg == nil {
		return nil
	}
	out := make(map[string]map[string]any, len(cfg.Tools))
	for name := range cfg.Tools {
		if meta, ok := cfg.ToolMeta(name); ok && meta.ArgsSchema != nil {
			out[name] = toolschema.ProjectArgsSchema(meta.ArgsSchema)
		}
	}
	return out
})

// ProjectedToolSchemas returns projected stock argument schemas for test fixtures.
func ProjectedToolSchemas(t testing.TB, names ...string) map[string]map[string]any {
	t.Helper()
	all := stockProjectedSchemasOnce()
	if len(all) == 0 {
		t.Fatal("promptstest: failed to load effective tool schemas")
	}
	if len(names) == 0 {
		return nil
	}
	out := make(map[string]map[string]any, len(names))
	for _, name := range names {
		if s, ok := all[name]; ok && s != nil {
			out[name] = s
		}
	}
	return out
}

// StockToolReach builds a test-only toolreach.Request carrying projected schemas for names.
func StockToolReach(t testing.TB, names ...string) toolreach.Request {
	t.Helper()
	return toolreach.Offered(names).WithSchemas(ProjectedToolSchemas(t, names...))
}

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
