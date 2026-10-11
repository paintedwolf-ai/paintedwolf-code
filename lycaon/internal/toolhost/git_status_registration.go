package toolhost

import (
	"context"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/tools"
)

func registerGitStatusTool(reg *tools.DefaultRegistry, deps buildDeps) error {
	if !deps.nativeConfig.HasTool("git_status") || deps.git == nil {
		return nil
	}
	return reg.Register("git_status", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		page := gitStatusPage(args)
		cwd := tctx.ActiveRootPath()
		if page.Ignored {
			return gitStatusIgnoredOutput(ctx, deps.git, cwd, page, tctx)
		}
		// Fresh status includes writes from the current turn.
		var status *git.GitStatus
		var err error
		if deps.statusCache != nil {
			if cache := deps.statusCache.Load(); cache != nil {
				cached, e := cache.GetOrLoad(ctx, cwd, true)
				status, err = cached.Status, e
				if err == nil && status != nil {
					if subjects, subErr := cache.RecentSubjects(ctx, cwd, true, 5); subErr == nil {
						status.RecentCommits = subjects
					}
				}
				return gitStatusOutput(status, page, err, tctx)
			}
		}
		status, err = deps.git.Status(ctx, cwd)
		if err != nil {
			return gitStatusOutput(status, page, err, tctx)
		}
		if commits, logErr := deps.git.Log(ctx, cwd, git.GitLogOpts{Limit: 5}); logErr == nil && status != nil {
			for _, c := range commits {
				status.RecentCommits = append(status.RecentCommits, c.Subject)
			}
		}
		return gitStatusOutput(status, page, nil, tctx)
	})
}
