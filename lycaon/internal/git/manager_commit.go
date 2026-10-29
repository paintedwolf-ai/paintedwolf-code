package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	// IdentityName and IdentityEmail, when set, replace the resolved author
	// and committer fields.
	IdentityName, IdentityEmail string
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
	paths := make([]string, 0, len(opts.Paths))
	for _, path := range opts.Paths {
		if path != "" {
			paths = append(paths, path)
		}
	}
	if len(paths) > 0 {
		return commitSelected(ctx, dir, opts, paths)
	}
	if !opts.Amend {
		if _, code, err := gitexec.Run(ctx, dir, []string{"add", "-A"}, hermeticOpts(0)); err != nil {
			return "", err
		} else if code != 0 {
			return "", fmt.Errorf("git add failed")
		}
	}
	return createCommit(ctx, dir, commitArgs(opts), hermeticOpts(0), opts)
}

func createCommit(ctx context.Context, dir string, args []string, run gitexec.Opts, opts GitCommitOpts) (string, error) {
	identity := commitIdentity(ctx, dir)
	if opts.IdentityName != "" {
		identity.Name = opts.IdentityName
	}
	if opts.IdentityEmail != "" {
		identity.Email = opts.IdentityEmail
	}
	run.Profile = gitexec.ProfileHermetic
	run.Identity = &identity
	out, code, err := gitexec.Run(ctx, dir, args, run)

	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", NewCommitFailed(out, code)
	}
	hash, err := resolveCommit(ctx, dir, "HEAD")
	notifyRepoChange(ctx, dir, repochange.HeadMoved)
	return hash, err
}

// commitSelected builds the commit in a private index seeded from HEAD, so
// unrelated staged work stays staged and out of the commit. Selected paths
// take their working-tree content, except a staged deletion, which wins even
// when the file remains on disk. After the commit only the selected paths of
// the live index are updated to the committed entries.
func commitSelected(ctx context.Context, dir string, opts GitCommitOpts, paths []string) (string, error) {
	scratch, err := os.MkdirTemp("", "git-commit-index-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	private := hermeticOpts(0)
	private.IndexFile = filepath.Join(scratch, "index")
	head, err := ResolveOptionalObject(ctx, dir, "HEAD^{commit}")
	if err != nil {
		return "", err
	}
	if head != "" {
		if _, err := restoreGit(ctx, dir, private, "read-tree", head); err != nil {
			return "", err
		}
	}
	live, err := readOperationIndex(ctx, dir, hermeticOpts(0), paths...)
	if err != nil {
		return "", err
	}
	headEntries, err := readOperationIndex(ctx, dir, private, paths...)
	if err != nil {
		return "", err
	}
	if out, code, err := gitexec.Run(ctx, dir, append([]string{"--literal-pathspecs", "add", "--"}, paths...), private); err != nil {
		return "", err
	} else if code != 0 {
		return "", fmt.Errorf("git add failed: %s", strings.TrimSpace(string(out)))
	}
	added, err := readOperationIndex(ctx, dir, private, paths...)
	if err != nil {
		return "", err
	}
	selected := operationIndex{}
	for key, entry := range added {
		if _, deleted := headEntries[operationIndexKey{key.path, 0}]; deleted && !indexHasPath(live, key.path) {
			continue
		}
		selected[key] = entry
	}
	if err := publishIndexDelta(ctx, dir, private, added, selected); err != nil {
		return "", err
	}
	args := []string{"commit", "-m", opts.Message}
	if opts.Amend {
		args = append(args, "--amend")
	}
	hash, err := createCommit(ctx, dir, args, private, opts)
	if err != nil {
		return "", err
	}
	if err := publishIndexDelta(ctx, dir, hermeticOpts(0), live, selected); err != nil {
		return hash, fmt.Errorf("update index after commit %s: %w", hash, err)
	}
	return hash, nil
}

func indexHasPath(index operationIndex, path string) bool {
	for stage := 0; stage <= 3; stage++ {
		if _, ok := index[operationIndexKey{path, stage}]; ok {
			return true
		}
	}
	return false
}

// A message-only amend keeps HEAD's tree rather than the live index.
func commitArgs(opts GitCommitOpts) []string {
	args := []string{"commit", "-m", opts.Message}
	if opts.Amend {
		args = append(args, "--amend", "--only")
	}
	return args
}
