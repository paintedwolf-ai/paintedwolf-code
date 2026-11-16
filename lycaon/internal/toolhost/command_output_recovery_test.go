package toolhost

import (
	"context"
	"io"

	"github.com/lycaon/lycaon/internal/tools/projectpaths"

	"github.com/lycaon/lycaon/internal/tools"
)

type outputViewOpen func(context.Context, tools.ToolContext, projectpaths.Resolved) (io.ReadCloser, error)

func (f outputViewOpen) OpenOutputView(ctx context.Context, tc tools.ToolContext, path projectpaths.Resolved) (io.ReadCloser, error) {
	return f(ctx, tc, path)
}
