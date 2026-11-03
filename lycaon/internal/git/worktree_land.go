package git

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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
	if err := validateLandWorktrees(ctx, m, req); err != nil {
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
	if err := mergeCommit(ctx, dir, source, message); err != nil {
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

func validateLandWorktrees(ctx context.Context, m *Manager, req WorktreeLandRequest) error {
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
func mergeCommit(ctx context.Context, dir, source, message string) error {
	identity := commitIdentity(ctx, dir)
	opts := gitexec.Opts{Profile: gitexec.ProfileHermetic, Identity: &identity}
	out, code, err := gitexec.Run(ctx, dir, []string{"merge", "--no-ff", "--no-edit", "-m", message, gitargv.EndOfOptions, source}, opts)
	if err != nil {
		return err
	}
	if code == 0 {
		return nil
	}
	return abortFailedMerge(ctx, dir, out)
}

func abortFailedMerge(ctx context.Context, dir string, mergeOut []byte) error {
	conflictOut, _, _ := gitexec.Run(ctx, dir, []string{"diff", "--name-only", "--diff-filter=U"}, hermeticOpts(0))
	paths := splitNonEmptyLines(string(conflictOut))
	abortOut, abortCode, abortErr := gitexec.Run(ctx, dir, []string{"merge", "--abort"}, hermeticOpts(0))
	if abortErr != nil {
		return fmt.Errorf("git merge failed: %s; merge --abort failed: %w",
			strings.TrimSpace(string(mergeOut)), abortErr)
	}
	if abortCode != 0 {
		return fmt.Errorf("git merge failed: %s; merge --abort failed: %s",
			strings.TrimSpace(string(mergeOut)), strings.TrimSpace(string(abortOut)))
	}
	return &MergeConflictError{Paths: paths}
}

// AheadBehindRefs counts commits unique to head and base.
func (m *Manager) AheadBehindRefs(ctx context.Context, dir, base, head string) (ahead, behind int, err error) {
	return aheadBehindRefs(ctx, dir, base, head)
}

func aheadBehindRefs(ctx context.Context, dir, base, head string) (ahead, behind int, err error) {
	cwd, err := absProjectDir(dir)
	if err != nil {
		return 0, 0, err
	}
	if err := validateGitRef(base, "base"); err != nil {
		return 0, 0, err
	}
	if err := validateGitRef(head, "head"); err != nil {
		return 0, 0, err
	}
	out, code, err := gitexec.Run(ctx, cwd, []string{"rev-list", "--left-right", "--count", base + "..." + head}, hermeticOpts(0))
	if err != nil {
		return 0, 0, err
	}
	if code != 0 {
		return 0, 0, fmt.Errorf("git rev-list failed: %s", strings.TrimSpace(string(out)))
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("unexpected rev-list output: %q", string(out))
	}
	behind, err = strconv.Atoi(fields[0])
	if err != nil {
		return 0, 0, fmt.Errorf("parse behind count %q: %w", fields[0], err)
	}
	ahead, err = strconv.Atoi(fields[1])
	if err != nil {
		return 0, 0, fmt.Errorf("parse ahead count %q: %w", fields[1], err)
	}
	return ahead, behind, nil
}

// RepoConfigFingerprint hashes configuration from the common git directory.
func (m *Manager) RepoConfigFingerprint(ctx context.Context, toplevel string) (string, error) {
	return repoConfigFingerprint(ctx, toplevel)
}

func repoConfigFingerprint(ctx context.Context, toplevel string) (string, error) {
	dir, err := absProjectDir(toplevel)
	if err != nil {
		return "", err
	}
	out, code, err := gitexec.Run(ctx, dir, []string{"rev-parse", "--git-common-dir"}, hermeticOpts(0))
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("git rev-parse --git-common-dir failed: %s", strings.TrimSpace(string(out)))
	}
	common := strings.TrimSpace(string(out))
	if common == "" {
		return "", fmt.Errorf("git rev-parse --git-common-dir returned empty")
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	common = filepath.Clean(common)
	cfgPath := filepath.Join(common, "config")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return "", fmt.Errorf("read git common config: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
