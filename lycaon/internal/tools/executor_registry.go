package tools

import (
	"context"
	"fmt"
)

// ToolInvoker executes a tool after policy checks.
type ToolInvoker interface {
	Invoke(ctx context.Context, qualifiedName string, args map[string]any, tctx ToolContext) (string, error)
}

// ExecutorRegistry adapts ToolInvoker to ToolRegistry for session dispatch.
type ExecutorRegistry struct {
	Invoker ToolInvoker
	Inner   *DefaultRegistry
}

// NewExecutorRegistry wraps an invoker for session manager dispatch.
func NewExecutorRegistry(invoker ToolInvoker, inner *DefaultRegistry) *ExecutorRegistry {
	return &ExecutorRegistry{Invoker: invoker, Inner: inner}
}

// Register delegates to the inner registry (tests/extensions).
func (r *ExecutorRegistry) Register(name string, handler ToolHandler) error {
	if r.Inner == nil {
		return fmt.Errorf("inner registry not configured")
	}
	return r.Inner.Register(name, handler)
}

// RegisterDefinition delegates to the inner registry.
func (r *ExecutorRegistry) RegisterDefinition(def Definition) error {
	if r.Inner == nil {
		return fmt.Errorf("inner registry not configured")
	}
	return r.Inner.RegisterDefinition(def)
}

// Definition returns the definition used by the executor.
func (r *ExecutorRegistry) Definition(name string) (Definition, bool) {
	if r.Inner == nil {
		return Definition{}, false
	}
	return r.Inner.Definition(name)
}

// Run invokes through the policy-aware executor.
func (r *ExecutorRegistry) Run(ctx context.Context, name string, args map[string]any, tctx ToolContext) (string, error) {
	return r.Invoker.Invoke(ctx, name, args, tctx)
}

// List returns tools from the inner registry.
func (r *ExecutorRegistry) List() []ToolMeta {
	if r.Inner == nil {
		return nil
	}
	return r.Inner.List()
}
