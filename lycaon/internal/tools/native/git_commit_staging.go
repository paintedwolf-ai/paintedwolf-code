package native

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/tools"
)

// maxReportedExcludedPaths limits the sample while preserving the full excluded count.
const maxReportedExcludedPaths = 20

// resolveCommitStaging uses explicit paths or the session's ledger-recorded authorship.
func resolveCommitStaging(
	ctx context.Context,
	args map[string]any,
	gm git.GitManager,
	tctx tools.ToolContext,
	cwd string,
) (git.CommitStaging, error) {
	if raw, ok := args["paths"].([]any); ok && len(raw) > 0 {
		paths, err := parseBoundedPaths(args, git.DefaultGitCommitMaxPaths, commitBulkDenied)
		if err != nil {
			return git.CommitStaging{}, err
		}
		return git.CommitStaging{Paths: paths}, nil
	}
	if gm == nil {
		return git.CommitStaging{}, fmt.Errorf("git manager required")
	}
	changed, err := gm.ChangedPaths(ctx, cwd)
	if err != nil {
		return git.CommitStaging{}, err
	}
	authored, err := sessionAuthoredPaths(ctx, tctx)
	if err != nil {
		return git.CommitStaging{}, err
	}
	if len(authored) == 0 {
		return git.CommitStaging{}, &toolrejection.ToolReject{
			Code: "GIT_COMMIT_NO_SESSION_AUTHORSHIP",
			Data: map[string]any{"git_changed_path_count": len(changed)},
		}
	}
	staging := splitAuthored(authored, changed)
	if len(staging.Paths) == 0 {
		return git.CommitStaging{}, fmt.Errorf("no changes to commit")
	}
	if len(staging.Paths) > git.DefaultGitCommitMaxPaths {
		return git.CommitStaging{}, commitBulkDenied(git.DefaultGitCommitMaxPaths, len(staging.Paths))
	}
	return staging, nil
}

func commitBulkDenied(max, got int) error {
	return &toolrejection.ToolReject{
		Code: "GIT_COMMIT_BULK_DENIED",
		Data: map[string]any{"max_paths": max, "requested": got},
	}
}

// sessionAuthoredPaths returns no paths when the ledger cannot establish authorship.
func sessionAuthoredPaths(ctx context.Context, tctx tools.ToolContext) ([]string, error) {
	reader := tctx.Source.History.Authorship
	if reader == nil {
		return nil, nil
	}
	return reader.SessionAuthoredPaths(ctx, tctx.Identity.ProjectID, tctx.Identity.SessionID, tctx.Source.ActiveRootID)
}

// splitAuthored keeps authored paths that still differ from HEAD, in authored
// order, and reports the rest of the dirty tree as untouched by this commit.
func splitAuthored(authored, changed []string) git.CommitStaging {
	changedSet := make(map[string]struct{}, len(changed))
	for _, p := range changed {
		changedSet[filepath.ToSlash(p)] = struct{}{}
	}
	authoredSet := make(map[string]struct{}, len(authored))
	var out git.CommitStaging
	for _, p := range authored {
		slash := filepath.ToSlash(p)
		if _, dup := authoredSet[slash]; dup {
			continue
		}
		authoredSet[slash] = struct{}{}
		if _, changedHere := changedSet[slash]; changedHere {
			out.Paths = append(out.Paths, slash)
		}
	}
	for _, p := range changed {
		slash := filepath.ToSlash(p)
		if _, mine := authoredSet[slash]; mine {
			continue
		}
		out.ExcludedCount++
		if len(out.Excluded) < maxReportedExcludedPaths {
			out.Excluded = append(out.Excluded, slash)
		}
	}
	return out
}
