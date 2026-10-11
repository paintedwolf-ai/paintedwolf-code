package persistence

import (
	"context"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/gitstate"
	"github.com/lycaon/lycaon/internal/sourceledger"
)

// gitStateReader observes live repository positions for the source ledger's
// transition minting: subprocess-free .git reads for position, the git engine
// for the reflog.
type gitStateReader struct {
	mgr *git.Manager
}

var _ sourceledger.GitStateReader = gitStateReader{}

func (r gitStateReader) HeadState(ctx context.Context, rootAbs string) gitstate.State {
	if _, ok := gitrepo.Discover(rootAbs); !ok {
		return gitstate.State{Repo: gitstate.RepoAbsent}
	}
	head, err := git.ReadHeadSHA(rootAbs)
	if err != nil {
		// An unborn branch (init, no commits) still names itself: a repository
		// with an empty position, not an unreadable one.
		if branch, branchErr := git.ReadBranch(rootAbs); branchErr == nil {
			return gitstate.State{Repo: gitstate.RepoPresent, HeadRef: branch}
		}
		return gitstate.State{Repo: gitstate.RepoUnreadable}
	}
	branch, _ := git.ReadBranch(rootAbs)
	return gitstate.State{Repo: gitstate.RepoPresent, HeadCommit: head, HeadRef: branch}
}

func (r gitStateReader) RefLogHead(ctx context.Context, rootAbs string, limit int) ([]gitstate.RefLogEntry, error) {
	rows, err := r.mgr.RefLogHead(ctx, rootAbs, limit)
	if err != nil {
		return nil, err
	}
	out := make([]gitstate.RefLogEntry, 0, len(rows))
	for _, row := range rows {
		out = append(out, gitstate.RefLogEntry{Commit: row.Commit, Subject: row.Subject})
	}
	return out, nil
}
