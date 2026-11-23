package gitcmd

import (
	"context"
	"sync"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
)

// treeForms identifies forms that can reach disk-backed worktree operations.
var treeForms = map[string]bool{
	"git status": true, "git diff": true, "git blame": true, "git ls-files": true, "git grep": true, "git describe": true,
	"git log": false, "git show": false, "git branch": false, "git tag": false, "git rev-parse": false,
	"git rev-list": false, "git merge-base": false, "git remote": false, "git config": false, "git stash list": false,
	"git worktree list": false, "git reflog": false, "git shortlog": false, "git ls-tree": false, "git cat-file": false,
	"git show-ref": false, "git for-each-ref": false,

	"git add": true, "git commit": true, "git restore": true, "git checkout": true, "git switch": true,
	"git merge": true, "git cherry-pick": true, "git revert": true, "git rebase": true, "git stash": true,
	"git reset": true, "git clean": true, "git worktree": true, "git pull": true, "git apply": false,
	"git rm": true, "git mv": true,
	"git bisect": true, "git bisect start": true, "git bisect skip": true, "git bisect run": true, "git bisect reset": true,
	"git init": false, "git fetch": false, "git push": false, "git ls-remote": false, "git clone": false,
	"git bisect log": false, "git bisect terms": false, "git bisect visualize": false, "git bisect view": false,
}

func (p Prepared) bindLaunch(ctx context.Context, tc tools.ToolContext, services []confine.HostService) (exec.LaunchPlan, func(context.Context, tools.AgentLaunchOutcome), error) {
	return p.owners.Launch(ctx, tc, "git", p.commandLine(), services)
}

func (p Prepared) readsWorktree() bool {
	switch p.Command {
	case "git diff":
		return !has(p.Argv, "--cached")
	case "git blame":
		return len(p.Argv.Operands()) == 1
	case "git ls-files":
		return has(p.Argv, "-m") || has(p.Argv, "-o") || has(p.Argv, "-d")
	case "git describe":
		return has(p.Argv, "--dirty")
	case "git restore":
		return !has(p.Argv, "-S") || has(p.Argv, "-W")
	case "git stash":
		sub := firstOrEmpty(p.command.beforeDash)
		return sub != "show" && sub != "drop"
	case "git reset":
		return has(p.Argv, "--hard") || has(p.Argv, "--keep") || has(p.Argv, "--merge")
	case "git worktree":
		return firstOrEmpty(p.Argv.Operands().Values()) == "remove"
	default:
		return treeForms[p.Command]
	}
}

// One Git invocation can read the worktree to plan an operation and again to apply it.
func (p *Prepared) bindWorktreeDrafts(tc tools.ToolContext) {
	if !p.readsWorktree() {
		return
	}
	var once sync.Once
	var err error
	p.saveWorktreeDrafts = func(ctx context.Context) error {
		once.Do(func() {
			err = saveWorktreeDrafts(ctx, tc)
		})
		return err
	}
}

func saveWorktreeDrafts(ctx context.Context, tc tools.ToolContext) error {
	launch, ok := gitexec.AgentLaunchFrom(ctx)
	if !ok {
		return gitexec.ErrLaunchAuthorityRequired
	}
	if check := launch.Plan.CurrentAuthority; check != nil {
		if err := check(ctx); err != nil {
			return err
		}
	}
	_, err := tools.SaveLaunchDrafts(ctx, tc)
	return err
}

func (p Prepared) prepareWorktree(ctx context.Context) error {
	if p.saveWorktreeDrafts == nil {
		return nil
	}
	return p.saveWorktreeDrafts(ctx)
}

func (p Prepared) mutations() native.GitMutations {
	return native.GitMutations{
		Git: p.owners.Git, Boundary: p.owners.Boundary, PrivateGit: p.owners.PrivateGit,
		BeforeWorktree: p.saveWorktreeDrafts,
	}
}
