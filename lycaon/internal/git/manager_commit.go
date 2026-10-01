package git

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/gitlease"
	"github.com/lycaon/lycaon/internal/gitstate"
	"github.com/lycaon/lycaon/internal/repochange"
)

// GitCommitOpts selects the message, staged paths, and whether to replace HEAD.
type GitCommitOpts struct {
	Message string
	Paths   []string
	Amend   bool
}

// Commit stages paths and creates or amends a commit.
func (m *Manager) Commit(ctx context.Context, projectDir string, opts GitCommitOpts) (hash string, err error) {
	dir, err := repositoryProjectDir(projectDir)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(opts.Message) == "" {
		return "", fmt.Errorf("commit message is required")
	}
	if opts.Amend && len(opts.Paths) == 0 {
		return "", fmt.Errorf("amend requires explicit paths")
	}
	release, err := gitlease.Repository(ctx, dir)
	if err != nil {
		return "", err
	}
	defer release()
	finish, err := gitstate.BeginMutation(ctx, dir, nil)
	if err != nil {
		return "", err
	}
	defer func() {
		if historyErr := finish(ctx); historyErr != nil {
			err = errors.Join(err, fmt.Errorf("record Git history: %w", historyErr))
		}
	}()
	addArgs := []string{"add"}
	if len(opts.Paths) == 0 {
		// No explicit paths: stage every change.
		addArgs = append(addArgs, "-A")
	} else {
		// Treat explicit paths as literal pathspecs.
		addArgs = append(addArgs, "--")
		for _, p := range opts.Paths {
			if p != "" {
				addArgs = append(addArgs, p)
			}
		}
	}
	if _, code, err := gitexec.Run(ctx, dir, addArgs, hermeticOpts(0)); err != nil {
		return "", err
	} else if code != 0 {
		return "", fmt.Errorf("git add failed")
	}
	identity := commitIdentity(ctx, dir)
	out, code, err := gitexec.Run(ctx, dir, commitArgs(opts), gitexec.Opts{Profile: gitexec.ProfileHermetic, Identity: &identity})
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", NewCommitFailed(out, code)
	}
	hash, err = m.HeadSHA(ctx, dir)
	notifyRepoChange(ctx, dir, repochange.HeadMoved)
	return hash, err
}

func commitArgs(opts GitCommitOpts) []string {
	args := []string{"commit", "-m", opts.Message}
	if opts.Amend {
		// Amend only the selected paths, leaving unrelated staged work in the index.
		args = append(args, "--amend", "--only", "--")
		args = append(args, opts.Paths...)
	}
	return args
}
