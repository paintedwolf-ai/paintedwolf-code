package worker

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/pkg/api"
)

const maxGitlinkFileBytes = 4096

// authorizedPromoteGitDirs returns verified linked-worktree Git metadata directories
// (worktree gitdir and shared commondir) for registered promotion roots.
// Only roots whose .git gitlink is structurally proven by privategit.ProveLinkedWorktree
// are authorized; unknown, detached, symlinked, or tampered metadata is excluded so
// checkpoint ref import and rollback still refuse it.
func authorizedPromoteGitDirs(task *api.WorkerTask, roots []projectroot.RootRef) []string {
	var rootDirs []string
	hasPrimary := false
	for _, r := range roots {
		if r.Outside {
			continue
		}
		if r.IsPrimary {
			hasPrimary = true
		}
		if p := strings.TrimSpace(r.Path); p != "" {
			rootDirs = append(rootDirs, p)
		}
	}
	if !hasPrimary && task != nil {
		if p := strings.TrimSpace(task.WorkspacePath); p != "" {
			rootDirs = append(rootDirs, p)
		}
	}
	var allowed []string
	add := func(dir string) {
		dir = filepath.Clean(strings.TrimSpace(dir))
		if dir != "" && dir != "." && !slices.Contains(allowed, dir) {
			allowed = append(allowed, dir)
		}
	}
	for _, rootDir := range rootDirs {
		if gitDir, commonDir, ok := verifiedLinkedWorktreeGitDirs(rootDir); ok {
			add(gitDir)
			add(commonDir)
		}
	}
	return allowed
}

func verifiedLinkedWorktreeGitDirs(rootDir string) (gitDir, commonDir string, ok bool) {
	rootDir = filepath.Clean(strings.TrimSpace(rootDir))
	if rootDir == "" || rootDir == "." {
		return "", "", false
	}
	dotGit := filepath.Join(rootDir, ".git")
	st, err := os.Lstat(dotGit)
	if err != nil || st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
		return "", "", false
	}
	raw, err := readBoundedGitlink(dotGit, maxGitlinkFileBytes)
	if err != nil {
		return "", "", false
	}
	line := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(line, "gitdir:") || strings.ContainsAny(line, "\r\n\x00") {
		return "", "", false
	}
	target := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	if target == "" {
		return "", "", false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(rootDir, target)
	}
	target = filepath.Clean(target)
	targetInfo, err := os.Lstat(target)
	if err != nil || targetInfo.Mode()&os.ModeSymlink != 0 || !targetInfo.IsDir() {
		return "", "", false
	}
	common, err := privategit.ProveLinkedWorktree(dotGit, target)
	if err != nil {
		return "", "", false
	}
	return target, common, true
}

func readBoundedGitlink(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // Path is <registered-root>/.git verified as a regular file via Lstat.
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > maxBytes {
		return nil, io.ErrUnexpectedEOF
	}
	return raw, nil
}
