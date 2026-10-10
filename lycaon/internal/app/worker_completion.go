package app

import (
	"context"

	"github.com/lycaon/lycaon/internal/tools"
	workertools "github.com/lycaon/lycaon/internal/tools/native/workercontrol"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func decodeCompleteLeg(b *serveBuilder) workertools.CompleteLegDecoder {
	return func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (workertools.CompleteLegRecord, error) {
		return b.delegations.DecodeCompleteLeg(ctx, args, tctx, func(c context.Context, task *wire.WorkerTask, review *wire.CoverageReview) error {
			return b.workflows.Manager.Coverage.ValidateCoverageCompletion(c, task, review)
		})
	}
}
