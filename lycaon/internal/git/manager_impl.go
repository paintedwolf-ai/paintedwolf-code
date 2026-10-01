package git

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/gitargv"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/gitlease"
	"github.com/lycaon/lycaon/internal/repochange"
)

const gitStatusRecentCommitLimit = 5

func validateGitRef(value, label string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", label)
	}
	if err := gitargv.ValidateRefArg(value); err != nil {
		return fmt.Errorf("invalid %s %q", label, value)
	}
	return nil
}

type Manager struct {
	histories immutableCache[[]GitFileCommit]
	commits   immutableCache[CommitDetails]
	reviews   immutableCache[commitReviewSnapshot]
	trees     immutableCache[map[string]string]
}

func NewManager() *Manager {
	return &Manager{}
}

// Status returns a porcelain git status summary.
func (m *Manager) Status(ctx context.Context, projectDir string) (*GitStatus, error) {
	dir, err := repositoryProjectDir(projectDir)
	if err != nil {
		return nil, err
	}
	branch, entries, err := readPorcelainStatus(ctx, dir, true)
	if err != nil {
		return nil, err
	}
	status := &GitStatus{Files: []GitStatusEntry{}}
	parseStatusBranchLine(branch, status)
	if head, readErr := ReadHeadSHA(dir); readErr == nil {
		if len(head) > 8 {
			head = head[:8]
		}
		status.HeadShort = head
	}
	for _, entry := range entries {
		status.Files = append(status.Files, GitStatusEntry{Path: entry.path, Status: entry.status})
		if entry.status[0] != ' ' && entry.status[0] != '?' {
			status.StagedCount++
		}
		if entry.status[1] != ' ' && entry.status[1] != '!' {
			status.UnstagedCount++
		}
	}
	status.Dirty = status.StagedCount > 0 || status.UnstagedCount > 0
	// Recent commits are loaded separately from status.
	return status, nil
}

// StatusIgnored returns ignored entries matching explicit paths.
func (m *Manager) StatusIgnored(ctx context.Context, projectDir string, paths []string) ([]GitStatusEntry, error) {
	dir, err := repositoryProjectDir(projectDir)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("paths are required for ignored status")
	}
	args := []string{"status", "--porcelain=v1", "-z", "--ignored=matching", "--"}
	for _, p := range paths {
		args = append(args, filepath.ToSlash(p))
	}
	var p porcelainParser
	if err := gitexec.RunRecords(ctx, dir, args, hermeticOpts(0), p.record); err != nil {
		return nil, err
	}
	_, entries, err := p.finish()
	if err != nil {
		return nil, err
	}
	out := make([]GitStatusEntry, 0, len(entries))
	for _, e := range entries {
		if strings.HasPrefix(e.status, "!") {
			out = append(out, GitStatusEntry{Path: e.path, Status: e.status})
		}
	}
	return out, nil
}

func parseStatusBranchLine(rest string, status *GitStatus) {
	rest = strings.TrimSpace(rest)
	if r, ok := strings.CutPrefix(rest, "No commits yet on "); ok {
		status.Branch = strings.TrimSpace(r)
		return
	}
	tracking := ""
	if idx := strings.Index(rest, "["); idx >= 0 {
		tracking = strings.TrimSuffix(strings.TrimSpace(rest[idx+1:]), "]")
		rest = strings.TrimSpace(rest[:idx])
	}
	if idx := strings.Index(rest, "..."); idx >= 0 {
		status.Branch = strings.TrimSpace(rest[:idx])
		status.Upstream = strings.TrimSpace(rest[idx+3:])
	} else {
		status.Branch = strings.TrimSpace(rest)
	}
	for _, part := range strings.Split(tracking, ",") {
		part = strings.TrimSpace(part)
		switch {
		case strings.HasPrefix(part, "ahead "):
			status.Ahead, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(part, "ahead ")))
		case strings.HasPrefix(part, "behind "):
			status.Behind, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(part, "behind ")))
		}
	}
}

// ChangedPaths returns repo-relative paths with staged or unstaged changes.
func (m *Manager) ChangedPaths(ctx context.Context, projectDir string) ([]string, error) {
	dir, err := repositoryProjectDir(projectDir)
	if err != nil {
		return nil, err
	}
	_, entries, err := readPorcelainStatus(ctx, dir, false)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	var paths []string
	for _, entry := range entries {
		if _, ok := seen[entry.path]; ok {
			continue
		}
		seen[entry.path] = struct{}{}
		paths = append(paths, entry.path)
	}
	return paths, nil
}

// Diff returns git diff output for optional paths (worktree vs index, staged
// vs HEAD, or either side against BaseRef).
func (m *Manager) Diff(ctx context.Context, projectDir string, opts GitDiffOpts) (string, error) {
	dir, err := repositoryProjectDir(projectDir)
	if err != nil {
		return "", err
	}
	args, err := diffArgs([]string{"diff"}, opts)
	if err != nil {
		return "", err
	}
	out, code, err := gitexec.Run(ctx, dir, args, hermeticOpts(0))
	if err != nil {
		return "", err
	}
	if code != 0 && code != 1 {
		return "", fmt.Errorf("git diff failed: %s", strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// Separators keep refs and paths out of option parsing.
func diffArgs(prefix []string, opts GitDiffOpts) ([]string, error) {
	args := append([]string(nil), prefix...)
	if opts.Staged {
		args = append(args, "--cached")
	}
	if ref := strings.TrimSpace(opts.BaseRef); ref != "" {
		if err := validateGitRef(ref, "base ref"); err != nil {
			return nil, err
		}
		args = append(args, gitargv.EndOfOptions, ref)
	}
	if opts.HeadRef != "" {
		if opts.BaseRef == "" || opts.Staged {
			return nil, fmt.Errorf("head_ref requires base_ref and a committed-tree comparison")
		}
		if err := validateGitRef(opts.HeadRef, "head ref"); err != nil {
			return nil, err
		}
		args = append(args, opts.HeadRef)
	}
	args = append(args, "--")
	if len(opts.Paths) > 0 {
		for _, p := range opts.Paths {
			if p != "" {
				args = append(args, p)
			}
		}
	}
	return args, nil
}

// UntrackedPaths lists untracked, non-ignored files under the optional
// pathspecs, in git's order.
func (m *Manager) UntrackedPaths(ctx context.Context, projectDir string, paths []string) ([]string, error) {
	dir, err := repositoryProjectDir(projectDir)
	if err != nil {
		return nil, err
	}
	args := []string{"ls-files", "-z", "--others", "--exclude-standard"}
	if len(paths) > 0 {
		args = append(args, "--")
		for _, p := range paths {
			if p != "" {
				args = append(args, p)
			}
		}
	}
	var out []string
	err = gitexec.RunRecords(ctx, dir, args, hermeticOpts(0), func(raw []byte) error {
		rel, err := parseRepoPath(raw)
		if err != nil {
			return err
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// UntrackedFileDiff returns the synthetic addition hunk for one untracked file.
func (m *Manager) UntrackedFileDiff(ctx context.Context, projectDir, rel string) (string, error) {
	dir, err := repositoryProjectDir(projectDir)
	if err != nil {
		return "", err
	}
	hunk, code, err := gitexec.Run(ctx, dir, []string{"diff", "--no-index", "--", "/dev/null", rel}, hermeticOpts(0))
	if err != nil {
		return "", err
	}
	if code != 0 && code != 1 {
		return "", fmt.Errorf("git diff failed: %s", strings.TrimSpace(string(hunk)))
	}
	return string(hunk), nil
}

// UntrackedDiff returns synthetic addition hunks for every untracked file.
func (m *Manager) UntrackedDiff(ctx context.Context, projectDir string) (string, error) {
	paths, err := m.UntrackedPaths(ctx, projectDir, nil)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, rel := range paths {
		hunk, err := m.UntrackedFileDiff(ctx, projectDir, rel)
		if err != nil {
			return "", err
		}
		if b.Len()+len(hunk)+1 > exec.DefaultMaxOutputBytes {
			return "", exec.ErrOutputTruncated
		}
		if b.Len() > 0 && len(hunk) > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(hunk)
	}
	return b.String(), nil
}

// DiffStat returns per-path insertion/deletion counts without hunks.
func (m *Manager) DiffStat(ctx context.Context, projectDir string, opts GitDiffOpts) ([]GitDiffStatEntry, error) {
	dir, err := repositoryProjectDir(projectDir)
	if err != nil {
		return nil, err
	}
	args, err := diffArgs([]string{"diff", "--numstat", "-z"}, opts)
	if err != nil {
		return nil, err
	}
	var p numstatParser
	if err := gitexec.RunRecords(ctx, dir, args, hermeticOpts(0), p.record); err != nil {
		return nil, err
	}
	return p.finish()
}

// LastTouchByPath returns each path's newest committer timestamp.
func (m *Manager) LastTouchByPath(ctx context.Context, projectDir string) (map[string]time.Time, error) {
	dir, err := repositoryProjectDir(projectDir)
	if err != nil {
		return nil, err
	}
	args := []string{"log", "--no-merges", "-z", "--format=%x1e%ct", "--name-only"}
	p := lastTouchParser{result: make(map[string]time.Time)}
	if err := gitexec.RunRecords(ctx, dir, args, hermeticOpts(0), p.record); err != nil {
		return nil, err
	}
	return p.result, nil
}

func (m *Manager) Init(ctx context.Context, projectDir string) error {
	dir, err := absProjectDir(projectDir)
	if err != nil {
		return err
	}
	release, err := gitlease.Path(ctx, dir)
	if err != nil {
		return err
	}
	defer release()
	out, code, err := gitexec.Run(ctx, dir, []string{"init"}, hermeticOpts(0))
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("git init failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func (m *Manager) Clone(ctx context.Context, url, destDir string) error {
	if err := gitargv.ValidateCloneURL(url); err != nil {
		return err
	}
	parent, err := absProjectDir(filepath.Dir(destDir))
	if err != nil {
		return err
	}
	parent, err = filepath.EvalSymlinks(parent)
	if err != nil {
		return fmt.Errorf("resolve clone parent: %w", err)
	}
	destDir = filepath.Join(parent, filepath.Base(destDir))
	// Source-first locking serializes local source writes.
	releaseSource, err := gitlease.CloneSource(ctx, url)
	if err != nil {
		return err
	}
	defer releaseSource()
	release, err := gitlease.Path(ctx, destDir)
	if err != nil {
		return err
	}
	defer release()
	owned, err := claimCloneDestination(destDir)
	if err != nil {
		return err
	}
	out, code, err := gitexec.Run(ctx, parent, []string{"clone", "--", url, destDir}, networkOpts(url, 10*time.Minute))
	if err != nil {
		return owned.failed(err)
	}
	if code != 0 {
		return owned.failed(fmt.Errorf("git clone failed: %s", strings.TrimSpace(string(out))))
	}
	return nil
}

// Stash saves the working tree (including untracked files) to a new stash entry.
func (m *Manager) Stash(ctx context.Context, projectDir string, message string) error {
	dir, err := repositoryProjectDir(projectDir)
	if err != nil {
		return err
	}
	release, err := gitlease.Repository(ctx, dir)
	if err != nil {
		return err
	}
	defer release()
	args := []string{"stash", "push", "-u"}
	if msg := strings.TrimSpace(message); msg != "" {
		args = append(args, "-m", msg)
	}
	out, code, err := gitexec.Run(ctx, dir, args, hermeticOpts(0))
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("git stash failed: %s", strings.TrimSpace(string(out)))
	}
	notifyRepoChange(ctx, dir, repochange.WorktreeChanged)
	return nil
}

// DiscardAll resets tracked files and removes untracked files.
func (m *Manager) DiscardAll(ctx context.Context, projectDir string) error {
	dir, err := repositoryProjectDir(projectDir)
	if err != nil {
		return err
	}
	release, err := gitlease.Repository(ctx, dir)
	if err != nil {
		return err
	}
	defer release()
	if head, _ := m.HeadSHA(ctx, dir); head != "" {
		out, code, err := gitexec.Run(ctx, dir, []string{"reset", "--hard", "HEAD"}, hermeticOpts(0))
		if err != nil {
			return err
		}
		if code != 0 {
			return fmt.Errorf("git reset failed: %s", strings.TrimSpace(string(out)))
		}
	}
	out, code, err := gitexec.Run(ctx, dir, []string{"clean", "-fd"}, hermeticOpts(0))
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("git clean failed: %s", strings.TrimSpace(string(out)))
	}
	notifyRepoChange(ctx, dir, repochange.WorktreeChanged)
	return nil
}

// Push publishes the current branch to its upstream.
func (m *Manager) Push(ctx context.Context, projectDir string) error {
	return m.runRemote(ctx, projectDir, "push", []string{"push"})
}

// Pull fast-forwards the current branch from its upstream.
func (m *Manager) Pull(ctx context.Context, projectDir string) error {
	return m.runRemote(ctx, projectDir, "pull", []string{"pull", "--ff-only"})
}

func (m *Manager) runRemote(ctx context.Context, projectDir, label string, args []string) error {
	dir, err := repositoryProjectDir(projectDir)
	if err != nil {
		return err
	}
	release, err := gitlease.Repository(ctx, dir)
	if err != nil {
		return err
	}
	defer release()
	out, code, err := gitexec.Run(ctx, dir, args, networkOpts("", 0))
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("git %s failed: %s", label, strings.TrimSpace(string(out)))
	}
	return nil
}

// Checkout switches the working tree to an existing branch.
func (m *Manager) Checkout(ctx context.Context, projectDir string, branch string) error {
	dir, err := repositoryProjectDir(projectDir)
	if err != nil {
		return err
	}
	if err := validateGitRef(branch, "branch"); err != nil {
		return err
	}
	release, err := gitlease.Repository(ctx, dir)
	if err != nil {
		return err
	}
	defer release()
	// The trailing separator forces a branch checkout rather than a path checkout.
	out, code, err := gitexec.Run(ctx, dir, []string{"checkout", gitargv.EndOfOptions, branch, "--"}, hermeticOpts(0))
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("git checkout failed: %s", strings.TrimSpace(string(out)))
	}
	notifyRepoChange(ctx, dir, repochange.HeadMoved)
	return nil
}

// Branch returns the current branch name (in-process .git read; falls back to git).
func (m *Manager) Branch(ctx context.Context, projectDir string) (string, error) {
	if name, err := ReadBranch(projectDir); err == nil {
		// A readable HEAD distinguishes detached state from an unresolved repository.
		if name != "" {
			return name, nil
		}
		if _, herr := ReadHeadSHA(projectDir); herr == nil {
			return "", nil
		}
	}
	dir, err := repositoryProjectDir(projectDir)
	if err != nil {
		return "", err
	}
	out, code, err := gitexec.Run(ctx, dir, []string{"branch", "--show-current"}, hermeticOpts(0))
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("git branch failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// AheadBehindCounts returns divergence from upstream.
func (m *Manager) AheadBehindCounts(ctx context.Context, projectDir string) (ahead, behind int, upstream string, err error) {
	ab, ferr := ReadAheadBehind(projectDir)
	if ferr == nil && ab.Exact {
		return ab.Ahead, ab.Behind, ab.Upstream, nil
	}
	dir, err := repositoryProjectDir(projectDir)
	if err != nil {
		return 0, 0, "", err
	}
	branch, err := m.Branch(ctx, dir)
	if err != nil || branch == "" {
		return 0, 0, ab.Upstream, err
	}
	up := "@{upstream}"
	if ab.Upstream != "" {
		up = strings.TrimPrefix(ab.Upstream, "refs/")
	}
	out, code, err := gitexec.Run(ctx, dir, []string{"rev-list", "--left-right", "--count", gitargv.EndOfOptions, branch + "..." + up}, hermeticOpts(0))
	if err != nil {
		return 0, 0, ab.Upstream, err
	}
	if code != 0 {
		return 0, 0, ab.Upstream, fmt.Errorf("git rev-list failed: %s", strings.TrimSpace(string(out)))
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) != 2 {
		return 0, 0, ab.Upstream, fmt.Errorf("unexpected rev-list output: %q", string(out))
	}
	ahead, _ = strconv.Atoi(fields[0])
	behind, _ = strconv.Atoi(fields[1])
	return ahead, behind, ab.Upstream, nil
}

// CreateBranch creates and checks out a branch.
func (m *Manager) CreateBranch(ctx context.Context, projectDir string, branch string) error {
	dir, err := repositoryProjectDir(projectDir)
	if err != nil {
		return err
	}
	if err := validateGitRef(branch, "branch"); err != nil {
		return err
	}
	release, err := gitlease.Repository(ctx, dir)
	if err != nil {
		return err
	}
	defer release()
	out, code, err := gitexec.Run(ctx, dir, []string{"checkout", "-b", branch, "--"}, hermeticOpts(0))
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("git checkout failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// DiffFromRef reports whether path changed since baseRef.
func (m *Manager) DiffFromRef(ctx context.Context, projectDir, baseRef, path string) (bool, error) {
	dir, err := repositoryProjectDir(projectDir)
	if err != nil {
		return false, err
	}
	if err := validateGitRef(baseRef, "base ref"); err != nil {
		return false, err
	}
	args := []string{"diff", gitargv.EndOfOptions, baseRef, "--", path}
	out, code, err := gitexec.Run(ctx, dir, args, hermeticOpts(0))
	if err != nil {
		return false, err
	}
	if code != 0 && code != 1 {
		return false, fmt.Errorf("git diff failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// HeadSHA returns the current commit hash (in-process .git read; falls back to git).
func (m *Manager) HeadSHA(ctx context.Context, projectDir string) (string, error) {
	if sha, err := ReadHeadSHA(projectDir); err == nil && sha != "" {
		return sha, nil
	}
	dir, err := repositoryProjectDir(projectDir)
	if err != nil {
		return "", err
	}
	out, code, err := gitexec.Run(ctx, dir, []string{"rev-parse", "HEAD"}, hermeticOpts(0))
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("git rev-parse failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func absProjectDir(projectDir string) (string, error) {
	if strings.TrimSpace(projectDir) == "" {
		return "", fmt.Errorf("project dir is required")
	}
	return filepath.Abs(projectDir)
}

func repositoryProjectDir(projectDir string) (string, error) {
	dir, err := absProjectDir(projectDir)
	if err != nil {
		return "", err
	}
	if _, err := discoverRepo(dir); err != nil {
		return "", err
	}
	return dir, nil
}

func notifyRepoChange(ctx context.Context, projectDir string, kind repochange.Kind) {
	dir, err := absProjectDir(projectDir)
	if err != nil {
		return
	}
	repochange.Notify(ctx, repochange.Event{
		ProjectDir: dir,
		Kind:       kind,
		Source:     repochange.SourceGitHost,
	})
}

// Untracked directories expand to files for staging and invalidation.
func readPorcelainStatus(ctx context.Context, dir string, branch bool) (string, []porcelainEntry, error) {
	args := []string{"status", "--porcelain=v1", "-z", "--untracked-files=all"}
	if branch {
		args = append(args, "-b")
	}
	var p porcelainParser
	if err := gitexec.RunRecords(ctx, dir, args, hermeticOpts(0), p.record); err != nil {
		return "", nil, err
	}
	return p.finish()
}
