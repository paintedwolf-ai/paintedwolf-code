package contractfixture

import (
	"context"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/tools"
)

type HiddenToolRegistry struct {
	tools.ToolRegistry
	Hidden map[string]struct{}
}

func (r HiddenToolRegistry) Definition(name string) (tools.Definition, bool) {
	if _, hidden := r.Hidden[name]; hidden {
		return tools.Definition{}, false
	}
	return r.ToolRegistry.Definition(name)
}

func (r HiddenToolRegistry) Run(ctx context.Context, name string, args map[string]any, tctx tools.ToolContext) (string, error) {
	if _, hidden := r.Hidden[name]; hidden {
		return "", fmt.Errorf("unknown tool: %s", name)
	}
	return r.ToolRegistry.Run(ctx, name, args, tctx)
}

func (r HiddenToolRegistry) List() []tools.ToolMeta {
	listed := r.ToolRegistry.List()
	visible := listed[:0]
	for _, meta := range listed {
		if _, hidden := r.Hidden[meta.Name]; !hidden {
			visible = append(visible, meta)
		}
	}
	return visible
}

// wireTestBindingRegistry isolates tests that replace the process-wide registry.

func WireTestBindingRegistry(t *testing.T) {
	t.Helper()
	reg, err := anchor.LoadRegistryFromConfigRoot()
	if err != nil {
		t.Fatalf("load Binding registry: %v", err)
	}
	previous := anchor.DefaultRegistry()
	anchor.SetDefaultRegistry(reg)
	t.Cleanup(func() { anchor.SetDefaultRegistry(previous) })
}

// Stop recurring work before fixture storage closes.

func WithoutTools(reg tools.ToolRegistry, names ...string) tools.ToolRegistry {
	hidden := make(map[string]struct{}, len(names))
	for _, name := range names {
		hidden[name] = struct{}{}
	}
	return HiddenToolRegistry{ToolRegistry: reg, Hidden: hidden}
}
