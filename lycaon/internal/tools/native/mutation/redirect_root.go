package mutation

import (
	"context"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

type RedirectRootReview func(context.Context, tools.ToolContext, string, string, string) (tools.ToolContext, error)

// bindOutput carries the reviewed location into both the binding and the revision capture.
func (p *Publication) bindOutput(ctx context.Context, destination commandprogram.OutputDestination) (projectpaths.BoundPath, tools.ToolContext, error) {
	path := destination.Target.Value
	tc := p.Tools
	bound, err := p.Program.Paths.Bind(ctx, path, true)
	reject := toolrejection.AsToolReject(err)
	if reject == nil || reject.Code != "SURVEY_PATH_ESCAPE" || p.ReviewRoot == nil ||
		!filepath.IsAbs(path) || tc.Source.WorkerBranchRoot != "" {
		return bound, tc, err
	}
	root := confine.SharedTempLocation(path)
	if root == "" || confine.ClassifyBlockedWrite(path).Kind != confine.WriteSubjectOrdinary {
		return bound, tc, err
	}
	tc, err = p.ReviewRoot(ctx, tc, destination.Redirect.Operator, path, root)
	if err != nil {
		return bound, tc, err
	}
	frame, err := p.Program.Paths.Rebind(ctx, tc)
	if err != nil {
		return bound, tc, err
	}
	bound, err = frame.Bind(ctx, path, true)
	return bound, tc, err
}
