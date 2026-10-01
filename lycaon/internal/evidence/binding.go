package evidence

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/ingestion"
)

// TranscriptDiet is the closed enum for evidence-kind diet routing.
// Omitted YAML key means TranscriptDietAgeDefault.
type TranscriptDiet string

const (
	TranscriptDietAgeDefault        TranscriptDiet = "age_default"
	TranscriptDietShapeBounded      TranscriptDiet = "shape_bounded"
	TranscriptDietAnchorResidue     TranscriptDiet = "anchor_residue"
	TranscriptDietSpillPointer      TranscriptDiet = "spill_pointer"
	TranscriptDietPreserveStructure TranscriptDiet = "preserve_structure"
)

func parseTranscriptDiet(raw string) (TranscriptDiet, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return TranscriptDietAgeDefault, nil
	}
	d := TranscriptDiet(raw)
	switch d {
	case TranscriptDietAgeDefault, TranscriptDietShapeBounded, TranscriptDietAnchorResidue,
		TranscriptDietSpillPointer, TranscriptDietPreserveStructure:
		return d, nil
	default:
		return "", fmt.Errorf("unknown transcript_diet %q", raw)
	}
}

type evidenceKindEntry struct {
	Shape          string `yaml:"shape"`
	Survey         bool   `yaml:"survey"`
	Mutation       bool   `yaml:"mutation"`
	TranscriptDiet string `yaml:"transcript_diet,omitempty"`
}

type producesEvidenceEntry struct {
	Kind     string                 `yaml:"kind"`
	Variants []evidenceVariantEntry `yaml:"variants,omitempty"`
}

type evidenceVariantEntry struct {
	WhenArgPresent string `yaml:"when_arg_present"`
	Kind           string `yaml:"kind"`
}

type toolEvidenceEntry struct {
	ProducesEvidence *producesEvidenceEntry `yaml:"produces_evidence"`
}

type evidenceKindsFile struct {
	EvidenceKinds map[string]evidenceKindEntry `yaml:"evidence_kinds"`
	Tools         map[string]toolEvidenceEntry `yaml:"tools"`
}

// Binding resolves tool → kind → shape from declarative config.
type Binding struct {
	mu            sync.RWMutex
	kindShapes    map[string]string
	kindDiets     map[string]TranscriptDiet
	toolKinds     map[string]string
	toolVariants  map[string][]evidenceVariantEntry
	surveyKinds   map[string]struct{}
	mutationKinds map[string]struct{}
}

var (
	defaultBinding    *Binding
	bundledBinding    *Binding
	bundledBindingMu  sync.Once
	bundledBindingErr error
)

// SetBinding installs the process-wide binding loaded at boot.
func SetBinding(b *Binding) {
	defaultBinding = b
}

// LoadBinding parses and validates the bundled evidence-kinds.yaml.
func LoadBinding() (*Binding, error) {
	data, err := config.Read(config.EvidenceKinds)
	if err != nil {
		return nil, err
	}
	var raw evidenceKindsFile
	if err := config.DecodeYAML(data, &raw); err != nil {
		return nil, fmt.Errorf("parse evidence binding: %w", err)
	}
	if err := validateBinding(&raw); err != nil {
		return nil, err
	}
	b := &Binding{
		kindShapes:    make(map[string]string, len(raw.EvidenceKinds)),
		kindDiets:     make(map[string]TranscriptDiet, len(raw.EvidenceKinds)),
		toolKinds:     make(map[string]string),
		toolVariants:  make(map[string][]evidenceVariantEntry),
		surveyKinds:   make(map[string]struct{}),
		mutationKinds: make(map[string]struct{}),
	}
	for kind, entry := range raw.EvidenceKinds {
		b.kindShapes[kind] = strings.TrimSpace(entry.Shape)
		diet, err := parseTranscriptDiet(entry.TranscriptDiet)
		if err != nil {
			return nil, fmt.Errorf("evidence_kinds.%q: %w", kind, err)
		}
		b.kindDiets[kind] = diet
		if entry.Survey {
			b.surveyKinds[kind] = struct{}{}
		}
		if entry.Mutation {
			b.mutationKinds[kind] = struct{}{}
		}
	}
	for tool, entry := range raw.Tools {
		if entry.ProducesEvidence == nil {
			continue
		}
		kind := strings.TrimSpace(entry.ProducesEvidence.Kind)
		if kind == "" {
			continue
		}
		tool = normalizeToolName(tool)
		b.toolKinds[tool] = kind
		b.toolVariants[tool] = append([]evidenceVariantEntry(nil), entry.ProducesEvidence.Variants...)
	}
	return b, nil
}

func validateBinding(raw *evidenceKindsFile) error {
	if raw == nil || len(raw.EvidenceKinds) == 0 {
		return fmt.Errorf("evidence_kinds must not be empty")
	}
	reg := DefaultShapeRegistry()
	for kind, entry := range raw.EvidenceKinds {
		shape := strings.TrimSpace(entry.Shape)
		if shape == "" {
			return fmt.Errorf("evidence_kinds.%q: missing shape", kind)
		}
		if _, ok := reg.Lookup(shape); !ok {
			return fmt.Errorf("evidence_kinds.%q: unknown shape %q", kind, shape)
		}
		if _, err := parseTranscriptDiet(entry.TranscriptDiet); err != nil {
			return fmt.Errorf("evidence_kinds.%q: %w", kind, err)
		}
	}
	for tool, entry := range raw.Tools {
		if entry.ProducesEvidence == nil {
			continue
		}
		kind := strings.TrimSpace(entry.ProducesEvidence.Kind)
		if kind == "" {
			return fmt.Errorf("tools.%q: produces_evidence.kind required", tool)
		}
		if _, ok := raw.EvidenceKinds[kind]; !ok {
			return fmt.Errorf("tools.%q: produces_evidence.kind %q not in evidence_kinds", tool, kind)
		}
		seenArgs := make(map[string]struct{}, len(entry.ProducesEvidence.Variants))
		for i, variant := range entry.ProducesEvidence.Variants {
			arg := strings.TrimSpace(variant.WhenArgPresent)
			variantKind := strings.TrimSpace(variant.Kind)
			if arg == "" {
				return fmt.Errorf("tools.%q: produces_evidence.variants[%d].when_arg_present required", tool, i)
			}
			if _, duplicate := seenArgs[arg]; duplicate {
				return fmt.Errorf("tools.%q: duplicate evidence variant for arg %q", tool, arg)
			}
			seenArgs[arg] = struct{}{}
			if _, ok := raw.EvidenceKinds[variantKind]; !ok {
				return fmt.Errorf("tools.%q: evidence variant kind %q not in evidence_kinds", tool, variantKind)
			}
		}
		_ = tool
	}
	return nil
}

// ToolKind returns the evidence kind for a tool, or "" when undeclared native.
// Undeclared MCP tools receive an auto-derived open kind.
func (b *Binding) ToolKind(toolName string) string {
	if b == nil {
		return ""
	}
	toolName = normalizeToolName(toolName)
	b.mu.RLock()
	kind := b.toolKinds[toolName]
	b.mu.RUnlock()
	if kind != "" {
		return kind
	}
	if ingestion.IsMCPToolName(toolName) {
		return MCPEvidenceKindFromTool(toolName)
	}
	return ""
}

// ToolKindForArgs selects an evidence variant by structured argument presence.
func (b *Binding) ToolKindForArgs(toolName string, args map[string]any) string {
	if b == nil {
		return ""
	}
	toolName = normalizeToolName(toolName)
	b.mu.RLock()
	variants := b.toolVariants[toolName]
	kind := b.toolKinds[toolName]
	b.mu.RUnlock()
	for _, variant := range variants {
		if _, present := args[strings.TrimSpace(variant.WhenArgPresent)]; present {
			return strings.TrimSpace(variant.Kind)
		}
	}
	if kind != "" {
		return kind
	}
	if ingestion.IsMCPToolName(toolName) {
		return MCPEvidenceKindFromTool(toolName)
	}
	return ""
}

// TranscriptDietForTool returns the diet strategy for a tool via its evidence kind.
// Unknown/undeclared tools default to age_default.
func (b *Binding) TranscriptDietForTool(toolName string) TranscriptDiet {
	if b == nil {
		return TranscriptDietAgeDefault
	}
	kind := b.ToolKind(toolName)
	if kind == "" {
		return TranscriptDietAgeDefault
	}
	b.mu.RLock()
	diet, ok := b.kindDiets[kind]
	b.mu.RUnlock()
	if !ok || diet == "" {
		return TranscriptDietAgeDefault
	}
	return diet
}

// ShapeForToolKind returns the shape for tool/kind, defaulting undeclared MCP to opaque.
func (b *Binding) ShapeForToolKind(toolName, kind string) string {
	if b == nil {
		return ""
	}
	kind = strings.TrimSpace(kind)
	b.mu.RLock()
	shape := b.kindShapes[kind]
	b.mu.RUnlock()
	if shape != "" {
		return shape
	}
	if ingestion.IsMCPToolName(toolName) {
		return DefaultUndeclaredMCPShape
	}
	return ""
}

// MCPToolDeclaration binds one discovered tool to an evidence kind.
type MCPToolDeclaration struct {
	ToolName string
	Kind     string
	Shape    string
}

// NewMCPToolDeclaration validates and normalizes one discovery declaration.
func NewMCPToolDeclaration(toolName, kind, shape string) (MCPToolDeclaration, error) {
	toolName = normalizeToolName(toolName)
	kind = strings.TrimSpace(kind)
	shape = strings.TrimSpace(shape)
	if toolName == "" || kind == "" {
		return MCPToolDeclaration{}, fmt.Errorf("mcp tool and kind required")
	}
	if !ingestion.IsMCPToolName(toolName) {
		return MCPToolDeclaration{}, fmt.Errorf("mcp tool %q: not a qualified mcp tool name", toolName)
	}
	if shape == "" {
		shape = DefaultUndeclaredMCPShape
	}
	if _, ok := DefaultShapeRegistry().Lookup(shape); !ok {
		return MCPToolDeclaration{}, fmt.Errorf("mcp tool %q: unknown shape %q", toolName, shape)
	}
	return MCPToolDeclaration{ToolName: toolName, Kind: kind, Shape: shape}, nil
}

func (b *Binding) registerMCPToolDeclarationLocked(decl MCPToolDeclaration) {
	kind := NamespaceMCPServerKind(decl.Kind)
	b.kindShapes[kind] = decl.Shape
	if _, ok := b.kindDiets[kind]; !ok {
		b.kindDiets[kind] = b.inheritedDietLocked(decl.Kind)
	}
	b.toolKinds[decl.ToolName] = kind
	delete(b.toolVariants, decl.ToolName)
}

// inheritedDietLocked returns the host diet for a server-declared kind.
func (b *Binding) inheritedDietLocked(declaredKind string) TranscriptDiet {
	if diet, ok := b.kindDiets[declaredKind]; ok && diet != "" {
		return diet
	}
	return TranscriptDietAgeDefault
}

// ReplaceMCPToolDeclarations publishes one discovery generation.
func (b *Binding) ReplaceMCPToolDeclarations(declarations []MCPToolDeclaration) error {
	if b == nil {
		return fmt.Errorf("evidence binding not initialized")
	}
	normalized := make([]MCPToolDeclaration, 0, len(declarations))
	seen := make(map[string]struct{}, len(declarations))
	for _, candidate := range declarations {
		decl, err := NewMCPToolDeclaration(candidate.ToolName, candidate.Kind, candidate.Shape)
		if err != nil {
			return err
		}
		if _, ok := seen[decl.ToolName]; ok {
			return fmt.Errorf("duplicate mcp evidence declaration %q", decl.ToolName)
		}
		seen[decl.ToolName] = struct{}{}
		normalized = append(normalized, decl)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.unregisterMCPToolDeclarationsLocked()
	for _, decl := range normalized {
		b.registerMCPToolDeclarationLocked(decl)
	}
	return nil
}

func (b *Binding) unregisterMCPToolDeclarationsLocked() {
	for tool, kind := range b.toolKinds {
		if ingestion.IsMCPToolName(tool) && IsMCPServerKind(kind) {
			delete(b.toolKinds, tool)
			delete(b.toolVariants, tool)
		}
	}
	for kind := range b.kindShapes {
		if IsMCPServerKind(kind) {
			delete(b.kindShapes, kind)
			delete(b.kindDiets, kind)
		}
	}
}

// KindShape returns the shape for kind, or "" when unknown.
func (b *Binding) KindShape(kind string) string {
	if b == nil {
		return ""
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.kindShapes[strings.TrimSpace(kind)]
}

// IsSurveyKind reports whether kind is declared as survey evidence in config.
func (b *Binding) IsSurveyKind(kind string) bool {
	if b == nil {
		return false
	}
	kind = strings.TrimSpace(kind)
	b.mu.RLock()
	defer b.mu.RUnlock()
	_, ok := b.surveyKinds[kind]
	return ok
}

// IsMutationKind reports whether kind is declared as mutation evidence in config.
func (b *Binding) IsMutationKind(kind string) bool {
	if b == nil {
		return false
	}
	kind = strings.TrimSpace(kind)
	b.mu.RLock()
	defer b.mu.RUnlock()
	_, ok := b.mutationKinds[kind]
	return ok
}

// MutationKinds returns mutation kind ids sorted for stable iteration.
func (b *Binding) MutationKinds() []string {
	if b == nil {
		return nil
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]string, 0, len(b.mutationKinds))
	for kind := range b.mutationKinds {
		out = append(out, kind)
	}
	sort.Strings(out)
	return out
}

// SurveyKinds returns survey kind ids sorted for stable iteration.
func (b *Binding) SurveyKinds() []string {
	if b == nil {
		return nil
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]string, 0, len(b.surveyKinds))
	for kind := range b.surveyKinds {
		out = append(out, kind)
	}
	sort.Strings(out)
	return out
}

// DeclaredToolNames returns the tool names declared as evidence producers, sorted.
func (b *Binding) DeclaredToolNames() []string {
	if b == nil {
		return nil
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]string, 0, len(b.toolKinds))
	for tool := range b.toolKinds {
		out = append(out, tool)
	}
	sort.Strings(out)
	return out
}

// KindShapeMap returns a copy of kind → shape for contract tests.
func (b *Binding) KindShapeMap() map[string]string {
	if b == nil {
		return nil
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make(map[string]string, len(b.kindShapes))
	for kind, shape := range b.kindShapes {
		out[kind] = shape
	}
	return out
}

func normalizeToolName(toolName string) string {
	return strings.TrimSpace(strings.ToLower(toolName))
}

// ActiveBinding returns the process-wide evidence binding.
func ActiveBinding() *Binding {
	if defaultBinding != nil {
		return defaultBinding
	}
	bundledBindingMu.Do(func() {
		bundledBinding, bundledBindingErr = LoadBinding()
	})
	if bundledBindingErr != nil {
		panic(bundledBindingErr)
	}
	return bundledBinding
}
