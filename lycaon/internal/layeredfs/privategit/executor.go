package privategit

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/layeredfs"
)

// GitExecutor executes write-leg repository operations against a view's private Git state
// while enforcing egress/remote policy and syncing working-tree mutations (#1671).
type GitExecutor struct {
	mgr    *Manager
	viewID string
	policy RemotePolicy
}

// NewGitExecutor constructs a GitExecutor for viewID with the given RemotePolicy.
func NewGitExecutor(mgr *Manager, viewID string, policy RemotePolicy) *GitExecutor {
	return &GitExecutor{
		mgr:    mgr,
		viewID: viewID,
		policy: policy,
	}
}

// Run executes a Git command in repoRel ("." for primary repo or submodule path) within the view.
func (e *GitExecutor) Run(ctx context.Context, repoRel string, args ...string) (string, error) {
	return executeViewGitCommand(ctx, e, repoRel, args)
}

// Status runs `git status` in the primary repository.
func (e *GitExecutor) Status(ctx context.Context, args ...string) (string, error) {
	return e.Run(ctx, ".", append([]string{"status"}, args...)...)
}

// Diff runs `git diff` in the primary repository.
func (e *GitExecutor) Diff(ctx context.Context, args ...string) (string, error) {
	return e.Run(ctx, ".", append([]string{"diff"}, args...)...)
}

// Log runs `git log` in the primary repository.
func (e *GitExecutor) Log(ctx context.Context, args ...string) (string, error) {
	return e.Run(ctx, ".", append([]string{"log"}, args...)...)
}

// Commit stages tracked/requested changes if needed and runs `git commit -m message`.
func (e *GitExecutor) Commit(ctx context.Context, message string, args ...string) (string, error) {
	cmdArgs := append([]string{"commit", "-m", message}, args...)
	return e.Run(ctx, ".", cmdArgs...)
}

// Checkout runs `git checkout` in the primary repository.
func (e *GitExecutor) Checkout(ctx context.Context, args ...string) (string, error) {
	return e.Run(ctx, ".", append([]string{"checkout"}, args...)...)
}

// Stash runs `git stash` in the primary repository.
func (e *GitExecutor) Stash(ctx context.Context, args ...string) (string, error) {
	return e.Run(ctx, ".", append([]string{"stash"}, args...)...)
}

// Fetch runs `git fetch <remote>` subject to RemotePolicy egress and approval controls.
func (e *GitExecutor) Fetch(ctx context.Context, remote string, args ...string) (string, error) {
	if strings.TrimSpace(remote) == "" {
		remote = "origin"
	}
	cmdArgs := append([]string{"fetch", remote}, args...)
	return e.Run(ctx, ".", cmdArgs...)
}

// Push runs `git push <remote>` subject to RemotePolicy egress and approval controls.
func (e *GitExecutor) Push(ctx context.Context, remote string, args ...string) (string, error) {
	if strings.TrimSpace(remote) == "" {
		remote = "origin"
	}
	cmdArgs := append([]string{"push", remote}, args...)
	return e.Run(ctx, ".", cmdArgs...)
}

// Repack runs a local-only private object repack (`git repack -l`) without copying borrowed packs.
func (e *GitExecutor) Repack(ctx context.Context, args ...string) (string, error) {
	return e.Run(ctx, ".", append([]string{"repack", "-l"}, args...)...)
}

func executeViewGitCommand(
	ctx context.Context,
	e *GitExecutor,
	repoRel string,
	args []string,
) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("privategit: git subcommand is required")
	}
	view, err := getManagedView(e.mgr, e.viewID)
	if err != nil {
		return "", err
	}
	if !view.materialized {
		if err := verifyActiveMountedView(e.mgr, view, "", ""); err != nil {
			return "", err
		}
	}
	repoRel = cleanSlashRel(repoRel)
	gitRel := ".git"
	if repoRel != "." {
		gitRel = repoRel + "/.git"
	}
	if _, err := e.mgr.InterceptGitAccess(e.viewID, layeredfs.QualifiedPath{
		RootID: view.rootID,
		Rel:    gitRel,
	}, true); err != nil {
		return "", err
	}

	targetWT := view.worktreeDir
	if repoRel != "." {
		targetWT = filepath.Join(view.worktreeDir, filepath.FromSlash(repoRel))
	}

	normalizedArgs, err := prepareAndAuthorizeGitArgs(ctx, view, targetWT, e.policy, args)
	if err != nil {
		return "", err
	}

	if err := syncCoreOverlayToWorktree(view); err != nil {
		return "", err
	}
	beforeTree := captureWorktreeMutationBaseline(view, targetWT)
	privGitDir, _ := readyPrivateGitDir(e.mgr, e.viewID, repoRel)
	if privGitDir == "" {
		privGitDir = filepath.Join(targetWT, ".git")
	}
	execArgs := normalizedArgs
	if privGitDir != filepath.Join(targetWT, ".git") {
		_, _, _ = gitexec.Run(ctx, targetWT, []string{"--git-dir=" + privGitDir, "--work-tree=" + targetWT, "update-index", "-q", "--refresh"}, gitexec.Opts{
			Profile: gitexec.ProfileHermetic,
		})
		execArgs = append([]string{"--git-dir=" + privGitDir, "--work-tree=" + targetWT}, normalizedArgs...)
	}
	privObjDir := filepath.Join(privGitDir, "objects")
	beforeObjBytes := measureDirBytes(privObjDir)

	out, code, runErr := gitexec.Run(ctx, targetWT, execArgs, gitexec.Opts{
		Profile: gitexec.ProfileHermetic,
		Identity: &gitexec.Identity{
			Name:      "Painted Wolf Worker",
			Email:     "worker@paintedwolf.dev",
			Timestamp: time.Now(),
		},
		ExtraConfig: []string{
			"protocol.file.allow=always",
			"repack.writeBitmaps=false",
			"gc.auto=0",
		},
	})

	afterObjBytes := measureDirBytes(privObjDir)
	if afterObjBytes > beforeObjBytes {
		e.mgr.store.RecordPrivateWriteBytes(afterObjBytes - beforeObjBytes)
	}

	if runErr != nil || code != 0 {
		return string(out), fmt.Errorf("git %v failed (exit %d): %v: %s", normalizedArgs, code, runErr, strings.TrimSpace(string(out)))
	}

	syncErr := syncWorktreeMutationsToCore(view, repoRel, beforeTree)
	retainErr := retainNewPrivateObjects(ctx, e.mgr, view, repoRel)
	if err := errors.Join(syncErr, retainErr); err != nil {
		return string(out), err
	}
	return string(out), nil
}

func measureDirBytes(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, iErr := d.Info(); iErr == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}
