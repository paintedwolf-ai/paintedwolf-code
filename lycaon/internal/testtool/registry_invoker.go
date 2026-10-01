// Package testtool provides explicit tool-surface adapters for tests.
package testtool

import (
	"context"

	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/tools"
)

// RegistryInvoker exposes exactly the tools executable by Registry to prompt
// construction. It keeps mock LLM fixtures subject to the same advertised
// capability surface as production providers.
type RegistryInvoker struct {
	Registry tools.ToolRegistry
}

func (i RegistryInvoker) Invoke(ctx context.Context, name string, args map[string]any, toolCtx tools.ToolContext) (string, error) {
	return i.Registry.Run(ctx, name, args, toolCtx)
}

func (i RegistryInvoker) List(context.Context, platform.ToolFilter) ([]tools.ToolMeta, error) {
	return i.Registry.List(), nil
}
