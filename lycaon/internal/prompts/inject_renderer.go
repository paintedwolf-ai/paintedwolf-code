package prompts

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/promptunit"
	"github.com/lycaon/lycaon/internal/sandbox"
)

var structuralInjectAllowlist = []string{
	"agents-md",
	"blueprint",
	"execution-state",
	"scan-guidance-ephemeral",
	"synthesis-evidence",
	"view-context",
	"worker-messages",
	"worker-round",
}

var structuralInjectSet = func() map[string]bool {
	out := make(map[string]bool, len(structuralInjectAllowlist))
	for _, ref := range structuralInjectAllowlist {
		out[ref] = true
	}
	return out
}()

// StructuralInjectFragments returns the sorted allowlist of reader-free structural inject refs.
func StructuralInjectFragments() []string {
	return slices.Clone(structuralInjectAllowlist)
}

// IsStructuralInjectFragment reports whether ref is an allowlisted reader-free structural inject.
func IsStructuralInjectFragment(ref string) bool {
	return structuralInjectSet[strings.TrimSpace(ref)]
}

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

// Render executes an explicitly allowlisted structural inject/{ref}.md fragment.
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
	if !IsStructuralInjectFragment(name) {
		return "", fmt.Errorf("inject %q is not an allowlisted structural fragment; use RenderWithReader", name)
	}
	if err := rejectCapabilityKeysInOccurrence("inject", name, data); err != nil {
		return "", err
	}
	if err := rejectStructuralTemplateViolations(r.engine, "inject", InjectTemplatePrefix+name+".md"); err != nil {
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
