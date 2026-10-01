package git

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/gitargv"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/gitlease"
	"github.com/lycaon/lycaon/internal/repochange"
)

// WorktreeLandRequest identifies both checkouts and their expected branches.
type WorktreeLandRequest struct {
	BaseDir, BaseBranch  string
	WorktreePath, Branch string
}

// WorktreeLandError is a state refusal, not a Git execution failure.
type WorktreeLandError struct {
	Reason        string
	CurrentBranch string
}

func (e *WorktreeLandError) Error() string { return "worktree land blocked: " + e.Reason }

// LandWorktree validates and merges under the repository lease.
// Zero commits means the source is already contained in the base.
func (m *Manager) LandWorktree(ctx context.Context, req WorktreeLandRequest) (int, error) {
	dir, err := absProjectDir(req.BaseDir)
	if err != nil {
		return 0, err
	}
	req.BaseDir = dir
	if err := validateGitRef(req.BaseBranch, "base branch"); err != nil {
		return 0, err
	}
	if err := validateGitRef(req.Branch, "branch"); err != nil {
		return 0, err
	}
	release, err := gitlease.Repository(ctx, dir)
	if err != nil {
		return 0, err
	}
	defer release()
	if err := m.validateLandWorktrees(ctx, req); err != nil {
		return 0, err
	}
	source, err := ReadHeadSHA(req.WorktreePath)
	if err != nil {
		return 0, err
	}
	ahead, _, err := m.AheadBehindRefs(ctx, dir, "refs/heads/"+req.BaseBranch, source)
	if err != nil || ahead == 0 {
		return 0, err
	}
	before, err := m.RepoConfigFingerprint(ctx, dir)
	if err != nil {
		return 0, err
	}
	message := fmt.Sprintf("Merge %s into %s", req.Branch, req.BaseBranch)
	if err := m.mergeCommit(ctx, dir, source, message); err != nil {
		return 0, err
	}
	notifyRepoChange(ctx, dir, repochange.HeadMoved)
	after, err := m.RepoConfigFingerprint(ctx, dir)
	if err != nil {
		return 0, err
	}
	if after != before {
		return 0, fmt.Errorf("repository config changed during land")
	}
	return ahead, nil
}

func (m *Manager) validateLandWorktrees(ctx context.Context, req WorktreeLandRequest) error {
	if err := m.ValidateWorktree(ctx, req.BaseDir, req.WorktreePath, req.Branch); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &WorktreeLandError{Reason: "worktree_stale"}
	}
	st, err := m.Status(ctx, req.WorktreePath)
	if err != nil {
		return err
	}
	if st.Dirty {
		return &WorktreeLandError{Reason: "worktree_dirty"}
	}
	exists, err := m.LocalBranchExists(ctx, req.BaseDir, req.BaseBranch)
	if err != nil {
		return err
	}
	if !exists {
		return &WorktreeLandError{Reason: "base_branch_missing"}
	}
	if err := m.ValidateWorktree(ctx, req.BaseDir, req.BaseDir, ""); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &WorktreeLandError{Reason: "base_missing"}
	}
	current, err := m.Branch(ctx, req.BaseDir)
	if err != nil {
		return err
	}
	if current != req.BaseBranch {
		return &WorktreeLandError{Reason: "base_on_other_branch", CurrentBranch: current}
	}
	st, err = m.Status(ctx, req.BaseDir)
	if err != nil {
		return err
	}
	if st.Dirty {
		return &WorktreeLandError{Reason: "base_dirty"}
	}
	return nil
}

// The caller holds the repository lease and has validated both checkouts.
func (m *Manager) mergeCommit(ctx context.Context, dir, source, message string) error {
	identity := commitIdentity(ctx, dir)
	opts := gitexec.Opts{Profile: gitexec.ProfileHermetic, Identity: &identity}
	out, code, err := gitexec.Run(ctx, dir, []string{"merge", "--no-ff", "--no-edit", "-m", message, gitargv.EndOfOptions, source}, opts)
	if err != nil {
		return err
	}
	if code == 0 {
		return nil
	}
	return m.abortFailedMerge(ctx, dir, out)
}
