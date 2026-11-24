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

// RedirectRootReview asks for a write root covering an output redirect.
type RedirectRootReview func(ctx context.Context, tc tools.ToolContext, operator, path, root string) (tools.ToolContext, error)

// RedirectReadReview asks for read access to an input redirect or sourced script.
type RedirectReadReview func(ctx context.Context, tc tools.ToolContext, operator, path string) (tools.ToolContext, error)

// bindOutput carries the reviewed location into both the binding and the revision capture.
func (p *Publication) bindOutput(ctx context.Context, destination commandprogram.OutputDestination) (projectpaths.BoundPath, tools.ToolContext, error) {
	path := destination.Target.Value
	tc := p.Tools
	bound, err := p.Program.Paths.Bind(ctx, path, true)
	if p.ReviewRoot == nil || !p.reviewable(err, path, true) {
		return bound, tc, err
	}
	root, ok := redirectWriteRoot(path, destination.RequestedWriteRoot)
	if !ok {
		return bound, tc, err
	}
	tc, err = p.ReviewRoot(ctx, tc, destination.Redirect.Operator, path, root)
	if err != nil {
		return bound, tc, err
	}
	return p.rebind(ctx, tc, path, true)
}

// bindInput carries reviewed read access into both the binding and the stream open.
func (p *Publication) bindInput(ctx context.Context, operator, path string) (projectpaths.BoundPath, tools.ToolContext, error) {
	tc := p.Tools
	bound, err := p.Program.Paths.Bind(ctx, path, false)
	if p.ReviewRead == nil || !p.reviewable(err, path, false) {
		return bound, tc, err
	}
	tc, err = p.ReviewRead(ctx, tc, operator, path)
	if err != nil {
		return bound, tc, err
	}
	return p.rebind(ctx, tc, path, false)
}

// reviewable reports a refused absolute path outside the attached folders that
// an approval could open. Worker branches and engine state never ask.
func (p *Publication) reviewable(err error, path string, write bool) bool {
	reject := toolrejection.AsToolReject(err)
	if reject == nil || reject.Code != "SURVEY_PATH_ESCAPE" || !filepath.IsAbs(path) {
		return false
	}
	host := p.Tools.Host
	return p.Tools.Source.WorkerBranchRoot == "" &&
		!confine.ControlPlanePathDenied(path, write, host.SessionScratchDir, host.SessionOutputDir)
}

func (p *Publication) rebind(ctx context.Context, tc tools.ToolContext, path string, write bool) (projectpaths.BoundPath, tools.ToolContext, error) {
	frame, err := p.Program.Paths.Rebind(ctx, tc)
	if err != nil {
		return projectpaths.BoundPath{}, tc, err
	}
	bound, err := frame.Bind(ctx, path, write)
	return bound, tc, err
}

// redirectWriteRoot is the root an outside redirect asks for: exact files for
// credential stores and key material, the temp location for shared temp, and
// otherwise the enclosing repository or folder. A declared write_root that
// covers the path replaces it when a grant there could be installed. A path no
// grant could cover is not reviewed.
func redirectWriteRoot(path, requested string) (string, bool) {
	subject := confine.ClassifyBlockedWrite(path)
	switch subject.Kind {
	case confine.WriteSubjectCredentialStore:
		return subject.GrantPath, true
	case confine.WriteSubjectKeyMaterial:
		return path, true
	case confine.WriteSubjectOrdinary:
	default:
		return "", false
	}
	declared := filepath.IsAbs(requested) && confine.PathWithinRoots(path, []string{requested})
	if temp := confine.SharedTempLocation(path); temp != "" {
		if declared && confine.PathWithinRoots(requested, []string{temp}) {
			return requested, true
		}
		return temp, true
	}
	if declared && grantable(requested) {
		return requested, true
	}
	root := confine.FloorRecovery{Grant: confine.GrantRepositoryTree}.GrantPath(path)
	return root, grantable(root)
}

func grantable(root string) bool {
	refused, _ := confine.GrantedWriteRootRefused(root)
	return !refused
}
