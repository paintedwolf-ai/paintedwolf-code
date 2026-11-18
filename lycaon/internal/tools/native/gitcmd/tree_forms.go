package gitcmd

import (
	"context"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/tools"
)

// treeForms says, for every form, whether it reads, stages or rewrites
// working-tree files. The others touch only history, refs, configuration or a
// remote. A form whose options decide it counts as reading the tree.
var treeForms = map[string]bool{
	"git status": true, "git diff": true, "git blame": true, "git ls-files": true, "git grep": true, "git describe": true,
	"git log": false, "git show": false, "git branch": false, "git tag": false, "git rev-parse": false,
	"git rev-list": false, "git merge-base": false, "git remote": false, "git config": false, "git stash list": false,
	"git worktree list": false, "git reflog": false, "git shortlog": false, "git ls-tree": false, "git cat-file": false,
	"git show-ref": false, "git for-each-ref": false,

	"git add": true, "git commit": true, "git restore": true, "git checkout": true, "git switch": true,
	"git merge": true, "git cherry-pick": true, "git revert": true, "git rebase": true, "git stash": true,
	"git reset": true, "git clean": true, "git worktree": true, "git pull": true, "git apply": true,
	"git rm": true, "git mv": true,
	"git bisect": true, "git bisect start": true, "git bisect skip": true, "git bisect run": true, "git bisect reset": true,
	"git init": false, "git fetch": false, "git push": false, "git ls-remote": false, "git clone": false,
	"git bisect log": false, "git bisect terms": false, "git bisect visualize": false, "git bisect view": false,
}

// bindLaunch admits the command's Git launch. A form that reads the working
// tree then has the person's unsaved documents saved, so Git sees the text
// native reads serve.
func (p Prepared) bindLaunch(ctx context.Context, tc tools.ToolContext, services []confine.HostService) (exec.LaunchPlan, func(context.Context, tools.AgentLaunchOutcome), error) {
	plan, release, err := p.owners.Launch(ctx, tc, "git", p.commandLine(), services)
	if err != nil || !treeForms[p.command.spec.Name] {
		return plan, release, err
	}
	if _, err := tools.SaveLaunchDrafts(ctx, tc); err != nil {
		release(context.WithoutCancel(ctx), tools.AgentLaunchOutcome{Failed: true})
		return exec.LaunchPlan{}, nil, err
	}
	return plan, release, nil
}
