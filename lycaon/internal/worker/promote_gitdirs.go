package worker

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/pkg/api"
)

const maxGitlinkFileBytes = 4096

// authorizedPromoteGitDirs maps each registered promotion root ID to its exact verified
// linked-worktree Git metadata directories (worktree gitdir and shared commondir).
// Only roots whose .git gitlink is structurally proven by privategit.ProveLinkedWorktree
// are authorized, and each authorization is scoped strictly to its own root ID so one root
// cannot authorize another root's metadata or widen prefix matches.
func authorizedPromoteGitDirs(task *api.WorkerTask, roots []projectroot.RootRef) map[string]checkpoint.AuthorizedGitDir {
	type rootTarget struct {
		id   string
		path string
	}
	var targets []rootTarget
	hasPrimary := false
	for _, r := range roots {
		if r.Outside {
			continue
		}
		if r.IsPrimary {
			hasPrimary = true
		}
		id := strings.TrimSpace(r.ID)
		p := strings.TrimSpace(r.Path)
		if id != "" && p != "" {
			targets = append(targets, rootTarget{id: id, path: p})
		}
	}
	if !hasPrimary && task != nil {
		id := strings.TrimSpace(task.WorkspaceRootID)
		p := strings.TrimSpace(task.WorkspacePath)
		if id != "" && p != "" {
			targets = append(targets, rootTarget{id: id, path: p})
		}
	}
	var allowed map[string]checkpoint.AuthorizedGitDir
	for _, t := range targets {
		if gitDir, commonDir, ok := verifiedLinkedWorktreeGitDirs(t.path); ok {
			if allowed == nil {
				allowed = make(map[string]checkpoint.AuthorizedGitDir, len(targets))
			}
			allowed[t.id] = checkpoint.AuthorizedGitDir{
				GitDir:    gitDir,
				CommonDir: commonDir,
			}
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
