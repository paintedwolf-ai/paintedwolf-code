package prompts

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/sandbox"
)

var zeroToolUtilityAllowlist = []string{
	"compaction/chunk-summary-system",
	"compaction/resume-user",
	"compaction/session-summary-system",
	"curator/system",
	"curator/user",
	"utility/activity-change-system",
	"utility/activity-change-user",
	"utility/commit-message-system",
	"utility/pick-seeds-system",
	"utility/pick-seeds-user",
	"utility/project-name-system",
	"utility/rationale-system",
	"utility/rationale-user",
	"utility/session-title-system",
	"utility/step-change-system",
	"utility/step-change-user",
	"utility/today-line",
	"utility/verify-detect-system",
	"utility/worker-result-system",
	"utility/worker-result-user",
}

var zeroToolUtilitySet = func() map[string]bool {
	out := make(map[string]bool, len(zeroToolUtilityAllowlist))
	for _, ref := range zeroToolUtilityAllowlist {
		out[ref] = true
	}
	return out
}()

// ZeroToolUtilityTemplates returns the sorted allowlist of zero-tool LLM utility prompt refs.
func ZeroToolUtilityTemplates() []string {
	return slices.Clone(zeroToolUtilityAllowlist)
}

// IsZeroToolUtilityTemplate reports whether ref is in the zero-tool utility prompt allowlist.
func IsZeroToolUtilityTemplate(ref string) bool {
	return zeroToolUtilitySet[strings.TrimSpace(ref)]
}

func rejectReaderFactsInOccurrence(kind, ref string, data map[string]any) error {
	if len(data) == 0 {
		return nil
	}
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if IsDeclaredOrReservedReaderFact(k) {
			return fmt.Errorf("%s %q carries reader fact %q without a Reader", kind, ref, k)
		}
	}
	return nil
}

func rejectReaderFactReferencesInEngineTemplate(engine PromptTemplateEngine, kind, templatePath string) error {
	fe, ok := engine.(*FileTemplateEngine)
	if !ok || fe == nil {
		return nil
	}
	loader := newLayeredLoader(fe.layers, fe.registered)
	if _, err := loader.Preflight(templatePath); err != nil {
		return nil
	}
	sourceKeys := make([]string, 0, len(loader.sources))
	for k := range loader.sources {
		sourceKeys = append(sourceKeys, k)
	}
	sort.Strings(sourceKeys)
	for _, k := range sourceKeys {
		for _, vref := range pongoplain.VariableReferences(loader.sources[k]) {
			root := vref
			if idx := strings.IndexByte(vref, '.'); idx >= 0 {
				root = vref[:idx]
			}
			if IsDeclaredOrReservedReaderFact(vref) || IsDeclaredOrReservedReaderFact(root) || root == "units" {
				return fmt.Errorf("%s %q references reader fact %q without a Reader", kind, templatePath, vref)
			}
		}
	}
	return nil
}

// GuidanceRenderer renders tool feedback and reader-bound guidance blocks.
type GuidanceRenderer struct {
	engine PromptTemplateEngine
}

// NewGuidanceRenderer wraps a template engine for guidance/*.md renders.
func NewGuidanceRenderer(engine PromptTemplateEngine) *GuidanceRenderer {
	if engine == nil {
		return nil
	}
	return &GuidanceRenderer{engine: engine}
}

// Engine returns the underlying template engine.
func (r *GuidanceRenderer) Engine() PromptTemplateEngine {
	if r == nil {
		return nil
	}
	return r.engine
}

// Render executes a reader-free structural guidance fragment (e.g. reject cards,
// survey specifics, or explicit projection templates). Zero-tool LLM utility
// prompts and reader-conditioned templates are rejected here.
func (r *GuidanceRenderer) Render(ctx context.Context, ref string, data map[string]any) (string, error) {
	if r == nil || r.engine == nil {
		return "", fmt.Errorf("guidance renderer not configured")
	}
	name := strings.TrimSpace(ref)
	if name == "" {
		return "", fmt.Errorf("empty guidance ref")
	}
	if sandbox.HasParentTraversal(name) || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("invalid guidance ref %q", ref)
	}
	if IsZeroToolUtilityTemplate(name) {
		return "", fmt.Errorf("guidance %q is a zero-tool utility prompt; use RenderUtilityPrompt", name)
	}
	if err := rejectReaderFactsInOccurrence("guidance", name, data); err != nil {
		return "", err
	}
	if err := rejectReaderFactReferencesInEngineTemplate(r.engine, "guidance", GuidanceTemplatePrefix+name+".md"); err != nil {
		return "", err
	}
	return r.engine.RenderGuidance(ctx, name, data)
}

// RenderWithReader executes guidance/{ref}.md against a validated Reader merged with occurrence data.
func (r *GuidanceRenderer) RenderWithReader(ctx context.Context, reader Reader, ref string, occurrence map[string]any) (string, error) {
	if r == nil || r.engine == nil {
		return "", fmt.Errorf("guidance renderer not configured")
	}
	name := strings.TrimSpace(ref)
	if name == "" {
		return "", fmt.Errorf("empty guidance ref")
	}
	if sandbox.HasParentTraversal(name) || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("invalid guidance ref %q", ref)
	}
	if IsZeroToolUtilityTemplate(name) {
		return "", fmt.Errorf("guidance %q is a zero-tool utility prompt; use RenderUtilityPrompt", name)
	}
	if !reader.Valid() {
		return "", fmt.Errorf("guidance %q requires a valid reader", name)
	}
	merged, err := mergeOccurrenceWithReader(reader, occurrence)
	if err != nil {
		return "", err
	}
	return r.engine.RenderGuidance(ctx, name, merged)
}

// UtilityPromptRenderer renders allowlisted zero-tool LLM utility prompts.
type UtilityPromptRenderer struct {
	engine PromptTemplateEngine
}

// NewUtilityPromptRenderer wraps a template engine for zero-tool utility prompts.
func NewUtilityPromptRenderer(engine PromptTemplateEngine) *UtilityPromptRenderer {
	if engine == nil {
		return nil
	}
	return &UtilityPromptRenderer{engine: engine}
}

// Render executes an allowlisted zero-tool utility prompt and fails closed on empty output.
func (r *UtilityPromptRenderer) Render(ctx context.Context, ref string, occurrence map[string]any) (string, error) {
	if r == nil || r.engine == nil {
		return "", fmt.Errorf("utility prompt renderer not configured")
	}
	name := strings.TrimSpace(ref)
	if name == "" {
		return "", fmt.Errorf("empty utility prompt ref")
	}
	if sandbox.HasParentTraversal(name) || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("invalid utility prompt ref %q", ref)
	}
	if !IsZeroToolUtilityTemplate(name) {
		return "", fmt.Errorf("template %q is not in the zero-tool utility prompt allowlist", name)
	}
	if err := rejectReaderFactsInOccurrence("utility prompt", name, occurrence); err != nil {
		return "", err
	}
	raw, err := r.engine.RenderGuidance(ctx, name, occurrence)
	if err != nil {
		return "", err
	}
	out := strings.TrimSpace(raw)
	if out == "" {
		return "", fmt.Errorf("utility prompt %q rendered empty", name)
	}
	return out, nil
}

var (
	utilityRendererMu      sync.RWMutex
	utilityRenderer        *UtilityPromptRenderer
	defaultUtilityOnce     sync.Once
	defaultUtilityRenderer *UtilityPromptRenderer
)

// SetUtilityPromptRenderer installs the process-wide zero-tool utility prompt renderer.
func SetUtilityPromptRenderer(renderer *UtilityPromptRenderer) {
	utilityRendererMu.Lock()
	utilityRenderer = renderer
	utilityRendererMu.Unlock()
}

// RenderUtilityPrompt renders an allowlisted zero-tool utility prompt.
func RenderUtilityPrompt(ctx context.Context, ref string, occurrence map[string]any) (string, error) {
	utilityRendererMu.RLock()
	renderer := utilityRenderer
	utilityRendererMu.RUnlock()
	if renderer == nil {
		defaultUtilityOnce.Do(func() {
			defaultUtilityRenderer = NewUtilityPromptRenderer(NewFileTemplateEngineLayers(PromptLayers{}))
		})
		renderer = defaultUtilityRenderer
	}
	return renderer.Render(ctx, ref, occurrence)
}
