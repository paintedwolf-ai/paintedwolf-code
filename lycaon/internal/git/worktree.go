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
)

// WorktreeEntry is one linked worktree as `git worktree list --porcelain` reports it.
type WorktreeEntry struct {
	Path   string
	Branch string // refs/heads/... trimmed to the short name; empty when detached
	Bare   bool
}

// MergeConflictError reports an aborted conflicting merge.
type MergeConflictError struct {
	Paths []string
}

func (e *MergeConflictError) Error() string {
	if len(e.Paths) == 0 {
		return "git merge conflict"
	}
	return "git merge conflict: " + strings.Join(e.Paths, ", ")
}

// Code returns the structured reject code for tool/API mapping.
func (e *MergeConflictError) Code() string { return "GIT_MERGE_CONFLICT" }

// AddWorktree creates a new branch checkout from baseRef.
func (m *Manager) AddWorktree(ctx context.Context, toplevel, path, branch, baseRef string) error {
	dir, err := absProjectDir(toplevel)
	if err != nil {
		return err
	}
	if err := validateGitRef(branch, "branch"); err != nil {
		return err
	}
	if err := validateGitRef(baseRef, "base ref"); err != nil {
		return err
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("worktree path must be absolute")
	}
	release, err := gitlease.Repository(ctx, dir)
	if err != nil {
		return err
	}
	defer release()
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("worktree path already exists: %s", path)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("worktree path: %w", err)
	}
	out, code, err := gitexec.Run(ctx, dir, []string{"worktree", "add", "-b", branch, gitargv.EndOfOptions, path, baseRef}, hermeticOpts(0))
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("git worktree add failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// RemoveWorktree removes a clean linked worktree.
func (m *Manager) RemoveWorktree(ctx context.Context, toplevel, path string) error {
	dir, err := absProjectDir(toplevel)
	if err != nil {
		return err
	}
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("worktree path is required")
	}
	release, err := gitlease.Repository(ctx, dir)
	if err != nil {
		return err
	}
	defer release()
	out, code, err := gitexec.Run(ctx, dir, []string{"worktree", "remove", gitargv.EndOfOptions, path}, hermeticOpts(0))
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("git worktree remove failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// ListWorktrees returns every linked worktree of the repository.
func (m *Manager) ListWorktrees(ctx context.Context, toplevel string) ([]WorktreeEntry, error) {
	dir, err := absProjectDir(toplevel)
	if err != nil {
		return nil, err
	}
	out, code, err := gitexec.Run(ctx, dir, []string{"worktree", "list", "--porcelain"}, hermeticOpts(0))
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("git worktree list failed: %s", strings.TrimSpace(string(out)))
	}
	return parseWorktreePorcelain(string(out)), nil
}

// ValidateWorktree confirms a registered branch checkout.
func (m *Manager) ValidateWorktree(ctx context.Context, toplevel, path, branch string) error {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "." || !filepath.IsAbs(path) {
		return fmt.Errorf("worktree path must be absolute")
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("worktree path: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("worktree path is not a directory: %s", path)
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("resolve worktree path: %w", err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve worktree path: %w", err)
	}
	path = filepath.Clean(path)
	entries, err := m.ListWorktrees(ctx, toplevel)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		entryPath, resolveErr := filepath.EvalSymlinks(entry.Path)
		if resolveErr != nil {
			continue
		}
		entryPath, absErr := filepath.Abs(entryPath)
		if absErr != nil || filepath.Clean(entryPath) != path {
			continue
		}
		if strings.TrimSpace(branch) != "" && entry.Branch != branch {
			return fmt.Errorf("worktree branch is %q, want %q", entry.Branch, branch)
		}
		current, branchErr := m.Branch(ctx, path)
		if branchErr != nil {
			return branchErr
		}
		if strings.TrimSpace(branch) != "" && current != branch {
			return fmt.Errorf("worktree branch is %q, want %q", current, branch)
		}
		return nil
	}
	return fmt.Errorf("worktree is not registered: %s", path)
}

// PruneWorktrees removes stale worktree records.
func (m *Manager) PruneWorktrees(ctx context.Context, toplevel string) error {
	dir, err := absProjectDir(toplevel)
	if err != nil {
		return err
	}
	release, err := gitlease.Repository(ctx, dir)
	if err != nil {
		return err
	}
	defer release()
	out, code, err := gitexec.Run(ctx, dir, []string{"worktree", "prune", "--expire", "now"}, hermeticOpts(0))
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("git worktree prune failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// LocalBranchExists distinguishes an absent ref from an execution failure.
func (m *Manager) LocalBranchExists(ctx context.Context, toplevel, branch string) (bool, error) {
	dir, err := absProjectDir(toplevel)
	if err != nil {
		return false, err
	}
	if err := validateGitRef(branch, "branch"); err != nil {
		return false, err
	}
	out, code, err := gitexec.Run(ctx, dir, []string{"show-ref", "--verify", "--quiet", "refs/heads/" + branch}, hermeticOpts(0))
	if err != nil {
		return false, err
	}
	switch code {
	case 0:
		return true, nil
	case 1:
		return false, nil
	default:
		return false, fmt.Errorf("git show-ref failed: %s", strings.TrimSpace(string(out)))
	}
}

func (m *Manager) abortFailedMerge(ctx context.Context, dir string, mergeOut []byte) error {
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

func parseWorktreePorcelain(out string) []WorktreeEntry {
	var entries []WorktreeEntry
	var cur *WorktreeEntry
	flush := func() {
		if cur != nil {
			entries = append(entries, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			flush()
			continue
		}
		key, value, _ := strings.Cut(line, " ")
		switch key {
		case "worktree":
			flush()
			cur = &WorktreeEntry{Path: value}
		case "branch":
			if cur != nil {
				cur.Branch = strings.TrimPrefix(value, "refs/heads/")
			}
		case "bare":
			if cur != nil {
				cur.Bare = true
			}
		case "detached", "HEAD", "locked", "prunable":
			// Detached worktrees have no branch.
		}
	}
	flush()
	return entries
}

func splitNonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
