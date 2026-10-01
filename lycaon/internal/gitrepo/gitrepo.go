// Package gitrepo discovers repository directories and linked worktree metadata from path facts.
// Discovery does not validate repository contents.
package gitrepo

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// dotGit marks a repository top: a directory for an ordinary clone, a pointer file for a
// linked worktree or a submodule.
const dotGit = ".git"

// Repo locates a repository; unreadable metadata leaves GitDir and CommonDir empty.
type Repo struct {
	// Root is the canonical work-tree top level.
	Root string
	// GitDir is this work tree's git directory: Root/.git for an ordinary clone, the
	// pointer target for a linked worktree or submodule.
	GitDir string
	// CommonDir holds refs, objects, and configuration shared by worktrees.
	CommonDir string
}

// IsRoot reports whether dir holds a .git entry.
//
// Lstat, so a .git symlink counts by name; a dangling one still marks a boundary.
func IsRoot(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, dotGit))
	return err == nil
}

// Discover returns the repository containing dir, walking up to the filesystem root.
// The second result is false when no ancestor holds a .git entry.
func Discover(dir string) (Repo, bool) {
	cur := CanonicalDir(dir)
	if cur == "" {
		return Repo{}, false
	}
	for {
		if IsRoot(cur) {
			return repoAt(cur), true
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return Repo{}, false
		}
		cur = parent
	}
}

// LocalConfigFiles returns the files whose content decides what `git config --local`
// reports, or nil when the layout did not resolve and no verdict may be cached.
// config.worktree is listed even when extensions.worktreeConfig is off.
func (r Repo) LocalConfigFiles() []string {
	if r.GitDir == "" || r.CommonDir == "" {
		return nil
	}
	return []string{
		filepath.Join(r.CommonDir, "config"),
		filepath.Join(r.GitDir, "config.worktree"),
	}
}

// CanonicalDir returns dir absolute, symlink-resolved and cleaned, or "" when it cannot be
// made absolute. A path that does not exist yet keeps its absolute form.
func CanonicalDir(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	abs = filepath.Clean(abs)
	if resolved, resolveErr := filepath.EvalSymlinks(abs); resolveErr == nil {
		return filepath.Clean(resolved)
	}
	return abs
}

// repoAt fills in the layout for a directory already known to hold a .git entry.
func repoAt(root string) Repo {
	repo := Repo{Root: root}
	dot := filepath.Join(root, dotGit)
	info, err := os.Stat(dot)
	if err != nil {
		// Present to Lstat, unreadable to Stat: a dangling symlink, or an untraversable
		// target. The root stands; the rest does not.
		return repo
	}
	if info.IsDir() {
		repo.GitDir = dot
	} else {
		repo.GitDir = readGitDirPointer(root, dot)
	}
	if repo.GitDir == "" {
		return repo
	}
	repo.CommonDir = readCommonDir(repo.GitDir)
	return repo
}

// gitDirPrefix is the one line a .git pointer file carries.
const gitDirPrefix = "gitdir:"

// readGitDirPointer resolves a .git file to the git directory it names, relative to the
// work tree holding it. Anything unparsable returns "" rather than a guess.
func readGitDirPointer(root, dot string) string {
	// #nosec G304 -- dot is the discovered .git entry.
	raw, err := os.ReadFile(dot)
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(line, gitDirPrefix) {
		return ""
	}
	return resolveAgainst(root, strings.TrimSpace(strings.TrimPrefix(line, gitDirPrefix)))
}

// readCommonDir resolves the shared git directory for gitDir. No commondir file means an
// ordinary repository, where gitDir is its own common dir. A commondir that exists and does
// not resolve returns "" rather than gitDir, which would name a config the repository does
// not read.
func readCommonDir(gitDir string) string {
	// #nosec G304 -- commondir is fixed inside the discovered git directory.
	raw, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if errors.Is(err, fs.ErrNotExist) {
		return gitDir
	}
	if err != nil {
		return ""
	}
	return resolveAgainst(gitDir, strings.TrimSpace(string(raw)))
}

// resolveAgainst cleans a pointer value, resolving a relative one against base.
func resolveAgainst(base, value string) string {
	if value == "" {
		return ""
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(base, value)
	}
	return filepath.Clean(value)
}
