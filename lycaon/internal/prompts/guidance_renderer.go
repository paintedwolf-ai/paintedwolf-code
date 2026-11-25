package prompts

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/toolschema"
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

var structuralGuidanceAllowlist = []string{
	"enrich/overlay-parent-rejected",
	"reject/_reject",
	"reject/spec-posture-block",
	"survey/grep-empty-structural",
	"survey/grep-empty-text",
	"survey/grep-scope-dense",
	"survey/jq-zoom",
	"utility/catalog-discovery",
	"utility/source-remove-permanent",
}

var structuralGuidanceSet = func() map[string]bool {
	out := make(map[string]bool, len(structuralGuidanceAllowlist))
	for _, ref := range structuralGuidanceAllowlist {
		out[ref] = true
	}
	return out
}()

// StructuralGuidanceFragments returns the sorted allowlist of reader-free structural guidance refs.
func StructuralGuidanceFragments() []string {
	return slices.Clone(structuralGuidanceAllowlist)
}

// IsStructuralGuidanceFragment reports whether ref is an allowlisted reader-free structural fragment.
func IsStructuralGuidanceFragment(ref string) bool {
	return structuralGuidanceSet[strings.TrimSpace(ref)]
}

var declaredReachGuidanceFacts = map[string]bool{
	"can_sed_read":              true,
	"can_recall_history":        true,
	"can_recall_history_record": true,
	"can_recall_query":          true,
	"can_preview_overlay":       true,
	"can_promote_overlay":       true,
	"can_instruct_worker":       true,
}

var stockDeclaredToolSetOnce = sync.OnceValue(func() map[string]bool {
	if vocab, err := BuildReaderFactVocabulary(nil); err == nil {
		return buildDeclaredToolAndOpSet(vocab)
	}
	return buildDeclaredToolAndOpSet(ReaderFactVocabulary{})
})

// DeclaredReachGuidanceFacts returns the reach-derived capability keys populated by GuidanceRenderer.RenderWithReach.
func DeclaredReachGuidanceFacts() map[string]bool {
	return maps.Clone(declaredReachGuidanceFacts)
}

// IsCapabilityKeyName reports whether key names a capability predicate rather than occurrence data.
func IsCapabilityKeyName(key string) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	root := key
	if idx := strings.IndexByte(key, '.'); idx >= 0 {
		root = key[:idx]
	}
	if IsDeclaredOrReservedReaderFact(key) || IsDeclaredOrReservedReaderFact(root) || DeclaredReachGuidanceFacts()[root] || root == "units" {
		return true
	}
	declared := stockDeclaredToolSetOnce()
	for _, suf := range []string{"_available", "_offered", "_allowed", "_reachable"} {
		if stem, ok := strings.CutSuffix(root, suf); ok && stem != "" && declared[stem] {
			return true
		}
	}
	return false
}

func rejectCapabilityKeysInOccurrence(kind, ref string, data map[string]any) error {
	if len(data) == 0 {
		return nil
	}
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if IsCapabilityKeyName(k) {
			return fmt.Errorf("%s %q carries capability key %q without a Reader or toolreach.Request", kind, ref, k)
		}
	}
	return nil
}

var (
	backtickShortFlagCmdRe = regexp.MustCompile("`([a-z][a-z0-9_]*)[ \t]+-[a-zA-Z]")
	backtickGitSubcmdRe    = regexp.MustCompile("`git[ \t]+([a-z-]+)\\b")
	unbacktickedToolCallRe = regexp.MustCompile(`\b([a-z][a-z0-9_]+)\s*\(\s*(?:[a-z_]+\s*=|\{|")`)
	parenCmdInvocationRe   = regexp.MustCompile(`\(\s*(sed|rg|grep|awk|jq|yq|cat|head|tail|find|curl)\s+-[a-zA-Z]`)
)

// StructuralActionClaims returns callable tool or command names claimed in text.
func StructuralActionClaims(text string, declared map[string]bool) []string {
	seen := map[string]bool{}
	for _, name := range skillUnreachableNames(text, declared, map[string]bool{}) {
		seen[name] = true
	}
	for _, m := range backtickShortFlagCmdRe.FindAllStringSubmatch(text, -1) {
		if declared[m[1]] {
			seen[m[1]] = true
		}
	}
	for _, m := range backtickGitSubcmdRe.FindAllStringSubmatch(text, -1) {
		seen["git "+m[1]] = true
	}
	for _, m := range unbacktickedToolCallRe.FindAllStringSubmatch(text, -1) {
		if declared[m[1]] {
			seen[m[1]] = true
		}
	}
	for _, m := range parenCmdInvocationRe.FindAllStringSubmatch(text, -1) {
		seen[m[1]] = true
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func rejectStructuralTemplateViolations(engine PromptTemplateEngine, kind, templatePath string) error {
	fe, ok := engine.(*FileTemplateEngine)
	if !ok || fe == nil {
		return nil
	}
	loader := newLayeredLoader(fe.layers, fe.registered)
	if _, err := loader.Preflight(templatePath); err != nil {
		return fmt.Errorf("%s template %s: %w", kind, templatePath, err)
	}
	var declared map[string]bool
	if vocab, err := BuildReaderFactVocabulary(fe.layers.Catalog); err == nil {
		declared = buildDeclaredToolAndOpSet(vocab)
	} else {
		declared = buildDeclaredToolAndOpSet(ReaderFactVocabulary{})
	}
	sourceKeys := make([]string, 0, len(loader.sources))
	for k := range loader.sources {
		sourceKeys = append(sourceKeys, k)
	}
	sort.Strings(sourceKeys)
	for _, k := range sourceKeys {
		src := loader.sources[k]
		for _, vref := range pongoplain.VariableReferences(src) {
			if IsCapabilityKeyName(vref) {
				return fmt.Errorf("%s %q (%s) references capability fact %q without a Reader", kind, templatePath, k, vref)
			}
		}
		if claims := StructuralActionClaims(src, declared); len(claims) > 0 {
			return fmt.Errorf("%s %q (%s) claims callable action %q in structural fragment", kind, templatePath, k, claims[0])
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

// Render executes an explicitly allowlisted structural guidance fragment.
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
	if !IsStructuralGuidanceFragment(name) {
		return "", fmt.Errorf("guidance %q is not an allowlisted structural fragment; use RenderWithReader or RenderWithReach", name)
	}
	if err := rejectCapabilityKeysInOccurrence("guidance", name, data); err != nil {
		return "", err
	}
	if err := rejectStructuralTemplateViolations(r.engine, "guidance", GuidanceTemplatePrefix+name+".md"); err != nil {
		return "", err
	}
	return r.engine.RenderGuidance(ctx, name, data)
}

// RenderWithReach executes guidance/{ref}.md against a frozen toolreach.Request.
func (r *GuidanceRenderer) RenderWithReach(ctx context.Context, req toolreach.Request, ref string, occurrence map[string]any) (string, error) {
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
	if !req.Known() {
		return "", fmt.Errorf("guidance %q requires a known toolreach.Request", name)
	}
	if err := rejectCapabilityKeysInOccurrence("guidance", name, occurrence); err != nil {
		return "", err
	}
	callableField := func(tool, field string) bool {
		return req.Status(tool) == toolreach.Callable && req.AcceptsField(tool, field)
	}
	canRecallHistory := callableField("recall", "history.after_ord") && callableField("recall", "history.through_ord")
	merged := make(map[string]any, len(occurrence)+7)
	maps.Copy(merged, occurrence)
	merged["can_sed_read"] = callableField(toolreach.ProgramTool, "command") && req.ProgramInvocation(ctx, "sed", "-n", "1,1p", "FILE")
	merged["can_recall_history"] = canRecallHistory
	merged["can_recall_history_record"] = canRecallHistory &&
		callableField("recall", "history.search") &&
		callableField("recall", "history.message_id") &&
		callableField("recall", "history.offset") &&
		explicitEnumAccepted(req, "recall", "history.format", "record")
	merged["can_recall_query"] = callableField("recall", "query")
	merged["can_preview_overlay"] = callableField("preview_overlay", "overlay_id")
	merged["can_promote_overlay"] = callableField("promote_overlay", "overlay_id") &&
		callableField("promote_overlay", "resolutions.path") &&
		callableField("promote_overlay", "resolutions.content")
	merged["can_instruct_worker"] = callableField("instruct_worker", "worker") &&
		callableField("instruct_worker", "instructions")
	return r.engine.RenderGuidance(ctx, name, merged)
}

func explicitEnumAccepted(req toolreach.Request, tool, field, value string) bool {
	if req.Status(tool) != toolreach.Callable {
		return false
	}
	schema, ok := req.ToolSchema(tool)
	return ok && toolschema.SchemaAcceptsEnumValue(schema, field, value)
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
	if err := rejectCapabilityKeysInOccurrence("utility prompt", name, occurrence); err != nil {
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
