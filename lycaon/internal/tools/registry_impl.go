package tools

import (
	"github.com/lycaon/lycaon/internal/platform"

	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/jsonvalue"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolschema"
)

// DefaultRegistry is a thread-safe ToolRegistry backed by registered handlers.
type DefaultRegistry struct {
	mu          sync.RWMutex
	dynamicMu   sync.RWMutex
	definitions map[string]Definition
	catalog     map[string]ToolMeta
}

// NewDefaultRegistry creates an empty registry.
func NewDefaultRegistry() *DefaultRegistry {
	return &DefaultRegistry{
		definitions: make(map[string]Definition),
	}
}

// NewCatalogRegistry creates a registry backed by the native metadata catalog.
func NewCatalogRegistry(catalog *toolschema.Config) (*DefaultRegistry, error) {
	if catalog == nil {
		return nil, fmt.Errorf("tool metadata catalog is required")
	}
	metadata := make(map[string]ToolMeta, len(catalog.Tools))
	for name := range catalog.Tools {
		declared, ok := catalog.ToolMeta(name)
		if !ok {
			continue
		}
		metadata[name] = cloneToolMeta(ToolMeta{
			Name: declared.Name, Description: declared.Description, Tags: declared.Tags, ArgsSchema: declared.ArgsSchema,
		})
	}
	return &DefaultRegistry{
		definitions: make(map[string]Definition),
		catalog:     metadata,
	}, nil
}

// Register adds or replaces a tool handler.
func (r *DefaultRegistry) Register(name string, handler ToolHandler) error {
	contract, ok := toolcontract.Lookup(name)
	if !ok {
		return fmt.Errorf("tool %q has no invocation contract", name)
	}
	meta := ToolMeta{Name: name, Description: name + " tool", ArgsSchema: map[string]any{"type": "object"}}
	if r.catalog != nil {
		declared, found := r.catalog[name]
		if !found {
			return fmt.Errorf("tool %q has no metadata declaration", name)
		}
		meta = declared
	}
	return r.RegisterDefinition(Definition{
		Meta:     meta,
		Contract: contract, Handler: handler,
	})
}

// RegisterDerived publishes metadata computed by a runtime source.
func (r *DefaultRegistry) RegisterDerived(meta ToolMeta, handler ToolHandler) error {
	if r.catalog != nil {
		if _, declared := r.catalog[meta.Name]; declared {
			return fmt.Errorf("tool %q metadata is catalog-defined", meta.Name)
		}
	}
	contract, ok := toolcontract.Lookup(meta.Name)
	if !ok {
		return fmt.Errorf("tool %q has no invocation contract", meta.Name)
	}
	return r.RegisterDefinition(Definition{Meta: meta, Contract: contract, Handler: handler})
}

// RegisterDefinition publishes handler, metadata, and contract together.
func (r *DefaultRegistry) RegisterDefinition(def Definition) error {
	def, err := validateDefinition(def)
	if err != nil {
		return err
	}
	name := def.Meta.Name
	def.Meta = cloneToolMeta(def.Meta)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.definitions[name] = def
	return nil
}

func validateDefinition(def Definition) (Definition, error) {
	name := strings.TrimSpace(def.Meta.Name)
	if name == "" {
		return Definition{}, fmt.Errorf("tool name is required")
	}
	if def.Meta.Name != name || name != strings.ToLower(name) {
		return Definition{}, fmt.Errorf("tool name %q is not canonical", def.Meta.Name)
	}
	if def.Handler == nil {
		return Definition{}, fmt.Errorf("tool %q handler is required", name)
	}
	owner := strings.TrimSpace(def.Contract.Owner)
	if owner == "" {
		return Definition{}, fmt.Errorf("tool %q subsystem owner is required", name)
	}
	if def.Contract.Owner != owner {
		return Definition{}, fmt.Errorf("tool %q subsystem owner is not canonical", name)
	}
	return def, nil
}

// Meta returns metadata for a registered tool.
func (r *DefaultRegistry) Meta(name string) (ToolMeta, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	def, ok := r.definitions[name]
	return cloneToolMeta(def.Meta), ok
}

// Definition returns one immutable registry snapshot.
func (r *DefaultRegistry) Definition(name string) (Definition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	def, ok := r.definitions[name]
	def.Meta = cloneToolMeta(def.Meta)
	return def, ok
}

// ReplacePrefix publishes definitions and source state as one generation.
func (r *DefaultRegistry) ReplacePrefix(prefix string, definitions []Definition, publishState func() error) error {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return fmt.Errorf("tool prefix is required")
	}
	next := make(map[string]Definition, len(definitions))
	for _, candidate := range definitions {
		def, err := validateDefinition(candidate)
		if err != nil {
			return err
		}
		name := def.Meta.Name
		if !strings.HasPrefix(name, prefix) {
			return fmt.Errorf("tool %q is outside prefix %q", name, prefix)
		}
		if _, exists := next[name]; exists {
			return fmt.Errorf("duplicate tool definition %q", name)
		}
		def.Meta = cloneToolMeta(def.Meta)
		next[name] = def
	}
	r.dynamicMu.Lock()
	defer r.dynamicMu.Unlock()
	if publishState != nil {
		if err := publishState(); err != nil {
			return err
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for name := range r.definitions {
		if strings.HasPrefix(name, prefix) {
			delete(r.definitions, name)
		}
	}
	for name, def := range next {
		r.definitions[name] = def
	}
	return nil
}

// LeasePrefix prevents a matching dynamic generation from changing until release.
func (r *DefaultRegistry) LeasePrefix(name, prefix string) func() {
	if r == nil || !strings.HasPrefix(strings.TrimSpace(name), strings.TrimSpace(prefix)) {
		return func() {}
	}
	r.dynamicMu.RLock()
	return r.dynamicMu.RUnlock
}

// Run executes a registered tool.
func (r *DefaultRegistry) Run(ctx context.Context, name string, args map[string]any, tctx ToolContext) (string, error) {
	r.mu.RLock()
	def, ok := r.definitions[name]
	r.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("unknown tool: %s", name)
	}
	if tctx.Effects.Out != nil {
		tctx.Effects.Out.OwnerInvoked = true
	}
	return def.Handler(ctx, args, tctx)
}

// List returns registered tool metadata sorted by name.
func (r *DefaultRegistry) List() []ToolMeta {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.definitions))
	for name := range r.definitions {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]ToolMeta, 0, len(names))
	for _, name := range names {
		out = append(out, cloneToolMeta(r.definitions[name].Meta))
	}
	return out
}

func cloneToolMeta(meta ToolMeta) ToolMeta {
	meta.ArgsSchema = jsonvalue.CloneMap(meta.ArgsSchema)
	meta.Tags = append([]string(nil), meta.Tags...)
	return meta
}

// ListVisiblePolicy evaluates whether a tool belongs in the LLM schema for a profile.
// Approval rules with effect "ask" still appear on the wire; checkpoints run at invoke.
type ListVisiblePolicy interface {
	platform.PolicyEngine
	EvaluateForList(ctx context.Context, eval platform.PolicyContext) (*platform.PolicyDecision, error)
}

// EvaluateListVisible returns profile allowlist visibility for tool listing.
func EvaluateListVisible(ctx context.Context, policy platform.PolicyEngine, eval platform.PolicyContext) (*platform.PolicyDecision, error) {
	if policy == nil {
		return &platform.PolicyDecision{Allowed: true}, nil
	}
	if lister, ok := policy.(ListVisiblePolicy); ok {
		return lister.EvaluateForList(ctx, eval)
	}
	return policy.Evaluate(ctx, eval)
}
