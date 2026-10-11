package tools

import (
	"context"

	"github.com/lycaon/lycaon/internal/platform"
)

type ToolProfileLister interface {
	List(ctx context.Context, filter platform.ToolFilter) ([]ToolMeta, error)
}

func ListToolsForProfile(ctx context.Context, lister ToolProfileLister, filter platform.ToolFilter) []ToolMeta {
	if lister == nil {
		return nil
	}
	metas, err := lister.List(ctx, filter)
	if err != nil {
		return nil
	}
	return metas
}
