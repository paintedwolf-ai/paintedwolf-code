package tools

import (
	"context"
	"time"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
)

// OffersSourceHistory reports whether the calling profile holds source_history,
// so rejection copy names it only to an agent that can call it.
func OffersSourceHistory(ctx context.Context, boundary *sandbox.Boundary, tc ToolContext) bool {
	return boundary != nil && boundary.AssertToolAllowed(ctx, tc.ProfileID(), "source_history", tc.Turn.ToolAccess) == nil
}

// SourceHistory supplies independent read services to a tool invocation.
type SourceHistory struct {
	Files      SourceFileHistory
	Comparison SourceComparison
	Git        SourceGitHistory
	Authorship sourceledger.AuthorshipReader
	// Windows is optional; without it nothing waits for a window to settle.
	Windows SourceWindowReader
	// Tree is optional; without it a worker's overview says it could not compare
	// the branch with its baseline.
	Tree SourceTreeReader
}

// SourceTree names a worker branch and the manifest sealed when its leg began.
type SourceTree struct {
	BaselinePath, BranchRoot string
	Roots                    []projectroot.RootRef
}

// SourceTreeReader compares a worker branch with its baseline, as promotion will.
type SourceTreeReader interface {
	TreeChanges(context.Context, SourceTree) ([]workspacebaseline.Change, error)
	TreePathChange(ctx context.Context, tree SourceTree, path string) (workspacebaseline.PathComparison, error)
}

// SourceWindowReader lists a session's command windows that have not settled.
type SourceWindowReader interface {
	OpenSessionWindows(projectID, sessionID string) []sourceledger.SessionWindow
}

// WindowSettleBudget bounds how long a read of the session's work waits for its
// closing windows; what is still settling after that is reported, not awaited.
const WindowSettleBudget = 3 * time.Second

// AwaitClosingWindows waits for windows whose process has ended, never for a
// running one, until they settle or the budget passes.
func AwaitClosingWindows(ctx context.Context, windows []sourceledger.SessionWindow, budget time.Duration) {
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	for _, window := range windows {
		if !window.Closing {
			continue
		}
		select {
		case <-window.Settled:
		case <-ctx.Done():
			return
		}
	}
}

type SourceFileHistory interface {
	ResolveHead(context.Context, string, sourcebranch.ID, string, string) (sourceledger.BranchHead, error)
	LatestFileEffect(context.Context, string, sourcebranch.ID, string) (sourceledger.Effect, bool, error)
	SessionActivityFloor(context.Context, string, string) (int64, bool, error)
	QueryFileEffects(context.Context, string, sourcebranch.ID, string, int64, int64, int) (sourceledger.FileEffectsResult, error)
	QueryAttribution(context.Context, string, sourcebranch.ID, string, string) (sourceledger.AttributionResult, error)
	ReadRestorableVersion(context.Context, string, string) (sourceledger.RestorableVersion, error)
	QueryFileVersions(context.Context, string, string, int, int64) (sourceledger.FileVersionsResult, error)
	ResolveHeadByFile(context.Context, string, sourcebranch.ID, string) (sourceledger.BranchHead, error)
	ResolveDeletedPath(context.Context, string, sourcebranch.ID, string, string) (sourceledger.DeletedPath, error)
	EffectsBetween(context.Context, string, int64, int64, int) ([]sourceledger.Effect, error)
	EffectsBetweenOnBranch(context.Context, string, map[string]sourcebranch.ID, int64, int64, int) ([]sourceledger.Effect, error)
	OldestFileEffectAfterOnBranch(context.Context, string, sourcebranch.ID, string, int64) (sourceledger.FileEffectBounds, bool, error)
	OldestPathEffectAfterOnBranch(context.Context, string, sourcebranch.ID, string, string, int64) (sourceledger.FileEffectBounds, bool, error)
}

type SourceComparison interface {
	CompareVersions(context.Context, string, string) (sourceledger.Comparison, error)
	CompareVersionPair(context.Context, string, string, string) (sourceledger.Comparison, error)
}

type SourceGitHistory interface {
	GitTransitionsByIDs(context.Context, []string) (map[string]sourceledger.GitTransition, error)
	GitTransitionsBetween(context.Context, string, int64, int64, int) ([]sourceledger.GitTransition, error)
}

type SourceGitMutations interface {
	GitMutationContext(context.Context, string, []sourceledger.RootSpec, sourceledger.Contributor) context.Context
}
