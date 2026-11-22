package prompts

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/promptunit"
	"github.com/lycaon/lycaon/internal/sandbox"
)

// InjectRenderer renders system inject partials under config/packs/painted-wolf/platform/guidance/.
type InjectRenderer struct {
	engine PromptTemplateEngine
}

// NewInjectRenderer wraps a template engine for inject/*.md renders.
func NewInjectRenderer(engine PromptTemplateEngine) *InjectRenderer {
	if engine == nil {
		return nil
	}
	return &InjectRenderer{engine: engine}
}

// Render executes reader-free inject/{ref}.md with occurrence data.
// Injects that carry or reference reader capability facts must use RenderWithReader.
func (r *InjectRenderer) Render(ctx context.Context, ref string, data map[string]any) (string, error) {
	if r == nil || r.engine == nil {
		return "", fmt.Errorf("inject renderer not configured")
	}
	name := strings.TrimSpace(ref)
	if name == "" {
		return "", fmt.Errorf("empty inject ref")
	}
	if sandbox.HasParentTraversal(name) || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("invalid inject ref %q", ref)
	}
	if err := rejectReaderFactsInOccurrence("inject", name, data); err != nil {
		return "", err
	}
	if err := rejectReaderFactReferencesInEngineTemplate(r.engine, "inject", InjectTemplatePrefix+name+".md"); err != nil {
		return "", err
	}
	return r.engine.RenderInject(ctx, name, data)
}

// RenderWithReader executes inject/{ref}.md against a validated Reader merged with occurrence data.
func (r *InjectRenderer) RenderWithReader(ctx context.Context, reader Reader, ref string, occurrence map[string]any) (string, error) {
	if r == nil || r.engine == nil {
		return "", fmt.Errorf("inject renderer not configured")
	}
	name := strings.TrimSpace(ref)
	if name == "" {
		return "", fmt.Errorf("empty inject ref")
	}
	if sandbox.HasParentTraversal(name) || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("invalid inject ref %q", ref)
	}
	if !reader.Valid() {
		return "", fmt.Errorf("inject %q requires a valid reader", name)
	}
	merged, err := mergeOccurrenceWithReader(reader, occurrence)
	if err != nil {
		return "", err
	}
	return r.engine.RenderInject(ctx, name, merged)
}

// UnitCatalog loads the unit catalog behind the wrapped engine.
func (r *InjectRenderer) UnitCatalog() (promptunit.Catalog, error) {
	if r == nil || r.engine == nil {
		return promptunit.Catalog{}, fmt.Errorf("inject renderer not configured")
	}
	var eff *extpacks.EffectiveCatalog
	if fe, ok := r.engine.(*FileTemplateEngine); ok {
		eff = fe.Layers().Catalog
	}
	return UnitCatalogFor(eff)
}

// RenderUnit renders one unit template through the wrapped engine.
func (r *InjectRenderer) RenderUnit(ctx context.Context, ref string, data map[string]any) (string, error) {
	if r == nil || r.engine == nil {
		return "", fmt.Errorf("inject renderer not configured")
	}
	return r.engine.Render(ctx, ref, data)
}
