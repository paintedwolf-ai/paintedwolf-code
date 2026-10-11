package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// StubRegistry is a tool registry with canned handlers for mock LLM tests.
type StubRegistry struct {
	definitions map[string]Definition
	fail        map[string]error
}

// NewStubRegistry returns a registry with read/write stubs.
func NewStubRegistry() *StubRegistry {
	r := &StubRegistry{
		definitions: make(map[string]Definition),
		fail:        make(map[string]error),
	}
	_ = r.Register("read", func(_ context.Context, args map[string]any, _ ToolContext) (string, error) {
		path, _ := args["path"].(string)
		if path == "" {
			path = "unknown"
		}
		return fmt.Sprintf("contents of %s", path), nil
	})
	_ = r.Register("write", func(_ context.Context, args map[string]any, _ ToolContext) (string, error) {
		path, _ := args["path"].(string)
		return fmt.Sprintf("wrote %s", path), nil
	})
	return r
}

// Register adds or replaces a tool handler.
func (r *StubRegistry) Register(name string, handler ToolHandler) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("tool name is required")
	}
	contract, ok := toolcontract.Lookup(name)
	if !ok {
		return fmt.Errorf("tool %q has no invocation contract", name)
	}
	return r.RegisterDefinition(Definition{
		Meta:     ToolMeta{Name: name, Description: "stub tool", ArgsSchema: map[string]any{"type": "object"}},
		Contract: contract, Handler: handler,
	})
}

// RegisterDefinition publishes a stub definition.
func (r *StubRegistry) RegisterDefinition(def Definition) error {
	def, err := validateDefinition(def)
	if err != nil {
		return err
	}
	r.definitions[def.Meta.Name] = def
	return nil
}

// Definition returns one stub definition.
func (r *StubRegistry) Definition(name string) (Definition, bool) {
	def, ok := r.definitions[name]
	return def, ok
}

// SetFail configures a tool to return a fixed error (tests).
func (r *StubRegistry) SetFail(name string, err error) {
	r.fail[name] = err
}

// Run executes a registered tool.
func (r *StubRegistry) Run(ctx context.Context, name string, args map[string]any, tctx ToolContext) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err, ok := r.fail[name]; ok {
		return "", err
	}
	def, ok := r.definitions[name]
	if !ok {
		return "", fmt.Errorf("unknown tool: %s", name)
	}
	if tctx.Effects.Out != nil {
		tctx.Effects.Out.OwnerInvoked = true
	}
	return def.Handler(ctx, args, tctx)
}

// List returns registered tool metadata.
func (r *StubRegistry) List() []ToolMeta {
	names := make([]string, 0, len(r.definitions))
	for name := range r.definitions {
		names = append(names, name)
	}
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			if names[j] < names[i] {
				names[i], names[j] = names[j], names[i]
			}
		}
	}
	out := make([]ToolMeta, 0, len(names))
	for _, name := range names {
		out = append(out, r.definitions[name].Meta)
	}
	return out
}
