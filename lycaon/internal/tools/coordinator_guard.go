package tools

import (
	"context"

	"github.com/lycaon/lycaon/internal/platform"
)

type ToolProfileLister interface {
	List(ctx context.Context, filter platform.ToolFilter) ([]ToolMeta, error)
}

func ListToolsForProfile(ctx context.Context, invoker ToolInvoker, filter platform.ToolFilter) []ToolMeta {
	lister, ok := invoker.(ToolProfileLister)
	if !ok {
		return nil
	}
	tools, err := lister.List(ctx, filter)
	if err != nil {
		return nil
	}
	return tools
}
